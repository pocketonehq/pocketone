package pages

import (
	"net/http/httptest"
	"strings"
	"testing"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
)

func TestSignInRefusesMFA(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"+config.js": "module.exports = { plugins: ['auth'] }",
		"pw.ts": `module.exports = { post(ctx) {
			const b = ctx.req.body()
			try { ctx.session.signInWithPassword(b.email, b.password) } catch (e) { return ctx.res.json({ error: String(e), auth: !!ctx.auth }) }
			return ctx.res.json({ ok: true, auth: !!ctx.auth })
		} }`,
		"anon.ts": `module.exports = { post(ctx) {
			try { ctx.session.signInAnonymously() } catch (e) { return ctx.res.json({ error: String(e) }) }
			return ctx.res.json({ ok: true })
		} }`,
		"otp.ts": `module.exports = { post(ctx) {
			const b = ctx.req.body()
			try { ctx.session.signInWithOTP(b.id, b.code) } catch (e) { return ctx.res.json({ error: String(e) }) }
			return ctx.res.json({ ok: true })
		} }`,
	})
	h, p := newServer(t, dir)
	post := func(path, body string) (string, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		h.ServeHTTP(rec, req)
		return rec.Body.String(), rec.Header().Get("Set-Cookie")
	}
	const creds = `{"email":"test@example.com","password":"1234567890"}`
	const refused = "needs a second sign-in step (MFA)"

	// MFA on for everyone
	setMFA(t, p, true, "")
	if body, cookie := post("/pw", creds); !strings.Contains(body, refused) || strings.Contains(body, `"auth":true`) || strings.Contains(cookie, "pb_auth=%7B") {
		t.Fatalf("password with MFA: %s (cookie %q)", body, cookie)
	}
	// a wrong password says so, without revealing MFA
	if body, _ := post("/pw", `{"email":"test@example.com","password":"nope"}`); !strings.Contains(body, "Invalid login credentials") {
		t.Fatalf("wrong password: %s", body)
	}
	users, _ := p.app.FindCollectionByNameOrId("users")
	before, _ := p.app.CountRecords(users)
	if body, _ := post("/anon", ""); !strings.Contains(body, refused) {
		t.Fatalf("anonymous with MFA: %s", body)
	}
	if after, _ := p.app.CountRecords(users); after != before {
		t.Fatalf("anonymous sign-in left a record behind: %d -> %d", before, after)
	}

	// OTP: refused, and the code isn't used up
	user, _ := p.app.FindAuthRecordByEmail(users, "test@example.com")
	otp := core.NewOTP(p.app)
	otp.SetCollectionRef(users.Id)
	otp.SetRecordRef(user.Id)
	otp.SetPassword("123456")
	if err := p.app.Save(otp); err != nil {
		t.Fatal(err)
	}
	users.OTP.Enabled = true
	_ = p.app.Save(users)
	if body, _ := post("/otp", `{"id":"`+otp.Id+`","code":"123456"}`); !strings.Contains(body, refused) {
		t.Fatalf("OTP with MFA: %s", body)
	}
	if _, err := p.app.FindOTPById(otp.Id); err != nil {
		t.Fatalf("the OTP was consumed: %v", err)
	}

	// MFA rule that matches this user: refused; one that doesn't: signed in
	setMFA(t, p, true, `email = "test@example.com"`)
	if body, _ := post("/pw", creds); !strings.Contains(body, refused) {
		t.Fatalf("matching MFA rule: %s", body)
	}
	setMFA(t, p, true, `email = "someone-else@example.com"`)
	if body, _ := post("/pw", creds); !strings.Contains(body, `"auth":true`) {
		t.Fatalf("non-matching MFA rule: %s", body)
	}
	before, _ = p.app.CountRecords(users)
	if body, _ := post("/anon", ""); !strings.Contains(body, `"ok":true`) {
		t.Fatalf("anonymous, rule doesn't match: %s", body)
	}

	// MFA off: plain sign-in
	setMFA(t, p, false, "")
	if body, _ := post("/pw", creds); !strings.Contains(body, `"auth":true`) {
		t.Fatalf("no MFA: %s", body)
	}
}
