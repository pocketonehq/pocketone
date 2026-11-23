package pages

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRedirectStatus(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"a.ts":    "module.exports = { get(ctx) { ctx.res.redirect('/x') } }",
		"b.ts":    "module.exports = { get(ctx) { ctx.res.redirect('/x', 301) } }",
		"c.ts":    "module.exports = { get(ctx) { ctx.res.redirect('/x', { status: 303, flash: 'hi' }) } }",
		"bad.ts":  "module.exports = { get(ctx) { ctx.res.redirect('/x', 200) } }",
		"bad2.ts": "module.exports = { get(ctx) { ctx.res.redirect('/x', { status: '301' }) } }",
	})
	h, _ := newServer(t, dir)
	for p, want := range map[string]int{"/a": 302, "/b": 301, "/c": 303, "/bad": 500, "/bad2": 500} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if rec.Code != want {
			t.Errorf("%s: %d, want %d", p, rec.Code, want)
		}
		if want < 400 && rec.Header().Get("Location") != "/x" {
			t.Errorf("%s: Location %q", p, rec.Header().Get("Location"))
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/c", nil))
	if !strings.Contains(strings.Join(rec.Result().Header.Values("Set-Cookie"), ";"), "pp_flash=") {
		t.Error("/c: flash cookie not set")
	}
}
