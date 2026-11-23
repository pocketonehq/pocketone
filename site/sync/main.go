// Command sync copies the extension docs from ext/ into the pocketone.dev site (site/pb_pages), so they are written once.
//
//	cd site/sync && go run .             # write the generated pages
//	cd site/sync && go run . --validate  # fail only if the pages can't be generated (pull requests)
//	cd site/sync && go run . --check     # also fail if a generated file is out of date
//
// Pull requests only validate: an extension author never has to commit generated files. On main, CI runs sync and
// commits what changed (.github/regenerate.sh), then deploys.
//
// Each extension gets one page, /docs/extensions/<name>: its README, which has Install, Configure, Use, Integrations and Example
// sections (.github/extensions checks them), with the pocketone.yaml sections the extension owns (read from
// `pocketone config --json`) added to Configure. Other top-level .md files in the extension folder become guides under
// the page.
//
// It owns: pb_pages/(main)/docs/extensions/**, pb_pages/(main)/docs/config-reference.md, pb_pages/(main)/docs/writing-an-extension.md (from docs/extensions.md), the Contracts and
// Extensions navs (pb_pages/(main)/_private/toc-contracts.md, toc-extensions.md; the main toc.md is hand-written), each site's llms.txt and llms-full.txt, and the Agent
// Skills' reference folders (agent/skills/*/reference; see agent.go). Each extension's catalogue entry (title, group,
// summary) is in the front matter of its README; site/extensions.json holds the groups and the few folders without a
// page of their own (links).
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	repoURL       = "https://github.com/pocketonehq/pocketone"
	extensionsOut = "site/pb_pages/(main)/docs/extensions"
	contractsOut  = "site/pb_pages/(main)/docs/contracts"
	// docs/contracts.md, a tutorial: the main nav lists it under Tutorials, so it isn't under /docs/contracts
	fitsTogetherOut = "site/pb_pages/(main)/docs/how-pocketone-fits-together.md"
	authorsOut      = "site/pb_pages/(main)/docs/writing-an-extension.md"
	configOut       = "site/pb_pages/(main)/docs/config-reference.md"
	tocFile         = "site/pb_pages/(main)/_private/toc.md"
)

type group struct {
	ID, Title, Nav, Intro string
}

type extension struct {
	Name, Title, Group, Summary string
	Link                        string // no README: point here (repo path) instead
	guides                      []guide
	site                        []string  // ext/<name>/docs, paths relative to it (extsite.go)
	blog                        []string  // ext/<name>/blog/*.md, file names
	nav                         string    // the site's nav sections, ready for the docs nav
	landing                     bool      // it has docs (ext/<name>/docs/index.md): they're /docs/extensions/<name>, the README moves under it
	section, docsTitle          string    // the main nav section that links its docs, and the link's text (docs/index.md's front matter)
	navLinks                    []navLink // other docs pages the main nav also lists (their front matter `section:`), such as a guide under Guides
}

type guide struct {
	src, slug, title string
}

type manifest struct {
	Groups     []group
	Links      []*extension // folders with no page of their own
	Extensions []*extension `json:"-"` // every ext/<name> folder: its README's front matter, or its entry in Links
	contracts  []contractPage
	ref        *reference          // `pocketone reference`: the standard binary's commands and contract APIs
	provides   map[string][]string // extension → the contracts it provides (each contract README's index)
}

// contractPage is a contract's README (ext/contracts/<id path>/vN/README.md), published at /docs/contracts/<id>.
type contractPage struct {
	ID, Src, Summary string
}

func main() {
	check := flag.Bool("check", false, "don't write; exit 1 if any generated file is out of date")
	validate := flag.Bool("validate", false, "don't write; exit 1 only if the pages can't be generated, and list the files that would change")
	root := flag.String("root", "../..", "repository root")
	flag.Parse()
	if *validate {
		*check = true
	}
	if err := run(*root, *check, *validate); err != nil {
		fmt.Fprintln(os.Stderr, "site/sync:", err)
		os.Exit(1)
	}
}

func run(root string, check, validate bool) error {
	m, err := loadManifest(root)
	if err != nil {
		return err
	}
	if m.contracts, err = loadContracts(root); err != nil {
		return err
	}
	secs, ref, err := loadBinary(root)
	if err != nil {
		return err
	}
	m.ref = ref
	m.provides = providedContracts(root, m.contracts)
	want, err := generate(root, m, secs)
	if err != nil {
		return err
	}
	stale, err := diff(root, want)
	if err != nil {
		return err
	}
	if len(stale) == 0 {
		fmt.Println("site/sync: up to date")
		return nil
	}
	if validate {
		fmt.Printf("site/sync: ok; %d generated files will change when main regenerates them (nothing to commit):\n  %s\n", len(stale), strings.Join(stale, "\n  "))
		return nil
	}
	if check {
		return fmt.Errorf("site is out of date; run `cd site/sync && go run .`:\n  %s", strings.Join(stale, "\n  "))
	}
	for _, p := range stale {
		full := filepath.Join(root, p)
		content, ok := want[p]
		if !ok {
			if err := os.Remove(full); err != nil {
				return err
			}
			fmt.Println("removed", p)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", p)
	}
	return nil
}

func loadManifest(root string) (*manifest, error) {
	raw, err := os.ReadFile(filepath.Join(root, "site/extensions.json"))
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("site/extensions.json: %w", err)
	}
	groups := map[string]bool{}
	for _, g := range m.Groups {
		groups[g.ID] = true
	}
	links := map[string]*extension{}
	for _, p := range m.Links {
		links[p.Name] = p
	}
	var problems []string
	exts, err := os.ReadDir(filepath.Join(root, "ext"))
	if err != nil {
		return nil, err
	}
	for _, e := range exts {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		dir := filepath.Join(root, "ext", name)
		if p, ok := links[name]; ok {
			if _, err := os.Stat(filepath.Join(root, p.Link)); err != nil {
				problems = append(problems, fmt.Sprintf("%s: link %s doesn't exist", name, p.Link))
			}
			m.Extensions = append(m.Extensions, p)
			delete(links, name)
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, "README.md"))
		if err != nil {
			problems = append(problems, fmt.Sprintf("ext/%s: no README.md (docs/extensions.md#what-an-extension-folder-contains)", name))
			continue
		}
		p, missing := catalogueEntry(name, string(src))
		if len(missing) > 0 {
			problems = append(problems, fmt.Sprintf("ext/%s/README.md: front matter needs %s (docs/extensions.md#what-an-extension-folder-contains)", name, strings.Join(missing, ", ")))
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			n := e.Name()
			if e.IsDir() || !strings.HasSuffix(n, ".md") || n == "README.md" || n == "CHANGELOG.md" || n == "TODO.md" {
				continue
			}
			p.guides = append(p.guides, guide{
				src:  path.Join("ext", name, n),
				slug: strings.ToLower(strings.TrimSuffix(n, ".md")),
			})
		}
		problems = append(problems, loadSite(root, p)...)
		m.Extensions = append(m.Extensions, p)
	}
	for name := range links {
		problems = append(problems, fmt.Sprintf("site/extensions.json: links %s, but there's no ext/%s", name, name))
	}
	for _, p := range m.Extensions {
		if !groups[p.Group] {
			problems = append(problems, fmt.Sprintf("ext/%s: unknown group %q (one of the ids in site/extensions.json)", p.Name, p.Group))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, errors.New("extensions:\n  " + strings.Join(problems, "\n  "))
	}
	// within a group, by title
	sort.SliceStable(m.Extensions, func(i, j int) bool {
		return strings.ToLower(m.Extensions[i].Title) < strings.ToLower(m.Extensions[j].Title)
	})
	return &m, nil
}

// catalogueEntry is an extension's catalogue entry from its README's front matter (`title`, `group`, `summary`), and
// the keys it's missing.
func catalogueEntry(name, src string) (*extension, []string) {
	fm := readFront(src)
	p := &extension{Name: name, Title: fm["title"], Group: fm["group"], Summary: fm["summary"]}
	var missing []string
	for _, k := range []string{"title", "group", "summary"} {
		if fm[k] == "" {
			missing = append(missing, "`"+k+":`")
		}
	}
	return p, missing
}

// readFront returns a README's front matter as key -> value; a value in double quotes is read as a JSON string.
func readFront(src string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(frontStripRe.FindString(src), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(line, " ") {
			continue
		}
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, `"`) {
			var u string
			if json.Unmarshal([]byte(v), &u) == nil {
				v = u
			}
		}
		out[strings.TrimSpace(k)] = v
	}
	return out
}

// generate returns every file sync owns, keyed by repo path.
func generate(root string, m *manifest, secs []section) (map[string]string, error) {
	out := map[string]string{}
	pages := map[string]string{"docs/extensions.md": "/docs/writing-an-extension"} // repo path -> site URL
	pages["docs/contracts.md"] = "/docs/how-pocketone-fits-together"
	pages["docs/extensions-overview.md"] = "/docs/extensions"
	pages["ext/contracts/README.md"] = "/docs/contracts"
	pages["ext/contracts"] = "/docs/contracts"
	for _, c := range m.contracts {
		pages[c.Src] = "/docs/contracts/" + c.ID
		pages[path.Dir(c.Src)] = "/docs/contracts/" + c.ID
	}
	for _, p := range m.Extensions {
		if p.Link != "" {
			continue
		}
		pages["ext/"+p.Name+"/README.md"] = readmeURL(p)
		pages["ext/"+p.Name] = readmeURL(p)
		for _, g := range p.guides {
			pages[g.src] = "/docs/extensions/" + p.Name + "/" + g.slug
		}
	}
	addSitePages(m, pages)
	rw := &rewriter{root: root, pages: pages}
	site, err := siteFiles(root, m, rw)
	if err != nil {
		return nil, err
	}
	for k, v := range site {
		out[k] = v
	}

	for _, p := range m.Extensions {
		if p.Link != "" {
			continue
		}
		for i := range p.guides {
			g := &p.guides[i]
			body, title, err := rw.page(g.src, "")
			if err != nil {
				return nil, err
			}
			g.title = title
			// a guide shares its extension's comment thread (see docs-footer.js)
			fm := strings.Replace(frontMatter(title, p.Title+": "+title, g.src), "\n---\n", "\ndiscussion: \"pocketone.dev/docs/plugins/"+p.Name+"\"\n---\n", 1)
			out[extensionsOut+"/"+p.Name+"/"+g.slug+".md"] = fm + body
		}
		if len(p.site) > 0 {
			if p.nav, err = siteNav(root, p, rw); err != nil {
				return nil, err
			}
		}
		page, err := extensionPage(rw, p, secs)
		if err != nil {
			return nil, err
		}
		page = insertBefore(page, "Integrations", extensionSurfaces(m, p))
		if p.landing {
			out[extensionsOut+"/"+p.Name+"/"+readmeSlug+".md"] = page
		} else {
			out[extensionsOut+"/"+p.Name+"/index.md"] = page
		}
	}
	out[configOut] = configReference(m, secs)
	out[referenceOut+"/cli.md"] = cliReference(m)
	out[extensionsOut+"/apis.md"] = restReference(m)
	out[referenceOut+"/mcp.md"] = mcpReference(m)
	gopkgs, err := goRefPackages(root)
	if err != nil {
		return nil, err
	}
	pbpkgs, err := pbGoPackages(root)
	if err != nil {
		return nil, err
	}
	allGo := append(append([]goRefPackage{}, gopkgs...), pbpkgs...)
	pbPages, pbNS := pbJSPages(root, pbpkgs)
	for u, page := range pbPages {
		out[referenceOut+strings.TrimPrefix(u, "/docs/reference")+".md"] = page
	}
	out[referenceOut+"/javascript.md"] = jsIndex(m, pbNS, pbpkgs)
	if err := fieldsInPage(root, out); err != nil {
		return nil, err
	}
	if err := starterPages(root, out); err != nil {
		return nil, err
	}
	if err := checkScreenshots(root, out); err != nil {
		return nil, err
	}
	if err := checkRESTCoverage(root); err != nil {
		return nil, err
	}
	for _, r := range jsRefs(m) {
		out[referenceOut+"/javascript/"+r.Extension+".md"] = typedPage(m, r, allGo)
	}
	out[path.Dir(contractsNav)+"/toc-javascript.md"] = jsRefNav(m, pbNS, pbpkgs)
	out[path.Dir(contractsNav)+"/toc-go.md"] = goRefNav(gopkgs, pbpkgs)
	out[referenceOut+"/go.md"] = goRefIndex(gopkgs, pbpkgs)
	for _, p := range allGo {
		for u, page := range p.Extra {
			out[referenceOut+strings.TrimPrefix(u, "/docs/reference")+".md"] = page
		}
		out[referenceOut+strings.TrimPrefix(p.URL, "/docs/reference")+".md"] = p.Page
	}

	for _, c := range m.contracts {
		body, title, err := rw.page(c.Src, "")
		if err != nil {
			return nil, err
		}
		body = insertBefore(stripFront(body), "Extensions", contractSurfaces(m.ref, c.ID))
		out[contractsOut+"/"+c.ID+"/index.md"] = frontMatter(title, c.Summary, c.Src) + body
	}
	if body, title, err := rw.page("docs/contracts.md", ""); err == nil {
		out[fitsTogetherOut] = frontMatter(title, "How PocketOne extensions work together: every extension API is a named, versioned contract that any extension can provide.", "docs/contracts.md") + body
	} else {
		return nil, err
	}
	if body, _, err := rw.page("ext/contracts/README.md", "Contracts"); err == nil {
		out[contractsOut+"/index.md"] = frontMatter("Contracts", "Every contract in PocketOne: what it promises and which extensions provide, require and work with it.", "ext/contracts/README.md") + body
	} else {
		return nil, err
	}

	body, title, err := rw.page("docs/extensions.md", "")
	if err != nil {
		return nil, err
	}
	out[authorsOut] = frontMatter(title, "How to write a PocketOne extension.", "docs/extensions.md") + body
	// /docs/extensions: what extensions are and how they work (docs/extensions-overview.md); the catalogue is under it
	if body, title, err := rw.page("docs/extensions-overview.md", ""); err == nil {
		out[extensionsOut+"/index.md"] = frontMatter(title, "What a PocketOne extension is and how extensions work: kinds, settings, contracts, extension APIs and JavaScript.", "docs/extensions-overview.md") + body
	} else {
		return nil, err
	}
	out[extensionsOut+"/all.md"] = index(m)

	sectionNavs(m, out)
	siteNavs(m, out)
	if out[tocFile], err = mainNav(root, m); err != nil {
		return nil, err
	}
	if err := versionFile(root, out); err != nil {
		return nil, err
	}

	if err := agentFiles(root, out); err != nil {
		return nil, err
	}
	return out, nil
}

// link is an extension's page in the catalogue and the Extensions nav: its README page. An extension's docs
// (ext/<name>/docs) are linked from the main nav's section their index.md names instead.
func link(p *extension) string {
	if p.Link != "" {
		return repoURL + "/blob/main/" + p.Link
	}
	if p.landing {
		return readmeURL(p)
	}
	return "/docs/extensions/" + p.Name
}

func index(m *manifest) string {
	var b strings.Builder
	b.WriteString(frontMatter("All extensions", "Every extension in PocketOne, grouped by what it's for.", ""))
	b.WriteString("# All extensions\n\nEach extension is a Go module in [`ext/`](" + repoURL + "/tree/main/ext) with its own README; the pages here are those READMEs. Want to add one? Read [Writing an extension](/docs/writing-an-extension).\n")
	for _, g := range m.Groups {
		fmt.Fprintf(&b, "\n## %s\n\n%s\n\n| Extension | What it does |\n|---|---|\n", g.Title, g.Intro)
		for _, p := range m.Extensions {
			if p.Group == g.ID {
				fmt.Fprintf(&b, "| [%s](%s) | %s |\n", p.Title, link(p), escapeEJS(p.Summary))
			}
		}
	}
	return b.String()
}

// The main nav (_private/toc.md) is hand-written; its Extend PocketOne section links Contracts and Extensions, whose pages
// show a nav of their own (_private/docs-nav.js), written here: toc-contracts.md lists every contract and
// toc-extensions.md every extension, by group.
const (
	contractsNav  = "site/pb_pages/(main)/_private/toc-contracts.md"
	extensionsNav = "site/pb_pages/(main)/_private/toc-extensions.md"
	backLink      = "- [← PocketOne docs](/docs)\n"
)

func sectionNavs(m *manifest, out map[string]string) {
	var b strings.Builder
	b.WriteString("<!-- Generated by site/sync from ext/contracts. Edit those READMEs, then run site/sync. -->\n\n# Contracts\n\n" + backLink +
		"- [Contracts](/docs/contracts)\n\n# By name\n\n")
	for _, c := range m.contracts {
		fmt.Fprintf(&b, "- [%s](/docs/contracts/%s)\n", c.ID, c.ID)
	}
	out[contractsNav] = b.String()

	b.Reset()
	b.WriteString("<!-- Generated by site/sync from site/extensions.json and each extension's README front matter. -->\n\n# Extensions\n\n" + backLink +
		"- [Overview](/docs/extensions)\n- [All extensions](/docs/extensions/all)\n- [Extension APIs](/docs/extensions/apis)\n- [Contracts](/docs/contracts)\n- [Writing an extension](/docs/writing-an-extension)\n")
	for _, g := range m.Groups {
		fmt.Fprintf(&b, "\n# %s\n\n", g.Title)
		for _, p := range m.Extensions {
			if p.Group != g.ID {
				continue
			}
			fmt.Fprintf(&b, "- [%s](%s)\n", p.Title, link(p))
			for _, gd := range p.guides {
				fmt.Fprintf(&b, "- [%s: %s](/docs/extensions/%s/%s)\n", p.Title, gd.title, p.Name, gd.slug)
			}
		}
	}
	out[extensionsNav] = b.String()
}

func frontMatter(title, desc, src string) string {
	q := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	s := "---\ntitle: " + q(title) + "\ndescription: " + q(desc) + "\n"
	if src != "" {
		s += "source: " + q(src) + "\n"
	}
	s += "---\n"
	if src != "" {
		s += "<!-- Generated by site/sync from " + src + ". Edit that file, then run site/sync. -->\n"
	} else {
		s += "<!-- Generated by site/sync from site/extensions.json and each extension's README front matter. -->\n"
	}
	return s + "\n"
}

var scriptServerRe = regexp.MustCompile(`(?i)<script(\s[^>]*)?\sserver\b`)

// escapeEJS keeps Pages from running anything in synced Markdown: "<%" renders as a literal "<%", and a
// `<script server>` tag (also run as code) is written out by EJS instead of being one.
func escapeEJS(s string) string {
	s = strings.ReplaceAll(s, "<%", "<%%")
	return scriptServerRe.ReplaceAllStringFunc(s, func(m string) string { return "<%- '<' %>" + m[1:] })
}

type rewriter struct {
	root  string
	pages map[string]string
}

var (
	h1Re     = regexp.MustCompile(`^#\s+(.+?)\s*$`)
	inlineRe = regexp.MustCompile(`(!?)\[([^\]]*)\]\(([^)\s]+)((?:\s+"[^"]*")?)\)`)
	refRe    = regexp.MustCompile(`^(\s*\[[^\]]+\]:\s*)(\S+)(.*)$`)
)

// page reads a repo Markdown file, rewrites its relative links, optionally replaces its first H1, and returns the
// body and the H1 text it ended with.
func (r *rewriter) page(src, title string) (string, string, error) {
	raw, err := os.ReadFile(filepath.Join(r.root, src))
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
	inFence, sawH1 := false, false
	for i, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if !sawH1 {
			if m := h1Re.FindStringSubmatch(l); m != nil {
				sawH1 = true
				if title == "" {
					title = stripMarkdown(m[1])
				}
				lines[i] = "# " + title
				continue
			}
		}
		if m := refRe.FindStringSubmatch(l); m != nil {
			lines[i] = m[1] + r.target(src, m[2], false) + m[3]
			continue
		}
		lines[i] = r.outsideCode(l, func(s string) string {
			return inlineRe.ReplaceAllStringFunc(s, func(match string) string {
				p := inlineRe.FindStringSubmatch(match)
				return p[1] + "[" + p[2] + "](" + r.target(src, p[3], p[1] == "!") + p[4] + ")"
			})
		})
	}
	if title == "" {
		title = path.Base(src)
	}
	body := strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
	return escapeEJS(body), title, nil
}

// outsideCode applies f to the parts of a line that aren't inline code spans.
var codeLinkRe = regexp.MustCompile("\\[`([^`\\]]*)`\\]\\(([^)\\s]+)\\)")

func (r *rewriter) outsideCode(l string, f func(string) string) string {
	// a link whose text is code ([`deploy/v1`](deploy/v1/README.md)) is still a link: rewrite it with its text set aside
	l = codeLinkRe.ReplaceAllStringFunc(l, func(m string) string {
		sm := codeLinkRe.FindStringSubmatch(m)
		return strings.Replace(f("[\x00]("+sm[2]+")"), "\x00", "`"+sm[1]+"`", 1)
	})
	parts := strings.Split(l, "`")
	for i := 0; i < len(parts); i += 2 {
		parts[i] = f(parts[i])
	}
	return strings.Join(parts, "`")
}

// target maps a link in src to where it should point on the site: a synced page, or the file on GitHub.
func (r *rewriter) target(src, t string, image bool) string {
	if t == "" || strings.HasPrefix(t, "#") || strings.Contains(t, "://") || strings.HasPrefix(t, "mailto:") || strings.HasPrefix(t, "/") {
		return t
	}
	frag := ""
	if i := strings.Index(t, "#"); i >= 0 {
		t, frag = t[:i], t[i:]
	}
	p := path.Clean(path.Join(path.Dir(src), t))
	if url, ok := r.pages[p]; ok {
		return url + frag
	}
	if image {
		return "https://raw.githubusercontent.com/pocketonehq/pocketone/main/" + p
	}
	kind := "blob"
	if st, err := os.Stat(filepath.Join(r.root, p)); err == nil && st.IsDir() {
		kind = "tree"
	}
	return repoURL + "/" + kind + "/main/" + p + frag
}

var mdMarks = regexp.MustCompile("[`*_]")

func stripMarkdown(s string) string { return mdMarks.ReplaceAllString(s, "") }

// diff lists the repo paths whose content differs from want, plus stale files under the extensions folder.
func diff(root string, want map[string]string) ([]string, error) {
	var out []string
	for p, content := range want {
		have, err := os.ReadFile(filepath.Join(root, p))
		if err != nil || !bytes.Equal(have, []byte(content)) {
			out = append(out, p)
		}
	}
	var err error
	for _, out1 := range append([]string{extensionsOut, contractsOut, referenceOut}, agentOutDirs()...) {
		if err = staleUnder(root, out1, want, &out); err != nil {
			return nil, err
		}
	}
	// extension site navs (toc-<name>.md) beside the main nav; a hand-written one (toc-examples.md, no generated
	// header) is kept
	navs, err := os.ReadDir(filepath.Join(root, path.Dir(tocFile)))
	if err != nil {
		return nil, err
	}
	for _, e := range navs {
		rel := path.Dir(tocFile) + "/" + e.Name()
		if _, ok := want[rel]; !ok && strings.HasPrefix(e.Name(), "toc-") && strings.HasSuffix(e.Name(), ".md") {
			raw, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				return nil, err
			}
			if strings.Contains(string(raw), "Generated by site/sync") {
				out = append(out, rel)
			}
		}
	}
	// the blog holds hand-written posts too: only a generated post nothing makes any more is stale
	posts, _ := os.ReadDir(filepath.Join(root, blogOut))
	for _, e := range posts {
		rel := blogOut + "/" + e.Name()
		if _, ok := want[rel]; ok || e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if raw, err := os.ReadFile(filepath.Join(root, rel)); err == nil && strings.Contains(string(raw), generatedPrefix+"ext/") {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out, nil
}

// staleUnder adds the files under dir that nothing generates any more.
func staleUnder(root, dir string, want map[string]string, out *[]string) error {
	return filepath.WalkDir(filepath.Join(root, dir), func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, full)
		rel = filepath.ToSlash(rel)
		if ownedElsewhere(rel) {
			return nil
		}
		if _, ok := want[rel]; !ok {
			*out = append(*out, rel)
		}
		return nil
	})
}

var frontStripRe = regexp.MustCompile(`(?s)\A---\n.*?\n---\n\s*`)

// stripFront drops a README's own front matter (owners, summary); the page gets the site's.
func stripFront(s string) string { return frontStripRe.ReplaceAllString(s, "") }

// loadContracts finds every contract README (ext/contracts/<id path>/vN/README.md), sorted by id.
func loadContracts(root string) ([]contractPage, error) {
	var out []contractPage
	base := filepath.Join(root, "ext/contracts")
	err := filepath.WalkDir(base, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(base, full)
		rel = filepath.ToSlash(rel)
		if d.IsDir() && (rel == "internal" || rel == "cmd") {
			return filepath.SkipDir
		}
		if d.IsDir() || d.Name() != "README.md" {
			return nil
		}
		dir := path.Dir(rel)
		parts := strings.Split(dir, "/")
		if len(parts) < 2 || !versionRe.MatchString(parts[len(parts)-1]) {
			return nil
		}
		raw, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		c := contractPage{ID: strings.Join(parts[:len(parts)-1], ".") + "/" + parts[len(parts)-1], Src: "ext/contracts/" + rel}
		if m := frontStripRe.FindString(string(raw)); m != "" {
			for _, line := range strings.Split(m, "\n") {
				if v, ok := strings.CutPrefix(line, "summary:"); ok {
					v = strings.TrimSpace(v)
					var u string
					if json.Unmarshal([]byte(v), &u) == nil {
						v = u
					}
					c.Summary = v
				}
			}
		}
		out = append(out, c)
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, err
}

var versionRe = regexp.MustCompile(`^v[0-9]+$`)
