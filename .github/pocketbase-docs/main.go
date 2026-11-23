// Command pocketbase-docs builds PocketBase's documentation (pocketbase.io/docs) as Markdown for PocketOne's Agent
// Skill. It checks out PocketOne's mirror of the PocketBase site (github.com/pocketonehq/pocketbase-site)
// at the commit in PIN, builds the site (npm ci, vite build: SvelteKit's static adapter writes every docs page as
// build/docs/<page>/index.html, at its pocketbase.io URL), and converts each page's article to Markdown:
//
//	agent/skills/pocketone/reference/pocketbase/<page>.md   one per page, each with its Source: https://pocketbase.io/docs/<page>/
//	agent/skills/pocketone/reference/pocketbase/llms-full.txt  every page in one file ("---" and "Source:" between pages)
//	agent/skills/pocketone/reference/pocketbase/README.md      what this is, the commit and PocketBase version, the license
//	agent/skills/pocketone/reference/pocketbase/jsvm/...       the JSVM reference (pocketbase.io/jsvm), one file per page
//	agent/skills/pocketone/reference/pocketbase/jsvm/types.d.ts  the declarations it's generated from
//
// The JSVM reference is typedoc's site for PocketBase's JS API, committed in the site repo as static/jsvm: its pages
// are converted as they are, each starting with its pocketbase.io/jsvm URL.
//
// Links and images point at pocketbase.io. Run it from its folder (Node and npm on PATH); it works on the repository it
// sits in:
//
//	cd .github/pocketbase-docs && go run .            # build at PIN and write the files
//	cd .github/pocketbase-docs && go run . --update   # move PIN to the mirror's master first
//	cd .github/pocketbase-docs && go run . --site DIR # convert an already-built site (DIR/build/docs), no clone or build
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	mirror  = "https://github.com/pocketonehq/pocketbase-site.git"
	site    = "https://pocketbase.io"
	pinFile = ".github/pocketbase-docs/PIN"
	outDir  = "agent/skills/pocketone/reference/pocketbase"
)

func main() {
	update := flag.Bool("update", false, "move PIN to the mirror's master before building")
	siteDir := flag.String("site", "", "an already-built checkout of the site (skips clone and build)")
	flag.Parse()
	if err := run(*update, *siteDir); err != nil {
		fmt.Fprintln(os.Stderr, "pocketbase-docs:", err)
		os.Exit(1)
	}
}

func logf(format string, args ...any) {
	fmt.Printf("%s %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

func run(update bool, siteDir string) error {
	if siteDir != "" {
		if abs, err := filepath.Abs(siteDir); err == nil {
			siteDir = abs
		}
	}
	root, err := output("", "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("not in a git checkout: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		return err
	}
	pinRaw, err := os.ReadFile(pinFile)
	if err != nil {
		return fmt.Errorf("%s: %v", pinFile, err)
	}
	pin := strings.TrimSpace(string(pinRaw))
	if siteDir == "" {
		tmp, err := os.MkdirTemp("", "pocketbase-site-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		siteDir = filepath.Join(tmp, "site")
		logf("cloning %s", mirror)
		if err := sh("", 5*time.Minute, "git", "clone", "-q", "--filter=blob:none", "--branch", "master", mirror, siteDir); err != nil {
			return err
		}
		if update {
			head, err := output(siteDir, "git", "rev-parse", "HEAD")
			if err != nil {
				return err
			}
			if head != pin {
				logf("PIN %s -> %s", short(pin), short(head))
				pin = head
				if err := os.WriteFile(pinFile, []byte(pin+"\n"), 0o644); err != nil {
					return err
				}
			} else {
				logf("PIN %s is the mirror's master already", short(pin))
			}
		}
		logf("checking out %s", short(pin))
		if err := sh(siteDir, time.Minute, "git", "checkout", "-q", pin); err != nil {
			return err
		}
		logf("npm ci")
		if err := sh(siteDir, 10*time.Minute, "npm", "ci", "--no-audit", "--no-fund", "--loglevel=error"); err != nil {
			return err
		}
		logf("vite build")
		if err := sh(siteDir, 10*time.Minute, "npx", "vite", "build", "--logLevel", "error"); err != nil {
			return err
		}
	}
	commit, err := output(siteDir, "git", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	date, err := output(siteDir, "git", "log", "-1", "--format=%cs")
	if err != nil {
		return err
	}
	docs := filepath.Join(siteDir, "build", "docs")
	pages, version, err := convertAll(docs)
	if err != nil {
		return err
	}
	logf("converted %d pages (PocketBase %s)", len(pages), version)
	if err := write(pages, commit, date, version); err != nil {
		return err
	}
	jsvm, err := convertJSVM(filepath.Join(siteDir, "static", "jsvm"))
	if err != nil {
		return err
	}
	logf("converted %d JSVM reference pages", len(jsvm))
	if err := writeJSVM(jsvm, commit); err != nil {
		return err
	}
	// the declarations the reference is generated from, as they are: unpublished, so linked at the mirror's commit
	types, err := os.ReadFile(filepath.Join(siteDir, "jsvm", "types.d.ts"))
	if err != nil {
		return err
	}
	head := fmt.Sprintf("// PocketBase's JS API declarations (the source of %s), copied by .github/pocketbase-docs from\n"+
		"// https://github.com/pocketonehq/pocketbase-site/blob/%s/jsvm/types.d.ts. Don't edit: run the tool again.\n\n", jsvmSite, commit)
	if err := writeIfChanged(filepath.Join(outDir, "jsvm", "types.d.ts"), head+string(types)); err != nil {
		return err
	}
	logf("copied jsvm/types.d.ts (%d KB)", len(types)/1024)
	logf("wrote %s/jsvm (%d pages)", outDir, len(jsvm))
	return nil
}

// page is one converted docs page.
type page struct {
	slug, title, source, body string
}

func convertAll(docs string) ([]page, string, error) {
	var files []string
	err := filepath.WalkDir(docs, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "index.html" {
			files = append(files, p)
		}
		return err
	})
	if err != nil {
		return nil, "", err
	}
	if len(files) == 0 {
		return nil, "", fmt.Errorf("no pages under %s (did the build run?)", docs)
	}
	sort.Strings(files)
	var out []page
	version := ""
	for _, f := range files {
		rel, _ := filepath.Rel(docs, filepath.Dir(f))
		slug := filepath.ToSlash(rel)
		if slug == "." {
			slug = ""
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, "", err
		}
		if version == "" {
			version = versionRe.FindString(string(raw))
		}
		urlPath := "/docs/"
		if slug != "" {
			urlPath += slug + "/"
		}
		title, body, err := convert(raw, site+urlPath)
		if err != nil {
			return nil, "", fmt.Errorf("%s: %w", f, err)
		}
		name := slug
		if name == "" {
			name = "index"
		}
		out = append(out, page{slug: name, title: title, source: site + urlPath, body: body})
	}
	return out, version, nil
}

var versionRe = regexp.MustCompile(`v0\.\d+\.\d+`)

func write(pages []page, commit, date, version string) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	want := map[string]bool{}
	var full strings.Builder
	fmt.Fprintf(&full, "# PocketBase documentation\n\n> PocketBase's own docs (%s/docs), as Markdown, from the PocketBase site at commit %s (%s), PocketBase %s. "+
		"PocketOne is PocketBase plus extensions: these pages describe PocketBase itself; PocketOne's docs (https://pocketone.dev/docs) add to them.\n",
		site, short(commit), date, version)
	for _, p := range pages {
		md := fmt.Sprintf("Source: %s\n\n# %s\n\n%s\n", p.source, p.title, strings.TrimSpace(p.body))
		name := p.slug + ".md"
		want[name] = true
		if err := writeIfChanged(filepath.Join(outDir, name), md); err != nil {
			return err
		}
		fmt.Fprintf(&full, "\n\n---\n\n%s", md)
	}
	want["llms-full.txt"] = true
	if err := writeIfChanged(filepath.Join(outDir, "llms-full.txt"), full.String()); err != nil {
		return err
	}
	want["README.md"] = true
	if err := writeIfChanged(filepath.Join(outDir, "README.md"), readme(pages, commit, date, version)); err != nil {
		return err
	}
	// drop pages the site no longer has
	entries, err := os.ReadDir(outDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() && !want[e.Name()] {
			logf("removing %s", e.Name())
			if err := os.Remove(filepath.Join(outDir, e.Name())); err != nil {
				return err
			}
		}
	}
	logf("wrote %s (%d pages)", outDir, len(pages))
	return nil
}

func readme(pages []page, commit, date, version string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `# PocketBase documentation

PocketBase's own documentation, [%[1]s/docs](%[1]s/docs/), converted to Markdown: one file per page, and every page in
[llms-full.txt](llms-full.txt). Generated by `+"`.github/pocketbase-docs`"+` from PocketOne's mirror of the
PocketBase site ([pocketonehq/pocketbase-site](https://github.com/pocketonehq/pocketbase-site)) at commit
`+"`%[2]s`"+` (%[3]s), which documents PocketBase %[4]s. Don't edit these files: run the command again.

PocketOne is PocketBase plus extensions. These pages describe PocketBase itself, and everything in them holds in a
PocketOne app unless PocketOne's own docs say otherwise. [PocketOne's docs](https://pocketone.dev/docs) cover what it adds
and changes (the JS engine, pocketone.yaml, the extensions). Each page starts with its pocketbase.io URL, and its links
point there. [jsvm/](jsvm/README.md) is the JSVM reference (pocketbase.io/jsvm): every function and type the JS hooks
see, with descriptions and examples.

## Pages

`, site, short(commit), date, version)
	for _, p := range pages {
		fmt.Fprintf(&b, "- [%s](%s.md)\n", p.title, p.slug)
	}
	b.WriteString(`
## License

The PocketBase site and its documentation are MIT licensed, Copyright (c) Gani Georgiev. PocketOne is not affiliated with
or endorsed by PocketBase or Gani Georgiev.
`)
	return b.String()
}

func writeIfChanged(path, content string) error {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, []byte(content)) {
		return nil
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// sh runs a command in dir with a time limit, its output going to ours.
func sh(dir string, limit time.Duration, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
		}
		return nil
	case <-time.After(limit):
		_ = cmd.Process.Kill()
		return fmt.Errorf("%s %s: gave up after %s", name, strings.Join(args, " "), limit)
	}
}

func output(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}
