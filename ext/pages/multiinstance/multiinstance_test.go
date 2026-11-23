// Package multiinstance tests pages under pocker: every instance serves its own pb_pages, isolated from the others.
package multiinstance

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"pocketone.dev/ext/contracts/instance/source/v1/sourcetest"
	"strings"
	"sync"
	"testing"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"pocketone.dev/ext/pages"
	"pocketone.dev/ext/pocker"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

type harness struct {
	m    *pocker.Manager
	root string
	mu   sync.Mutex
	apps map[string][]core.App
}

func start(t *testing.T, insts []pocker.Instance, files map[string]map[string]string, limits pocker.Limits) *harness {
	t.Helper()
	h := &harness{root: t.TempDir(), apps: map[string][]core.App{}}
	for id, f := range files {
		write(t, filepath.Join(h.root, id), f)
	}
	h.m = pocker.NewManager(sourcetest.ScopeOf(insts...), pocker.ManagerOptions{Standard: testStandard,
		PBRoot:   h.root,
		Logger:   quiet,
		Defaults: limits,
		Init:     pocker.InitExtensions(pages.New(pages.Config{})), // what pocker.Server wires for extkit extensions
		Setup: func(inst pocker.Instance, app core.App) error {
			h.mu.Lock()
			h.apps[inst.ID] = append(h.apps[inst.ID], app)
			h.mu.Unlock()
			return nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	go h.m.Run(ctx)
	t.Cleanup(func() { h.m.Shutdown(); cancel() })
	deadline := time.Now().Add(10 * time.Second)
	for !h.m.Ready() {
		if time.Now().After(deadline) {
			t.Fatal("manager not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return h
}

func (h *harness) get(t *testing.T, url string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.m.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	return rec.Code, rec.Body.String()
}

func (h *harness) latest(id string) core.App {
	h.mu.Lock()
	defer h.mu.Unlock()
	l := h.apps[id]
	if len(l) == 0 {
		return nil
	}
	return l[len(l)-1]
}

func inst(id string, extensions map[string]any) pocker.Instance {
	return pocker.Instance{ID: id, Hosts: []string{id + ".test"}, Extensions: extensions}
}

func TestInstancesAreIsolated(t *testing.T) {
	h := start(t,
		[]pocker.Instance{inst("a", nil), inst("b", nil), inst("off", map[string]any{"pages": map[string]any{"enabled": false}})},
		map[string]map[string]string{
			"a":   {"pb_pages/index.ejs": "site A", "pb_pages/only-a.ejs": "a only", "pb_pages/_private/x.ejs": "A partial"},
			"b":   {"pb_pages/index.tsx": "export default () => <p>site B</p>", "pb_pages/_private/x.ejs": "B partial", "pb_pages/inc.ejs": "<%- include('x') %>"},
			"off": {"pb_pages/index.ejs": "should not render"},
		},
		pocker.Limits{})

	if code, body := h.get(t, "http://a.test/"); code != 200 || body != "site A" {
		t.Fatalf("a: %d %q", code, body)
	}
	if code, body := h.get(t, "http://b.test/"); code != 200 || body != "<p>site B</p>" {
		t.Fatalf("b: %d %q", code, body)
	}
	if code, body := h.get(t, "http://b.test/inc"); code != 200 || body != "B partial" {
		t.Fatalf("b's include must use b's _private: %d %q", code, body)
	}
	if code, _ := h.get(t, "http://b.test/only-a"); code != 404 {
		t.Fatalf("b served a's page: %d", code)
	}
	if code, body := h.get(t, "http://off.test/"); code == 200 || strings.Contains(body, "should not render") {
		t.Fatalf("extensions.pages.enabled=false still served pages: %d %q", code, body)
	}
	if pages.For(h.latest("off")) != nil {
		t.Fatal("pages registered on an instance that turned it off")
	}
}

func TestSleepDropsCaches(t *testing.T) {
	h := start(t, []pocker.Instance{inst("a", nil)},
		map[string]map[string]string{"a": {"pb_pages/index.ejs": "v1"}}, pocker.Limits{})
	if _, body := h.get(t, "http://a.test/"); body != "v1" {
		t.Fatalf("first: %q", body)
	}
	before := pages.For(h.latest("a"))

	// stop the instance (what sleeping does) and change its page while it's down
	err := h.m.WithInstanceStopped(context.Background(), "a", func(dir string) error {
		return os.WriteFile(filepath.Join(dir, "pb_pages", "index.ejs"), []byte("v2"), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, body := h.get(t, "http://a.test/"); body != "v2" {
		t.Fatalf("after restart: %q", body)
	}
	after := pages.For(h.latest("a"))
	if after == nil || after == before {
		t.Fatal("a restarted app must get a new pages service (fresh route table and caches)")
	}
}

func TestSandboxHoldsInPages(t *testing.T) {
	limits := pocker.Limits{MaxJSExecution: 2 * time.Second, MaxJSAllocation: 64 << 20, MaxCallStackSize: 500}
	a := inst("a", nil)
	a.Dev = true // error pages say why, so the test can check each attack failed for the right reason
	h := start(t, []pocker.Instance{a, inst("b", nil)},
		map[string]map[string]string{
			"b": {"pb_pages/_private/secret.ejs": "B-SECRET", "pb_pages/secret.txt": "B-SECRET", "secret.txt": "B-SECRET"},
			"a": {
				"pb_pages/inc.ejs":           "<%- include('../../b/pb_pages/_private/secret') %>",
				"pb_pages/req.ejs":           "<% require('../../b/pb_pages/secret.txt') %>",
				"pb_pages/resolve.ejs":       "<%- ctx.resolve('../../b/secret.txt', { mode: 'raw' }) %>",
				"pb_pages/file.ts":           "module.exports = { get(ctx) { ctx.res.file('../../b/pb_pages/secret.txt') } }",
				"pb_pages/os.ejs":            "<%= $os.readFile('../b/secret.txt') %>",
				"pb_pages/asset.ejs":         "<%= asset('../../b/pb_pages/secret.txt') %>",
				"pb_pages/loop.ejs":          "<% while (true) {} %>",
				"pb_pages/alloc.ejs":         "<%= 'x'.repeat(1e9).length %>",
				"pb_pages/grow.ejs":          "<% const a = []; for (;;) a.push(new Uint8Array(1 << 20).fill(a.length)) %>",
				"pb_pages/recurse.ejs":       "<% function f() { return f() + 1 } f() %>",
				"pb_pages/self.ejs":          "<%- include('self') %>",
				"pb_pages/_private/self.ejs": "<%- include('self') %>",
				"pb_pages/ok.ejs":            "still fine",
			},
		},
		limits)

	// escapes: refused, and nothing of b's ever comes back
	escapes := map[string]string{
		"/inc":     "invalid name",     // include names can't climb out
		"/req":     "leaves pb_pages",  // require resolves inside pb_pages, never above it
		"/resolve": "invalid name",     // same rule as include
		"/file":    "leaves pb_pages",  // ctx.res.file stays inside pb_pages
		"/os":      "openat ../b/",     // $os file access is confined to the instance folder
		"/asset":   "../../b/pb_pages", // not a file in this pb_pages: returned unchanged, no lookup elsewhere
	}
	for path, want := range escapes {
		code, body := h.get(t, "http://a.test"+path)
		if strings.Contains(body, "B-SECRET") {
			t.Fatalf("%s leaked another instance's file: %d %q", path, code, body)
		}
		if !strings.Contains(body, want) {
			t.Fatalf("%s: want %q in the response, got %d %.300q", path, want, code, body)
		}
	}

	// runaway code: stopped by the instance's limits, quickly, with a 500.
	// /grow grows by 1 MiB buffers, not arrays of JS values: filling 4M values one by one took ~10s under -race (the
	// engine is translated C and every memory access is checked), and QuickJS consults the time limit only once per
	// 10000 calls and loop turns, so MaxJSExecution never stopped it first and it failed the 10s check on CI.
	runaway := map[string]string{
		"/loop":    "interrupted",              // MaxJSExecution
		"/alloc":   "out of memory",            // MaxJSAllocation: one huge allocation is refused
		"/grow":    "out of memory",            // steady growth: QuickJS's heap cap stops it before the time limit
		"/recurse": "recurse.ejs",              // MaxCallStackSize
		"/self":    "nested more than 32 deep", // pages' own include guard
	}
	for path, want := range runaway {
		start := time.Now()
		code, body := h.get(t, "http://a.test"+path)
		d := time.Since(start)
		t.Logf("%s: %d in %v", path, code, d.Round(time.Millisecond))
		if code != http.StatusInternalServerError || !strings.Contains(body, want) {
			t.Fatalf("%s: want a 500 mentioning %q, got %d %.300q", path, want, code, body)
		}
		if d > 10*time.Second {
			t.Fatalf("%s took %v; MaxJSExecution is 2s and the check allows 10s", path, d)
		}
	}
	if code, body := h.get(t, "http://a.test/ok"); code != 200 || body != "still fine" {
		t.Fatalf("instance broken after the attacks: %d %q", code, body)
	}
	if code, _ := h.get(t, "http://b.test/secret.txt"); code != 200 {
		t.Fatalf("b's own static file should still serve: %d", code)
	}
}
