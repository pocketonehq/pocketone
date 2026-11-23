package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// tablePrefix is the prefix an extension's collections start with: `_<name>_`, hyphens as underscores (the same rule
// conformance.TablePrefix checks at run time).
func tablePrefix(name string) string { return "_" + strings.ReplaceAll(name, "-", "_") + "_" }

var quotedTable = regexp.MustCompile("[\"`](_[a-z0-9_]+)[\"`]")

// checkTablePrefixes: conformance sees one extension at a time, so it can't tell that `_llm_chat_x`, made by llm,
// sits under llm-chat's prefix. When one extension's prefix starts with another's (llm's `_llm_` and
// llm-chat's `_llm_chat_`), the shorter one may not name a table under the longer one's prefix.
func checkTablePrefixes() []string {
	entries, err := os.ReadDir("ext")
	if err != nil {
		return []string{err.Error()}
	}
	var names []string
	for _, e := range entries {
		if _, err := os.Stat("ext/" + e.Name() + "/go.mod"); e.IsDir() && err == nil {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	var problems []string
	for _, short := range names {
		var longer []string
		for _, long := range names {
			if long != short && strings.HasPrefix(tablePrefix(long), tablePrefix(short)) {
				longer = append(longer, long)
			}
		}
		if len(longer) == 0 {
			continue
		}
		dir := "ext/" + short
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path != dir {
					if _, err := os.Stat(path + "/go.mod"); err == nil {
						return filepath.SkipDir // a nested module
					}
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range quotedTable.FindAllStringSubmatch(string(src), -1) {
				for _, long := range longer {
					if strings.HasPrefix(m[1], tablePrefix(long)) {
						problems = append(problems, fmt.Sprintf("%s: table %q is under %s's prefix %s; name %s's tables so they don't start with it (docs/extensions.md, Table names)",
							filepath.ToSlash(path), m[1], long, tablePrefix(long), short))
					}
				}
			}
			return nil
		})
	}
	return problems
}
