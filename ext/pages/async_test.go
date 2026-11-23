package pages

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

// Page code may be async: the driver awaits what load, handlers, middleware, templates and components return.
// countRecords.async runs off the loop and settles through it, so these tests need the event loop to run.

// asyncCount is an expression that awaits a real off-loop call and checks it against the sync one.
const asyncCount = `((await ctx.app.countRecords.async('_superusers')) === ctx.app.countRecords('_superusers') ? 'ok' : 'mismatch')`

func doRequest(t *testing.T, dir, method, path string) (int, string) {
	t.Helper()
	h, p := newServer(t, dir)
	dev := true
	p.cfg.Dev = &dev
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	b, _ := io.ReadAll(rec.Result().Body)
	return rec.Code, string(b)
}

func wantBody(t *testing.T, code int, body string, wants ...string) {
	t.Helper()
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Fatalf("missing %q in\n%s", w, body)
		}
	}
}

func TestAsyncLoad(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"index.ts":  "export default { async load(ctx: any) { ctx.data.n = " + asyncCount + "; return { m: await Promise.resolve(2) } } }",
		"index.ejs": "n=<%= data.n %> m=<%= data.m %>",
	})
	code, body := get(t, dir, "/", true)
	wantBody(t, code, body, "n=ok m=2")
}

func TestAsyncMethodHandler(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"index.ts": `module.exports = {
			async load(ctx) { ctx.data.trail = ['load'] },
			async post(ctx) { await null; ctx.data.trail.push('post'); return { posted: ` + asyncCount + ` } },
		}`,
		"index.ejs": "<%= data.trail.join(',') %> <%= data.posted %>",
	})
	code, body := doRequest(t, dir, "POST", "/")
	wantBody(t, code, body, "load,post ok")
}

func TestAsyncAPIHandler(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"feed.ts": "export async function get(ctx: any) { return ctx.res.json({ n: " + asyncCount + " }) }",
		"none.ts": "module.exports = { async get(ctx) { await null } }",
		"err.ts":  "module.exports = { async get(ctx) { await null; throw ctx.httpError(418, 'teapot') } }",
	})
	code, body := get(t, dir, "/feed", true)
	wantBody(t, code, body, `"n":"ok"`)
	// an async handler that sends nothing is still the usual error, checked once it settles
	code, body = get(t, dir, "/none", true)
	if code != 500 || !strings.Contains(body, "returned without sending a response") {
		t.Fatalf("%d %s", code, body)
	}
	// a rejected handler answers like a throwing one
	code, body = get(t, dir, "/err", true)
	if code != 418 || !strings.Contains(body, "teapot") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestAsyncMiddleware(t *testing.T) {
	dir := writeTree(t, map[string]string{
		// async, awaits next() and sees the rest of the request finish
		"+middleware.js": `module.exports = async (ctx, next) => {
			ctx.data.trail = ['root:' + ` + asyncCount + `]
			await next()
			ctx.log.info('after', ctx.res.sent)
		}`,
		// sync, calls next() without returning it: the async rest is still awaited
		"a/+middleware.js": "module.exports = (ctx, next) => { ctx.data.trail.push('a'); next() }",
		"a/index.ts":       "module.exports = { async load(ctx) { await null; ctx.data.trail.push('load') } }",
		"a/index.ejs":      "<%= data.trail.join(',') %>",
		// async, catches what next() throws
		"b/+middleware.js": `module.exports = async (ctx, next) => {
			try { await next() } catch (e) { ctx.res.html('caught ' + e.message) }
		}`,
		"b/index.ts":  "module.exports = { async load(ctx) { await null; throw new Error('boom') } }",
		"b/index.ejs": "never",
		// async, ends the request without next() or a response
		"c/+middleware.js": "module.exports = async (ctx, next) => { await null }",
		"c/index.ejs":      "never",
	})
	code, body := get(t, dir, "/a", true)
	wantBody(t, code, body, "root:ok,a,load")
	code, body = get(t, dir, "/b", true)
	wantBody(t, code, body, "caught boom")
	code, body = get(t, dir, "/c", true)
	if code != 500 || !strings.Contains(body, "without calling next()") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestAsyncEJS(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"+layout.ejs":                  "<title><%- slot('title') %></title><%- await Promise.resolve('L') %>[<%- slot() %>]",
		"index.ejs":                    "<% const n = " + asyncCount + " %>n=<%= n %> <%- await include('part', { x: 1 }) %> <%- include('sync') %>",
		"_private/part.ejs":            "part<%= await Promise.resolve(x) %>",
		"_private/sync.ejs":            "sync",
		"slots.ejs":                    "<% await slot('title', async () => { %>T<%= await Promise.resolve('!') %><% }) %>body",
		"bad.ejs":                      "<%- include('part', { x: 1 }) %>",
		"err.ejs":                      "line1\n<% await null %>\n<%= nope.x %>",
		"md.md":                        "# <%= await Promise.resolve('Hi') %>",
		"_private/unused-awaitish.ejs": "<%= 'await' %>",
	})
	code, body := get(t, dir, "/", true)
	wantBody(t, code, body, "L[n=ok part1 sync]")
	code, body = get(t, dir, "/slots", true)
	wantBody(t, code, body, "<title>T!</title>L[body]")
	code, body = get(t, dir, "/md", true)
	wantBody(t, code, body, "<h1")
	if !strings.Contains(body, "Hi</h1>") {
		t.Fatalf("markdown after await: %s", body)
	}
	// a sync template printing an async include gets an error, not [object Promise]
	code, body = get(t, dir, "/bad", true)
	if code != 500 || !strings.Contains(body, "await include") {
		t.Fatalf("%d %s", code, body)
	}
	// errors after an await still name the template line
	code, body = get(t, dir, "/err", true)
	if code != 500 || !strings.Contains(body, "err.ejs:3") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestEJSWithoutAwaitStaysSync(t *testing.T) {
	for src, async := range map[string]bool{
		"<%= x %>":                           false,
		"<%# await nothing %>":               false,
		"awaited text outside tags":          false,
		"<% const awaited = 1 %>":            false,
		"<%= await x %>":                     true,
		"<% for await (const r of rs) {} %>": true,
	} {
		code, err := CompileEJS(src)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.HasPrefix(code, "(async function"); got != async {
			t.Errorf("%q: async = %v, want %v", src, got, async)
		}
	}
}

func TestAsyncJSX(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"+layout.tsx": `export default async function Layout({ children, slot }: any) {
			const t = await Promise.resolve('Site')
			return <html><head><title>{slot('title') || t}</title></head><body>{children}</body></html>
		}`,
		"_private/Count.tsx": `export default async function Count({ ctx }: any) {
			return <b>{` + asyncCount + `}</b>
		}`,
		"_private/part.ejs": "ejs<%= await Promise.resolve('!') %>",
		"index.tsx": `import Count from './_private/Count'
			async function Item({ n }: { n: number }) { await null; return <li>{n}</li> }
			export default function Page({ ctx, include, slot }: any) {
				slot('title', <Item n={0} />)
				return <main>
					<Count ctx={ctx} />
					<ul>{[1, 2, 3].map((n) => <Item n={n} />)}</ul>
					<>{include('part')}</>
					<p>sync</p>
				</main>
			}`,
	})
	code, body := get(t, dir, "/", true)
	wantBody(t, code, body,
		"<title><li>0</li></title>",
		"<main><b>ok</b><ul><li>1</li><li>2</li><li>3</li></ul>ejs!<p>sync</p></main>",
	)
}
