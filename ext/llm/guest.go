package llm

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"

	"ganigeorgiev.com/pocketbase/pocketbase/core"

	kvv1 "pocketone.dev/ext/contracts/kv/v1"
	llmc "pocketone.dev/ext/contracts/llm"
)

// GuestCookie is the cookie that tells one anonymous visitor from another, so each guest gets a bucket of their own.
// Its value is a random id and a signature (HMAC-SHA256 with a random key the app keeps in its key-value store, private
// to Go), so a visitor can't pick someone else's id.
const GuestCookie = "pocketone_llm_guest"

// guestCookieAge is how long a guest keeps their id: a year.
const guestCookieAge = 365 * 24 * 60 * 60

// SubjectOf is whom a request's calls are for: its auth record, else the guest its cookie names, else its IP. A
// request without a valid guest cookie gets one set on its response and counts by IP until the browser sends it
// back, so a client that drops cookies can't get a fresh bucket on every call.
func SubjectOf(app core.App, e *core.RequestEvent) string {
	if e == nil {
		return ""
	}
	if e.Auth != nil {
		return llmc.UserSubject(e.Auth.Collection().Name, e.Auth.Id)
	}
	key := guestKey(app)
	if c, err := e.Request.Cookie(GuestCookie); err == nil {
		if id, ok := verifyGuest(key, c.Value); ok {
			return "guest:" + id
		}
	}
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	http.SetCookie(e.Response, &http.Cookie{
		Name: GuestCookie, Value: id + "." + signGuest(key, id), Path: "/", MaxAge: guestCookieAge,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: e.Request.TLS != nil,
	})
	return "guest:ip:" + e.RealIP()
}

// guestKeyName is where the signing key lives in the app's key-value store: a private name, so only Go reads it.
// Deleting it gives every guest a new id.
const guestKeyName = "_llm/guest-key"

var guestKeyMu sync.Mutex

// guestKey is the signing key, made (32 random bytes) and stored the first time an app needs one.
func guestKey(app core.App) []byte {
	guestKeyMu.Lock()
	defer guestKeyMu.Unlock()
	var k string
	found, err := kvv1.Get(app, guestKeyName, &k)
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	if err != nil {
		// the stored key is still there: a key made now is used once and never saved over it, so guests keep their ids
		app.Logger().Warn("llm: guest key: not read, so guest cookies won't verify and guests count by IP", "error", err)
		return b
	}
	if found && len(k) == 64 {
		if kb, err := hex.DecodeString(k); err == nil { // a stored value that isn't hex is replaced
			return kb
		}
	}
	if err := kvv1.Set(app, guestKeyName, hex.EncodeToString(b)); err != nil {
		app.Logger().Warn("llm: guest key: not saved, so guest cookies won't verify and guests count by IP", "error", err)
	}
	return b
}

func signGuest(key []byte, id string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(id))
	return hex.EncodeToString(m.Sum(nil))[:32]
}

func verifyGuest(key []byte, value string) (string, bool) {
	id, sig, ok := strings.Cut(value, ".")
	if !ok || len(id) != 24 {
		return "", false
	}
	return id, hmac.Equal([]byte(sig), []byte(signGuest(key, id)))
}
