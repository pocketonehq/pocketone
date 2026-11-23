package pages

import (
	"net/http/httptest"
	"testing"
)

// A module's require() belongs to the module, not to the request that first loaded it: modules are cached per VM,
// so a require() that runs at request time (inside a handler or a function) must work on every later request too.
// It used to resolve through the first request's pb_pages root, closed when that request ended, so from the second
// request on it failed with `require("x"): not found`.
func TestRequireAtRequestTime(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"_private/helper.ts": `module.exports = { twice: (n: number) => n * 2 }`,
		"_private/lazy.ts":   `module.exports = { get: () => require('helper').twice(5) }`,
		"_private/data.json": `{ "n": 7 }`,
		"check.ts": `module.exports = {
			get(ctx: any) {
				return ctx.res.json({ a: require('helper').twice(21), b: require('lazy').get(), c: require('data').n })
			},
		}`,
	})
	h, _ := newServer(t, dir)
	for i := 1; i <= 3; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/check", nil))
		if got := rec.Body.String(); rec.Code != 200 || got != `{"a":42,"b":10,"c":7}` {
			t.Fatalf("request %d: %d %s", i, rec.Code, got)
		}
	}
}
