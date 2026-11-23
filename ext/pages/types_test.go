package pages_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/tests"

	"pocketone.dev/ext/jsvm"
	"pocketone.dev/ext/pages"
)

// The app's types.d.ts carries the pages declarations when the extension is on.
func TestTypesInAppTypesFile(t *testing.T) {
	got, _ := appTypes(t)
	for _, want := range []string{"declare namespace pages", "interface Context", "declare namespace JSX"} {
		if !strings.Contains(got, want) {
			t.Fatalf("types.d.ts should contain %q", want)
		}
	}
}

// tsc type-checks page code written against the declarations: middleware, page code, endpoints, +config with a JS plugin,
// a TSX page and a partial with typed locals, an augmented ctx.data. Skipped when tsc isn't installed.
func TestTypesCheckWithTSC(t *testing.T) {
	files := map[string]string{
		"pb_pages/_private/types.d.ts": `
			declare namespace pages {
				interface Data { section?: string; product?: core.Record }
				interface Context { hello(): string }
			}
		`,
		"pb_pages/+config.ts": `
			const hello: pages.PluginFactory = (config) => ({
				name: "hello",
				extendContext(ctx) { ctx.hello = () => "hi " + (config.who ?? "you") },
			})
			export default { plugins: ["auth", "realtime", { factory: hello, who: "Ben" }], debug: false } satisfies pages.Config
		`,
		"pb_pages/admin/+middleware.ts": `
			const mw: pages.Middleware = async (ctx, next) => {
				if (!ctx.auth) return ctx.res.redirect("/login", { flash: "Sign in first" })
				ctx.data.section = "admin"
				ctx.log.info("admin", ctx.req.method, ctx.req.url.pathname, ctx.hello())
				return await next()
			}
			export default mw
		`,
		"pb_pages/products/[id]/index.ts": `
			export default {
				async load(ctx) {
					ctx.data.product = await ctx.app.findRecordById.async("products", ctx.params.id)
					const tab: string | undefined = ctx.req.query("tab")
					const tags: string[] = ctx.req.query("tag", { all: true })
					const prefs = ctx.req.cookie<{ dark: boolean }>("prefs", { json: true })
					ctx.meta("title", ctx.data.product.getString("name"))
					return { tab, tags, dark: prefs?.dark ?? false }
				},
				post(ctx) {
					const { qty } = ctx.req.body<{ qty: number }>()
					const files = ctx.req.files("photo")
					ctx.realtime.send("cart", { qty, n: files.length }, { filter: (c) => c.hasSubscription("cart") })
					return ctx.res.status(303).redirect("/cart/" + ctx.params.id)
				},
			} satisfies pages.Handlers
		`,
		"pb_pages/feeds.ts": `
			export default {
				get(ctx) {
					if (ctx.req.header("x-key") !== ctx.env("FEED_KEY")) throw ctx.httpError(403, "no")
					const s = ctx.res.sse()
					s.send("hello", { at: Date.now() }, { id: 1 })
					s.close()
				},
				post(ctx) {
					const { record } = ctx.session.signInWithPassword("a@b.c", "pw")
					const html: string = ctx.include("card", { record })
					const lib = ctx.resolve<{ twice(n: number): number }>("lib")
					return ctx.res.json(201, { html, n: lib.twice(2), css: ctx.resolve("app.css", { mode: "style" }) })
				},
			} satisfies pages.Handlers
		`,
		"pb_pages/_private/Card.tsx": `
			export default function Card({ title, children }: { title: string; children?: any }) {
				return <div class="card" hx-get="/x"><h2>{title}</h2>{children}</div>
			}
		`,
		"pb_pages/_private/badge.tsx": `
			export default function Badge({ label, asset }: pages.Props<{ label: string }>) {
				return <span class="badge"><img src={asset("logo.png")} />{label}</span>
			}
		`,
		"pb_pages/index.tsx": `
			import Card from "./_private/Card.tsx"
			export default async function Home({ ctx, data, slot }: pages.Props) {
				slot("title", "Home")
				const n = await ctx.app.countRecords.async("products")
				const badge = ctx.include("badge", { label: "new" })
				return <html><body><Card title={data.section ?? "home"}>{n}<span dangerouslySetInnerHTML={{ __html: badge }} /></Card></body></html>
			}
		`,
	}
	out, err := runTSC(t, files)
	if err != nil {
		t.Fatalf("tsc: %v\n%s", err, out)
	}

	// The types catch mistakes: a misspelled ctx member, a wrong redirect argument, a missing component prop.
	for name, src := range map[string]string{
		"pb_pages/bad1.ts":  `export default { load(ctx) { ctx.reqq.method } } satisfies pages.Handlers`,
		"pb_pages/bad2.ts":  `const m: pages.Middleware = (ctx, next) => ctx.res.redirect(42)`,
		"pb_pages/bad3.tsx": `import Card from "./_private/Card.tsx"; export default () => <Card />`,
	} {
		bad := map[string]string{name: src}
		for k, v := range files {
			bad[k] = v
		}
		out, err := runTSC(t, bad)
		if err == nil || strings.Count(out, "error TS") != 1 || !strings.Contains(out, filepath.Base(name)+"(") {
			t.Fatalf("tsc should reject %s, and only it:\n%s", name, out)
		}
	}
}

// appTypes is the types.d.ts and tsconfig.json the JS engine writes for an app with pages on.
func appTypes(t *testing.T) (types, tsconfig string) {
	t.Helper()
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	dir := t.TempDir()
	if err := jsvm.Register(app, jsvm.Config{
		HooksDir:      filepath.Join(dir, "pb_hooks"),
		MigrationsDir: filepath.Join(dir, "pb_migrations"),
		TypesDir:      dir,
		Extensions:    []any{pages.New(pages.Config{})},
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "types.d.ts"))
	if err != nil {
		t.Fatal(err)
	}
	c, err := os.ReadFile(filepath.Join(dir, "tsconfig.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b), string(c)
}

func runTSC(t *testing.T, files map[string]string) (string, error) {
	t.Helper()
	tsc, err := exec.LookPath("tsc")
	if err != nil {
		t.Skip("tsc isn't installed")
	}
	dir := t.TempDir()
	types, tsconfig := appTypes(t)
	// the files as the engine writes them in the project folder, so its own tsconfig.json is what's tested
	all := map[string]string{"types.d.ts": types, "tsconfig.json": tsconfig}
	for k, v := range files {
		all[k] = v
	}
	for name, content := range all {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, tsc, "-p", dir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// The tsx starter kit's and the example app's TypeScript (.ts, .tsx) type-checks under the docs' tsconfig.
func TestTypesKitsAndExample(t *testing.T) {
	for _, src := range []string{"starters/tsx/pb_pages", "example/pb_pages"} {
		t.Run(src, func(t *testing.T) {
			files := map[string]string{}
			err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				if ext := filepath.Ext(p); ext == ".ts" || ext == ".tsx" {
					b, err := os.ReadFile(p)
					if err != nil {
						return err
					}
					rel, _ := filepath.Rel(src, p)
					files[filepath.ToSlash(filepath.Join("pb_pages", rel))] = string(b)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if out, err := runTSC(t, files); err != nil {
				t.Fatalf("tsc: %v\n%s", err, out)
			}
		})
	}
}
