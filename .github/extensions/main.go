// Command extensions is CI for PocketOne's extension modules (ext/*, examples/pocketone): which modules a change affects,
// and the rules every extension module follows. Run it from the repository root:
//
//	go run ./.github/extensions affected <base> <head>   # module dirs to build and test, one per line
//	go run ./.github/extensions check [dir...]            # extension rules (default: every module)
//	go run ./.github/extensions examples                  # copy each example/main.go into its README's ## Example
//	go run ./.github/extensions contracts                 # write each contract README's provides/requires/works-with index
//	go run ./.github/extensions approvers <base> <head>   # who may approve each area a change touches (owners at base)
//	go run ./.github/extensions test [dir...]             # what CI runs on a pull request, locally (default: what you changed)
//	go run ./.github/extensions release vX.Y.Z            # cut a release: raise each extension with unreleased changes, stamp the changelogs
//	go run ./.github/extensions release-notes vX.Y.Z      # a release's notes: its section of CHANGELOG.md, links made absolute
//
// A module is affected when a file in it changes (Markdown aside), or when a module it reaches through a `replace`
// is affected. A change to core (the root module) or to this tool affects every module.
//
// The rules (`check`), per module:
//   - a module with an extkit extension (a `Register(app core.App, _ extkit.Config) error` method) has a test
//     that calls conformance.Run (docs/extensions.md).
//   - every extension (ext/<name>) has a README.md with `## Install`, `## Configure`, `## Use`, `## Integrations` and
//     `## Example` sections, in that order; it is the extension's page on pocketone.dev (docs/extensions.md). When the
//     extension has example/main.go, the Example section shows it exactly, in a ```go block (`examples` writes it).
//   - every contract (ext/contracts/<id path>/vN) has a README with owners and a summary in its front matter and
//     `## Use`, `## Provide`, `## Extensions` and `## License` sections; its Extensions section lists, generated from
//     every extension's Contracts(), who provides, requires and works with it (`contracts` writes it). Every contract
//     an extension declares exists. These run with every `check`.
package main

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "affected":
		if len(os.Args) != 4 {
			usage()
		}
		dirs, err := affected(os.Args[2], os.Args[3])
		if err != nil {
			fail(err)
		}
		for _, d := range dirs {
			fmt.Println(d)
		}
	case "check":
		dirs := os.Args[2:]
		if len(dirs) == 0 {
			mods, err := modules()
			if err != nil {
				fail(err)
			}
			dirs = mods
		}
		problems := check(dirs)
		problems = append(problems, checkContracts()...)
		problems = append(problems, checkOwnership()...)
		problems = append(problems, checkChangelogs()...)
		problems = append(problems, checkTablePrefixes()...)
		if len(problems) > 0 {
			for _, p := range problems {
				fmt.Println("::error::" + p)
			}
			os.Exit(1)
		}
		fmt.Printf("ok: %d modules follow the extension rules\n", len(dirs))
	case "approvers":
		if len(os.Args) != 4 {
			usage()
		}
		list, err := approvers(os.Args[2], os.Args[3])
		if err != nil {
			fail(err)
		}
		for _, a := range list {
			fmt.Printf("%s: any of %s (%s)\n", a.Area, strings.Join(a.AnyOf, ", "), a.Reason)
		}
	case "test":
		if err := runTest(os.Args[2:]); err != nil {
			fail(err)
		}
	case "quick":
		if err := runQuick(os.Args[2:]); err != nil {
			fail(err)
		}
	case "release":
		if len(os.Args) < 3 || len(os.Args) > 4 {
			usage()
		}
		date := ""
		if len(os.Args) == 4 {
			date = os.Args[3]
		}
		changed, err := release(os.Args[2], date)
		if err != nil {
			fail(err)
		}
		fmt.Printf("ok: %d files stamped for %s; review and commit them\n", len(changed), os.Args[2])
	case "release-notes":
		if len(os.Args) != 3 {
			usage()
		}
		notes, err := releaseNotes(os.Args[2])
		if err != nil {
			fail(err)
		}
		fmt.Print(notes)
	case "contracts":
		changed, err := syncContracts()
		if err != nil {
			fail(err)
		}
		for _, f := range changed {
			fmt.Println("wrote", f)
		}
		fmt.Printf("ok: %d contract READMEs updated\n", len(changed))
	case "examples":
		changed, err := syncExamples()
		if err != nil {
			fail(err)
		}
		for _, f := range changed {
			fmt.Println("wrote", f)
		}
		fmt.Printf("ok: %d READMEs updated\n", len(changed))
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: extensions affected <base> <head> | extensions check [dir...] | extensions examples | extensions contracts | extensions approvers <base> <head> | extensions test [dir...] | extensions quick [dir...] | extensions release vX.Y.Z [YYYY-MM-DD] | extensions release-notes vX.Y.Z")
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "extensions:", err)
	os.Exit(1)
}

// modules lists the extension-side module dirs (relative, slash-separated): every go.mod under ext/ and examples/.
func modules() ([]string, error) {
	var out []string
	for _, root := range []string{"ext", "examples"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (d.Name() == "node_modules" || d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			if !d.IsDir() && d.Name() == "go.mod" {
				out = append(out, filepath.ToSlash(filepath.Dir(path)))
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

// owner is the module a file belongs to: the deepest module dir containing it, or "." (core).
func owner(file string, mods []string) string {
	best := "."
	for _, m := range mods {
		if strings.HasPrefix(file, m+"/") && len(m) > len(best) {
			best = m
		}
	}
	return best
}

var replaceLine = regexp.MustCompile(`=>\s*(\.[^\s]*)`)

// deps maps each module dir to the module dirs it reaches through local `replace` directives ("." is core).
func deps(mods []string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, m := range mods {
		f, err := os.Open(filepath.Join(m, "go.mod"))
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			if !strings.Contains(line, "=>") {
				continue
			}
			sub := replaceLine.FindStringSubmatch(line)
			if sub == nil {
				continue
			}
			target := filepath.ToSlash(filepath.Clean(filepath.Join(m, sub[1])))
			out[m] = append(out[m], target)
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// changedFiles lists the files that differ from base to head; with head "", from base to the working tree, new
// (untracked) files included.
func changedFiles(base, head string) ([]string, error) {
	args := []string{"diff", "--name-only", base}
	if head != "" {
		args = append(args, head)
	}
	b, err := exec.Command("git", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	files := strings.Fields(string(b))
	if head == "" {
		u, err := exec.Command("git", "ls-files", "--others", "--exclude-standard").Output()
		if err != nil {
			return nil, err
		}
		files = append(files, strings.Fields(string(u))...)
	}
	return files, nil
}

func affected(base, head string) ([]string, error) {
	mods, err := modules()
	if err != nil {
		return nil, err
	}
	files, err := changedFiles(base, head)
	if err != nil {
		return nil, err
	}
	hit := map[string]bool{}
	all := false
	for _, f := range files {
		if f == "" || strings.HasSuffix(f, ".md") {
			continue
		}
		o := owner(f, mods)
		if o != "." {
			hit[o] = true
			continue
		}
		// the root module: core code, or this CI. Root-level files that aren't code (docs, .claude/) don't count.
		if strings.HasPrefix(f, ".github/extensions/") || f == ".github/workflows/extensions.yml" || isCore(f) {
			all = true
		}
	}
	if all {
		return mods, nil
	}
	// a module is affected when anything it replaces (transitively) is
	dep, err := deps(mods)
	if err != nil {
		return nil, err
	}
	var reaches func(m string, seen map[string]bool) bool
	reaches = func(m string, seen map[string]bool) bool {
		if hit[m] {
			return true
		}
		if seen[m] {
			return false
		}
		seen[m] = true
		for _, d := range dep[m] {
			if reaches(d, seen) {
				return true
			}
		}
		return false
	}
	var out []string
	for _, m := range mods {
		if reaches(m, map[string]bool{}) {
			out = append(out, m)
		}
	}
	return out, nil
}

// isCore matches .github/core-audit/core-diff.sh: everything PocketOne doesn't own.
func isCore(f string) bool {
	for _, p := range []string{"ext/", "site/", "examples/pocketone/", "docs/", ".github/", ".claude/"} {
		if strings.HasPrefix(f, p) {
			return false
		}
	}
	switch f {
	case "README.md", "AGENTS.md", "CLAUDE.md", "CONTRIBUTING.md", "CHANGELOG.md", "CHANGELOG.pocketbase.md":
		return false
	}
	return true
}

var (
	extensionMethod = regexp.MustCompile(`(?m)^func \(\w+ \*?\w+\) Register\(\w+ core\.App, \w+ extkit\.Config\) error`)
	conformance     = regexp.MustCompile(`conformance\.(Run|CheckLifecycle)\(`)
)

// check applies the extension rules to each module dir.
func check(dirs []string) []string {
	var problems []string
	mods, err := modules()
	if err != nil {
		problems = append(problems, "list the modules: "+err.Error())
	}
	for _, dir := range dirs {
		dir = strings.TrimSuffix(filepath.ToSlash(dir), "/")
		var extensions []string
		tested := false
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			p := filepath.ToSlash(path)
			if d.IsDir() {
				if p != dir && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".") || owner(p+"/x", mods) != dir) {
					return filepath.SkipDir // another module, or not code
				}
				return nil
			}
			if !strings.HasSuffix(p, ".go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.HasSuffix(p, "_test.go") {
				tested = tested || conformance.Match(src)
			} else if extensionMethod.Match(src) {
				extensions = append(extensions, p)
			}
			return nil
		})
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", dir, err))
			continue
		}
		if len(extensions) > 0 && !tested {
			problems = append(problems, fmt.Sprintf("%s: implements extkit.Extension (%s) but has no test calling conformance.Run; add conformance_test.go (docs/extensions.md)", dir, strings.Join(extensions, ", ")))
		}
		problems = append(problems, checkDocs(dir)...)
	}
	// Extension folders with no Go module yet (a README for a planned extension) get the docs rules too.
	entries, err := os.ReadDir("ext")
	if err != nil {
		problems = append(problems, err.Error())
	}
	for _, e := range entries {
		dir := "ext/" + e.Name()
		if _, err := os.Stat(dir + "/go.mod"); e.IsDir() && err != nil {
			problems = append(problems, checkDocs(dir)...)
		}
	}
	return problems
}

// docsExempt are extension folders that aren't something a user installs, configures or uses, with the reason.
var docsExempt = map[string]string{
	"extkit":    "the extension contract, a library for extension authors (docs/extensions.md covers it)",
	"contracts": "the interfaces extensions use to work together, a library (its README and docs/extensions.md cover it)",
	"devusers":  "test users for examples and tests only; never in a production build",
}

// docsPending are extensions written before the README sections were required; remove each when its README has them.
// New extensions don't go here.
var docsPending = map[string]bool{}

// requiredSections are the README sections every extension has, in this order.
var requiredSections = []string{"Install", "Configure", "Use", "Integrations", "Example"}

// licensedMIT: the README's last H2 is "License" and its body is exactly "MIT". Every extension in this repo is MIT,
// pending docs or not.
func licensedMIT(src string, h2 []string) bool {
	if len(h2) == 0 || h2[len(h2)-1] != "License" {
		return false
	}
	i := strings.LastIndex(src, "\n## License")
	return i >= 0 && strings.TrimSpace(src[i+len("\n## License"):]) == "MIT"
}

// checkDocs: an extension folder (ext/<name>) has a README with the required sections, in order.
func checkDocs(dir string) []string {
	name, ok := strings.CutPrefix(dir, "ext/")
	if !ok || strings.Contains(name, "/") {
		return nil // not an extension folder (examples/pocketone, or a module inside an extension)
	}
	if _, ok := docsExempt[name]; ok {
		return nil
	}
	src, err := os.ReadFile(dir + "/README.md")
	if err != nil {
		return []string{dir + "/README.md: missing (docs/extensions.md)"}
	}
	var h2 []string
	fence := false
	for _, line := range strings.Split(string(src), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fence = !fence
		}
		if !fence && strings.HasPrefix(line, "## ") {
			h2 = append(h2, strings.TrimSpace(line[3:]))
		}
	}
	next := 0
	for _, h := range h2 {
		if next < len(requiredSections) && h == requiredSections[next] {
			next++
		}
	}
	complete := next == len(requiredSections)
	if !licensedMIT(string(src), h2) {
		return []string{dir + "/README.md: must end with a `## License` section that says `MIT`; extensions in this repo are MIT (host another license in your own repo) (docs/extensions.md)"}
	}
	switch {
	case !complete && !docsPending[name]:
		return []string{fmt.Sprintf("%s/README.md: needs the sections ## %s, in that order; it is the extension's page on pocketone.dev (docs/extensions.md)",
			dir, strings.Join(requiredSections, ", ## "))}
	case complete && docsPending[name]:
		return []string{fmt.Sprintf("%s: its README has the required sections now; remove %q from docsPending in .github/extensions/main.go", dir, name)}
	case complete:
		if want, err := os.ReadFile(dir + "/example/main.go"); err == nil {
			if _, got, ok := exampleBlock(string(src)); !ok || got != string(want) {
				return []string{dir + "/README.md: its ## Example section must show example/main.go exactly, in a ```go block; run `go run ./.github/extensions examples` (docs/extensions.md)"}
			}
		}
	}
	return nil
}

// exampleBlock finds the first ```go block in the README's "## Example" section: where its body starts and ends in src
// (start, end), and the body.
func exampleBlock(src string) (span [2]int, body string, ok bool) {
	i := strings.Index(src, "\n## Example\n")
	if i < 0 {
		return span, "", false
	}
	sec := i + len("\n## Example\n")
	end := len(src)
	if j := strings.Index(src[sec:], "\n## "); j >= 0 {
		end = sec + j
	}
	k := strings.Index(src[sec:end], "```go\n")
	if k < 0 {
		return span, "", false
	}
	start := sec + k + len("```go\n")
	m := strings.Index(src[start:end], "\n```")
	if m < 0 {
		return span, "", false
	}
	return [2]int{start, start + m + 1}, src[start : start+m+1], true
}

// syncExamples copies each extension's example/main.go into its README's ## Example section (replacing the section's
// first ```go block, or adding one at the end of the section) and returns the READMEs it changed.
func syncExamples() ([]string, error) {
	dirs, err := filepath.Glob("ext/*/example/main.go")
	if err != nil {
		return nil, err
	}
	var changed []string
	for _, f := range dirs {
		dir := filepath.ToSlash(filepath.Dir(filepath.Dir(f)))
		readme := dir + "/README.md"
		raw, err := os.ReadFile(readme)
		if err != nil {
			continue
		}
		code, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		src := string(raw)
		var out string
		if span, body, ok := exampleBlock(src); ok {
			if body == string(code) {
				continue
			}
			out = src[:span[0]] + string(code) + src[span[1]:]
		} else {
			i := strings.Index(src, "\n## Example\n")
			if i < 0 {
				continue // no section yet: checkDocs reports it
			}
			sec := i + len("\n## Example\n")
			end := len(src)
			if j := strings.Index(src[sec:], "\n## "); j >= 0 {
				end = sec + j
			}
			out = strings.TrimRight(src[:end], "\n") + "\n\n```go\n" + string(code) + "```\n" + src[end:]
		}
		if err := os.WriteFile(readme, []byte(out), 0o644); err != nil {
			return nil, err
		}
		changed = append(changed, readme)
	}
	return changed, nil
}
