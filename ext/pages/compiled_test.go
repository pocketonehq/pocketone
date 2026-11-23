package pages

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	cachev1 "pocketone.dev/ext/contracts/cache/v1"
)

// mapCache is a cache/v1 provider for tests: one map shared by every app it's provided on, counting writes.
type mapCache struct {
	mu   sync.Mutex
	m    map[string][]byte
	sets int
}

func (c *mapCache) Bucket(name string, _ cachev1.Options) (cachev1.Bucket, error) {
	return mapBucket{c, name}, nil
}

func (c *mapCache) Clear(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	clear(c.m)
	return nil
}

type mapBucket struct {
	c    *mapCache
	name string
}

func (b mapBucket) Get(_ context.Context, k string) ([]byte, bool, error) {
	b.c.mu.Lock()
	defer b.c.mu.Unlock()
	v, ok := b.c.m[b.name+"/"+k]
	return append([]byte(nil), v...), ok, nil
}

func (b mapBucket) Set(_ context.Context, k string, v []byte, _ time.Duration) error {
	b.c.mu.Lock()
	defer b.c.mu.Unlock()
	b.c.m[b.name+"/"+k] = append([]byte(nil), v...)
	b.c.sets++
	return nil
}

func (b mapBucket) Delete(context.Context, string) error { return nil }
func (b mapBucket) Clear(context.Context) error          { return nil }

// Compiled templates and modules go to cache/v1's pages.compiled bucket, and a VM that hasn't met a file takes it
// from there instead of compiling it, front matter included.
func TestCompiledFilesUseCache(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"index.ts":           "const h = require('./_private/helper'); module.exports = { load(ctx) { return { v: h.twice(21) } } }",
		"_private/helper.js": "exports.twice = (n) => n * 2",
		"index.md":           "---\ntitle: Cached\n---\n<%= meta('title') %> <%= data.v %>",
	})
	c := &mapCache{m: map[string][]byte{}}
	serve := func() (int, string) {
		t.Helper()
		h, p := newServer(t, dir)
		cachev1.Contract.Provide(p.app, c) // the scope seals itself on the first lookup, at request time
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
		b, _ := io.ReadAll(rec.Result().Body)
		return rec.Code, string(b)
	}

	code, body := serve()
	if code != 200 || !strings.Contains(body, "Cached 42") {
		t.Fatalf("first app: %d %s", code, body)
	}
	written := c.sets
	if written < 3 { // the page, its index.ts and the helper
		t.Fatalf("%d compiled files cached, want 3", written)
	}

	// a new app has new VMs: everything comes from the cache, with the page's front matter
	code, body = serve()
	if code != 200 || !strings.Contains(body, "Cached 42") {
		t.Fatalf("second app: %d %s", code, body)
	}
	if c.sets != written {
		t.Fatalf("second app compiled %d file(s) again; want every one from the cache", c.sets-written)
	}
}

func TestCompiledEncoding(t *testing.T) {
	in := &compiledFile{src: "(function(){})", fmRaw: "title: x\nn: 2"}
	out, err := decodeCompiled(in.encode())
	if err != nil || out.src != in.src || out.fmRaw != in.fmRaw || out.frontMatter["title"] != "x" || out.frontMatter["n"] != 2 {
		t.Fatalf("round trip: %+v %v", out, err)
	}
	for _, bad := range [][]byte{nil, {0, 0, 0}, {0, 0, 0, 9, 'x'}} {
		if _, err := decodeCompiled(bad); err == nil {
			t.Fatalf("%v decoded", bad)
		}
	}
}
