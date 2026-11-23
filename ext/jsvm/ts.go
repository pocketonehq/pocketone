package jsvm

// TypeScript: .ts, .mts and .cts files (hooks, migrations, modules) go through esbuild, linked into the binary, which
// strips the types. .ts and .mts are ES modules (TypeScript is written that way); .cts is the CommonJS exception. There is no type checking at runtime; use tsc or your
// editor with types.d.ts for that. Each file's source map is kept, so error positions point at the .ts source.

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

// isTS reports a TypeScript file.
func isTS(name string) bool {
	switch filepath.Ext(name) {
	case ".ts", ".mts", ".cts":
		return !strings.HasSuffix(name, ".d.ts")
	}
	return false
}

// tsFormat is how esbuild emits a file: an ES module, a CommonJS module (import becomes require), or a script as
// written (a hook or migration script: type-only imports are dropped).
type tsFormat int

const (
	tsScript tsFormat = iota
	tsCommonJS
	tsESM
)

// transpile strips the types from a TypeScript file named name (as it shows in stack traces) and records its source
// map. lineOffset is how many lines the engine adds above the code before running it.
func (vm *VM) transpile(src, name string, format tsFormat, lineOffset int) (string, error) {
	opts := api.TransformOptions{
		Loader:     api.LoaderTS,
		Target:     api.ES2022,
		Sourcefile: name,
		Sourcemap:  api.SourceMapExternal,
	}
	switch format {
	case tsCommonJS:
		opts.Format = api.FormatCommonJS
	case tsESM:
		opts.Format = api.FormatESModule
	}
	res := api.Transform(src, opts)
	if len(res.Errors) > 0 {
		var b strings.Builder
		for i, m := range res.Errors {
			if i > 0 {
				b.WriteString("; ")
			}
			if m.Location != nil {
				fmt.Fprintf(&b, "%s:%d:%d: ", name, m.Location.Line, m.Location.Column+1)
			}
			b.WriteString(m.Text)
		}
		return "", fmt.Errorf("TypeScript: %s", b.String())
	}
	sm, err := parseSourceMap(res.Map, lineOffset)
	if err != nil {
		return "", fmt.Errorf("TypeScript: %s: source map: %w", name, err)
	}
	if vm.srcMaps == nil {
		vm.srcMaps = map[string]*sourceMap{}
	}
	vm.srcMaps[name] = sm
	return string(res.Code), nil
}

// ---- source maps ----

type mapping struct{ genCol, srcLine, srcCol int }

type sourceMap struct {
	lines      [][]mapping // by generated line (0-based), sorted by genCol
	lineOffset int
}

func parseSourceMap(raw []byte, lineOffset int) (*sourceMap, error) {
	var m struct {
		Mappings string `json:"mappings"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	sm := &sourceMap{lineOffset: lineOffset}
	srcLine, srcCol, src := 0, 0, 0
	for _, line := range strings.Split(m.Mappings, ";") {
		var segs []mapping
		genCol := 0
		for _, seg := range strings.Split(line, ",") {
			if seg == "" {
				continue
			}
			f, ok := decodeVLQ(seg)
			if !ok || len(f) < 1 {
				return nil, fmt.Errorf("bad mapping %q", seg)
			}
			genCol += f[0]
			if len(f) >= 4 {
				src += f[1]
				srcLine += f[2]
				srcCol += f[3]
				segs = append(segs, mapping{genCol, srcLine, srcCol})
			}
		}
		sm.lines = append(sm.lines, segs)
	}
	return sm, nil
}

const b64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

func decodeVLQ(s string) ([]int, bool) {
	var out []int
	val, shift := 0, 0
	for i := 0; i < len(s); i++ {
		d := strings.IndexByte(b64, s[i])
		if d < 0 {
			return nil, false
		}
		val += (d & 31) << shift
		if d&32 != 0 {
			shift += 5
			continue
		}
		if val&1 != 0 {
			out = append(out, -(val >> 1))
		} else {
			out = append(out, val>>1)
		}
		val, shift = 0, 0
	}
	return out, true
}

// lookup maps a 1-based generated line and column to the 1-based source position.
func (sm *sourceMap) lookup(line, col int) (int, int, bool) {
	gl := line - 1 - sm.lineOffset
	for ; gl >= 0; gl-- {
		if gl >= len(sm.lines) {
			continue
		}
		segs := sm.lines[gl]
		if len(segs) == 0 {
			continue
		}
		best := segs[0]
		if gl == line-1-sm.lineOffset {
			for _, s := range segs {
				if s.genCol <= col-1 {
					best = s
				}
			}
		} else {
			best = segs[len(segs)-1]
		}
		return best.srcLine + 1, best.srcCol + 1, true
	}
	return 0, 0, false
}

var stackPos = regexp.MustCompile(`([^\s()]+):(\d+):(\d+)`)

// remapStack rewrites file:line:col in a stack trace to TypeScript source positions.
func (vm *VM) remapStack(s string) string {
	if len(vm.srcMaps) == 0 || s == "" {
		return s
	}
	return stackPos.ReplaceAllStringFunc(s, func(m string) string {
		p := stackPos.FindStringSubmatch(m)
		sm := vm.srcMaps[p[1]]
		if sm == nil {
			return m
		}
		line, _ := strconv.Atoi(p[2])
		col, _ := strconv.Atoi(p[3])
		if l, c, ok := sm.lookup(line, col); ok {
			return p[1] + ":" + strconv.Itoa(l) + ":" + strconv.Itoa(c)
		}
		return m
	})
}
