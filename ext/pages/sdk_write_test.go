package pages

import (
	"net/http/httptest"
	"testing"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
)

// ctx.pb() writes: create, update and delete go through the API (rules and validators apply), and a validation
// failure comes back as a ClientResponseError with the field errors. The SDK checks `body instanceof FormData` on
// every request with a body, which threw in QuickJS until Go constructors got a real prototype.
func TestJSSDKWrites(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"check.ts": `export function get(ctx: any) {
			const c = ctx.pb().collection('items')
			const created = c.create({ title: 'first' })
			const updated = c.update(created.id, { title: 'second' })
			let invalid: any = null
			try { c.create({ title: '' }) } catch (e: any) {
				invalid = { name: e.constructor.name, status: e.status, code: e.response?.data?.title?.code }
			}
			c.delete(created.id)
			return ctx.res.json({ created: created.title, updated: updated.title, invalid, left: c.getFullList().length })
		}`,
	})
	h, p := newServer(t, dir)
	col := core.NewBaseCollection("items")
	col.Fields.Add(&core.TextField{Name: "title", Required: true})
	open := ""
	col.ListRule, col.ViewRule, col.CreateRule, col.UpdateRule, col.DeleteRule = &open, &open, &open, &open, &open
	if err := p.app.Save(col); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/check", nil))
	want := `{"created":"first","invalid":{"code":"validation_required","name":"ClientResponseError","status":400},"left":0,"updated":"second"}`
	if got := rec.Body.String(); rec.Code != 200 || got != want {
		t.Fatalf("%d %s\nwant %s", rec.Code, got, want)
	}
}

// API rules still decide: a create the rule refuses fails as it would from a browser.
func TestJSSDKWriteRuleRefused(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"check.ts": `export function get(ctx: any) {
			try { ctx.pb().collection('locked').create({ title: 'x' }); return ctx.res.json({ ok: true }) }
			catch (e: any) { return ctx.res.json({ status: e.status }) }
		}`,
	})
	h, p := newServer(t, dir)
	col := core.NewBaseCollection("locked")
	col.Fields.Add(&core.TextField{Name: "title"})
	if err := p.app.Save(col); err != nil { // nil rules: superusers only
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/check", nil))
	if got := rec.Body.String(); rec.Code != 200 || got != `{"status":403}` {
		t.Fatalf("%d %s", rec.Code, got)
	}
}
