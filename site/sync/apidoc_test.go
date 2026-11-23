package main

import (
	"strings"
	"testing"
)

const apidocFixture = `/**
 * The store.
 */
declare var $kv: {
  /**
   * The value stored under a name.
   *
   * @param name the value's name
   * @returns the value, or null
   * @example
   * const v = $kv.get("a")
   * @go kvv1.Get
   */
  get(name: string): any
}

/** An entry. */
interface KvEntry {
  /** The name. */
  name: string
  value?: any
}

declare namespace pages {
  /** The request context. */
  interface Context {
    /** True in dev mode. */
    dev: boolean
    env(name: string): string
  }
  type Mode = "a" | "b"
  namespace inner {
    type Opts = {
      x: number
    }
  }
}
`

func TestParseJSAPI(t *testing.T) {
	api := parseJSAPI(apidocFixture)

	es := api.Entries["$kv"]
	if len(es) != 1 || es[0].Path != "$kv.get" {
		t.Fatalf("entries = %+v", es)
	}
	d := es[0].Doc
	if d.Summary != "The value stored under a name." || len(d.Params) != 1 || d.Params[0].Name != "name" ||
		d.Returns != "the value, or null" || len(d.Examples) != 1 || len(d.Go) != 1 || d.Go[0] != "kvv1.Get" {
		t.Fatalf("doc = %+v", d)
	}
	if got := es[0].Sig.display("$kv."); !strings.Contains(got, "get(name: string): any") {
		t.Fatalf("display = %q", got)
	}

	types := map[string]apiType{}
	for _, ty := range api.Types {
		types[ty.Name] = ty
	}
	for _, name := range []string{"KvEntry", "pages.Context", "pages.Mode", "pages.inner.Opts"} {
		if _, ok := types[name]; !ok {
			t.Errorf("type %s missing; have %v", name, func() (n []string) {
				for k := range types {
					n = append(n, k)
				}
				return
			}())
		}
	}
	if c := types["pages.Context"]; len(c.Fields) != 2 || c.Doc.Summary != "The request context." {
		t.Errorf("pages.Context = %+v", c)
	}
	if m := types["pages.Mode"]; m.Text != `"a" | "b"` {
		t.Errorf("pages.Mode text = %q", m.Text)
	}
	if o := types["pages.inner.Opts"]; len(o.Fields) != 1 {
		t.Errorf("pages.inner.Opts = %+v", o)
	}
}

func TestGoRefKV(t *testing.T) {
	p, ok, err := goRefRead("../..", "ext/contracts/kv/v1", "pocketone.dev/ext/contracts/kv/v1", goRefURL("ext/contracts/kv/v1"))
	if err != nil || !ok {
		t.Fatal("kv/v1 not read:", err)
	}
	for _, want := range []string{"# kvv1", "### Get\n", `<a id="maxnamelength"></a>`, "](#get)", "Example:\n\n```go"} {
		if !strings.Contains(p.Page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if strings.Contains(p.Page, "](#Get)") {
		t.Error("in-page links not lowercased")
	}
	if l := goRefLink([]goRefPackage{p}, "kvv1.Get"); l != "/docs/reference/go/kv/v1#get" {
		t.Errorf("goRefLink = %q", l)
	}
}
