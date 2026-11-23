package pages

import (
	"net/http/httptest"
	"testing"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
)

// ctx.pb()'s list calls return records, not lists of lists: getFullList (over several batches), getList and
// getFirstListItem.
func TestJSSDKLists(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"check.ts": `export function get(ctx: any) {
			const c = ctx.pb().collection('items')
			const all = c.getFullList({ sort: 'n' })
			const batched = c.getFullList({ sort: 'n', batch: 2 })
			const page = c.getList(1, 2, { sort: 'n' })
			const first = c.getFirstListItem('n = 3')
			return ctx.res.json({
				all: all.map((r: any) => r.n), allIsArray: Array.isArray(all),
				batched: batched.map((r: any) => r.n),
				page: page.items.map((r: any) => r.n), first: first.n,
			})
		}`,
	})
	h, p := newServer(t, dir)
	col := core.NewBaseCollection("items")
	col.Fields.Add(&core.NumberField{Name: "n"})
	open := ""
	col.ListRule, col.ViewRule = &open, &open
	if err := p.app.Save(col); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		r := core.NewRecord(col)
		r.Set("n", i)
		if err := p.app.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/check", nil))
	want := `{"all":[1,2,3,4,5],"allIsArray":true,"batched":[1,2,3,4,5],"first":3,"page":[1,2]}`
	if got := rec.Body.String(); rec.Code != 200 || got != want {
		t.Fatalf("%d %s\nwant %s", rec.Code, got, want)
	}
}
