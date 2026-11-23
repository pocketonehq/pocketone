package apitokens

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"pocketone.dev/ext/extkit"
	"slices"
	"strings"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/apis"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/security"
)

const (
	sessionCookie = "pb_oauth"
	csrfCookie    = "pb_oauth_csrf"
	sessionTTL    = 15 * time.Minute
	refreshGrace  = 2 * time.Minute
	codeTTL       = 60 * time.Second
	deviceTTL     = 10 * time.Minute

	authorizePath    = "/api/_c/oauth/v1/authorize"
	tokenPath        = "/api/_c/oauth/v1/token"
	devicePath       = "/api/_c/oauth/v1/device"
	deviceVerifyPath = "/api/_c/oauth/v1/device/verify"
	revokePath       = "/api/_c/oauth/v1/revoke"
	registerPath     = "/api/_c/oauth/v1/register"
)

// pollInterval is the device flow's polling interval in seconds (tests shorten it).
var pollInterval = 5

// bindRoutes adds the metadata at the root (RFC 8414); the rest is oauth/v1's, mounted under /api/_c/oauth/v1/ by the
// contracts' seal and served by ServeEvent.
func (s *OAuthServer) bindRoutes(se *core.ServeEvent) {
	// public metadata: a stale token mustn't hide where to sign in (PocketBase v0.42 401s unknown tokens)
	se.Router.GET(MetadataPath, s.metadata).Unbind(apis.DefaultRejectUnknownTokenMiddlewareId)
}

// OwnsAuthorization implements extkit.OwnsAuthorization: the token and revoke endpoints read an OAuth client's
// "Basic" credentials (client_secret_basic) from the Authorization header, which PocketBase v0.42 would reject as an
// unknown token. Every endpoint checks its own credentials.
func (s *OAuthServer) OwnsAuthorization() bool { return true }

// ServeEvent implements extkit.EventServer: oauth/v1's routes.
func (s *OAuthServer) ServeEvent(e *core.RequestEvent) error {
	p, m := e.Request.URL.Path, e.Request.Method
	get, post := m == http.MethodGet, m == http.MethodPost
	switch {
	case p == authorizePath && (get || post):
		return s.authorize(e)
	case p == tokenPath && post:
		return s.token(e)
	case p == devicePath && post:
		return s.deviceAuthorization(e)
	case p == deviceVerifyPath && (get || post):
		return s.deviceVerify(e)
	case p == revokePath && post:
		return s.revoke(e)
	case p == registerPath && post:
		return s.register(e) // answers 404 unless dynamic registration is on
	case p == providerCallbackPath && (get || post): // Apple answers with form_post
		return s.providerCallback(e)
	}
	return e.NotFoundError("", nil)
}

// ServeHTTP implements http.Handler (oauth/v1's provider type); inside an app the contract calls ServeEvent.
func (s *OAuthServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if e := extkit.RequestEvent(r); e != nil {
		_ = s.ServeEvent(e)
		return
	}
	http.NotFound(w, r)
}

// Issuer is the public base URL: OAuthConfig.Issuer, else the request's scheme and host (so it always matches the URL
// the metadata was fetched from). It's the metadata's issuer; ext/mcp lists it in its protected resource metadata.
func (s *OAuthServer) Issuer(e *core.RequestEvent) string {
	if s.cfg.Issuer != "" {
		return strings.TrimRight(s.cfg.Issuer, "/")
	}
	scheme := "http"
	if e.IsTLS() || strings.EqualFold(e.Request.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + e.Request.Host
}

func (s *OAuthServer) metadata(e *core.RequestEvent) error {
	iss := s.Issuer(e)
	scopes := []string{OfflineAccess}
	for _, sc := range s.tokens().Scopes() {
		scopes = append(scopes, sc.Name)
	}
	m := map[string]any{
		"issuer":                                         iss,
		"authorization_endpoint":                         iss + authorizePath,
		"token_endpoint":                                 iss + tokenPath,
		"device_authorization_endpoint":                  iss + devicePath,
		"revocation_endpoint":                            iss + revokePath,
		"response_types_supported":                       []string{"code"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token", "urn:ietf:params:oauth:grant-type:device_code"},
		"code_challenge_methods_supported":               []string{"S256"},
		"token_endpoint_auth_methods_supported":          []string{"none", "client_secret_post", "client_secret_basic"},
		"scopes_supported":                               scopes,
		"client_id_metadata_document_supported":          len(s.cfg.CIMDHosts) > 0,
		"authorization_response_iss_parameter_supported": true,
	}
	if s.dcr.Load() {
		m["registration_endpoint"] = iss + registerPath
	}
	return e.JSON(http.StatusOK, m)
}

// ---------------------------------------------------------------------------------------------------------------
// authorize

// authRequest is an /oauth/authorize request (or the device verification it stands in for).
type authRequest struct {
	client      *Client
	redirectURI string
	state       string
	challenge   string
	scope       string
	resource    string // RFC 8707 audience, "" for none
	// account is the auth collection the client needs the token from ("account" parameter, e.g. "_superusers"
	// for the CLI): the sign-in page shows that form, a session of another collection doesn't count, and no other
	// collection is issued a token. "" lets the user choose.
	account string
	device  *core.Record // set on the device verification page
}

// validResource: an absolute http(s) URL on this server's host, without a fragment (RFC 8707).
func (s *OAuthServer) validResource(e *core.RequestEvent, r string) bool {
	u, err := url.Parse(r)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Fragment != "" {
		return false
	}
	iss, _ := url.Parse(s.Issuer(e))
	return strings.EqualFold(u.Host, e.Request.Host) || (iss != nil && strings.EqualFold(u.Host, iss.Host))
}

func param(e *core.RequestEvent, name string) string {
	if e.Request.Method == http.MethodPost {
		return e.Request.PostFormValue(name)
	}
	return e.Request.URL.Query().Get(name)
}

// parseAuthorize validates the request. A problem with the client or redirect URI is shown as a page (redirect=false);
// anything else can be sent back to the client.
func (s *OAuthServer) parseAuthorize(e *core.RequestEvent) (*authRequest, string, bool) {
	return s.parseAuthorizeWith(e, func(name string) string { return param(e, name) })
}

// parseAuthorizeWith is parseAuthorize reading the parameters from get (the OAuth2 provider callback restores them
// from its cookie).
func (s *OAuthServer) parseAuthorizeWith(e *core.RequestEvent, get func(string) string) (*authRequest, string, bool) {
	c, err := s.FindClient(get("client_id"))
	if err != nil {
		return nil, "Unknown client.", false
	}
	ar := &authRequest{client: c, redirectURI: get("redirect_uri"), state: get("state"),
		challenge: get("code_challenge"), scope: get("scope"), resource: get("resource"), account: get("account")}
	if ar.redirectURI == "" && len(c.RedirectURIs) == 1 {
		ar.redirectURI = c.RedirectURIs[0]
	}
	if !c.AllowsRedirect(ar.redirectURI) {
		return nil, "This client didn't register that redirect address.", false
	}
	if ar.resource != "" && !s.validResource(e, ar.resource) {
		return ar, "invalid_target: resource must be a URL on this server", true
	}
	if ar.account != "" {
		col, err := s.pickCollection(ar.account)
		if err != nil {
			return ar, "invalid_request: account: " + err.Error(), true
		}
		ar.account = col.Name
	}
	if get("response_type") != "code" {
		return ar, "unsupported_response_type", true
	}
	if ar.challenge == "" || get("code_challenge_method") != "S256" {
		return ar, "invalid_request: PKCE with code_challenge_method=S256 is required", true
	}
	return ar, "", true
}

func (ar *authRequest) hidden() map[string]string {
	if ar.device != nil {
		return map[string]string{"user_code": ar.device.GetString("user_code")}
	}
	return map[string]string{
		"client_id": ar.client.Id, "redirect_uri": ar.redirectURI, "state": ar.state, "scope": ar.scope,
		"response_type": "code", "code_challenge": ar.challenge, "code_challenge_method": "S256",
		"resource": ar.resource, "account": ar.account,
	}
}

func (ar *authRequest) errorRedirect(e *core.RequestEvent, code string) error {
	desc := ""
	if c, d, ok := strings.Cut(code, ": "); ok {
		code, desc = c, d
	}
	u, _ := url.Parse(ar.redirectURI)
	q := u.Query()
	q.Set("error", code)
	if desc != "" {
		q.Set("error_description", desc)
	}
	if ar.state != "" {
		q.Set("state", ar.state)
	}
	u.RawQuery = q.Encode()
	return e.Redirect(http.StatusFound, u.String())
}

func (s *OAuthServer) authorize(e *core.RequestEvent) error {
	if e.Request.Method == http.MethodPost {
		_ = e.Request.ParseForm()
	}
	ar, problem, canRedirect := s.parseAuthorize(e)
	if !canRedirect {
		return s.render(e, http.StatusBadRequest, PageData{Page: PageError, Error: problem})
	}
	if problem != "" {
		return ar.errorRedirect(e, problem)
	}
	return s.flow(e, ar, authorizePath)
}

// flow runs sign-in → consent for an authorize request or a device verification.
func (s *OAuthServer) flow(e *core.RequestEvent, ar *authRequest, action string) error {
	base := s.basePage(e, ar, action)
	if _, err := s.resolveScopes(ar.scope, nil); err != nil {
		if ar.device == nil {
			return ar.errorRedirect(e, "invalid_scope: "+err.Error())
		}
		base.Page, base.Error = PageDone, err.Error()
		return s.render(e, http.StatusBadRequest, base)
	}
	user := s.sessionUser(e)
	if user != nil && !ar.accepts(user) {
		// signed in, but as an account the client can't use: ask for the one it needs instead of offering consent
		base.Notice = s.wrongAccountNotice(ar, user)
		user = nil
	}

	if e.Request.Method == http.MethodPost {
		if !s.checkCSRF(e) {
			base.Page, base.Error = PageError, "This form expired. Go back and try again."
			return s.render(e, http.StatusForbidden, base)
		}
		step := e.Request.PostFormValue("step")
		switch {
		case step == "password":
			return s.stepPassword(e, ar, base)
		case step == "request_otp":
			return s.stepRequestOTP(e, ar, base)
		case step == "otp":
			return s.stepOTP(e, ar, base)
		case strings.HasPrefix(step, "oauth2:"):
			return s.stepOAuth2(e, base, strings.TrimPrefix(step, "oauth2:"))
		case strings.HasPrefix(step, "use:"):
			// switch forms: another user collection, the superuser form ("use:_superusers") or back ("use:"); a client
			// that needs one account type keeps its form
			name := strings.TrimPrefix(step, "use:")
			if ar.account != "" {
				name = ar.account
			}
			col, err := s.pickCollection(name)
			if err != nil {
				return s.signInPage(e, base, nil, err.Error(), "")
			}
			return s.signInPage(e, base, col, "", strings.TrimSpace(e.Request.PostFormValue("email")))
		case step == "switch":
			s.clearSession(e)
			return s.signInPage(e, base, nil, "", "")
		case step == "allow" || step == "deny":
			if user == nil {
				return s.signInPage(e, base, nil, "Your sign-in expired. Sign in again.", "")
			}
			return s.decide(e, ar, base, user, step == "allow")
		}
	}
	if user == nil {
		return s.signInPage(e, base, nil, "", "")
	}
	return s.consentPage(e, ar, base, user)
}

func (s *OAuthServer) basePage(e *core.RequestEvent, ar *authRequest, action string) PageData {
	d := PageData{Action: action, Hidden: ar.hidden(), CSRF: s.csrf(e), ClientName: ar.client.Name, Account: ar.account}
	if ar.device == nil {
		if u, err := url.Parse(ar.redirectURI); err == nil {
			d.ClientHost = u.Host
			if isLoopback(u.Hostname()) {
				d.ClientHost = "an app on this computer"
			}
		}
	}
	return d
}

func (s *OAuthServer) sessionUser(e *core.RequestEvent) *core.Record {
	c, err := e.Request.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	rec, err := s.app.FindAuthRecordByToken(c.Value, core.TokenTypeAuth)
	if err != nil || !s.allows(rec.Collection()) {
		return nil
	}
	return rec
}

func (s *OAuthServer) clearSession(e *core.RequestEvent) {
	e.SetCookie(&http.Cookie{Name: sessionCookie, Value: "", Path: "/api/_c/oauth/v1", HttpOnly: true, MaxAge: -1})
}

func (s *OAuthServer) csrf(e *core.RequestEvent) string {
	if c, err := e.Request.Cookie(csrfCookie); err == nil && len(c.Value) >= 20 {
		return c.Value
	}
	v := security.RandomString(32)
	e.SetCookie(&http.Cookie{Name: csrfCookie, Value: v, Path: "/api/_c/oauth/v1", HttpOnly: true, Secure: e.IsTLS(), SameSite: http.SameSiteLaxMode})
	// the same request may render a form right away
	e.Request.AddCookie(&http.Cookie{Name: csrfCookie, Value: v})
	return v
}

func (s *OAuthServer) checkCSRF(e *core.RequestEvent) bool {
	c, err := e.Request.Cookie(csrfCookie)
	return err == nil && c.Value != "" && subtle.ConstantTimeCompare([]byte(c.Value), []byte(e.Request.PostFormValue("csrf"))) == 1
}

// accepts reports whether user's account type is one the client can use (any, when it didn't name one).
func (ar *authRequest) accepts(user *core.Record) bool {
	return ar.account == "" || user.Collection().Name == ar.account
}

// wrongAccountNotice tells a user signed in as one account type that the client needs another.
func (s *OAuthServer) wrongAccountNotice(ar *authRequest, user *core.Record) string {
	who := "This app"
	if ar.client.Name != "" {
		who = ar.client.Name
	}
	need := "a " + ar.account + " account"
	if ar.account == core.CollectionNameSuperusers {
		need = "a superuser"
	}
	return "You're signed in as " + accountName(user) + ". " + who + " needs " + need + ": sign in as one below."
}

func (s *OAuthServer) consentPage(e *core.RequestEvent, ar *authRequest, d PageData, user *core.Record) error {
	if !ar.accepts(user) {
		d.Notice = s.wrongAccountNotice(ar, user)
		return s.signInPage(e, d, nil, "", "")
	}
	scopes, err := s.resolveScopes(ar.scope, user.Collection())
	if err != nil {
		if ar.device != nil {
			d.Page, d.Error = PageDone, err.Error()
			return s.render(e, http.StatusForbidden, d)
		}
		return ar.errorRedirect(e, "invalid_scope: "+err.Error())
	}
	if !s.tokens().AllowedCollection(user.Collection()) {
		d.Page, d.Error = PageError, s.noTokensMessage(user.Collection())
		return s.render(e, http.StatusForbidden, d)
	}
	d.Page, d.Email, d.Scopes, d.FullAccess = PageConsent, accountName(user), scopes, len(scopes) == 0
	d.ScopeChoices = s.scopeChoices(scopes, user.Collection())
	if d.Error != "" && e.Request.PostFormValue("access") == "limited" {
		// keep what the user ticked when the form comes back with an error
		d.FullAccess = false
		for i := range d.ScopeChoices {
			d.ScopeChoices[i].Checked = slices.Contains(e.Request.PostForm["grant"], d.ScopeChoices[i].Name)
		}
	}
	s.fillAccount(&d, user.Collection())
	return s.render(e, http.StatusOK, d)
}

func (s *OAuthServer) decide(e *core.RequestEvent, ar *authRequest, d PageData, user *core.Record, allow bool) error {
	if allow && !ar.accepts(user) {
		return s.consentPage(e, ar, d, user) // back to the sign-in form for the account the client needs
	}
	requested, err := s.resolveScopes(ar.scope, user.Collection())
	if err != nil {
		return s.consentPage(e, ar, d, user)
	}
	scopes := requested
	if allow {
		if scopes, err = chosenScopes(e, requested, s.scopeChoices(requested, user.Collection())); err != nil {
			d.Error = err.Error()
			return s.consentPage(e, ar, d, user)
		}
	}
	if ar.device != nil {
		dev := ar.device
		if allow {
			dev.Set("scopes", scopeNames(scopes))
			dev.Set("status", StatusApproved)
			dev.Set("owner_collection", user.Collection().Id)
			dev.Set("owner", user.Id)
		} else {
			dev.Set("status", StatusDenied)
		}
		if err := s.app.Save(dev); err != nil {
			return err
		}
		d.Page = PageDone
		if allow {
			d.Message = ar.client.Name + " is connected. You can close this page and go back to your device."
		} else {
			d.Error = "You denied access. You can close this page."
		}
		return s.render(e, http.StatusOK, d)
	}
	if !allow {
		return ar.errorRedirect(e, "access_denied")
	}
	code := security.RandomString(40)
	col, err := s.app.FindCachedCollectionByNameOrId(CodesCollectionId)
	if err != nil {
		return err
	}
	rec := core.NewRecord(col)
	rec.Set("kind", KindCode)
	rec.Set("hash", oauthHashSecret(code))
	rec.Set("client_id", ar.client.Id)
	rec.Set("redirect_uri", ar.redirectURI)
	rec.Set("challenge", ar.challenge)
	rec.Set("resource", ar.resource)
	rec.Set("scopes", scopeNames(scopes))
	rec.Set("owner_collection", user.Collection().Id)
	rec.Set("owner", user.Id)
	rec.Set("expires", s.cfg.Now().Add(codeTTL).UTC())
	if err := s.app.Save(rec); err != nil {
		return err
	}
	u, _ := url.Parse(ar.redirectURI)
	q := u.Query()
	q.Set("code", code)
	if ar.state != "" {
		q.Set("state", ar.state)
	}
	q.Set("iss", s.Issuer(e))
	u.RawQuery = q.Encode()
	return e.Redirect(http.StatusFound, u.String())
}

// ---------------------------------------------------------------------------------------------------------------
// device flow (RFC 8628)

const userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ" // no vowels: no words

func normalizeUserCode(v string) string {
	v = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(v)))
	if len(v) == 8 {
		return v[:4] + "-" + v[4:]
	}
	return v
}

func (s *OAuthServer) deviceAuthorization(e *core.RequestEvent) error {
	_ = e.Request.ParseForm()
	c, err := s.clientAuth(e)
	if err != nil {
		return oauthError(e, http.StatusUnauthorized, "invalid_client", err.Error())
	}
	scope := e.Request.PostFormValue("scope")
	if _, err := s.resolveScopes(scope, nil); err != nil {
		return oauthError(e, http.StatusBadRequest, "invalid_scope", err.Error())
	}
	resource := e.Request.PostFormValue("resource")
	if resource != "" && !s.validResource(e, resource) {
		return oauthError(e, http.StatusBadRequest, "invalid_target", "resource must be a URL on this server")
	}
	account := e.Request.PostFormValue("account")
	if account != "" {
		col, err := s.pickCollection(account)
		if err != nil {
			return oauthError(e, http.StatusBadRequest, "invalid_request", "account: "+err.Error())
		}
		account = col.Name
	}
	deviceCode := security.RandomString(40)
	userCode := normalizeUserCode(security.RandomStringWithAlphabet(8, userCodeAlphabet))
	col, err := s.app.FindCachedCollectionByNameOrId(CodesCollectionId)
	if err != nil {
		return err
	}
	rec := core.NewRecord(col)
	rec.Set("kind", KindDevice)
	rec.Set("hash", oauthHashSecret(deviceCode))
	rec.Set("user_code", userCode)
	rec.Set("client_id", c.Id)
	rec.Set("scopes", strings.Fields(scope))
	rec.Set("resource", resource)
	rec.Set("account", account)
	rec.Set("status", StatusPending)
	rec.Set("expires", s.cfg.Now().Add(deviceTTL).UTC())
	if err := s.app.Save(rec); err != nil {
		return err
	}
	verify := s.Issuer(e) + deviceVerifyPath
	e.Response.Header().Set("Cache-Control", "no-store")
	return e.JSON(http.StatusOK, map[string]any{
		"device_code": deviceCode, "user_code": userCode, "verification_uri": verify,
		"verification_uri_complete": verify + "?user_code=" + url.QueryEscape(userCode),
		"expires_in":                int(deviceTTL.Seconds()), "interval": pollInterval,
	})
}

func (s *OAuthServer) deviceVerify(e *core.RequestEvent) error {
	if e.Request.Method == http.MethodPost {
		_ = e.Request.ParseForm()
	}
	userCode := normalizeUserCode(param(e, "user_code"))
	base := PageData{Page: PageDevice, Action: deviceVerifyPath, UserCode: userCode, CSRF: s.csrf(e)}
	if userCode == "" {
		return s.render(e, http.StatusOK, base)
	}
	ar, err := s.deviceRequest(userCode)
	if err != nil {
		base.Error = err.Error()
		return s.render(e, http.StatusNotFound, base)
	}
	return s.flow(e, ar, deviceVerifyPath)
}

// deviceRequest finds the pending device authorization for a user code.
func (s *OAuthServer) deviceRequest(userCode string) (*authRequest, error) {
	dev, err := s.app.FindFirstRecordByData(CodesCollectionId, "user_code", userCode)
	if err != nil || dev.GetString("kind") != KindDevice || dev.GetString("status") != StatusPending ||
		!dev.GetDateTime("expires").Time().After(s.cfg.Now()) {
		return nil, errors.New("That code is wrong or expired. Check your device for a new one.")
	}
	c, err := s.FindClient(dev.GetString("client_id"))
	if err != nil {
		return nil, errors.New("That code is wrong or expired.")
	}
	var scopes []string
	_ = dev.UnmarshalJSONField("scopes", &scopes)
	return &authRequest{client: c, scope: strings.Join(scopes, " "), account: dev.GetString("account"), device: dev}, nil
}

// ---------------------------------------------------------------------------------------------------------------
// token endpoint

func oauthError(e *core.RequestEvent, status int, code, desc string) error {
	e.Response.Header().Set("Cache-Control", "no-store")
	body := map[string]string{"error": code}
	if desc != "" {
		body["error_description"] = desc
	}
	return e.JSON(status, body)
}

// clientAuth identifies the client: client_id (+ client_secret) in the form, or HTTP Basic.
func (s *OAuthServer) clientAuth(e *core.RequestEvent) (*Client, error) {
	id, secret, ok := e.Request.BasicAuth()
	if ok {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = e.Request.PostFormValue("client_id"), e.Request.PostFormValue("client_secret")
	}
	c, err := s.FindClient(id)
	if err != nil {
		return nil, err
	}
	if !c.checkSecret(secret) {
		return nil, errors.New("client authentication failed")
	}
	return c, nil
}

func (s *OAuthServer) token(e *core.RequestEvent) error {
	_ = e.Request.ParseForm()
	c, err := s.clientAuth(e)
	if err != nil {
		return oauthError(e, http.StatusUnauthorized, "invalid_client", err.Error())
	}
	switch e.Request.PostFormValue("grant_type") {
	case "authorization_code":
		return s.grantCode(e, c)
	case "refresh_token":
		return s.grantRefresh(e, c)
	case "urn:ietf:params:oauth:grant-type:device_code":
		return s.grantDevice(e, c)
	}
	return oauthError(e, http.StatusBadRequest, "unsupported_grant_type", "")
}

// takeCode finds a code row by its secret; single-use kinds are deleted on the spot.
func (s *OAuthServer) findCode(kind, secret string) (*core.Record, error) {
	if secret == "" {
		return nil, sql.ErrNoRows
	}
	rec, err := s.app.FindFirstRecordByData(CodesCollectionId, "hash", oauthHashSecret(secret))
	if err != nil {
		return nil, err
	}
	if rec.GetString("kind") != kind {
		return nil, sql.ErrNoRows
	}
	return rec, nil
}

func (s *OAuthServer) grantCode(e *core.RequestEvent, c *Client) error {
	rec, err := s.findCode(KindCode, e.Request.PostFormValue("code"))
	if err != nil {
		return oauthError(e, http.StatusBadRequest, "invalid_grant", "unknown or used code")
	}
	_ = s.app.Delete(rec) // single use, whatever happens next
	if !rec.GetDateTime("expires").Time().After(s.cfg.Now()) {
		return oauthError(e, http.StatusBadRequest, "invalid_grant", "the code expired")
	}
	if rec.GetString("client_id") != c.Id || rec.GetString("redirect_uri") != e.Request.PostFormValue("redirect_uri") {
		return oauthError(e, http.StatusBadRequest, "invalid_grant", "client or redirect_uri mismatch")
	}
	sum := sha256.Sum256([]byte(e.Request.PostFormValue("code_verifier")))
	if subtle.ConstantTimeCompare([]byte(base64.RawURLEncoding.EncodeToString(sum[:])), []byte(rec.GetString("challenge"))) != 1 {
		return oauthError(e, http.StatusBadRequest, "invalid_grant", "PKCE verification failed")
	}
	return s.issue(e, c, rec)
}

func (s *OAuthServer) grantRefresh(e *core.RequestEvent, c *Client) error {
	rec, err := s.findCode(KindRefresh, e.Request.PostFormValue("refresh_token"))
	if err != nil || rec.GetString("client_id") != c.Id {
		return oauthError(e, http.StatusBadRequest, "invalid_grant", "unknown refresh token")
	}
	now := s.cfg.Now()
	if !rec.GetDateTime("expires").Time().After(now) {
		_ = s.app.Delete(rec)
		return oauthError(e, http.StatusBadRequest, "invalid_grant", "the refresh token expired")
	}
	// Rotation without logouts: the used refresh token keeps working for refreshGrace (two processes of one client
	// often refresh at once, and the loser would otherwise be signed out), and the access token it replaced stays
	// valid until its own expiry instead of being revoked mid-request.
	if grace := now.Add(refreshGrace); rec.GetDateTime("expires").Time().After(grace) {
		rec.Set("expires", grace.UTC())
		if err := s.app.Save(rec); err != nil {
			return err
		}
	}
	return s.issue(e, c, rec)
}

func (s *OAuthServer) grantDevice(e *core.RequestEvent, c *Client) error {
	rec, err := s.findCode(KindDevice, e.Request.PostFormValue("device_code"))
	if err != nil || rec.GetString("client_id") != c.Id {
		return oauthError(e, http.StatusBadRequest, "invalid_grant", "unknown device code")
	}
	now := s.cfg.Now()
	if !rec.GetDateTime("expires").Time().After(now) {
		_ = s.app.Delete(rec)
		return oauthError(e, http.StatusBadRequest, "expired_token", "")
	}
	switch rec.GetString("status") {
	case StatusDenied:
		_ = s.app.Delete(rec)
		return oauthError(e, http.StatusBadRequest, "access_denied", "")
	case StatusApproved:
		_ = s.app.Delete(rec)
		return s.issue(e, c, rec)
	}
	last := rec.GetDateTime("last_poll")
	rec.Set("last_poll", now.UTC())
	_ = s.app.Save(rec)
	if !last.IsZero() && now.Sub(last.Time()) < time.Duration(pollInterval)*time.Second*4/5 {
		return oauthError(e, http.StatusBadRequest, "slow_down", "")
	}
	return oauthError(e, http.StatusBadRequest, "authorization_pending", "")
}

// issue mints an access token (an API token) and a refresh token for the owner and scopes of rec.
func (s *OAuthServer) issue(e *core.RequestEvent, c *Client, rec *core.Record) error {
	col, err := s.app.FindCachedCollectionByNameOrId(rec.GetString("owner_collection"))
	if err != nil || !s.allows(col) {
		return oauthError(e, http.StatusBadRequest, "invalid_grant", "the account can't sign in here")
	}
	owner, err := s.app.FindRecordById(col, rec.GetString("owner"))
	if err != nil {
		return oauthError(e, http.StatusBadRequest, "invalid_grant", "the account is gone")
	}
	var scopes []string
	_ = rec.UnmarshalJSONField("scopes", &scopes)
	scopes = slices.DeleteFunc(scopes, func(v string) bool { return v == OfflineAccess })
	audience := rec.GetString("resource")
	if r := e.Request.PostFormValue("resource"); r != "" {
		switch {
		case audience != "" && r != audience:
			return oauthError(e, http.StatusBadRequest, "invalid_target", "resource differs from the authorized one")
		case audience == "" && !s.validResource(e, r):
			return oauthError(e, http.StatusBadRequest, "invalid_target", "resource must be a URL on this server")
		}
		audience = r
	}
	now := s.cfg.Now()
	var expires time.Time // zero: never expires
	if ttl := s.accessTTL(audience); ttl > 0 {
		expires = now.Add(ttl)
	}
	access, tokRec, err := s.tokens().Create(owner, CreateOptions{
		Name: c.Name + " (OAuth)", Scopes: scopes, Expires: expires, ClientId: c.Id, Audience: audience,
	})
	if err != nil {
		return oauthError(e, http.StatusBadRequest, "invalid_scope", err.Error())
	}
	refresh := "pbr_" + security.RandomString(40)
	codes, err := s.app.FindCachedCollectionByNameOrId(CodesCollectionId)
	if err != nil {
		return err
	}
	r := core.NewRecord(codes)
	r.Set("kind", KindRefresh)
	r.Set("hash", oauthHashSecret(refresh))
	r.Set("client_id", c.Id)
	r.Set("scopes", scopes)
	r.Set("owner_collection", col.Id)
	r.Set("owner", owner.Id)
	r.Set("token_id", tokRec.Id)
	r.Set("resource", audience)
	r.Set("expires", now.Add(s.cfg.RefreshTTL).UTC())
	if err := s.app.Save(r); err != nil {
		return err
	}
	e.Response.Header().Set("Cache-Control", "no-store")
	out := map[string]any{"access_token": access, "token_type": "Bearer", "refresh_token": refresh, "scope": strings.Join(scopes, " ")}
	if exp := tokRec.GetDateTime("expires"); !exp.IsZero() {
		out["expires_in"] = int(exp.Time().Sub(now).Seconds())
	}
	return e.JSON(http.StatusOK, out)
}

// ---------------------------------------------------------------------------------------------------------------
// revoke (RFC 7009) and registration (RFC 7591)

func (s *OAuthServer) revoke(e *core.RequestEvent) error {
	_ = e.Request.ParseForm()
	c, err := s.clientAuth(e)
	if err != nil {
		return oauthError(e, http.StatusUnauthorized, "invalid_client", err.Error())
	}
	tok := e.Request.PostFormValue("token")
	// an unknown or someone else's token answers 200 (RFC 7009 2.2); a revoke that fails answers server_error, so the
	// client never thinks a token is dead while it still works
	failed := func(err error) error {
		s.app.Logger().Error("api-tokens: oauth: revoke failed", "client", c.Id, "error", err)
		return oauthError(e, http.StatusInternalServerError, "server_error", "the token couldn't be revoked; try again")
	}
	switch {
	case s.tokens().prefixOf(tok) != "":
		if info, err := s.tokens().Verify(tok); err == nil && info.Token.GetString("client_id") == c.Id {
			if err := s.tokens().Revoke(info.Token.Id); err != nil {
				return failed(err)
			}
			if r, err := s.app.FindFirstRecordByData(CodesCollectionId, "token_id", info.Token.Id); err == nil {
				if err := s.app.Delete(r); err != nil {
					return failed(err)
				}
			}
		}
	default:
		if r, err := s.findCode(KindRefresh, tok); err == nil && r.GetString("client_id") == c.Id {
			if id := r.GetString("token_id"); id != "" {
				// the access token may be gone already (revoked on its own, or expired and cleaned up)
				if err := s.tokens().Revoke(id); err != nil && !errors.Is(err, sql.ErrNoRows) {
					return failed(err)
				}
			}
			if err := s.app.Delete(r); err != nil {
				return failed(err)
			}
		}
	}
	return e.NoContent(http.StatusOK)
}

func (s *OAuthServer) register(e *core.RequestEvent) error {
	if !s.dcr.Load() {
		return e.NotFoundError("", nil)
	}
	var body struct {
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
		AuthMethod   string   `json:"token_endpoint_auth_method"`
	}
	if err := e.BindBody(&body); err != nil {
		return oauthError(e, http.StatusBadRequest, "invalid_client_metadata", "invalid JSON")
	}
	public := body.AuthMethod == "" || body.AuthMethod == "none"
	if !public && body.AuthMethod != "client_secret_post" && body.AuthMethod != "client_secret_basic" {
		return oauthError(e, http.StatusBadRequest, "invalid_client_metadata", "unsupported token_endpoint_auth_method")
	}
	c, secret, err := s.AddClient(body.ClientName, body.RedirectURIs, public, true)
	if err != nil {
		return oauthError(e, http.StatusBadRequest, "invalid_redirect_uri", err.Error())
	}
	method := "none"
	if !public {
		method = body.AuthMethod
	}
	out := map[string]any{
		"client_id": c.Id, "client_name": c.Name, "redirect_uris": c.RedirectURIs, "token_endpoint_auth_method": method,
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
		"client_id_issued_at": s.cfg.Now().Unix(),
	}
	if secret != "" {
		out["client_secret"] = secret
		out["client_secret_expires_at"] = 0
	}
	return e.JSON(http.StatusCreated, out)
}
