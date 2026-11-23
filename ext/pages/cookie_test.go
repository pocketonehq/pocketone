package pages

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCookieExpires(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"c.ts": `module.exports = { get(ctx) {
			ctx.res.cookie('date', 'a', { expires: new Date(Date.UTC(2030, 0, 2, 3, 4, 5)) })
			ctx.res.cookie('ms', 'b', { expires: Date.UTC(2030, 0, 2, 3, 4, 5) })
			ctx.res.cookie('str', 'c', { expires: '2030-01-02T03:04:05Z' })
			ctx.res.cookie('both', 'd', { expires: new Date(Date.UTC(2030, 0, 2, 3, 4, 5)), maxAge: 60 })
			ctx.res.cookie('bad', 'e', { expires: 'soon' })
			return ctx.res.json({ ok: true })
		} }`,
	})
	h, _ := newServer(t, dir)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/c", nil))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	got := map[string]string{}
	for _, line := range rec.Result().Header.Values("Set-Cookie") {
		got[strings.SplitN(line, "=", 2)[0]] = line
	}
	const want = "Expires=Wed, 02 Jan 2030 03:04:05 GMT"
	for _, name := range []string{"date", "ms", "str", "both"} {
		if !strings.Contains(got[name], want) {
			t.Errorf("%s: %q, want it to contain %q", name, got[name], want)
		}
	}
	if !strings.Contains(got["both"], "Max-Age=60") {
		t.Errorf("both: %q, want Max-Age=60 too", got["both"])
	}
	if strings.Contains(got["bad"], "Expires=") || got["bad"] == "" {
		t.Errorf("bad: %q, want the cookie without Expires", got["bad"])
	}
}
