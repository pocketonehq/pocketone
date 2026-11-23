package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	deployv1 "pocketone.dev/ext/contracts/deploy/v1"
	fsv1 "pocketone.dev/ext/contracts/instance/fs/v1"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxJSONBytes      = 20 << 20
	maxBundleBytes    = 64 << 20 // compressed upload per request; Cloudflare caps bodies at 100 MB
	maxBundleRawBytes = 256 << 20
)

// Instance is what the deploy API needs to know about the instance a request's host names.
type Instance struct {
	ID, Subdomain, OwnerID      string
	Power                       bool
	Suspension, OwnerSuspension string
	// Elsewhere, if set, means another edge serves the instance (421 with this message).
	Elsewhere string
}

// Backend is the control plane: which instance a request is for and who may deploy to it. The default for a single
// app is [AppBackend] (API tokens with the deploy scope, or a superuser JWT); a host brings its own (its tokens,
// instances by hostname).
type Backend interface {
	// Lookup finds the instance a request addresses (e.g. by its Host).
	Lookup(r *http.Request) (Instance, bool)
	// Authenticate says who the request acts as on inst. Return *AuthError to refuse with its status, or
	// ErrNoAccess to answer 404 (so tokens can't probe instance names). Any other error is an outage (500).
	Authenticate(r *http.Request, inst Instance) (Auth, error)
}

// Options configure the API.
type Options struct {
	// Prefix is the API path (default deploy/v1's, /api/_c/deploy/v1). Whoami, restart and logs are remote/v1's.
	Prefix  string
	Backend Backend
	// Files returns the instances' files (instance.fs/v1), looked up at serve time; nil answers 503.
	Files func() fsv1.Files
	// PlatformPaths are code-set paths that aren't the customer's: never compared or deleted.
	PlatformPaths []string
	// IsRemote, if set, reports hosts whose instance another edge runs: their requests go to next (which forwards
	// them) instead of being answered here.
	IsRemote func(host string) bool
	Logger   *slog.Logger
}

// API serves the deploy API.
type API struct {
	opts Options
	log  *slog.Logger

	mu     sync.Mutex
	stores map[string]*Store
}

// New creates the deploy API.
func New(opts Options) *API {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	if opts.Prefix == "" {
		opts.Prefix = deployv1.Prefix
	}
	opts.Prefix = strings.TrimSuffix(opts.Prefix, "/")
	return &API{opts: opts, log: log.With("component", "deploy"), stores: map[string]*Store{}}
}

// Prefix returns the API path.
func (a *API) Prefix() string { return a.opts.Prefix }

// Wrap routes deploy API requests to the API and everything else to next.
func (a *API) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isAPI := r.URL.Path == a.opts.Prefix || strings.HasPrefix(r.URL.Path, a.opts.Prefix+"/")
		if !isAPI || (a.opts.IsRemote != nil && a.opts.IsRemote(hostOf(r))) {
			next.ServeHTTP(w, r)
			return
		}
		a.ServeHTTP(w, r)
	})
}

// httpError is an answer with a status and JSON body {status, message, ...extra}.
type httpError struct {
	status  int
	message string
	extra   map[string]any
}

func (e *httpError) Error() string { return e.message }

func errf(status int, format string, args ...any) error {
	return &httpError{status: status, message: fmt.Sprintf(format, args...)}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *API) fail(w http.ResponseWriter, err error) {
	body := func(status int, msg string) map[string]any { return map[string]any{"status": status, "message": msg} }
	var (
		he *httpError
		me *ManifestError
		be *BundleError
		mb *MissingBlobsError
		ce *ConflictError
		ae *AuthError
		mx *http.MaxBytesError
	)
	switch {
	case errors.As(err, &he):
		b := body(he.status, he.message)
		for k, v := range he.extra {
			b[k] = v
		}
		writeJSON(w, he.status, b)
	case errors.As(err, &ae):
		writeJSON(w, ae.Status, body(ae.Status, ae.Message))
	case errors.As(err, &me), errors.As(err, &be):
		writeJSON(w, 400, body(400, err.Error()))
	case errors.As(err, &mb):
		b := body(409, mb.Error())
		b["missing"] = mb.Missing
		writeJSON(w, 409, b)
	case errors.As(err, &ce):
		writeJSON(w, 409, body(409, ce.Error()))
	case errors.As(err, &mx):
		writeJSON(w, 413, body(413, "Request too large"))
	default:
		a.log.Error("deploy API error", "error", err)
		writeJSON(w, 500, body(500, "Deploy failed: "+err.Error()))
	}
}

func hostOf(r *http.Request) string {
	h := strings.ToLower(r.Host)
	if i := strings.LastIndexByte(h, ':'); i >= 0 && !strings.Contains(h[i:], "]") {
		if _, err := strconv.Atoi(h[i+1:]); err == nil {
			h = h[:i]
		}
	}
	return h
}

// clientIP: the edge sits behind Cloudflare (and possibly a proxy), so the peer address is rarely the client.
func clientIP(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("Cf-Connecting-Ip")); v != "" {
		return v
	}
	if v := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); v != "" {
		return v
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type reqCtx struct {
	inst  Instance
	auth  Auth
	fs    fsv1.FS
	store *Store
	dir   string
}

// lock takes the instance's writers' lock (instance.fs/v1's): commits and file writes never interleave.
func (c *reqCtx) lock(ctx context.Context) (func(), error) { return c.fs.Lock(ctx) }

func (a *API) storeFor(id, dir, stateDir string) *Store {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.stores[id]
	if !ok || s.instanceDir != dir {
		s = newStore(stateDir, dir, a.opts.PlatformPaths)
		a.stores[id] = s
	}
	return s
}

// filesFor is the instance's instance.fs/v1 FS: a single app's own (AppInstanceID) is fsv1.Self.
func (a *API) filesFor(ctx context.Context, inst Instance) (fsv1.FS, error) {
	var files fsv1.Files
	if a.opts.Files != nil {
		files = a.opts.Files()
	}
	if files == nil {
		return nil, errf(503, "The server is starting; try again.")
	}
	id := inst.ID
	if id == AppInstanceID {
		id = fsv1.Self
	}
	f, err := files.Instance(ctx, id)
	if errors.Is(err, fsv1.ErrNoInstance) {
		return nil, errf(404, "%s not found", inst.Subdomain)
	}
	return f, err
}

// authorize resolves the instance and checks the request may act on it. Not yours and not in the token's scope get
// the same 404, so tokens can't probe instance names.
func (a *API) authorize(r *http.Request, needWrite bool) (*reqCtx, error) {
	host := hostOf(r)
	inst, ok := a.opts.Backend.Lookup(r)
	if !ok {
		return nil, errf(404, "%s not found", host)
	}
	auth, err := a.opts.Backend.Authenticate(r, inst)
	if errors.Is(err, ErrNoAccess) {
		return nil, errf(404, "%s not found", host)
	}
	if err != nil {
		return nil, err
	}
	if needWrite && !auth.can("write") {
		return nil, errf(403, "This token is read-only. Create a token with write access to deploy.")
	}
	if s := strings.TrimSpace(inst.OwnerSuspension); s != "" {
		return nil, errf(403, "%s", s)
	}
	if s := strings.TrimSpace(inst.Suspension); s != "" {
		return nil, errf(403, "%s", s)
	}
	if inst.Elsewhere != "" {
		return nil, errf(http.StatusMisdirectedRequest, "%s", inst.Elsewhere)
	}
	f, err := a.filesFor(r.Context(), inst)
	if err != nil {
		return nil, err
	}
	dir, err := f.Dir(r.Context())
	if err != nil {
		return nil, errf(503, "The server is starting; try again.")
	}
	cacheDir, err := f.CacheDir(r.Context(), Name)
	if err != nil {
		return nil, err
	}
	return &reqCtx{inst: inst, auth: auth, fs: f, store: a.storeFor(inst.ID, dir, cacheDir), dir: dir}, nil
}

func readJSON(w http.ResponseWriter, r *http.Request) (map[string]any, error) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxJSONBytes))
	if err != nil {
		return nil, err
	}
	body := map[string]any{}
	if len(strings.TrimSpace(string(b))) == 0 {
		return body, nil
	}
	if err := json.Unmarshal(b, &body); err != nil {
		return nil, errf(400, "Invalid JSON body: %v", err)
	}
	return body, nil
}

func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, a.opts.Prefix), "/")
	var err error
	switch {
	case path == "/check" && r.Method == http.MethodPost:
		err = a.check(w, r)
	case path == "/blobs" && r.Method == http.MethodPost:
		err = a.blobs(w, r)
	case path == "/commit" && r.Method == http.MethodPost:
		err = a.commit(w, r)
	case path == "/files" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		err = a.listFiles(w, r)
	case path == "/files" && r.Method == http.MethodDelete:
		err = a.deleteFile(w, r)
	case path == "/files/raw" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		err = a.readFile(w, r)
	case path == "/files/raw" && r.Method == http.MethodPut:
		err = a.writeFile(w, r)
	case path == "/files/bundle" && r.Method == http.MethodPost:
		err = a.readBundle(w, r)
	case path == "/tree" && r.Method == http.MethodGet:
		err = a.tree(w, r)
	default:
		err = errf(404, "Not found")
	}
	if err != nil && err != errHeadersSent {
		a.fail(w, err)
	}
}

// errHeadersSent: a streaming handler failed after answering; nothing more to write.
var errHeadersSent = errors.New("headers sent")

func (a *API) check(w http.ResponseWriter, r *http.Request) error {
	c, err := a.authorize(r, true)
	if err != nil {
		return err
	}
	body, err := readJSON(w, r)
	if err != nil {
		return err
	}
	m, err := validateManifest(body["files"])
	if err != nil {
		return err
	}
	missing, err := c.store.findMissing(m, body["nodeModules"] == true)
	if err != nil {
		return err
	}
	a.log.Debug("check", "instance", c.inst.ID, "files", len(m), "missing", len(missing))
	writeJSON(w, 200, map[string]any{"missing": missing})
	return nil
}

func (a *API) blobs(w http.ResponseWriter, r *http.Request) error {
	c, err := a.authorize(r, true)
	if err != nil {
		return err
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBundleBytes))
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return errf(400, "Empty upload")
	}
	entries, err := decodeBundle(b, maxBundleRawBytes)
	if err != nil {
		return err
	}
	stored := 0
	for _, e := range entries {
		ok, err := c.store.putBlob(e.hash, e.data)
		if err != nil {
			return err
		}
		if ok {
			stored++
		}
	}
	writeJSON(w, 200, map[string]any{"received": len(entries), "stored": stored})
	return nil
}

// restartNeeded: files are swapped in while the app runs (each rename is atomic), so the whole batch lands before
// anything restarts. The running app picks up most of it by itself: pb_pages, pb_public and pb_templates are read on each request,
// and every runtime watches pb_hooks (PocketBase's --hooksWatch, Pocker's and Docker's shared watcher). Only a
// migration written (migrations run at startup), changed packages (require caches them) or a newly created pb_hooks
// (a watcher only sees folders that existed when the app started) need a restart. Deploy never restarts on its own:
// the client asks, with "restart": true on the commit or POST /restart. Deleting a migration doesn't undo it.
func restartNeeded(st *staged, createsHooks bool) bool {
	if st.replaceNodeModules || createsHooks {
		return true
	}
	for _, p := range st.plan.toWrite {
		if strings.HasPrefix(p, "pb_migrations/") || isNodeModulesPath(p) {
			return true
		}
	}
	for _, p := range st.plan.toDelete {
		if isNodeModulesPath(p) {
			return true
		}
	}
	return false
}

// writesUnder reports whether the plan writes a file under prefix.
func writesUnder(st *staged, prefix string) bool {
	for _, p := range st.plan.toWrite {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// restart restarts the instance so it applies migrations and packages: POST /restart. An instance that isn't
// running is left alone (it applies them when it next starts).
func (a *API) restart(w http.ResponseWriter, r *http.Request) error {
	c, err := a.authorize(r, true)
	if err != nil {
		return err
	}
	ctx := context.WithoutCancel(r.Context())
	unlock, err := c.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	res := restartResult{}
	if running, err := c.fs.Running(ctx); err != nil || !c.inst.Power || !running {
		writeJSON(w, 200, res)
		return nil
	}
	res.WasRunning = true
	startErr, err := c.fs.Restart(ctx, nil)
	if err != nil {
		return err
	}
	if startErr != nil {
		res.StartError = startErr.Error()
	} else {
		res.Restarted = true
	}
	a.log.Info("restart", "instance", c.inst.ID, "subdomain", c.inst.Subdomain, "restarted", res.Restarted)
	writeJSON(w, 200, res)
	return nil
}

type restartResult struct {
	WasRunning bool   `json:"wasRunning"`
	Restarted  bool   `json:"restarted"`
	StartError string `json:"startError,omitempty"`
}

// parsePartial reads {"partial": true, "delete": [paths]} from a commit body (nil for a full deploy).
func parsePartial(body map[string]any) (*Partial, error) {
	if body["partial"] != true {
		return nil, nil
	}
	p := &Partial{}
	raw, _ := body["delete"].([]any)
	if len(raw) > maxManifestFiles {
		return nil, &ManifestError{fmt.Sprintf("Too many deletes (%d); at most %d.", len(raw), maxManifestFiles)}
	}
	for _, v := range raw {
		path, _ := v.(string)
		if !fsv1.IsCodePath(path) || isNodeModulesPath(path) {
			return nil, &ManifestError{fmt.Sprintf("delete: %q is outside the deployable set", path)}
		}
		p.Delete = append(p.Delete, path)
	}
	return p, nil
}

type commitResult struct {
	Instance            map[string]any `json:"instance"`
	FileCount           int            `json:"fileCount"`
	Written             []string       `json:"written"`
	Deleted             []string       `json:"deleted"`
	WrittenCount        int            `json:"writtenCount"`
	DeletedCount        int            `json:"deletedCount"`
	Unchanged           int            `json:"unchanged"`
	TotalBytes          int64          `json:"totalBytes"`
	ReplacedNodeModules bool           `json:"replacedNodeModules"`
	NothingChanged      bool           `json:"nothingChanged"`
	WasRunning          bool           `json:"wasRunning"`
	Restarted           bool           `json:"restarted"`
	StartError          string         `json:"startError,omitempty"`
	// RestartNeeded: the commit wrote a migration or changed packages and the app hasn't restarted yet (POST
	// /restart, or commit with "restart": true).
	RestartNeeded bool `json:"restartNeeded,omitempty"`
}

func first(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func (a *API) commit(w http.ResponseWriter, r *http.Request) error {
	c, err := a.authorize(r, true)
	if err != nil {
		return err
	}
	body, err := readJSON(w, r)
	if err != nil {
		return err
	}
	m, err := validateManifest(body["files"])
	if err != nil {
		return err
	}
	nodeModules := body["nodeModules"] == true
	wantRestart := body["restart"] == true
	partial, err := parsePartial(body)
	if err != nil {
		return err
	}
	// a restart outlives a client that hangs up
	ctx := context.WithoutCancel(r.Context())

	unlock, err := c.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()

	_ = c.store.cleanTmp()
	// slow work (copying, hashing) happens here, before the instance is touched
	st, err := c.store.stage(m, nodeModules, partial)
	if err != nil {
		return err
	}
	changed := append(append([]string{}, st.plan.toWrite...), st.plan.toDelete...)
	if st.replaceNodeModules {
		changed = append(changed, fsv1.NodeModulesDir)
	}
	nothingChanged := len(changed) == 0
	_, hooksDirErr := os.Stat(filepath.Join(c.dir, "pb_hooks"))
	createsHooks := hooksDirErr != nil && writesUnder(st, "pb_hooks/")
	needsRestart := restartNeeded(st, createsHooks)

	var applied *applyResult
	running, err := c.fs.Running(ctx)
	if err != nil {
		_ = os.RemoveAll(st.stagingDir)
		return err
	}
	wasRunning := false
	restarted, startError := false, ""
	if needsRestart && wantRestart && running && c.inst.Power {
		wasRunning = true
		var startErr error
		startErr, err = c.fs.Restart(ctx, func(context.Context) error {
			var aerr error
			applied, aerr = c.store.apply(m, st)
			return aerr
		})
		if startErr != nil {
			startError = startErr.Error()
		} else if err == nil {
			restarted = true
		}
	} else {
		applied, err = c.store.apply(m, st)
	}
	if err != nil {
		_ = os.RemoveAll(st.stagingDir)
		return err
	}

	if !nothingChanged {
		// the manifest and its blobs make the next deploy's upload smaller; gc keeps the last few
		if err := c.store.recordManifest(strconv.FormatInt(time.Now().UnixMilli(), 10), m); err != nil {
			a.log.Warn("recordManifest failed", "instance", c.inst.ID, "error", err)
		}
		if _, err := c.store.gc(keepManifests); err != nil {
			a.log.Warn("deploy gc failed", "instance", c.inst.ID, "error", err)
		}
	}

	// the app only picks up these changes when it next starts; an instance that isn't running will on its own
	pendingRestart := needsRestart && !restarted && startError == "" && running

	a.log.Info("deploy", "instance", c.inst.ID, "subdomain", c.inst.Subdomain,
		"written", len(applied.written), "deleted", len(applied.deleted), "unchanged", applied.unchanged, "restarted", restarted)
	writeJSON(w, 200, commitResult{
		Instance: map[string]any{"id": c.inst.ID, "subdomain": c.inst.Subdomain}, FileCount: len(m),
		Written: first(applied.written, 500), Deleted: first(applied.deleted, 500),
		WrittenCount: len(applied.written), DeletedCount: len(applied.deleted), Unchanged: applied.unchanged,
		TotalBytes: applied.totalBytes, ReplacedNodeModules: applied.replacedNodeModule, NothingChanged: nothingChanged,
		WasRunning: wasRunning, Restarted: restarted, StartError: startError, RestartNeeded: pendingRestart,
	})
	return nil
}
