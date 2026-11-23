package main

// Extension docs. An extension's home is ext/<name>: its README (its page in the Extensions reference, under
// /docs/extensions), its samples, and optionally end-user docs in Pages format:
//
//	ext/<name>/docs/            mounted at /docs/extensions/<name>/; the README moves to /docs/extensions/<name>/extension
//	ext/<name>/docs/index.md    the docs' landing page; its front matter `section:` names the main nav's section (a
//	                            `# Heading` of _private/toc.md: Tutorials, Reference, ...) whose list links it
//	ext/<name>/docs/_private/toc.md   its nav: the docs sidebar on its pages, in place of the main nav
//	any other page's `section:`  also lists that page in the main nav's section (a guide in Guides); `nav:` is the
//	                            link's text when it isn't the page's title
//	ext/<name>/blog/*.md        posts merged into the pocketone.dev blog (/blog/<file name>)
//
// Unlike a README, these files are pages-native: EJS runs, so a code sample writes "<%%" for a literal "<%". Sync
// copies them, adding `source:` to each Markdown page's front matter (the "Edit this page" link) and pointing its
// relative links at site URLs (a link is relative to the file, as on GitHub: `ctx/req.md`, `../routing.md`, an image
// next to the page). A relative link to a file that doesn't exist fails the sync. The subtree is content only: no
// `+` files (layouts, config, middleware, API routes).

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const blogOut = "site/pb_pages/(main)/blog"

// docsDir is the folder of an extension's docs, in its home (ext/<name>/docs).
const docsDir = "docs"

// siteToc is the extension docs' nav file, relative to ext/<name>/docs; sync reads it and doesn't copy it.
const siteToc = "_private/toc.md"

// loadSite lists ext/<name>/docs (paths relative to it) and ext/<name>/blog/*.md (file names), with what's wrong.
func loadSite(root string, p *extension) []string {
	var problems []string
	base := filepath.Join(root, "ext", p.Name, docsDir)
	if st, err := os.Stat(base); err == nil && st.IsDir() {
		_ = filepath.WalkDir(base, func(full string, d fs.DirEntry, err error) error {
			if err != nil {
				problems = append(problems, err.Error())
				return nil
			}
			rel, _ := filepath.Rel(base, full)
			rel = filepath.ToSlash(rel)
			n := d.Name()
			if d.IsDir() || n == ".DS_Store" {
				return nil
			}
			where := "ext/" + p.Name + "/" + docsDir + "/" + rel
			switch {
			case strings.HasPrefix(n, "+"):
				problems = append(problems, where+": an extension's docs are content only (no + files); the pocketone.dev site owns layouts, config, middleware and API routes")
			case !strings.Contains(rel, "/") && strings.HasPrefix(n, "index."):
				p.landing = true
				raw, err := os.ReadFile(full)
				if err != nil {
					problems = append(problems, where+": "+err.Error())
					break
				}
				fm := readFront(string(raw))
				p.section, p.docsTitle = fm["section"], fm["title"]
				if p.section == "" {
					problems = append(problems, where+": needs `section:` in its front matter (the main nav section that links the docs, such as Reference)")
				}
			case strings.HasSuffix(n, ".md") && !strings.HasPrefix(rel, "_private/"):
				// any other page may also be listed in the main nav (a guide under Guides): front matter `section:`
				raw, err := os.ReadFile(full)
				if err != nil {
					problems = append(problems, where+": "+err.Error())
					break
				}
				if fm := readFront(string(raw)); fm["section"] != "" {
					u := strings.TrimSuffix(strings.TrimSuffix(rel, ".md"), "/index")
					title := fm["nav"]
					if title == "" {
						title = fm["title"]
					}
					p.navLinks = append(p.navLinks, navLink{Section: fm["section"], Title: title, URL: "/docs/extensions/" + p.Name + "/" + u})
				}
			case rel == readmeSlug+".md" || rel == readmeSlug+".ejs" || strings.HasPrefix(rel, readmeSlug+"/"):
				problems = append(problems, where+": /docs/extensions/"+p.Name+"/"+readmeSlug+" is the README's page")
			}
			p.site = append(p.site, rel)
			return nil
		})
		sort.Strings(p.site)
		if !contains(p.site, siteToc) {
			problems = append(problems, "ext/"+p.Name+"/"+docsDir+": needs "+siteToc+" (its nav)")
		}
		if !p.landing {
			problems = append(problems, "ext/"+p.Name+"/"+docsDir+": needs index.md (the docs' landing page, with `section:` in its front matter)")
		}
		for _, g := range p.guides {
			if contains(p.site, g.slug+".md") || contains(p.site, g.slug+"/index.md") {
				problems = append(problems, fmt.Sprintf("ext/%s: %s and %s/%s.md are both /docs/extensions/%s/%s", p.Name, path.Base(g.src), docsDir, g.slug, p.Name, g.slug))
			}
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "ext", p.Name, "blog"))
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				p.blog = append(p.blog, e.Name())
			}
		}
	}
	return problems
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// sitePageURL maps a page in an extension's docs (relative to docs/) to its URL, or "" for a file that isn't a page.
func sitePageURL(name, rel string) string {
	if !strings.HasSuffix(rel, ".md") && !strings.HasSuffix(rel, ".ejs") || strings.HasPrefix(rel, "_private/") || strings.Contains(rel, "/_private/") {
		return ""
	}
	u := strings.TrimSuffix(strings.TrimSuffix(rel, ".md"), ".ejs")
	u = strings.TrimSuffix(strings.TrimSuffix(u, "index"), "/")
	if u == "" {
		return "/docs/extensions/" + name
	}
	return "/docs/extensions/" + name + "/" + u
}

// addSitePages adds every extension's docs pages, files and blog posts to pages (repo path -> site URL), so links
// from anywhere (READMEs included) reach them.
func addSitePages(m *manifest, pages map[string]string) {
	for _, p := range m.Extensions {
		for _, rel := range p.site {
			src := "ext/" + p.Name + "/" + docsDir + "/" + rel
			if u := sitePageURL(p.Name, rel); u != "" {
				pages[src] = u
				if d, f := path.Split(src); strings.HasPrefix(f, "index.") {
					pages[strings.TrimSuffix(d, "/")] = u
				}
			} else if !strings.Contains(rel, "_private/") {
				pages[src] = "/docs/extensions/" + p.Name + "/" + rel // a file served next to the pages (images)
			}
		}
		for _, f := range p.blog {
			pages["ext/"+p.Name+"/blog/"+f] = "/blog/" + strings.TrimSuffix(f, ".md")
		}
	}
}

// siteFiles returns the generated files for every extension's docs and blog: repo path -> content.
func siteFiles(root string, m *manifest, rw *rewriter) (map[string]string, error) {
	out := map[string]string{}
	var broken []string
	for _, p := range m.Extensions {
		for _, rel := range p.site {
			if rel == siteToc {
				continue
			}
			src := "ext/" + p.Name + "/" + docsDir + "/" + rel
			raw, err := os.ReadFile(filepath.Join(root, src))
			if err != nil {
				return nil, err
			}
			dst := extensionsOut + "/" + p.Name + "/" + rel
			if strings.HasSuffix(rel, ".md") && !strings.Contains(rel, "_private/") {
				body, bad := rw.sitePage(src, string(raw))
				broken = append(broken, bad...)
				out[dst] = withSource(body, src)
				continue
			}
			out[dst] = string(raw) // templates, partials and files (images) as they are
		}
		for _, f := range p.blog {
			src := "ext/" + p.Name + "/blog/" + f
			raw, err := os.ReadFile(filepath.Join(root, src))
			if err != nil {
				return nil, err
			}
			dst := blogOut + "/" + f
			if have, err := os.ReadFile(filepath.Join(root, dst)); err == nil && !strings.Contains(string(have), generatedPrefix) {
				return nil, fmt.Errorf("%s: %s is a hand-written post with the same name", src, dst)
			}
			body, bad := rw.sitePage(src, string(raw))
			broken = append(broken, bad...)
			out[dst] = withSource(body, src)
		}
	}
	if len(broken) > 0 {
		sort.Strings(broken)
		return nil, fmt.Errorf("broken links in extension docs:\n  %s", strings.Join(broken, "\n  "))
	}
	return out, nil
}

// sitePage points a pages-native Markdown file's relative links at site URLs (no EJS escaping: it's a template), and
// lists the relative links that reach no file.
func (r *rewriter) sitePage(src, content string) (string, []string) {
	var broken []string
	target := func(t string, image bool) string {
		if t == "" || strings.HasPrefix(t, "#") || strings.Contains(t, "://") || strings.HasPrefix(t, "mailto:") || strings.HasPrefix(t, "/") || strings.Contains(t, "<%") {
			return t
		}
		clean := t
		if i := strings.Index(clean, "#"); i >= 0 {
			clean = clean[:i]
		}
		p := path.Clean(path.Join(path.Dir(src), clean))
		if _, ok := r.pages[p]; !ok {
			if _, err := os.Stat(filepath.Join(r.root, p)); err != nil {
				broken = append(broken, fmt.Sprintf("%s: %s", src, t))
				return t
			}
		}
		return r.target(src, t, image)
	}
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	fence := false
	for i, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = !fence
			continue
		}
		if fence {
			continue
		}
		if m := refRe.FindStringSubmatch(l); m != nil {
			lines[i] = m[1] + target(m[2], false) + m[3]
			continue
		}
		lines[i] = r.outsideCode(l, func(s string) string {
			return inlineRe.ReplaceAllStringFunc(s, func(match string) string {
				p := inlineRe.FindStringSubmatch(match)
				return p[1] + "[" + p[2] + "](" + target(p[3], p[1] == "!") + p[4] + ")"
			})
		})
	}
	return strings.Join(lines, "\n"), broken
}

var frontEndRe = regexp.MustCompile(`(?s)\A---\n.*?\n---\n`)

// withSource adds `source: <src>` to a page's front matter (the "Edit this page" link) and the generated-file note.
func withSource(body, src string) string {
	q := `"` + strings.ReplaceAll(src, `"`, `\"`) + `"`
	if fm := frontEndRe.FindString(body); fm != "" {
		return fm[:len(fm)-4] + "source: " + q + "\n---\n" + generatedNote(src) + body[len(fm):]
	}
	return "---\nsource: " + q + "\n---\n" + generatedNote(src) + body
}

// siteNav is an extension docs' nav (its _private/toc.md) with its links pointed at site URLs.
func siteNav(root string, p *extension, rw *rewriter) (string, error) {
	src := "ext/" + p.Name + "/" + docsDir + "/" + siteToc
	raw, err := os.ReadFile(filepath.Join(root, src))
	if err != nil {
		return "", err
	}
	var b strings.Builder
	var broken []string
	for _, l := range strings.Split(string(raw), "\n") {
		if m := h1Re.FindStringSubmatch(l); m != nil {
			fmt.Fprintf(&b, "\n# %s\n\n", m[1])
			continue
		}
		if m := tocLinkRe.FindStringSubmatch(l); m != nil {
			u := m[2]
			if !strings.Contains(u, "://") && !strings.HasPrefix(u, "/") {
				frag := ""
				if i := strings.Index(u, "#"); i >= 0 {
					u, frag = u[:i], u[i:]
				}
				site, ok := rw.pages[path.Clean(path.Join(path.Dir(src), u))]
				if !ok {
					broken = append(broken, src+": "+m[2])
					continue
				}
				u = site + frag
			}
			fmt.Fprintf(&b, "- [%s](%s)\n", m[1], u)
		}
	}
	if len(broken) > 0 {
		return "", fmt.Errorf("broken links in extension docs navs:\n  %s", strings.Join(broken, "\n  "))
	}
	return b.String(), nil
}

// siteNavsOut lists the extensions with a nav of their own, for _private/docs-nav.js.
const siteNavsOut = "site/pb_pages/_private/site-navs.js"

// siteNavs writes each extension docs' sidebar, (main)/_private/toc-<name>.md: a way back to the main docs, the docs'
// landing page, then its nav. Its pages show it instead of the main nav. The README isn't in it: that's the extension's
// page in the Extensions reference, which lists it in its group like any other.
func siteNavs(m *manifest, out map[string]string) {
	var names []string
	for _, p := range m.Extensions {
		if p.nav == "" {
			continue
		}
		names = append(names, p.Name)
		out[path.Dir(tocFile)+"/toc-"+p.Name+".md"] = "<!-- Generated by site/sync from ext/" + p.Name + "/" + docsDir + "/" + siteToc + ". Edit that file, then run site/sync. -->\n\n" +
			"# " + p.Title + "\n\n- [← PocketOne docs](/docs)\n" + homeLink(p) + p.nav
	}
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = "'" + n + "'"
	}
	out[siteNavsOut] = "// Generated by site/sync: the extensions whose docs (ext/<name>/docs) have a nav of their own, (main)/_private/toc-<name>.md.\nmodule.exports = [" + strings.Join(q, ", ") + "]\n"
}

// navLink is an extension docs page the main nav lists.
type navLink struct {
	Section, Title, URL, Ext string
}

// readmeSlug is where an extension's README goes when it has docs: their landing page is /docs/extensions/<name>.
const readmeSlug = "extension"

// readmeURL is the extension's README page: /docs/extensions/<name>, or under it when it has docs.
func readmeURL(p *extension) string {
	if p.landing {
		return "/docs/extensions/" + p.Name + "/" + readmeSlug
	}
	return "/docs/extensions/" + p.Name
}

func homeLink(p *extension) string {
	if !p.landing {
		return ""
	}
	return "- [Home](/docs/extensions/" + p.Name + ")\n"
}

// extPageRe matches an extension's page URL (/docs/extensions/<name>), not its guides below it.
var extPageRe = regexp.MustCompile(`^/docs/extensions/[^/#?]+$`)

// mainNav is the main nav (_private/toc.md, otherwise hand-written) with each extension's docs linked at the end of
// the section their index.md names (`section:`), by the index's title. Sync owns those links: a line that links an
// extension's docs anywhere else is dropped, so moving docs to another section is a front matter change. A line that
// links them in the right section keeps its place, so the toc can order them among its own links.
func mainNav(root string, m *manifest) (string, error) {
	raw, err := os.ReadFile(filepath.Join(root, tocFile))
	if err != nil {
		return "", err
	}
	docs := map[string][]navLink{} // section -> the extension docs pages it links
	owned := map[string]bool{}     // the URLs sync links
	placed := map[string]bool{}    // owned links the toc already has in their section: they stay where they are
	exists := map[string]bool{}    // every extension's page
	for _, p := range m.Extensions {
		exists["/docs/extensions/"+p.Name] = true
		if p.landing {
			title := p.docsTitle
			if title == "" {
				title = p.Title
			}
			docs[p.section] = append(docs[p.section], navLink{Section: p.section, Title: title, URL: "/docs/extensions/" + p.Name, Ext: p.Name})
			owned["/docs/extensions/"+p.Name] = true
		}
		for _, l := range p.navLinks {
			l.Ext = p.Name
			docs[l.Section] = append(docs[l.Section], l)
			owned[l.URL] = true
		}
	}
	// gone is a link to the page of an extension that no longer exists (a removed or renamed one): dropped too
	gone := func(url string) bool { return extPageRe.MatchString(url) && !exists[url] }
	var out []string
	section := ""
	flush := func() { // the section's docs links, after its own
		for _, l := range docs[section] {
			if !placed[l.URL] {
				out = append(out, "- ["+l.Title+"]("+l.URL+")")
			}
		}
		delete(docs, section)
	}
	// flushEmpty links the current section's docs when it had no links of its own to follow: under its heading
	flushEmpty := func() {
		left := 0
		for _, l := range docs[section] {
			if !placed[l.URL] {
				left++
			}
		}
		if left == 0 {
			delete(docs, section)
			return
		}
		for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
			out = out[:len(out)-1]
		}
		out = append(out, "")
		flush()
		out = append(out, "")
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	for i, l := range lines {
		if m := tocLinkRe.FindStringSubmatch(l); m != nil && (owned[m[2]] || gone(m[2])) {
			// an owned link in the section its front matter names keeps its place (with sync's text); elsewhere, dropped
			if owned[m[2]] {
				for _, d := range docs[section] {
					if d.URL == m[2] && !placed[d.URL] {
						placed[d.URL] = true
						out = append(out, "- ["+d.Title+"]("+d.URL+")")
					}
				}
			}
			continue
		}
		if h := h1Re.FindStringSubmatch(l); h != nil {
			flushEmpty() // a section with no links of its own still gets its docs
			section = h[1]
		}
		out = append(out, l)
		// a section's list ends at its last link: the next line is blank (or the file ends) and what follows isn't a link
		if tocLinkRe.MatchString(l) && section != "" {
			rest := ""
			for _, n := range lines[i+1:] {
				if m := tocLinkRe.FindStringSubmatch(n); m != nil && (owned[m[2]] || gone(m[2])) {
					continue
				}
				if strings.TrimSpace(n) != "" {
					rest = n
					break
				}
			}
			if !tocLinkRe.MatchString(rest) {
				flush()
			}
		}
	}
	flushEmpty()
	if len(docs) > 0 {
		var bad []string
		for s, ls := range docs {
			for _, l := range ls {
				bad = append(bad, fmt.Sprintf("ext/%s/%s (%s): section %q isn't a section of %s", l.Ext, docsDir, l.URL, s, tocFile))
			}
		}
		sort.Strings(bad)
		return "", errors.New(strings.Join(bad, "\n"))
	}
	return strings.Join(out, "\n") + "\n", nil
}
