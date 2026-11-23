package pages

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/ozzo-validation/v4/is"
	"ganigeorgiev.com/pocketbase/pocketbase/apis"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/mails"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/routine"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/search"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/security"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/subscriptions"
	"pocketone.dev/ext/extkit/js"
)

// ---- ctx.res.sse() ----

// sse starts a text/event-stream response and returns the stream object for JS.
func (r *request) sse() (js.Object, error) {
	if r.sent {
		return nil, errors.New("ctx.res.sse(): a response was already sent")
	}
	r.sent = true
	h := r.e.Response.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	r.e.Response.WriteHeader(http.StatusOK)
	_ = r.e.Flush()

	closed := false
	write := func(s string) {
		if closed || r.isHead() {
			return
		}
		_, _ = r.e.Response.Write([]byte(s))
		_ = r.e.Flush()
	}
	o := r.rt.NewObject()
	err := errors.Join(
		o.Set("send", func(event string, data any, opts map[string]any) error {
			var b strings.Builder
			if id, ok := opts["id"]; ok && id != nil && fmt.Sprint(id) != "" {
				fmt.Fprintf(&b, "id: %v\n", id)
			}
			if retry, ok := toInt(opts["retry"]); ok && retry > 0 {
				fmt.Fprintf(&b, "retry: %d\n", retry)
			}
			if event != "" {
				fmt.Fprintf(&b, "event: %s\n", event)
			}
			text := ""
			if s, ok := data.(string); ok {
				text = s
			} else if data != nil {
				j, err := marshalJSON(data)
				if err != nil {
					return err
				}
				text = string(j)
			}
			for _, line := range strings.Split(text, "\n") {
				fmt.Fprintf(&b, "data: %s\n", line)
			}
			b.WriteString("\n")
			write(b.String())
			return nil
		}),
		o.Set("comment", func(text string) {
			var b strings.Builder
			for _, line := range strings.Split(text, "\n") {
				fmt.Fprintf(&b, ": %s\n", line)
			}
			b.WriteString("\n")
			write(b.String())
		}),
		o.Set("close", func() { closed = true }),
	)
	return o, err
}

// ---- realtime (ctx.realtime, built by the driver) ----

// realtimeClients are the clients subscribed to topic, for a send with a filter (the driver calls the filter).
func (r *request) realtimeClients(topic string) []subscriptions.Client {
	var out []subscriptions.Client
	for _, c := range r.p.app.SubscriptionsBroker().Clients() {
		if c.HasSubscription(topic) {
			out = append(out, c)
		}
	}
	return out
}

// realtimeSend(topic, data, clients) sends data as JSON to clients, or, when clients is null, to every client
// subscribed to topic.
func (r *request) realtimeSend(c js.Call) (any, error) {
	topic := c.Arg(0).String()
	payload, err := marshalJSON(c.Arg(1).Export())
	if err != nil {
		return nil, err
	}
	var to []subscriptions.Client
	if list := c.Arg(2).Object(); list != nil {
		for _, k := range list.Keys() {
			if client, ok := list.Get(k).Export().(subscriptions.Client); ok {
				to = append(to, client)
			}
		}
	} else {
		to = r.realtimeClients(topic)
	}
	msg := subscriptions.Message{Name: topic, Data: payload}
	for _, client := range to {
		subscriptions.SendOrdered(client, msg) // queued per client, in order; never stalls the page
	}
	return nil, nil
}

func (r *request) realtimeClient(id string) any {
	c, err := r.p.app.SubscriptionsBroker().ClientById(id)
	if err != nil || c == nil {
		return nil
	}
	return c
}

// ---- ctx.realtime.subscribe ----

// realtimeQueue is how many messages a subscription holds for page code that hasn't read them yet; one more ends it.
const realtimeQueue = 64

// errRealtimeBehind ends a subscription whose page code fell behind: the client reconnects and gets current state,
// instead of a stream that silently skipped an update.
var errRealtimeBehind = fmt.Errorf("ctx.realtime.subscribe(): the page fell more than %d messages behind", realtimeQueue)

// realtimeSub is one ctx.realtime.subscribe(): a realtime client of the app's broker, like a browser on
// /api/realtime, with the visitor's auth, so PocketBase applies the collection's rules, filter and expand to every
// record message for this visitor. Page code reads it through Next (as an async Go call, off the VM's loop).
type realtimeSub struct {
	broker *subscriptions.Broker
	client *subscriptions.DefaultClient
	queue  chan subscriptions.Message
	done   chan struct{}
	ctx    context.Context // the request's
	once   sync.Once
	err    error // set before done closes
}

// realtimeSubscribe(topic, { filter, expand, fields }) registers a realtime client for the request, subscribed to
// topic with PocketBase's own subscription options, and returns it. It ends when the request does.
func (r *request) realtimeSubscribe(topic string, opts map[string]any) (*realtimeSub, error) {
	if topic == "" {
		return nil, errors.New("ctx.realtime.subscribe(): topic is required")
	}
	query := map[string]string{}
	for _, k := range []string{"filter", "expand", "fields"} {
		if v, ok := opts[k]; ok && v != nil {
			query[k] = fmt.Sprint(v)
		}
	}
	sub := topic
	if len(query) > 0 {
		raw, err := json.Marshal(map[string]any{"query": query})
		if err != nil {
			return nil, err
		}
		sub += "?options=" + url.QueryEscape(string(raw))
	}

	client := subscriptions.NewDefaultClient()
	if r.auth != nil {
		client.Set(apis.RealtimeClientAuthKey, r.auth) // PocketBase keeps it current when the record changes
	}
	client.Subscribe(sub)
	s := &realtimeSub{
		broker: r.p.app.SubscriptionsBroker(),
		client: client,
		queue:  make(chan subscriptions.Message, realtimeQueue),
		done:   make(chan struct{}),
		ctx:    r.context(),
	}

	r.subsMu.Lock()
	if r.subsEnded {
		r.subsMu.Unlock()
		return nil, errors.New("ctx.realtime.subscribe(): the request has ended")
	}
	r.subs = append(r.subs, s)
	r.subsMu.Unlock()

	s.broker.Register(client)
	// The broker's senders block until the client's channel is read, so it's read at once into the queue.
	go func() {
		for m := range client.Channel() { // closed when the client is unregistered
			select {
			case s.queue <- m:
			default:
				s.end(errRealtimeBehind)
			}
		}
	}()
	go func() {
		select {
		case <-s.ctx.Done():
			s.end(nil)
		case <-s.done:
		}
	}()
	return s, nil
}

// end unregisters the client (once); err, if any, is what the next Next returns.
func (s *realtimeSub) end(err error) {
	s.once.Do(func() {
		s.err = err
		close(s.done)
		s.broker.Unregister(s.client.Id())
	})
}

// Next waits for the next message and returns its data (JSON), or "" once the subscription has ended (closed, or
// the request over). It returns an error if the page fell behind. Called as an async Go call, off the loop.
func (s *realtimeSub) Next() (string, error) {
	select {
	case <-s.done: // a page that fell behind gets the error, not the stale messages before it
		return "", s.err
	default:
	}
	select {
	case m := <-s.queue:
		return string(m.Data), nil
	case <-s.done:
		return "", s.err
	case <-s.ctx.Done():
		return "", nil
	}
}

// Close ends the subscription.
func (s *realtimeSub) Close() { s.end(nil) }

// endSubscriptions ends the request's subscriptions, once its handler has returned, and refuses new ones.
func (r *request) endSubscriptions() {
	r.subsMu.Lock()
	subs := r.subs
	r.subs, r.subsEnded = nil, true
	r.subsMu.Unlock()
	for _, s := range subs {
		s.end(nil)
	}
}

// ---- auth (ctx.session) ----

const authCookie = "pb_auth"

type authCookieValue struct {
	Token  string       `json:"token"`
	Record *core.Record `json:"record"`
}

// readAuthCookie sets r.auth from a valid pb_auth cookie, or clears a bad one.
func (r *request) readAuthCookie() {
	raw, ok := r.cookies()[authCookie]
	if !ok || raw == "" {
		return
	}
	var v struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil || v.Token == "" {
		r.setCookie(authCookie, "", map[string]any{"maxAge": 0})
		return
	}
	rec, err := r.p.app.FindAuthRecordByToken(v.Token, core.TokenTypeAuth)
	if err != nil || rec == nil {
		r.setCookie(authCookie, "", map[string]any{"maxAge": 0})
		return
	}
	r.auth = rec
	r.e.Auth = rec
}

func (r *request) authCollection(opts map[string]any) (*core.Collection, error) {
	name := "users"
	if c, ok := opts["collection"].(string); ok && c != "" {
		name = c
	}
	col, err := r.p.app.FindCollectionByNameOrId(name)
	if err != nil || !col.IsAuth() {
		return nil, fmt.Errorf("ctx.session: %q is not an auth collection", name)
	}
	return col, nil
}

// errMFA is the error every pages sign-in gives an account that needs MFA.
var errMFA = errors.New(mfaMessage)

// mfaMessage is the error every pages sign-in gives an account that needs MFA (the OAuth2 flow says the same).
const mfaMessage = "This account needs a second sign-in step (MFA), which pages doesn't support yet."

// needsMFA reports whether signing rec in takes a second factor, as PocketBase's own auth endpoints decide it: the
// collection has MFA on and its MFA rule matches rec (an empty rule matches everyone). A rule that can't be checked
// counts as yes, as in PocketBase.
func (r *request) needsMFA(rec *core.Record) bool {
	col := rec.Collection()
	if !col.MFA.Enabled {
		return false
	}
	if col.MFA.Rule == "" {
		return true
	}
	info, err := r.e.RequestInfo()
	if err != nil {
		return true
	}
	resolver := core.NewRecordFieldResolver(r.p.app, col, info, true)
	expr, err := search.FilterData(col.MFA.Rule).BuildExpr(resolver)
	if err != nil {
		return true
	}
	query := r.p.app.RecordQuery(col).Select("(1)").AndWhere(dbx.HashExp{col.Name + ".id": rec.Id})
	if err := resolver.UpdateQuery(query); err != nil {
		return true
	}
	var exists int
	if err := query.AndWhere(expr).Limit(1).Row(&exists); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return true
	}
	return exists > 0
}

// signInChecked is signIn for pages' own sign-in methods: it refuses an account that needs MFA, since pages has no
// second step yet. ctx.session.signIn(record), for records the app authenticated itself, doesn't go through it.
func (r *request) signInChecked(rec *core.Record) (map[string]any, error) {
	if r.needsMFA(rec) {
		return nil, errMFA
	}
	return r.signIn(rec)
}

// signIn sets the session cookie for record and returns { token, record }.
func (r *request) signIn(rec *core.Record) (map[string]any, error) {
	token, err := rec.NewAuthToken()
	if err != nil {
		return nil, err
	}
	val, err := json.Marshal(authCookieValue{Token: token, Record: rec})
	if err != nil {
		return nil, err
	}
	maxAge := int(rec.Collection().AuthToken.DurationTime() / time.Second)
	r.setCookie(authCookie, string(val), map[string]any{"maxAge": maxAge})
	r.auth = rec
	r.e.Auth = rec
	return map[string]any{"token": token, "record": rec}, nil
}

func (r *request) findByIdentity(col *core.Collection, identity string) *core.Record {
	fields := col.PasswordAuth.IdentityFields
	if len(fields) == 0 {
		fields = []string{core.FieldNameEmail}
	}
	for _, f := range fields {
		rec, err := r.p.app.FindFirstRecordByData(col, f, identity)
		if err == nil && rec != nil {
			return rec
		}
	}
	return nil
}

func (r *request) sessionObject() js.Object {
	app := r.p.app
	o := r.rt.NewObject()
	set := func(name string, v any) { _ = o.Set(name, v) }

	set("current", func() any {
		if r.auth == nil {
			return nil
		}
		return r.auth
	})

	set("authMethods", func(opts map[string]any) (map[string]any, error) {
		col, err := r.authCollection(opts)
		if err != nil {
			return nil, err
		}
		providers := []any{}
		for _, p := range col.OAuth2.Providers {
			providers = append(providers, map[string]any{"name": p.Name, "displayName": p.DisplayName})
		}
		return map[string]any{
			"password": map[string]any{"enabled": col.PasswordAuth.Enabled, "identityFields": col.PasswordAuth.IdentityFields},
			"otp":      map[string]any{"enabled": col.OTP.Enabled},
			"mfa":      map[string]any{"enabled": col.MFA.Enabled},
			"oauth2":   map[string]any{"enabled": col.OAuth2.Enabled, "providers": providers},
		}, nil
	})

	set("signIn", func(rec *core.Record) (map[string]any, error) {
		if rec == nil || !rec.Collection().IsAuth() {
			return nil, errors.New("ctx.session.signIn: needs an auth record")
		}
		return r.signIn(rec)
	})

	set("signInWithPassword", func(identity, password string, opts map[string]any) (map[string]any, error) {
		col, err := r.authCollection(opts)
		if err != nil {
			return nil, err
		}
		if !col.PasswordAuth.Enabled {
			return nil, errors.New("password sign-in is turned off for " + col.Name)
		}
		rec := r.findByIdentity(col, identity)
		if rec == nil || !rec.ValidatePassword(password) {
			return nil, errors.New("Invalid login credentials.")
		}
		return r.signInChecked(rec)
	})

	set("register", func(email, password string, opts map[string]any) (map[string]any, error) {
		col, err := r.authCollection(opts)
		if err != nil {
			return nil, err
		}
		rec := core.NewRecord(col)
		if data, ok := opts["data"].(map[string]any); ok {
			for k, v := range data {
				rec.Set(k, v)
			}
		}
		rec.SetEmail(email)
		rec.SetPassword(password)
		if err := app.Save(rec); err != nil {
			return nil, err
		}
		if v, ok := opts["verify"].(bool); !ok || v {
			if err := mails.SendRecordVerification(app, rec); err != nil {
				app.Logger().Error("pages: verification email failed", "error", err)
			}
		}
		if v, ok := opts["signIn"].(bool); !ok || v {
			return r.signInChecked(rec)
		}
		return map[string]any{"token": "", "record": rec}, nil
	})

	set("signInAnonymously", func(opts map[string]any) (map[string]any, error) {
		col, err := r.authCollection(opts)
		if err != nil {
			return nil, err
		}
		if col.MFA.Enabled && col.MFA.Rule == "" {
			return nil, errMFA // every account needs MFA: don't create one that can't sign in
		}
		rec := core.NewRecord(col)
		rec.SetEmail("anon-" + strings.ToLower(security.RandomString(16)) + "@anonymous.invalid")
		rec.SetPassword(security.RandomString(32))
		if err := app.Save(rec); err != nil {
			return nil, err
		}
		if r.needsMFA(rec) {
			_ = app.Delete(rec)
			return nil, errMFA
		}
		return r.signIn(rec)
	})

	set("requestOTP", func(email string, opts map[string]any) (string, error) {
		col, err := r.authCollection(opts)
		if err != nil {
			return "", err
		}
		if !col.OTP.Enabled {
			return "", errors.New("OTP sign-in is turned off for " + col.Name)
		}
		rec, err := app.FindAuthRecordByEmail(col, email)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		if rec == nil {
			if c, _ := opts["create"].(bool); !c {
				return core.GenerateDefaultRandomId(), nil
			}
			rec = core.NewRecord(col)
			rec.SetEmail(email)
			rec.SetPassword(security.RandomString(32))
			if err := app.Save(rec); err != nil {
				return "", err
			}
		}
		pass := security.RandomStringWithAlphabet(col.OTP.Length, "1234567890")
		otp := core.NewOTP(app)
		otp.SetCollectionRef(col.Id)
		otp.SetRecordRef(rec.Id)
		otp.SetPassword(pass)
		otp.SetSentTo(email)
		if err := app.Save(otp); err != nil {
			return "", err
		}
		routine.FireAndForget(func() {
			if err := mails.SendRecordOTP(app, rec, otp.Id, pass); err != nil {
				app.Logger().Error("pages: OTP email failed", "error", errors.Join(err, app.Delete(otp)))
			}
		})
		return otp.Id, nil
	})

	set("signInWithOTP", func(otpId, code string, opts map[string]any) (map[string]any, error) {
		col, err := r.authCollection(opts)
		if err != nil {
			return nil, err
		}
		errOTP := errors.New("Invalid or expired one-time password.")
		otp, err := app.FindOTPById(otpId)
		if err != nil || otp.CollectionRef() != col.Id || otp.HasExpired(col.OTP.DurationTime()) || !otp.ValidatePassword(code) {
			return nil, errOTP
		}
		rec, err := app.FindRecordById(col, otp.RecordRef())
		if err != nil {
			return nil, errOTP
		}
		if r.needsMFA(rec) {
			return nil, errMFA
		}
		_ = app.Delete(otp)
		if !rec.Verified() && otp.SentTo() != "" && strings.EqualFold(otp.SentTo(), rec.Email()) {
			rec.SetVerified(true)
			_ = app.Save(rec)
		}
		return r.signInChecked(rec)
	})

	set("signOut", func() {
		r.setCookie(authCookie, "", map[string]any{"maxAge": 0})
		r.auth = nil
		r.e.Auth = nil
	})

	set("requestVerification", func(email string, opts map[string]any) error {
		col, err := r.authCollection(opts)
		if err != nil {
			return err
		}
		rec, err := app.FindAuthRecordByEmail(col, email)
		if err != nil || rec.Verified() {
			return nil // never reveal whether the email exists
		}
		routine.FireAndForget(func() {
			if err := mails.SendRecordVerification(app, rec); err != nil {
				app.Logger().Error("pages: verification email failed", "error", err)
			}
		})
		return nil
	})

	set("confirmVerification", func(token string) (*core.Record, error) {
		rec, err := app.FindAuthRecordByToken(token, core.TokenTypeVerification)
		if err != nil || !tokenEmailMatches(token, rec.Email()) {
			return nil, errors.New("Invalid or expired verification token.")
		}
		if !rec.Verified() {
			rec.SetVerified(true)
			if err := app.Save(rec); err != nil {
				return nil, err
			}
		}
		return rec, nil
	})

	set("requestPasswordReset", func(email string, opts map[string]any) error {
		col, err := r.authCollection(opts)
		if err != nil {
			return err
		}
		rec, err := app.FindAuthRecordByEmail(col, email)
		if err != nil {
			return nil
		}
		routine.FireAndForget(func() {
			if err := mails.SendRecordPasswordReset(app, rec); err != nil {
				app.Logger().Error("pages: password reset email failed", "error", err)
			}
		})
		return nil
	})

	set("confirmPasswordReset", func(token, password, confirm string) (*core.Record, error) {
		if password != confirm {
			return nil, errors.New("The passwords don't match.")
		}
		rec, err := app.FindAuthRecordByToken(token, core.TokenTypePasswordReset)
		if err != nil || !tokenEmailMatches(token, rec.Email()) {
			return nil, errors.New("Invalid or expired password reset token.")
		}
		rec.SetPassword(password)
		if !rec.Verified() {
			rec.SetVerified(true)
		}
		if err := app.Save(rec); err != nil {
			return nil, err
		}
		return rec, nil
	})

	set("requestEmailChange", func(newEmail string) error {
		if r.auth == nil {
			return errors.New("ctx.session.requestEmailChange: not signed in")
		}
		rec := r.auth
		// the checks PocketBase's request-email-change API makes
		newEmail = strings.TrimSpace(newEmail)
		if is.EmailFormat.Validate(newEmail) != nil || newEmail == "" {
			return errors.New("Enter a valid email address.")
		}
		if strings.EqualFold(newEmail, rec.Email()) {
			return errors.New("That's already your email address.")
		}
		if other, err := app.FindAuthRecordByEmail(rec.Collection(), newEmail); err == nil && other != nil {
			return errors.New("That email address is already in use.")
		}
		routine.FireAndForget(func() {
			if err := mails.SendRecordChangeEmail(app, rec, newEmail); err != nil {
				app.Logger().Error("pages: email change email failed", "error", err)
			}
		})
		return nil
	})

	set("confirmEmailChange", func(token, password string) (*core.Record, error) {
		rec, err := app.FindAuthRecordByToken(token, core.TokenTypeEmailChange)
		if err != nil || !tokenEmailMatches(token, rec.Email()) || !rec.ValidatePassword(password) {
			return nil, errors.New("Invalid or expired email change token, or wrong password.")
		}
		newEmail := tokenClaim(token, core.TokenClaimNewEmail)
		if newEmail == "" {
			return nil, errors.New("Invalid email change token.")
		}
		rec.SetEmail(newEmail)
		rec.SetVerified(true)
		if err := app.Save(rec); err != nil {
			return nil, err
		}
		if r.auth != nil && r.auth.Id == rec.Id {
			if _, err := r.signIn(rec); err != nil {
				return nil, err
			}
		}
		return rec, nil
	})

	set("requestOAuth2", func(provider string, opts map[string]any) (string, error) {
		return r.requestOAuth2(provider, opts)
	})
	set("signInWithOAuth2", func(state, code string, opts map[string]any) (map[string]any, error) {
		return r.signInWithOAuth2(state, code, opts)
	})
	return o
}

// tokenClaim reads a claim from a token that was already verified.
func tokenClaim(token, claim string) string {
	claims, err := security.ParseUnverifiedJWT(token)
	if err != nil {
		return ""
	}
	s, _ := claims[claim].(string)
	return s
}

// tokenEmailMatches checks a token's email claim against the record's current email (PocketBase's own check for
// verification, password reset and email change tokens).
func tokenEmailMatches(token, email string) bool {
	return strings.EqualFold(tokenClaim(token, core.TokenClaimEmail), email)
}

// marshalJSON is json.Marshal without HTML escaping (payloads often carry HTML).
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
