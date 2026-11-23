package main

// The screenshot catalog (site/screenshots.json): every product screenshot in the docs, with what it shows, the pages
// that use it, the feature it depends on and how to take it again, so a change to a feature finds its screenshots.
// site/sync checks it on every run: each file exists, each listed page shows it, and every image under
// docs/screenshots is listed. Starter kit pages get their screenshots from it.

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	screenshotsFile = "site/screenshots.json"
	screenshotsDir  = "site/pb_pages/(main)/docs/screenshots"
)

type screenshot struct {
	File    string   `json:"file"`
	Shows   string   `json:"shows"`
	Pages   []string `json:"pages"`
	Feature string   `json:"feature"`
	Retake  string   `json:"retake"`
}

func loadScreenshots(root string) ([]screenshot, error) {
	raw, err := os.ReadFile(filepath.Join(root, screenshotsFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var doc struct {
		Screenshots []screenshot `json:"screenshots"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", screenshotsFile, err)
	}
	return doc.Screenshots, nil
}

// screenshotURL is where the site serves a screenshot file.
func screenshotURL(file string) string {
	if rest, ok := strings.CutPrefix(file, "site/pb_pages/(main)"); ok {
		return rest
	}
	return "/" + file
}

// screenshotsFor are the catalog's screenshots shown on a page, as Markdown images.
func screenshotsFor(shots []screenshot, page string) string {
	var b strings.Builder
	for _, s := range shots {
		for _, p := range s.Pages {
			if p == page {
				fmt.Fprintf(&b, "![%s](%s)\n\n", s.Shows, screenshotURL(s.File))
			}
		}
	}
	return b.String()
}

// checkScreenshots fails when the catalog and the docs disagree: a missing file, a listed page that doesn't show it,
// or an image under docs/screenshots the catalog doesn't list. pageText is a page's text by URL (generated pages from
// out, the rest from disk).
func checkScreenshots(root string, out map[string]string) error {
	shots, err := loadScreenshots(root)
	if err != nil {
		return err
	}
	listed := map[string]bool{}
	var problems []string
	for _, s := range shots {
		listed[s.File] = true
		if _, err := os.Stat(filepath.Join(root, s.File)); err != nil {
			problems = append(problems, s.File+": no such file")
		}
		if s.Shows == "" || s.Feature == "" || s.Retake == "" || len(s.Pages) == 0 {
			problems = append(problems, s.File+": needs shows, feature, retake and pages")
		}
		for _, p := range s.Pages {
			text, ok := pageText(root, out, p)
			if !ok {
				problems = append(problems, fmt.Sprintf("%s: page %s doesn't exist", s.File, p))
			} else if !strings.Contains(text, path.Base(s.File)) {
				problems = append(problems, fmt.Sprintf("%s: page %s doesn't show it", s.File, p))
			}
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, screenshotsDir))
	if err != nil {
		problems = append(problems, err.Error())
	}
	for _, e := range entries {
		if f := screenshotsDir + "/" + e.Name(); !e.IsDir() && !listed[f] {
			problems = append(problems, f+": not in "+screenshotsFile+" (say what it shows, where, the feature and how to retake it)")
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("screenshots:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}

// pageText is the source of the page at a site URL: a generated page from out, else the file on disk (a page's .md,
// or its folder's index.md, under (main)/docs, or an extension's docs page).
func pageText(root string, out map[string]string, url string) (string, bool) {
	rel := strings.TrimPrefix(url, "/")
	var cands []string
	cands = append(cands, "site/pb_pages/(main)/"+rel+".md", "site/pb_pages/(main)/"+rel+"/index.md")
	if r, ok := strings.CutPrefix(url, "/docs/extensions/"); ok {
		name, sub, _ := strings.Cut(r, "/")
		if sub == "" {
			sub = "index"
		}
		cands = append(cands, "ext/"+name+"/docs/"+sub+".md", "ext/"+name+"/docs/"+sub+"/index.md")
	}
	for _, c := range cands {
		if t, ok := out[c]; ok {
			return t, true
		}
		if raw, err := os.ReadFile(filepath.Join(root, c)); err == nil {
			return string(raw), true
		}
	}
	return "", false
}
