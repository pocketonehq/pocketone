package pages

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEndpoints(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"+middleware.js": "module.exports = (ctx, next) => { ctx.seen = 'mw'; return next() }",
		"logout.ts":      "module.exports = { post(ctx) { ctx.res.redirect('/') } }",
		"hello.ts":       "module.exports = { get(ctx) { ctx.res.html('<p>hi from js, ' + ctx.seen + '</p>') } }",
		"users/[x].ts":   "module.exports = { get(ctx) { ctx.res.html('user ' + ctx.params.x) } }",
		"silent.ts":      "module.exports = { get(ctx) {} }",
		"boom.ts":        "module.exports = { get(ctx) { throw ctx.httpError(418, 'teapot') } }",
		// a .ts beside a template is that page's code
		"shop/about.ejs": "about <%= data.n %>",
		"shop/about.ts":  "module.exports = { load() { return { n: 1 } } }",
		// .js is a browser script, served as-is
		"app.js": "console.log('hi')",
	})
	h, _ := newServer(t, dir)
	do := func(method, p, accept string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, p, nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := do("POST", "/logout", ""); rec.Code != 302 || rec.Header().Get("Location") != "/" {
		t.Fatalf("POST /logout: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := do("GET", "/logout", ""); rec.Code != 405 || rec.Header().Get("Allow") != "POST" {
		t.Fatalf("GET /logout: %d Allow %q", rec.Code, rec.Header().Get("Allow"))
	}
	if rec := do("GET", "/hello", ""); rec.Code != 200 || rec.Body.String() != "<p>hi from js, mw</p>" {
		t.Fatalf("GET /hello: %d %q", rec.Code, rec.Body.String())
	}
	if rec := do("GET", "/users/ben", ""); rec.Code != 200 || rec.Body.String() != "user ben" {
		t.Fatalf("GET /users/ben: %d %q", rec.Code, rec.Body.String())
	}
	if rec := do("GET", "/silent", ""); rec.Code != 500 { // dev mode shows "returned without sending a response"
		t.Fatalf("GET /silent: %d %.200q", rec.Code, rec.Body.String())
	}
	if rec := do("GET", "/shop/about", ""); rec.Code != 200 || rec.Body.String() != "about 1" {
		t.Fatalf("GET /shop/about: %d %q", rec.Code, rec.Body.String())
	}
	if rec := do("GET", "/app.js", ""); rec.Code != 200 || rec.Body.String() != "console.log('hi')" {
		t.Fatalf("GET /app.js: %d %q", rec.Code, rec.Body.String())
	}

	// an endpoint's errors are JSON, unless the request puts HTML first (a browser navigating to it)
	for accept, wantJSON := range map[string]bool{
		"":                             true,
		"*/*":                          true,
		"application/json":             true,
		"text/event-stream, text/html": true, // Datastar
		"text/html,application/xhtml+xml;q=0.9,*/*;q=0.8": false,
		"application/xhtml+xml":                           false,
	} {
		rec := do("GET", "/boom", accept)
		isJSON := strings.Contains(rec.Header().Get("Content-Type"), "json")
		if rec.Code != 418 || isJSON != wantJSON || !strings.Contains(rec.Body.String(), "teapot") {
			t.Errorf("Accept %q: %d %q %q", accept, rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
		}
	}
}

func TestPageRendersForAnyMethod(t *testing.T) {
	dir := writeTree(t, map[string]string{
		// a form handled in the template, like PHP: no .ts
		"contact.ejs": "<% if (ctx.req.method === 'POST') { %>thanks <%= ctx.req.body().name %><% } else { %>form<% } %>",
		// code with only load: other methods still render
		"shop/index.ejs": "<%= ctx.req.method %> <%= data.n %>",
		"shop/index.ts":  "module.exports = { load() { return { n: 1 } } }",
	})
	h, _ := newServer(t, dir)
	do := func(method, p, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, p, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := do("GET", "/contact", ""); rec.Code != 200 || rec.Body.String() != "form" {
		t.Fatalf("GET /contact: %d %q", rec.Code, rec.Body.String())
	}
	if rec := do("POST", "/contact", `{"name":"Ben"}`); rec.Code != 200 || rec.Body.String() != "thanks Ben" {
		t.Fatalf("POST /contact: %d %q", rec.Code, rec.Body.String())
	}
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		if rec := do(m, "/shop", ""); rec.Code != 200 || rec.Body.String() != m+" 1" {
			t.Fatalf("%s /shop: %d %q", m, rec.Code, rec.Body.String())
		}
	}
}
