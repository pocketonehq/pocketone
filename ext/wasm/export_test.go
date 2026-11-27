package wasm

import (
	"context"
	"os"
	"testing"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
)

// For the tests only (this file isn't part of the package outside go test): modules are otherwise called only from
// pb_hooks.

// Apps in the tests that don't set CacheDir share one compilation cache for the whole run, in a temporary folder
// removed at the end, so the suite compiles each module once instead of once per test app.
//
// P1_WASM_TEST_CACHE names a folder to use instead, kept after the run, so later runs load the modules instead of
// compiling them. CI sets it: under -race, wazero compiles a Go module about ten times slower (the whole suite takes
// ~4 min instead of ~30 s), and its machine code doesn't depend on -race, so go-cache.yml fills the folder with a run
// without -race and the -race runs load from it. Tests about compiling (TestCold, TestCompilationCache) use their own
// cache or none, so they still compile.
func TestMain(m *testing.M) {
	dir, keep := os.Getenv("P1_WASM_TEST_CACHE"), true
	if dir == "" {
		var err error
		if dir, err = os.MkdirTemp("", "ext-wasm-cache-"); err != nil {
			panic(err)
		}
		keep = false
	}
	defaultCacheDir = func(core.App) (string, error) { return dir, nil }
	code := m.Run()
	if !keep {
		os.RemoveAll(dir)
	}
	os.Exit(code)
}

// AppCacheDir is where CacheDir points when it isn't set (outside the tests, which share one folder).
var AppCacheDir = appCacheDir

// CacheMaxAge is how old a cache file gets before the cache prunes it.
const CacheMaxAge = cacheMaxAge

// PruneCache deletes the files in dir older than maxAge.
var PruneCache = pruneCache

// Call runs a module function directly.
func (h *Host) Call(ctx context.Context, name, function string, input []byte) ([]byte, error) {
	return h.run(ctx, name, function, input)
}

// Config is the settings the Host runs with.
func (h *Host) Config() Config { return h.cfg }

// ProducersLanguage is the language a "producers" custom section names.
var ProducersLanguage = producersLanguage

// Truncate cuts a message to at most n bytes, never inside a UTF-8 character.
var Truncate = truncate
