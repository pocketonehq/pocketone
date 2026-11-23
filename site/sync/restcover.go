package main

// The REST API pages (site/pb_pages/(main)/docs/rest) are hand-written, so this checks they cover the API: every route
// core registers under /api (read from apis/*.go: each Group's prefix and the routes added to it) must appear on one
// of them as "`METHOD /api/path`". An upstream sync that adds an endpoint fails site/sync until a page names it.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const restPagesDir = "site/pb_pages/(main)/docs/rest"

var (
	restGroupRe = regexp.MustCompile(`(\w+)\s*:?=\s*(\w+)\.Group\("([^"]*)"\)`)
	restRouteRe = regexp.MustCompile(`(\w+)\.(GET|POST|PATCH|PUT|DELETE)\("([^"]*)"`)
)

// coreRoutes reads "METHOD /api/path" for every route core registers under /api.
func coreRoutes(root string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(root, "apis", "*.go"))
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		prefix := map[string]string{"rg": "/api"} // the api group every bind function gets
		for _, line := range strings.Split(string(raw), "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "//") {
				continue
			}
			if m := restGroupRe.FindStringSubmatch(t); m != nil {
				if base, ok := prefix[m[2]]; ok {
					prefix[m[1]] = base + m[3]
				}
				continue
			}
			if m := restRouteRe.FindStringSubmatch(t); m != nil {
				if base, ok := prefix[m[1]]; ok {
					seen[m[2]+" "+base+m[3]] = true
				}
			}
		}
	}
	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out, nil
}

// checkRESTCoverage fails when a core route is on no REST page.
func checkRESTCoverage(root string) error {
	routes, err := coreRoutes(root)
	if err != nil {
		return err
	}
	pages, err := filepath.Glob(filepath.Join(root, restPagesDir, "*.md"))
	if err != nil {
		return err
	}
	if len(pages) == 0 {
		return fmt.Errorf("no REST pages in %s", restPagesDir)
	}
	var all strings.Builder
	for _, p := range pages {
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		all.Write(raw)
	}
	text := all.String()
	var missing []string
	for _, r := range routes {
		if !strings.Contains(text, "`"+r+"`") && !strings.Contains(text, "`"+r+"?") {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("these core API routes are on no page in %s (name each as `METHOD /api/path`):\n  %s",
			restPagesDir, strings.Join(missing, "\n  "))
	}
	return nil
}
