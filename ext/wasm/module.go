package wasm

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
	"pocketone.dev/ext/extkit"
)

// A module is a WASI program (wasip1), the kind any language with a WASI target builds from a plain main: its input
// on stdin, its output on stdout, and an exit code that says whether it worked. It gets the function asked for as
// its first argument (os.Args[1] in Go), and nothing else: no files, no environment, no network. wasip1 has no
// call that opens a connection, and the host gives it no folder, no variables and no sockets.

// Compiled code is kept on disk, not in memory: every call runs in a runtime of its own and closes its compiled
// module after, so a module costs memory only while it runs, and wazero's file cache (Config.CacheDir) keeps the
// machine code between calls. A module's first call compiles it (~1 s for a Go module, ~30 ms for Rust or Zig) and
// writes the result; later calls, in this process or after a restart, load it from the file (~40 ms for Go, 1–5 ms for
// Rust, Zig, C or TinyGo). The cache is keyed by the module's bytes, so a changed module compiles again and identical
// modules in one cache folder share one entry.

// cacheMaxAge is how old a cache file may get before it's deleted, when the process first opens the cache: wazero
// never deletes its entries, and every version of every module adds one. A module still in use compiles again then,
// once.
const cacheMaxAge = 30 * 24 * time.Hour

var (
	cacheMu sync.Mutex
	caches  = map[string]wazero.CompilationCache{} // one per folder, for the life of the process
)

// cacheFor opens the compilation cache in dir (creating it), shared by every Host that names the same folder.
func cacheFor(dir string) (wazero.CompilationCache, error) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if c, ok := caches[dir]; ok {
		return c, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	pruneCache(dir, cacheMaxAge)
	c, err := wazero.NewCompilationCacheWithDir(dir)
	if err != nil {
		return nil, err
	}
	caches[dir] = c
	return c, nil
}

// pruneCache deletes the files in dir older than maxAge (entries of old modules, and of older wazero versions).
func pruneCache(dir string, maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().Before(cutoff) {
			os.Remove(path)
		}
		return nil
	})
}

// defaultCacheDir is where Config.CacheDir points when it isn't set: the app's pb_cache/wasm. The tests point it at
// one temporary folder for the whole run (export_test.go).
var defaultCacheDir = appCacheDir

func appCacheDir(app core.App) (string, error) { return extkit.CacheDir(app, "wasm") }

// newRuntime builds a runtime for one call: the memory cap, WASI, and a run that stops when its context ends. With a
// cache, the call must close its compiled module when it's done: the cache's engine outlives the runtime, and would
// keep the module's machine code in memory otherwise.
func newRuntime(ctx context.Context, maxCallMemoryMB int, cache wazero.CompilationCache) (wazero.Runtime, error) {
	cfg := wazero.NewRuntimeConfig().
		WithMemoryLimitPages(uint32(maxCallMemoryMB) * 16). // 64 KiB pages
		WithCloseOnContextDone(true)
	if cache != nil {
		cfg = cfg.WithCompilationCache(cache)
	}
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		rt.Close(ctx)
		return nil, err
	}
	return rt, nil
}

// compiling keeps calls of the same module from compiling it at the same moment: with a shared cache, wazero (v1.12)
// puts a module it loads from the cache folder in its shared engine before the module is ready, and a call compiling
// the same bytes then crashes (index out of range in moduleEngine.NewFunction). Its docs also advise against
// compiling one module in parallel. Reported upstream as https://github.com/wazero/wazero/issues/2547, with a fix
// proposed in https://github.com/wazero/wazero/pull/2574; once a wazero release has it, this lock can go (run
// TestMaxModulesMB, whose calls of one module run side by side, to check). The lock is per module content (wazero
// keys its cache the same way, so identical modules in two instances share it), and only held while compiling or
// loading, never while the module runs; calls of other modules never wait for it. An entry lives while calls use it.
var (
	compilingMu sync.Mutex
	compiling   = map[[sha256.Size]byte]*compileLock{}
)

// compileLock is one module's lock: a one-slot channel, so a call waiting for it can give up when its context ends.
type compileLock struct {
	slot  chan struct{}
	users int // calls holding or waiting for it
}

// compile compiles code in rt (or loads it from rt's cache), one call of the same module at a time. A call that
// gives up waiting (its context ended) returns the context's error.
func compile(ctx context.Context, rt wazero.Runtime, code []byte) (wazero.CompiledModule, error) {
	sum := sha256.Sum256(code)
	compilingMu.Lock()
	l := compiling[sum]
	if l == nil {
		l = &compileLock{slot: make(chan struct{}, 1)}
		compiling[sum] = l
	}
	l.users++
	compilingMu.Unlock()
	defer func() {
		compilingMu.Lock()
		if l.users--; l.users == 0 {
			delete(compiling, sum)
		}
		compilingMu.Unlock()
	}()

	select {
	case l.slot <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-l.slot }()
	return rt.CompileModule(ctx, code)
}

// checkCode checks a module when its file is listed, so a broken one shows right away, not on its first call, and
// asks it for its manifest: the module run once as `<name> --describe`, with nothing on stdin and the limits of a
// call. It uses wazero's interpreter, which decodes and validates without making machine code: ~0.25 s for a Go
// module, against ~1.7 s to compile it. err is why the module can't run; describeErr what's wrong with what it
// printed for --describe (the module still runs, with the defaults).
func checkCode(code []byte, name string, maxCallMemoryMB int, timeout time.Duration) (language string, m Manifest, describeErr, err error) {
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigInterpreter().
		WithMemoryLimitPages(uint32(maxCallMemoryMB)*16).WithCustomSections(true).WithCloseOnContextDone(true))
	defer rt.Close(ctx)
	mod, err := rt.CompileModule(ctx, code)
	if err != nil {
		return "", m, nil, err
	}
	if err := checkModule(mod); err != nil {
		return "", m, nil, err
	}
	for _, s := range mod.CustomSections() {
		if s.Name() == "producers" {
			language = producersLanguage(s.Data())
			break
		}
	}
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		return "", m, nil, err
	}
	m, describeErr = describe(ctx, rt, mod, name, timeout)
	return language, m, describeErr, nil
}

// describe runs the module as `<name> --describe` and reads its manifest. A module that exits non-zero or prints
// nothing doesn't answer --describe: the zero Manifest, with no error.
func describe(ctx context.Context, rt wazero.Runtime, mod wazero.CompiledModule, name string, timeout time.Duration) (Manifest, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	r := runModule(ctx, rt, mod, name, describeFlag, nil, maxManifest)
	switch {
	case ctx.Err() != nil:
		return Manifest{}, fmt.Errorf("over the time limit (%s)", timeout)
	case r.tooLarge:
		return Manifest{}, fmt.Errorf("over %d KB", maxManifest>>10)
	case r.err != nil || len(bytes.TrimSpace(r.stdout)) == 0:
		return Manifest{}, nil
	}
	m, err := parseManifest(r.stdout)
	if err != nil {
		return Manifest{}, err // nothing of it shown: the defaults
	}
	return m, nil
}

// producersLanguage is the source language a module's "producers" section names (the tool-conventions format: Go
// writes it, Rust too unless stripped), or "". C is skipped: it's WASI's libc in a Rust or Zig program (Zig names
// only C99), not what the module was written in.
func producersLanguage(b []byte) string {
	// the section is LEB128 counts (binary.Uvarint) and length-prefixed strings; ok turns false past the end, for good
	ok := true
	num := func() uint64 {
		v, n := binary.Uvarint(b)
		if n <= 0 {
			b, ok = nil, false
			return 0
		}
		b = b[n:]
		return v
	}
	str := func() string {
		n := num()
		if !ok || n > uint64(len(b)) {
			b, ok = nil, false
			return ""
		}
		s := string(b[:n])
		b = b[n:]
		return s
	}
	for range min(num(), 16) {
		field := str()
		for range min(num(), 32) {
			name, _ := str(), str()
			if field == "language" && ok && name != "" && !isC(name) && validLanguage.MatchString(name) {
				return name
			}
		}
	}
	return ""
}

func isC(lang string) bool {
	return lang == "C" || (len(lang) > 1 && lang[0] == 'C' && (lang[1] >= '0' && lang[1] <= '9' || lang[1] == '+'))
}

// checkModule refuses a module that can't run here: one that imports anything but WASI (built for host functions
// this host doesn't have, such as an Extism PDK's), or a library with no main to run.
func checkModule(m wazero.CompiledModule) error {
	for _, f := range m.ImportedFunctions() {
		mod, name, _ := f.Import()
		if mod == "wasi_snapshot_preview1" {
			continue
		}
		if strings.HasPrefix(mod, "extism:") {
			return fmt.Errorf("imports %s.%s: an Extism plugin; a module here is a WASI program that reads stdin and writes stdout", mod, name)
		}
		return fmt.Errorf("imports %s.%s, which this host doesn't provide (modules get only their input)", mod, name)
	}
	if m.ExportedFunctions()["_start"] == nil {
		return errors.New("not a WASI program (no _start): build it from a main, not as a library (in Go, without -buildmode=c-shared)")
	}
	return nil
}

// result is what a program run left: its output, what it wrote to stderr, how it ended.
type result struct {
	stdout   []byte
	stderr   string
	exitCode uint32
	tooLarge bool  // stdout went over the limit
	err      error // a trap, the context ending, or a non-zero exit (a *sys.ExitError)
}

// runModule runs the module once, in a fresh instance: args [name, function], input on stdin.
func runModule(ctx context.Context, rt wazero.Runtime, m wazero.CompiledModule, name, function string, input []byte, maxOutput int) result {
	out := &capped{max: maxOutput, strict: true}
	errOut := &capped{max: maxStderr}
	mod, err := rt.InstantiateModule(ctx, m, wazero.NewModuleConfig().
		WithName(""). // anonymous: one instance per call
		WithArgs(name, function).
		WithStdin(bytes.NewReader(input)).
		WithStdout(out).WithStderr(errOut).
		WithSysWalltime().WithSysNanotime().
		WithRandSource(rand.Reader))
	if mod != nil {
		mod.Close(context.Background())
	}
	r := result{stdout: out.b.Bytes(), stderr: errOut.b.String(), tooLarge: out.over}
	var exit *sys.ExitError
	if errors.As(err, &exit) {
		r.exitCode = exit.ExitCode()
		if r.exitCode == 0 {
			err = nil
		}
	}
	r.err = err
	return r
}

const maxStderr = 64 << 10

// capped keeps up to max bytes. Past that, a strict one (stdout) fails the write, so the program sees its output
// refused; the other (stderr) drops the rest quietly.
type capped struct {
	b      bytes.Buffer
	max    int
	strict bool
	over   bool
}

func (c *capped) Write(p []byte) (int, error) {
	if c.over || c.b.Len()+len(p) > c.max {
		c.over = true
		if room := c.max - c.b.Len(); room > 0 {
			c.b.Write(p[:room])
		}
		if c.strict {
			return 0, errors.New("output over the limit")
		}
		return len(p), nil
	}
	return c.b.Write(p)
}
