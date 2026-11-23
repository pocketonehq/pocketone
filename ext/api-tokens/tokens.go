// Package tokens adds API tokens to a PocketBase app: long-lived bearer secrets (pbt_...) owned by an auth record,
// optionally limited to scopes that extensions register, listed and revoked one by one. A request with
// `Authorization: Bearer pbt_...` is authenticated as the token's owner; JWTs keep working unchanged.
//
//	app := pocketbase.New()
//	apitokens.MustRegister(app, apitokens.Config{})
//
// It adds the _api_tokens_tokens collection, the token API (api-tokens/v1, /api/_c/api-tokens/v1), an "API tokens" screen under Settings and, on a
// *pocketbase.PocketBase, the `token` command.
package apitokens

import (
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	apitokensv1 "pocketone.dev/ext/contracts/api-tokens/v1"
	verifyv1 "pocketone.dev/ext/contracts/api-tokens/verify/v1"
	"pocketone.dev/ext/extkit"
	"slices"
	"strings"
	"sync"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/security"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/types"
)

// Prefix starts every token secret, so secret scanners can find leaked ones.
const Prefix = "pbt_"

const (
	storeKey      = "api-tokens"
	secretLen     = 40          // random alphanumerics after the prefix (~238 bits)
	touchInterval = time.Minute // last_used is written at most this often per token
	minSecret     = 32          // characters a secret needs after its prefix
)

// Errors returned by [Tokens.Verify]: api-tokens/v1's, so callers through the contract can tell them apart.
var (
	ErrInvalid = apitokensv1.ErrInvalid
	ErrExpired = apitokensv1.ErrExpired
)

// Config is set by whoever builds the binary (with Pocker, an instance can override parts of it in its
// settings).
type Config struct {
	// OAuth configures the OAuth server that comes with tokens (oauth/v1 at /api/_c/oauth/v1/): browser and device
	// sign-in for the CLI, and OAuth clients such as Claude's MCP connectors. On unless OAuth.Disabled.
	OAuth OAuthConfig

	// Collections are the auth collections (by name) whose records may own tokens. Default (empty): every auth
	// collection, superusers and app users alike (an OAuth sign-in, e.g. to /mcp, gets a token owned by the account).
	Collections []string

	// MaxTTL caps a new token's lifetime (0: no cap; a token may never expire).
	MaxTTL time.Duration

	// Now is the clock (tests).
	Now func() time.Time
}

// Tokens is the token service of one app. Get it with [For].
type Tokens struct {
	app      core.App
	cfg      Config
	registry registry

	mu       sync.RWMutex
	prefixes []string // token prefixes this app accepts: Prefix, then RegisterPrefix's
}

//go:embed ui
var uiFiles embed.FS

// MustRegister is [Register] that panics on error.
func MustRegister(app core.App, cfg Config) *Tokens {
	t, err := Register(app, cfg)
	if err != nil {
		panic(err)
	}
	return t
}

// Register adds API tokens to app: the _api_tokens_tokens collection (on bootstrap), the bearer middleware, routes and
// dashboard screen (on serve), and on a *pocketbase.PocketBase the `token` command. Call it before app.Start(), or
// from an extension's Register (as [Extension] does).
func Register(app core.App, cfg Config) (*Tokens, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	t := &Tokens{app: app, cfg: cfg, prefixes: []string{Prefix}}
	if p, ok := app.Store().Get(pendingKey).(*registry); ok { // registered by pb_hooks before Register
		for _, s := range p.list() {
			if err := t.registry.add(s); err != nil {
				return nil, err
			}
		}
		app.Store().Remove(pendingKey)
	}
	app.Store().Set(storeKey, t)
	// api-tokens/v1: Verify in Go, and the token API under /api/_c/api-tokens/v1 (the contracts' seal mounts it)
	apitokensv1.Contract.Provide(app, issuer{t})
	verifyv1.Contract.Provide(app, verifier{t}) // api-tokens.verify/v1: other servers check a token (superusers)
	if !cfg.OAuth.Disabled {
		if _, err := registerOAuth(app, cfg.OAuth); err != nil {
			return nil, err
		}
	}
	// the scopes of the wire contracts this app offers (remote, remote:read, ...), on their exact paths, once its
	// contracts are sealed
	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		for _, ws := range extkit.ProvidedScopes(app) {
			if err := t.registry.add(Scope{Name: ws.Name, Description: ws.Description, Paths: ws.Paths}); err != nil {
				return err
			}
		}
		return se.Next()
	})

	app.OnBootstrap().BindFunc(func(e *core.BootstrapEvent) error {
		if err := e.Next(); err != nil {
			return err
		}
		return EnsureSchema(e.App)
	})

	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		se.Router.Bind(t.middleware(), t.scopeHeadersMiddleware())
		ui, err := fs.Sub(uiFiles, "ui")
		if err != nil {
			return err
		}
		se.UIExtensions = append(se.UIExtensions, core.UIExtension{Name: "api-tokens", FS: ui})
		return se.Next()
	})

	// an owner is gone: so are its tokens
	app.OnRecordAfterDeleteSuccess().BindFunc(func(e *core.RecordEvent) error {
		if e.Record.Collection().IsAuth() {
			if _, err := t.revokeWhere(dbx.HashExp{"owner_collection": e.Record.Collection().Id, "owner": e.Record.Id}); err != nil {
				e.App.Logger().Warn("api-tokens: revoke the deleted owner's tokens", "error", err)
			}
		}
		return e.Next()
	})

	app.Cron().MustAdd("__pbTokensCleanup__", "17 3 * * *", func() {
		if n, err := t.DeleteExpired(); err != nil {
			app.Logger().Warn("api-tokens: delete expired", "error", err)
		} else if n > 0 {
			app.Logger().Info("api-tokens: deleted expired tokens", "count", n)
		}
	})

	if pb, ok := app.(*pocketbase.PocketBase); ok {
		pb.RootCmd.AddCommand(NewCommand(t))
	}
	return t, nil
}

// For returns the token service registered on app, or nil.
func For(app core.App) *Tokens {
	t, _ := app.Store().Get(storeKey).(*Tokens)
	return t
}

// App returns the app this service belongs to.
func (t *Tokens) App() core.App { return t.app }

// RegisterScope adds a scope tokens can be limited to (see [Scope]). Registering a name again merges its paths,
// methods and guards. It fails for an invalid name or one whose header collides with another scope's.
func (t *Tokens) RegisterScope(s Scope) error { return t.registry.add(s) }

// MustRegisterScope is [Tokens.RegisterScope] that panics on error.
func (t *Tokens) MustRegisterScope(s Scope) {
	if err := t.RegisterScope(s); err != nil {
		panic(err)
	}
}

// Scopes returns the registered scopes, sorted by name.
func (t *Tokens) Scopes() []Scope { return t.registry.list() }

// AllowedCollection reports whether records of this auth collection may own tokens.
func (t *Tokens) AllowedCollection(c *core.Collection) bool {
	return c != nil && c.IsAuth() && (len(t.cfg.Collections) == 0 || slices.Contains(t.cfg.Collections, c.Name))
}

// CreateOptions describe a new token.
type CreateOptions struct {
	Name string
	// Scopes limit the token: registered names, or "name=value" for a scope with Values. Empty: the owner's full
	// rights.
	Scopes []string
	// Expires is when the token stops working (zero: never, unless Config.MaxTTL caps it).
	Expires time.Time
	// ClientId is the OAuth client issuing it, if any.
	ClientId string
	// Audience binds the token to a resource URL (RFC 8707): it authenticates only under that URL's path (and
	// remote/v1's whoami). Empty: anywhere its scopes allow.
	Audience string
}

// Create issues a token for owner and returns its secret (shown once, never stored) and its record.
func (t *Tokens) Create(owner *core.Record, opts CreateOptions) (string, *core.Record, error) {
	scopes, err := t.checkNew(owner, &opts)
	if err != nil {
		return "", nil, err
	}
	secret := Prefix + security.RandomString(secretLen)
	rec, err := t.save(owner, scopes, opts, secret, len(Prefix))
	if err != nil {
		return "", nil, err
	}
	return secret, rec, nil
}

// Import stores a token issued elsewhere, with the secret its owner already holds, so it keeps working here: a
// PocketHost personal access token moving onto API tokens, or a pbt_ token from another app. The secret must start
// with a registered prefix ([Tokens.RegisterPrefix]; pbt_ always is) and have at least 32 characters after it. Like
// [Tokens.Create] otherwise: only its hash is stored, and owner, scopes and expiry are checked the same way.
func (t *Tokens) Import(owner *core.Record, secret string, opts CreateOptions) (*core.Record, error) {
	if !t.IsSecret(secret) {
		return nil, fmt.Errorf("api-tokens: an imported secret starts with one of %v and has at least %d characters after it",
			t.Prefixes(), minSecret)
	}
	scopes, err := t.checkNew(owner, &opts)
	if err != nil {
		return nil, err
	}
	return t.save(owner, scopes, opts, secret, len(t.prefixOf(secret)))
}

// checkNew checks a new token's owner, scopes and expiry (capped by Config.MaxTTL).
func (t *Tokens) checkNew(owner *core.Record, opts *CreateOptions) ([]string, error) {
	if owner == nil || !t.AllowedCollection(owner.Collection()) {
		return nil, fmt.Errorf("api-tokens: records of this collection can't own API tokens")
	}
	scopes, err := t.normalizeScopes(owner, opts.Scopes)
	if err != nil {
		return nil, err
	}
	now := t.cfg.Now()
	if t.cfg.MaxTTL > 0 {
		limit := now.Add(t.cfg.MaxTTL)
		if opts.Expires.IsZero() || opts.Expires.After(limit) {
			opts.Expires = limit
		}
	}
	if !opts.Expires.IsZero() && !opts.Expires.After(now) {
		return nil, fmt.Errorf("api-tokens: expiry is in the past")
	}
	return scopes, nil
}

// save writes a token record for secret; prefixLen is its prefix's length (the display prefix shows 6 more).
func (t *Tokens) save(owner *core.Record, scopes []string, opts CreateOptions, secret string, prefixLen int) (*core.Record, error) {
	col, err := t.app.FindCachedCollectionByNameOrId(CollectionId)
	if err != nil {
		return nil, err
	}
	rec := core.NewRecord(col)
	rec.Set("owner_collection", owner.Collection().Id)
	rec.Set("owner", owner.Id)
	rec.Set("name", strings.TrimSpace(opts.Name))
	rec.Set("hash", hashSecret(secret))
	rec.Set("prefix", secret[:prefixLen+6])
	rec.Set("scopes", scopes)
	rec.Set("client_id", opts.ClientId)
	rec.Set("audience", strings.TrimSpace(opts.Audience))
	if !opts.Expires.IsZero() {
		rec.Set("expires", opts.Expires.UTC())
	}
	if err := t.app.Save(rec); err != nil {
		return nil, err
	}
	return rec, nil
}

// normalizeScopes checks a new token's scopes for owner: registered, allowed for its collection, values only on
// scopes with Values (and passing their Check). Names are lower-cased, values kept as given; a bare name drops the
// same scope's values (it already holds every value).
func (t *Tokens) normalizeScopes(owner *core.Record, in []string) ([]string, error) {
	col := owner.Collection()
	out := []string{}
	for _, raw := range in {
		name, value := apitokensv1.SplitScope(strings.TrimSpace(raw))
		name = strings.ToLower(name)
		s := apitokensv1.JoinScope(name, value)
		if name == "" || slices.Contains(out, s) {
			continue
		}
		sc, ok := t.registry.get(name)
		if !ok {
			return nil, fmt.Errorf("api-tokens: unknown scope %q", name)
		}
		if len(sc.Collections) > 0 && !slices.Contains(sc.Collections, col.Name) {
			return nil, fmt.Errorf("api-tokens: %s records can't hold the %q scope", col.Name, name)
		}
		if strings.Contains(raw, apitokensv1.ScopeValueSep) {
			if sc.Values == nil {
				return nil, fmt.Errorf("api-tokens: the %q scope takes no value", name)
			}
			if !ValidScopeValue(value) {
				return nil, fmt.Errorf("api-tokens: invalid value %q for the %q scope", value, name)
			}
			if sc.Values.Check != nil {
				if err := sc.Values.Check(owner, value); err != nil {
					return nil, fmt.Errorf("api-tokens: %s: %w", s, err)
				}
			}
		}
		out = append(out, s)
	}
	out = slices.DeleteFunc(out, func(s string) bool {
		name, value := apitokensv1.SplitScope(s)
		return value != "" && slices.Contains(out, name)
	})
	slices.Sort(out)
	return out, nil
}

// Info is a verified token.
type Info struct {
	Token    *core.Record
	Owner    *core.Record
	Scopes   []string // empty: the owner's full rights
	Audience string   // RFC 8707 resource it's bound to, or ""
}

// HasScope reports whether the token may act with scope: unscoped tokens have every scope. A bare name is held by
// the name or any "name=value"; "name=value" by the bare name or that value.
func (i *Info) HasScope(scope string) bool { return apitokensv1.HoldsScope(i.Scopes, scope) }

// HasScopeFor reports whether the token may act with scope on value (see [HasScopeFor]).
func (i *Info) HasScopeFor(scope, value string) bool {
	return apitokensv1.HoldsScope(i.Scopes, apitokensv1.JoinScope(scope, value))
}

// Verify resolves a secret to its token and owner. It doesn't record the use (the middleware does).
func (t *Tokens) Verify(secret string) (*Info, error) {
	if !t.IsSecret(secret) {
		return nil, ErrInvalid
	}
	rec, err := t.app.FindFirstRecordByData(CollectionId, "hash", hashSecret(secret))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalid
		}
		return nil, err
	}
	if exp := rec.GetDateTime("expires"); !exp.IsZero() && !exp.Time().After(t.cfg.Now()) {
		return nil, ErrExpired
	}
	col, err := t.app.FindCachedCollectionByNameOrId(rec.GetString("owner_collection"))
	if err != nil || !t.AllowedCollection(col) {
		return nil, ErrInvalid
	}
	owner, err := t.app.FindRecordById(col, rec.GetString("owner"))
	if err != nil {
		return nil, ErrInvalid
	}
	return &Info{Token: rec, Owner: owner, Scopes: scopesOf(rec), Audience: rec.GetString("audience")}, nil
}

func scopesOf(rec *core.Record) []string {
	var s []string
	_ = rec.UnmarshalJSONField("scopes", &s)
	return s
}

// touch records a use, at most once per touchInterval per token, without firing record hooks.
func (t *Tokens) touch(rec *core.Record, ip string) {
	now := t.cfg.Now()
	if last := rec.GetDateTime("last_used"); !last.IsZero() && now.Sub(last.Time()) < touchInterval && rec.GetString("last_ip") == ip {
		return
	}
	dt, _ := types.ParseDateTime(now.UTC())
	_, err := t.app.NonconcurrentDB().Update(Collection,
		dbx.Params{"last_used": dt.String(), "last_ip": ip},
		dbx.HashExp{"id": rec.Id}).Execute()
	if err != nil {
		t.app.Logger().Debug("api-tokens: record last use", "error", err)
	}
}

// List returns owner's tokens, newest first. A nil owner lists every token.
func (t *Tokens) List(owner *core.Record) ([]*core.Record, error) {
	q := t.app.RecordQuery(CollectionId).OrderBy("created DESC")
	if owner != nil {
		q = q.AndWhere(dbx.HashExp{"owner_collection": owner.Collection().Id, "owner": owner.Id})
	}
	var recs []*core.Record
	return recs, q.All(&recs)
}

// Get returns one token by id.
func (t *Tokens) Get(id string) (*core.Record, error) {
	return t.app.FindRecordById(CollectionId, id)
}

// Revoke deletes one token.
func (t *Tokens) Revoke(id string) error {
	rec, err := t.Get(id)
	if err != nil {
		return err
	}
	return t.app.Delete(rec)
}

// RevokeAll deletes every token of owner and returns how many.
func (t *Tokens) RevokeAll(owner *core.Record) (int, error) {
	return t.revokeWhere(dbx.HashExp{"owner_collection": owner.Collection().Id, "owner": owner.Id})
}

// DeleteExpired deletes tokens past their expiry and returns how many.
func (t *Tokens) DeleteExpired() (int, error) {
	dt, _ := types.ParseDateTime(t.cfg.Now().UTC())
	return t.revokeWhere(dbx.And(dbx.NewExp("expires != ''"), dbx.NewExp("expires <= {:now}", dbx.Params{"now": dt.String()})))
}

func (t *Tokens) revokeWhere(where dbx.Expression) (int, error) {
	var recs []*core.Record
	err := t.app.RecordQuery(CollectionId).AndWhere(where).All(&recs)
	if err != nil {
		return 0, err
	}
	n := 0
	err = t.app.RunInTransaction(func(tx core.App) error {
		for _, r := range recs {
			if err := tx.Delete(r); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

// RegisterPrefix lets token secrets start with p as well as pbt_: tokens another system issued, brought in with
// [Tokens.Import] so the secrets their owners already hold keep working (PocketHost's "ph_pat_"). Tokens this app
// creates always start with pbt_. A prefix is lower-case letters, digits and "_", ends with "_", and doesn't overlap
// one already registered (one can't start with another). Registering the same prefix again does nothing.
func (t *Tokens) RegisterPrefix(p string) error {
	if !validPrefix(p) {
		return fmt.Errorf("api-tokens: prefix %q: lower-case letters, digits and \"_\", ending with \"_\"", p)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, q := range t.prefixes {
		if q == p {
			return nil
		}
		if strings.HasPrefix(p, q) || strings.HasPrefix(q, p) {
			return fmt.Errorf("api-tokens: prefix %q overlaps %q", p, q)
		}
	}
	t.prefixes = append(t.prefixes, p)
	return nil
}

// Prefixes lists the prefixes token secrets may start with: pbt_ first, then the registered ones.
func (t *Tokens) Prefixes() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return slices.Clone(t.prefixes)
}

// IsSecret reports whether s has the shape of one of this app's token secrets: a registered prefix and at least 32
// characters after it (pbt_ tokens have 40). Anything else in an Authorization header is left to PocketBase (a JWT).
func (t *Tokens) IsSecret(s string) bool {
	p := t.prefixOf(s)
	return p != "" && len(s)-len(p) >= minSecret
}

// prefixOf is the registered prefix s starts with, or "".
func (t *Tokens) prefixOf(s string) string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, p := range t.prefixes {
		if strings.HasPrefix(s, p) {
			return p
		}
	}
	return ""
}

func validPrefix(p string) bool {
	if len(p) < 2 || !strings.HasSuffix(p, "_") {
		return false
	}
	for _, r := range p {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

func hashSecret(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// Collections returns the auth collections whose records may own tokens.
func (t *Tokens) Collections() ([]string, error) {
	if len(t.cfg.Collections) > 0 {
		return t.cfg.Collections, nil
	}
	cols, err := t.app.FindAllCollections(core.CollectionTypeAuth)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, c := range cols {
		out = append(out, c.Name)
	}
	return out, nil
}
