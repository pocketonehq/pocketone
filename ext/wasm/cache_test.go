package wasm_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"pocketone.dev/ext/extkit"
	"pocketone.dev/ext/wasm"
)

// cacheFiles counts the compiled modules in a cache folder.
func cacheFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// TestCompilationCache: a module's first call compiles it and writes its machine code to CacheDir; later calls, and
// a restarted app with the same CacheDir, load it from there instead of compiling. Another module adds its own entry,
// and "off" turns the cache off.
func TestCompilationCache(t *testing.T) {
	// not parallel: it times calls, and a parallel test compiling the same module holds its compile lock
	dir := t.TempDir()
	cfg := wasm.Config{CacheDir: dir}
	e := withBasic(t, cfg)
	if got := e.h.Config().CacheDir; got != dir {
		t.Fatalf("CacheDir %q, want %q", got, dir)
	}

	timed := func(e env, name string, input any) time.Duration {
		t.Helper()
		start := time.Now()
		if _, err := call(t, e, name, "run", input); err != nil {
			t.Fatal(err)
		}
		return time.Since(start)
	}
	first := timed(e, "basic", map[string]string{"text": "x"}) // a Go module: compiling takes most of a second
	if n := cacheFiles(t, dir); n != 1 {
		t.Fatalf("%d files in the cache after the first call, want 1", n)
	}
	second := timed(e, "basic", map[string]string{"text": "x"})
	t.Logf("first call %s, second %s", first.Round(time.Millisecond), second.Round(time.Millisecond))
	if second*4 > first {
		t.Fatalf("second call took %s, the first %s: it compiled again", second, first)
	}

	restarted := newApp(t, cfg, func(_ core.App, d string) { install(t, d, "basic", module(t, basic)) })
	if again := timed(restarted, "basic", map[string]string{"text": "x"}); again*4 > first {
		t.Fatalf("first call after a restart took %s: it compiled again", again)
	}

	install(t, e.dir, "summary", prebuiltModule(t, "example/summary/zig"))
	timed(e, "summary", map[string]string{"title": "t", "body": "a b"})
	if n := cacheFiles(t, dir); n != 2 {
		t.Fatalf("%d files in the cache after a second module, want 2", n)
	}

	// a Zig module: compiles in milliseconds (a Go one takes ~20 s under -race), and this only checks where it writes
	off := newApp(t, wasm.Config{CacheDir: "off"}, func(_ core.App, d string) {
		install(t, d, "summary", prebuiltModule(t, "example/summary/zig"))
	})
	timed(off, "summary", map[string]string{"title": "t", "body": "a b"})
	if n := cacheFiles(t, dir); n != 2 {
		t.Fatalf("a call with CacheDir off wrote to another cache (%d files)", n)
	}
}

// TestPruneCache: cache files older than CacheMaxAge are deleted when the process opens the cache; newer ones stay.
func TestPruneCache(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "wazero-v1-arch-os")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	old, fresh := filepath.Join(sub, "old"), filepath.Join(sub, "fresh")
	for _, p := range []string{old, fresh} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	then := time.Now().Add(-wasm.CacheMaxAge - time.Hour)
	if err := os.Chtimes(old, then, then); err != nil {
		t.Fatal(err)
	}
	wasm.PruneCache(dir, wasm.CacheMaxAge)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatalf("old file kept (%v)", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh file deleted: %v", err)
	}
}

// TestDefaultCacheDir: CacheDir defaults to the app's pb_cache/wasm, never a folder that needs $HOME: next to pb_data
// for a single app, and for an instance of a multisite host with a cache root (Pocker) outside the instance's folder.
func TestDefaultCacheDir(t *testing.T) {
	t.Parallel()
	single := core.NewBaseApp(core.BaseAppConfig{DataDir: filepath.Join(t.TempDir(), "pb_data")})
	if d, err := wasm.AppCacheDir(single); err != nil || d != filepath.Join(filepath.Dir(single.DataDir()), "pb_cache", "wasm") {
		t.Fatalf("single app: %q, %v", d, err)
	}
	root := t.TempDir()
	host := extkit.NewScope("multisite")
	host.SetCacheRoot(filepath.Join(root, "pb_cache"))
	inst := core.NewBaseApp(core.BaseAppConfig{DataDir: filepath.Join(root, "i1", "pb_data")})
	extkit.AttachHost(inst, host, "i1")
	if d, err := wasm.AppCacheDir(inst); err != nil || d != filepath.Join(root, "pb_cache", "instances", "i1", "wasm") {
		t.Fatalf("instance: %q, %v", d, err)
	}
}
