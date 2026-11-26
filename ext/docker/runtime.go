// Package docker is a runtime provider for ext/multisite: it runs each instance in its own container with an official
// PocketBase (or PocketOne) binary of the version the instance asks for, so one host can serve every version.
//
// Each instance folder (<DataRoot>/<id>) is mounted at /home/pockethost, the binary read-only at
// /home/pockethost/pocketbase, and the container runs `./pocketbase serve --http 0.0.0.0:8090` (plus --dev and
// --encryptionEnv when set), published on a loopback port the host proxies to. That is the layout PocketHost's Node
// edge uses, so its instance folders and binary cache move as they are. A container starts on the first request,
// stops after IdleTTL without requests, and keeps running across a host restart (the next process adopts it).
package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	remotev1 "pocketone.dev/ext/contracts/remote/v1"
	runtimev1 "pocketone.dev/ext/contracts/runtime/v1"
	"pocketone.dev/ext/multisite"
)

// Labels on every container this runtime creates.
const (
	LabelRuntime  = "p1.runtime"  // "docker"
	LabelInstance = "p1.instance" // instance id
	LabelVersion  = "p1.version"  // resolved release: "pb:0.36.9" or "p1:0.0.1"
	LabelSpec     = "p1.spec"     // hash of the container's configuration
)

// Spec is what a host adds to an instance's container (see [Config.Prepare]).
type Spec struct {
	Binds  []string          // extra host:container[:ro] mounts, e.g. platform hooks
	Env    map[string]string // extra environment, applied over the instance's secrets
	Labels map[string]string
	// Cmd, if set, adjusts the container's command. It gets the default (./pocketbase serve --http 0.0.0.0:8090, plus
	// --dev and --encryptionEnv when they apply), e.g. to start a release that predates a flag.
	Cmd func(defaults []string) []string
	// HealthPath is what is polled until the instance is ready (default /api/health); with HealthAnyStatus, any HTTP
	// answer counts, not only 200 (for releases without a health endpoint).
	HealthPath      string
	HealthAnyStatus bool
}

// Config configures a [Runtime].
type Config struct {
	Socket   string         // Docker socket (default [DefaultSocket])
	Image    string         // base image the binary runs in (required); pulled when missing
	DataRoot string         // host folder with one folder per instance (<DataRoot>/<id>; Instance.Dir overrides)
	Home     string         // the instance folder inside the container (default /home/pockethost)
	Binaries *Binaries      // the release binary cache (required)
	Versions VersionsSource // the releases this host serves (required); missing ones are seeded in the background

	DefaultVersion string // for instances without a Version (e.g. "pb:0.36.*" or "p1:0.0.*"); empty = such instances fail
	// MinUpstream is the oldest upstream PocketBase release this host serves (default [MinUpstream]). A host that keeps
	// older releases in its binary cache and lists them in Versions can lower it.
	MinUpstream string

	IdleTTL             time.Duration // stop after this long without requests (default 10m)
	MaxConcurrentStarts int           // containers starting at once (default 4)
	StartTimeout        time.Duration // until /api/health answers (default 60s)
	StopGrace           time.Duration // SIGINT, then kill after this (default 10s)

	// Per-container limits (0 = unlimited / Docker's default).
	NanoCPUs   int64
	PidsLimit  int64
	NofileSoft int64
	NofileHard int64
	// Hardening: drop these capabilities ("ALL") and set no-new-privileges.
	CapDrop         []string
	NoNewPrivileges bool
	Network         string // Docker network (default: the bridge)
	HostIP          string // where container ports are published (default 127.0.0.1)

	// Serves picks the instances this runtime runs (default: those asking for "docker", or any version when nothing
	// else claims them; see [multisite.Runtime.Serves]).
	Serves func(inst multisite.Instance) bool
	// Prepare adds host-specific mounts, environment and labels (e.g. platform hooks, an app URL). version is the
	// resolved release with its line, e.g. "pb:0.36.9" or "p1:0.0.1".
	Prepare func(ctx context.Context, inst multisite.Instance, version string, spec *Spec) error
	// Version, if set, rewrites an instance's version before the binary is picked (e.g. "every instance on this host
	// runs PocketOne unless it says pb:").
	Version func(inst multisite.Instance) string

	// AdoptImage: at start, also adopt running containers of this image that carry no labels and are named after an
	// instance id (containers started by another manager, e.g. PocketHost's Node edge).
	AdoptImage string

	OnLog  func(instanceID string, stderr bool, line string) // container output (default: debug log)
	Logger *slog.Logger
}

// Runtime runs instances in containers; it implements [multisite.Runtime] (+ Syncer, Statser, Shutdowner, Starter).
type Runtime struct {
	cfg  Config
	e    *engine
	log  *slog.Logger
	sem  chan struct{}
	http *http.Client
	tr   *http.Transport

	mu     sync.Mutex
	ctrs   map[string]*ctr
	held   map[string]chan struct{} // instances under maintenance (WithInstanceStopped)
	seedMu sync.Mutex               // one seeding run at a time
	ready  atomic.Bool
	watch  *multisite.ScriptWatcher // restarts a container when its pb_hooks change

	releases atomic.Pointer[[]Release] // the current list from Versions
	logs     map[string]*logRing       // recent output per instance, for remote/v1 (under mu)

	bg     context.Context
	cancel context.CancelFunc

	onExit atomic.Pointer[func(runtimev1.Exit)] // runtime/v1's exit signal, set by the host
}

// healthHint is how the host's supervisor asks an instance whether it's up (runtime/v1's HealthChecker).
type healthHint struct {
	path      string
	anyStatus bool
}

type ctr struct {
	inst string

	mu        sync.Mutex          // serializes start/stop
	container string              // container id ("" = not running)
	last      *multisite.Instance // the instance as last started (nil = adopted, not started by this process)
	port      int
	hash      string // its LabelSpec ("" = adopted from another manager: kept until the instance changes)
	dirty     bool   // the instance changed since the container started

	poweredBy atomic.Value // string: the X-Powered-By for responses that carry none ("PocketBase 0.36.9")

	running  atomic.Bool  // container != "" (readable without mu, which a start holds for seconds)
	started  atomic.Int64 // unix nanos the container started (or was adopted)
	portNow  atomic.Int64 // port, readable without mu (a request waiting out a restart stops if it changes)
	inflight atomic.Int64
	lastUsed atomic.Int64 // unix nanos
	health   atomic.Pointer[healthHint]
}

var (
	_ multisite.Runtime        = (*Runtime)(nil)
	_ multisite.Syncer         = (*Runtime)(nil)
	_ multisite.Statser        = (*Runtime)(nil)
	_ multisite.Shutdowner     = (*Runtime)(nil)
	_ multisite.RuntimeStarter = (*Runtime)(nil)

	_ runtimev1.ExitNotifier  = (*Runtime)(nil)
	_ runtimev1.RunningLister = (*Runtime)(nil)
	_ runtimev1.HealthChecker = (*Runtime)(nil)
)

// OnExit implements runtime/v1's ExitNotifier: fn hears every container that stops, ends on its own or fails to start.
func (rt *Runtime) OnExit(fn func(runtimev1.Exit)) { rt.onExit.Store(&fn) }

func (rt *Runtime) exit(x runtimev1.Exit) {
	if fn := rt.onExit.Load(); fn != nil {
		(*fn)(x)
	}
}

// RunningIDs implements runtime/v1's RunningLister: instances with a running container (adopted ones included).
func (rt *Runtime) RunningIDs() []string {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	ids := make([]string, 0, len(rt.ctrs))
	for id, c := range rt.ctrs {
		if c.running.Load() {
			ids = append(ids, id)
		}
	}
	return ids
}

// HealthCheck implements runtime/v1's HealthChecker: the health path and rule its container started with (a release
// without a health endpoint takes any answer). An adopted container, started by an earlier process, is up: any answer.
func (rt *Runtime) HealthCheck(id string) (string, bool) {
	rt.mu.Lock()
	c, ok := rt.ctrs[id]
	rt.mu.Unlock()
	if !ok {
		return "", false
	}
	if h := c.health.Load(); h != nil {
		return h.path, h.anyStatus
	}
	if c.running.Load() {
		return "", true
	}
	return "", false
}

// ended is a container that stopped on its own (its output stream ended): it's dropped and the host hears an exit.
// A stop the runtime made itself clears the container first, so it isn't reported twice.
func (rt *Runtime) ended(instID, containerID string) {
	if rt.bg.Err() != nil { // shutting down: containers keep running for the next process
		return
	}
	c := rt.get(instID)
	c.mu.Lock()
	if c.container != containerID {
		c.mu.Unlock()
		return
	}
	c.container, c.port, c.hash, c.dirty = "", 0, "", false
	c.portNow.Store(0)
	c.running.Store(false)
	rt.watch.Remove(instID)
	c.mu.Unlock()
	rt.log.Info("docker: instance stopped on its own", "instance", instID)
	rt.exit(runtimev1.Exit{InstanceID: instID, Err: errors.New("the instance stopped on its own (see its log)")})
}

// New returns a runtime for cfg. Call Start before serving.
func New(cfg Config) (*Runtime, error) {
	if cfg.Image == "" || cfg.Binaries == nil || cfg.DataRoot == "" || cfg.Versions == nil {
		return nil, errors.New("docker: Image, Binaries, Versions and DataRoot are required")
	}
	if cfg.Socket == "" {
		cfg.Socket = DefaultSocket()
	}
	if cfg.Home == "" {
		cfg.Home = "/home/pockethost"
	}
	if cfg.IdleTTL <= 0 {
		cfg.IdleTTL = 10 * time.Minute
	}
	if cfg.MaxConcurrentStarts <= 0 {
		cfg.MaxConcurrentStarts = 4
	}
	if cfg.StartTimeout <= 0 {
		cfg.StartTimeout = 60 * time.Second
	}
	if cfg.StopGrace <= 0 {
		cfg.StopGrace = 10 * time.Second
	}
	if cfg.HostIP == "" {
		cfg.HostIP = "127.0.0.1"
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	// its own transport: an edge's egress policy guards http.DefaultTransport and refuses loopback
	tr := &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     90 * time.Second,
	}
	bg, cancel := context.WithCancel(context.Background())
	rt := &Runtime{cfg: cfg, e: newEngine(cfg.Socket), log: log, sem: make(chan struct{}, cfg.MaxConcurrentStarts),
		http: &http.Client{Transport: tr, Timeout: 3 * time.Second}, tr: tr, ctrs: map[string]*ctr{}, bg: bg, cancel: cancel}
	tr.DialContext = rt.dial
	rt.watch = multisite.NewScriptWatcher(log, rt.scriptsChanged)
	return rt, nil
}

// proxiedInstance carries the container a proxied request is for, so dial can wait out a restart.
type proxiedInstance struct{}

type proxied struct {
	c    *ctr
	port int
}

// stillServing: the instance's container is the one on port and still runs (inspected when check is set).
func (rt *Runtime) stillServing(ctx context.Context, p proxied, check bool) bool {
	if p.c.portNow.Load() != int64(p.port) {
		return false // stopped, or replaced by a new container on another port
	}
	if !check {
		return true
	}
	info, err := rt.e.inspect(ctx, p.c.inst)
	return err == nil && info.State.Running
}

// dial connects to an instance's port. PocketBase restarts itself in place when its pb_hooks change (its hooks
// watcher is on): the container keeps running and the port is closed for a moment. A proxied request then waits,
// as it does while the runtime restarts a container, instead of failing. It stops waiting when the container is
// gone (inspected every second) or after StartTimeout. Nothing has been sent yet, so this is safe for any request.
func (rt *Runtime) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	d := net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	conn, err := d.DialContext(ctx, network, addr)
	px, ok := ctx.Value(proxiedInstance{}).(proxied)
	if err == nil || !ok || !errors.Is(err, syscall.ECONNREFUSED) {
		return conn, err
	}
	deadline := time.Now().Add(rt.cfg.StartTimeout)
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		if conn, err = d.DialContext(ctx, network, addr); err == nil || !errors.Is(err, syscall.ECONNREFUSED) {
			return conn, err
		}
		if !rt.stillServing(ctx, px, i%10 == 9) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, err
		}
	}
}

// Name implements [multisite.Runtime].
func (rt *Runtime) Name() string { return "docker" }

// Serves implements [multisite.Runtime]: every instance, unless Config.Serves says otherwise.
func (rt *Runtime) Serves(inst multisite.Instance) bool {
	if rt.cfg.Serves != nil {
		return rt.cfg.Serves(inst)
	}
	return true
}

// Ready implements [multisite.Runtime]: true once Start has adopted the running containers.
func (rt *Runtime) Ready() bool { return rt.ready.Load() }

// Start implements [multisite.RuntimeStarter]: it makes sure the image is there, adopts running containers and starts the
// idle reaper.
func (rt *Runtime) Start(ctx context.Context) error {
	ok, err := rt.e.imageExists(ctx, rt.cfg.Image)
	if err != nil {
		return fmt.Errorf("docker: %w", err)
	}
	if !ok {
		rt.log.Info("docker: pulling image", "image", rt.cfg.Image)
		if err := rt.e.pull(ctx, rt.cfg.Image); err != nil {
			return fmt.Errorf("docker: pull %s: %w", rt.cfg.Image, err)
		}
	}
	if err := rt.adopt(ctx); err != nil {
		return err
	}
	if err := rt.loadReleases(ctx); err != nil {
		return err
	}
	go rt.reap()
	go rt.followReleases()
	rt.ready.Store(true)
	return nil
}

// loadReleases reads the list from Versions, logs what isn't cached yet, and seeds it in the background.
func (rt *Runtime) loadReleases(ctx context.Context) error {
	rs, err := rt.cfg.Versions.Releases(ctx)
	if err != nil {
		return fmt.Errorf("docker: versions: %w", err)
	}
	prev := rt.releases.Swap(&rs)
	var missing []Release
	for _, r := range rs {
		if !rt.cfg.Binaries.Has(r) {
			missing = append(missing, r)
		}
	}
	if prev == nil || len(missing) > 0 {
		keys := make([]string, 0, len(rs))
		for _, r := range rs {
			keys = append(keys, r.Key())
		}
		rt.log.Info("docker: serving releases", "releases", keys, "not_cached", len(missing))
	}
	if len(rs) == 0 {
		rt.log.Warn("docker: no releases listed; instances can't start (docker.versions, then docker update)")
	}
	if len(missing) > 0 {
		go rt.seed(missing)
	}
	return nil
}

// seed downloads releases in the background, one at a time (a failure is logged and retried on the next refresh).
func (rt *Runtime) seed(rs []Release) {
	rt.seedMu.Lock()
	defer rt.seedMu.Unlock()
	for _, r := range rs {
		if rt.cfg.Binaries.Has(r) {
			continue
		}
		t0 := time.Now()
		if err := rt.cfg.Binaries.Fetch(rt.bg, r); err != nil {
			rt.log.Warn("docker: seeding a release failed", "release", r.Key(), "error", err)
			continue
		}
		rt.log.Info("docker: release seeded", "release", r.Key(), "ms", time.Since(t0).Milliseconds())
	}
}

// followReleases re-reads the list every 30 s (a `docker update`, or a fleet's change) until shutdown.
func (rt *Runtime) followReleases() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-rt.bg.Done():
			return
		case <-t.C:
			if err := rt.loadReleases(rt.bg); err != nil {
				rt.log.Warn("docker: refreshing versions", "error", err)
			}
		}
	}
}

// Releases returns the releases this host currently serves.
func (rt *Runtime) Releases() []Release {
	if p := rt.releases.Load(); p != nil {
		return *p
	}
	return nil
}

func (rt *Runtime) adopt(ctx context.Context) error {
	list, err := rt.e.list(ctx, map[string][]string{"status": {"running"}})
	if err != nil {
		return fmt.Errorf("docker: list containers: %w", err)
	}
	n := 0
	for _, s := range list {
		id, hash := "", ""
		switch {
		case s.Labels[LabelRuntime] == "docker" && s.Labels[LabelInstance] != "":
			id, hash = s.Labels[LabelInstance], s.Labels[LabelSpec]
		case rt.cfg.AdoptImage != "" && strings.HasPrefix(s.Image, rt.cfg.AdoptImage) && len(s.Names) > 0 && s.Labels[LabelRuntime] == "":
			id = strings.TrimPrefix(s.Names[0], "/")
		default:
			continue
		}
		if !multisite.ValidID(id) {
			continue
		}
		info, err := rt.e.inspect(ctx, s.ID)
		if err != nil || !info.State.Running || info.hostPort() == 0 {
			continue
		}
		c := rt.get(id)
		c.container, c.port, c.hash = s.ID, info.hostPort(), hash
		c.portNow.Store(int64(c.port))
		c.poweredBy.Store(PoweredBy(s.Labels[LabelVersion]))
		c.started.Store(time.Now().UnixNano())
		c.running.Store(true)
		c.lastUsed.Store(time.Now().UnixNano())
		c.health.Store(&healthHint{anyStatus: true})
		go rt.follow(c.inst, s.ID, time.Now())
		rt.watchScripts(id, filepath.Join(rt.cfg.DataRoot, id))
		n++
	}
	if n > 0 {
		rt.log.Info("docker: adopted running containers", "count", n)
	}
	return nil
}

func (rt *Runtime) get(id string) *ctr {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	c, ok := rt.ctrs[id]
	if !ok {
		c = &ctr{inst: id}
		rt.ctrs[id] = c
	}
	return c
}

// ServeInstance implements [multisite.Runtime]: it starts the instance's container if needed and proxies to it.
func (rt *Runtime) ServeInstance(w http.ResponseWriter, r *http.Request, inst multisite.Instance) {
	c := rt.get(inst.ID)
	c.inflight.Add(1)
	c.lastUsed.Store(time.Now().UnixNano())
	defer func() {
		c.lastUsed.Store(time.Now().UnixNano())
		c.inflight.Add(-1)
	}()
	// a request that is safe to repeat, dropped without an answer while the instance restarted (PocketBase's hooks
	// watcher restarting it in place, or the runtime replacing the container), goes again, for up to StartTimeout. A
	// pb_hooks change restarts it twice in a row (PocketBase in place, then the runtime's new container), so one
	// repeat isn't enough: the repeat can land in the second restart.
	var deadline time.Time // StartTimeout after the first drop
	for rt.proxy(w, r, c, inst, &deadline) {
	}
}

// proxy starts the instance if needed and proxies r to it. True: nothing was written and r should go again; it goes
// again until *deadline, which the first drop sets.
func (rt *Runtime) proxy(w http.ResponseWriter, r *http.Request, c *ctr, inst multisite.Instance, deadline *time.Time) (again bool) {
	port, err := rt.ensure(r.Context(), c, inst)
	if err != nil {
		rt.log.Warn("docker: instance failed to start", "instance", inst.ID, "error", err)
		http.Error(w, "instance failed to start: "+err.Error(), http.StatusServiceUnavailable)
		return false
	}
	target := "127.0.0.1:" + strconv.Itoa(port)
	px := proxied{c: c, port: port}
	p := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = "http", target
			pr.Out.Host = pr.In.Host
			pr.SetXForwarded()
		},
		Transport:     rt.tr,
		FlushInterval: -1, // realtime (SSE) streams flush at once
		ModifyResponse: func(res *http.Response) error {
			// the instance's server names itself; a release that doesn't (upstream PocketBase) gets named here
			if res.Header.Get("X-Powered-By") == "" {
				if v, _ := c.poweredBy.Load().(string); v != "" {
					res.Header.Set("X-Powered-By", v)
				}
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if errors.Is(r.Context().Err(), context.Canceled) {
				return
			}
			var op *net.OpError
			dialErr := errors.As(err, &op) && op.Op == "dial"
			dropped := droppedUnanswered(err)
			if (dropped || dialErr) && deadline.IsZero() {
				*deadline = time.Now().Add(rt.cfg.StartTimeout)
			}
			if time.Now().Before(*deadline) && (dropped || dialErr) && safeToRepeat(r) {
				// wait until this container answers again; if it was stopped or replaced meanwhile, ensure (on the
				// next try) finds or starts the current one
				if !rt.waitAnswering(r.Context(), px, *deadline) && dialErr {
					rt.forget(c, port)
				}
				if r.Context().Err() == nil {
					again = true
					return
				}
			}
			if dialErr {
				rt.forget(c, port) // the container went away; the next request starts it again
			}
			rt.log.Debug("docker: proxy error", "instance", inst.ID, "error", err)
			http.Error(w, "instance unavailable", http.StatusBadGateway)
		},
	}
	p.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), proxiedInstance{}, px)))
	return again
}

// droppedUnanswered: the instance (or Docker's port proxy in front of it, once the instance behind it is gone) closed
// or reset the connection before answering, as it does while it restarts. "server closed idle connection" is
// net/http's unexported error for a kept-alive connection the instance closed as the request went out.
func droppedUnanswered(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) || strings.Contains(err.Error(), "server closed idle connection")
}

// safeToRepeat: requests that can be sent again after the instance dropped them without an answer.
func safeToRepeat(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0
	}
	return false
}

// waitAnswering waits until the instance's port answers HTTP again (any status), while the same container runs, until
// deadline. False: the container is gone or was replaced (the caller's next try then finds or starts the current one),
// or it didn't come back.
func (rt *Runtime) waitAnswering(ctx context.Context, p proxied, deadline time.Time) bool {
	url := "http://127.0.0.1:" + strconv.Itoa(p.port) + "/"
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if res, err := rt.http.Do(req); err == nil {
			res.Body.Close()
			return true
		}
		if !rt.stillServing(ctx, p, i%10 == 9) {
			return false
		}
		if time.Now().After(deadline) {
			return false
		}
	}
}

// PoweredBy names a release for X-Powered-By: "pb:0.23.8" → "PocketBase 0.23.8", "p1:0.0.1" → "PocketOne 0.0.1"
// ("" for anything else, e.g. an adopted container without a version label).
func PoweredBy(key string) string {
	line, v, ok := strings.Cut(key, ":")
	if !ok || v == "" {
		return ""
	}
	switch line {
	case LinePB:
		return "PocketBase " + v
	case LineP1:
		return "PocketOne " + v
	}
	return ""
}

// forget drops a container that no longer answers on port.
func (rt *Runtime) forget(c *ctr, port int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.port == port && c.container != "" {
		c.container, c.port, c.hash = "", 0, ""
		c.portNow.Store(0)
		c.running.Store(false)
		rt.exit(runtimev1.Exit{InstanceID: c.inst, Err: errors.New("the instance stopped answering")})
	}
}

// ensure returns the port of the instance's running container, starting (or restarting a changed) one.
func (rt *Runtime) ensure(ctx context.Context, c *ctr, inst multisite.Instance) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.container != "" && !c.dirty {
		return c.port, nil
	}
	cfg, version, err := rt.containerConfig(ctx, inst)
	if err != nil {
		return 0, err
	}
	if c.container != "" { // dirty: restart only if what it would run changed
		if cfg.Labels[LabelSpec] == c.hash {
			c.dirty = false
			return c.port, nil
		}
		rt.log.Info("docker: instance changed, restarting", "instance", inst.ID)
		rt.stopLocked(ctx, c, true)
	}
	return rt.startLocked(ctx, c, inst, cfg, version)
}

// folder is an instance's folder on the host: Instance.Dir, or <DataRoot>/<id>.
func (rt *Runtime) folder(inst multisite.Instance) string {
	if inst.Dir != "" {
		return inst.Dir
	}
	return filepath.Join(rt.cfg.DataRoot, inst.ID)
}

// watchScripts makes sure the instance's pb_hooks exists and watches it; a change restarts the container (PocketBase's
// HooksWatch, done from the host). Migrations aren't watched: they run when the container boots.
func (rt *Runtime) watchScripts(id, dir string) {
	hooks := filepath.Join(dir, "pb_hooks")
	_ = os.MkdirAll(hooks, 0o755)
	rt.watch.Add(id, hooks)
}

// scriptsChanged restarts an instance's container after its pb_hooks changed. Requests for it wait for the new
// container (they queue on the instance's lock). A container adopted from an earlier process is stopped instead;
// its next request starts it with the current configuration.
func (rt *Runtime) scriptsChanged(id string) {
	rt.mu.Lock()
	c, ok := rt.ctrs[id]
	rt.mu.Unlock()
	if !ok {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.container == "" {
		return
	}
	rt.log.Info("docker: pb_hooks changed, restarting", "instance", id)
	ctx := rt.bg
	if c.last == nil {
		rt.stopLocked(ctx, c, false)
		return
	}
	inst := *c.last
	cfg, version, err := rt.containerConfig(ctx, inst)
	if err != nil {
		rt.log.Warn("docker: restart after pb_hooks change", "instance", id, "error", err)
		rt.stopLocked(ctx, c, false)
		return
	}
	rt.stopLocked(ctx, c, true)
	if _, err := rt.startLocked(ctx, c, inst, cfg, version); err != nil {
		rt.log.Warn("docker: restart after pb_hooks change", "instance", id, "error", err)
	}
}

// containerConfig is the container an instance should run, labelled with a hash of itself.
func (rt *Runtime) containerConfig(ctx context.Context, inst multisite.Instance) (containerConfig, string, error) {
	want := inst.Version
	if rt.cfg.Version != nil {
		want = rt.cfg.Version(inst)
	}
	if want == "" {
		want = rt.cfg.DefaultVersion
	}
	if want == "" {
		return containerConfig{}, "", errors.New("no PocketBase version")
	}
	rel, err := PickMin(rt.Releases(), want, rt.cfg.MinUpstream)
	if err != nil {
		return containerConfig{}, "", err
	}
	if !rt.cfg.Binaries.Has(rel) {
		go rt.seed([]Release{rel})
		return containerConfig{}, "", fmt.Errorf("%s is still downloading on this host; retry shortly", rel.Key())
	}
	bin := rt.cfg.Binaries.Path(rel.Line, rel.Version)
	version := rel.Key() // e.g. "pb:0.36.9", "p1:0.0.1"
	dir := rt.folder(inst)
	// create the instance folder here: otherwise the daemon creates a missing bind source itself (root-owned on
	// Linux; inside the VM with Docker Desktop, where the host never sees it)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return containerConfig{}, "", fmt.Errorf("instance folder: %w", err)
	}
	spec := Spec{Env: map[string]string{}, Labels: map[string]string{}}
	for k, v := range inst.Secrets {
		spec.Env[k] = v
	}
	if inst.EncryptionKey != "" {
		spec.Env["PB_ENCRYPTION_KEY"] = inst.EncryptionKey
	}
	if rt.cfg.Prepare != nil {
		if err := rt.cfg.Prepare(ctx, inst, version, &spec); err != nil {
			return containerConfig{}, "", fmt.Errorf("prepare: %w", err)
		}
	}
	// PocketBase's hooks watcher stays on (its default). Besides restarting on changes (the runtime's host watcher
	// restarts the container too, which is harmless), it decides what a pb_hooks file that fails to load does: with
	// the watcher on, PocketBase logs it and runs without it; with it off (--hooksWatch=false), the instance won't boot.
	cmd := []string{"./pocketbase", "serve", "--http", "0.0.0.0:8090"}
	if inst.Dev {
		cmd = append(cmd, "--dev")
	}
	if spec.Env["PB_ENCRYPTION_KEY"] != "" {
		cmd = append(cmd, "--encryptionEnv=PB_ENCRYPTION_KEY")
	}
	if spec.Cmd != nil {
		cmd = spec.Cmd(cmd)
	}
	env := make([]string, 0, len(spec.Env))
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)
	binds := append([]string{dir + ":" + rt.cfg.Home, bin + ":" + rt.cfg.Home + "/pocketbase:ro"}, spec.Binds...)
	hc := hostConfig{
		Binds:        binds,
		PortBindings: map[string][]portBinding{"8090/tcp": {{HostIP: rt.cfg.HostIP, HostPort: ""}}},
		Init:         true,
		AutoRemove:   true,
		NanoCPUs:     rt.cfg.NanoCPUs,
		PidsLimit:    rt.cfg.PidsLimit,
		CapDrop:      rt.cfg.CapDrop,
		NetworkMode:  rt.cfg.Network,
	}
	if rt.cfg.NofileSoft > 0 || rt.cfg.NofileHard > 0 {
		hc.Ulimits = []ulimit{{Name: "nofile", Soft: rt.cfg.NofileSoft, Hard: max(rt.cfg.NofileHard, rt.cfg.NofileSoft)}}
	}
	if rt.cfg.NoNewPrivileges {
		hc.SecurityOpt = []string{"no-new-privileges"}
	}
	cfg := containerConfig{
		Image:        rt.cfg.Image,
		Cmd:          cmd,
		Env:          env,
		WorkingDir:   rt.cfg.Home,
		ExposedPorts: map[string]struct{}{"8090/tcp": {}},
		HostConfig:   hc,
		healthPath:   spec.HealthPath,
		healthAny:    spec.HealthAnyStatus,
	}
	labels := map[string]string{}
	for k, v := range spec.Labels {
		labels[k] = v
	}
	labels[LabelRuntime], labels[LabelInstance], labels[LabelVersion] = "docker", inst.ID, version
	cfg.Labels = labels
	b, _ := json.Marshal(cfg)
	sum := sha256.Sum256(b)
	cfg.Labels[LabelSpec] = hex.EncodeToString(sum[:8])
	return cfg, version, nil
}

func (rt *Runtime) startLocked(ctx context.Context, c *ctr, inst multisite.Instance, cfg containerConfig, version string) (port int, err error) {
	defer func() {
		if err != nil && ctx.Err() == nil { // a start that failed (not one whose request went away): the host hears it
			rt.exit(runtimev1.Exit{InstanceID: inst.ID, Err: err})
		}
	}()
	select {
	case rt.sem <- struct{}{}:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	defer func() { <-rt.sem }()

	t0 := time.Now()
	name := inst.ID
	_ = rt.e.remove(ctx, name) // a stale container of the same name
	id, err := rt.e.create(ctx, name, cfg)
	if isConflict(err) {
		time.Sleep(500 * time.Millisecond) // its removal is still in progress
		id, err = rt.e.create(ctx, name, cfg)
	}
	if err != nil {
		return 0, fmt.Errorf("create: %w", err)
	}
	// Attach to its output before it starts: a container that exits at once is auto-removed, and following its logs
	// after start would lose what it printed (the reason it exited).
	var piped chan struct{} // closed once all of its output is read
	out, aerr := rt.e.attach(rt.bg, id)
	if aerr == nil {
		piped = make(chan struct{})
		go func() {
			defer close(piped)
			defer out.Close()
			readMux(out, rt.output(inst.ID))
			rt.ended(inst.ID, id) // a no-op when the runtime stopped it, or it never came up
		}()
	} else {
		rt.log.Warn("docker: attach before start; following logs after start instead", "instance", inst.ID, "error", aerr)
	}
	failed := func() {
		_ = rt.e.remove(context.Background(), id)
		if piped != nil { // let its last lines reach the log before the failure is reported
			select {
			case <-piped:
			case <-time.After(2 * time.Second):
			}
		}
	}
	since := time.Now()
	if err := rt.e.start(ctx, id); err != nil {
		failed()
		return 0, fmt.Errorf("start: %w", err)
	}
	if piped == nil {
		go rt.follow(inst.ID, id, since.Add(-time.Second))
	}
	info, err := rt.e.inspect(ctx, id)
	if err != nil || info.hostPort() == 0 {
		failed()
		return 0, fmt.Errorf("no published port (running %v, ports %v, err %v)", info.State.Running, info.NetworkSettings.Ports, err)
	}
	port = info.hostPort()
	if err := rt.waitHealthy(ctx, id, port, cfg.healthPath, cfg.healthAny); err != nil {
		failed()
		return 0, err
	}
	c.container, c.port, c.hash, c.dirty = id, port, cfg.Labels[LabelSpec], false
	c.portNow.Store(int64(port))
	c.poweredBy.Store(PoweredBy(version))
	c.started.Store(time.Now().UnixNano())
	c.health.Store(&healthHint{path: cfg.healthPath, anyStatus: cfg.healthAny})
	c.running.Store(true)
	started := inst
	c.last = &started
	rt.watchScripts(inst.ID, rt.folder(inst))
	rt.log.Info("docker: instance started", "instance", inst.ID, "version", version, "ms", time.Since(t0).Milliseconds())
	return port, nil
}

// waitHealthy polls path (default /api/health) until it answers 200 (with anyStatus: anything), the container exits or
// StartTimeout passes.
func (rt *Runtime) waitHealthy(ctx context.Context, id string, port int, path string, anyStatus bool) error {
	deadline := time.Now().Add(rt.cfg.StartTimeout)
	if path == "" {
		path = "/api/health"
	}
	url := "http://127.0.0.1:" + strconv.Itoa(port) + path
	for i := 0; ; i++ {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if res, err := rt.http.Do(req); err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK || anyStatus {
				return nil
			}
		}
		if i%10 == 9 { // every second: still running?
			if info, err := rt.e.inspect(ctx, id); err != nil || !info.State.Running {
				return errors.New("container exited during startup (see the instance log)")
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not healthy within %s", rt.cfg.StartTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// follow sends an already running container's output to OnLog (and remote/v1's logs) until it stops. Containers this
// runtime starts are attached before they start instead (startLocked), so a fast crash can't lose its output.
func (rt *Runtime) follow(instID, containerID string, since time.Time) {
	_ = rt.e.logs(rt.bg, containerID, rt.output(instID))
	rt.ended(instID, containerID) // its logs end when it stops
}

// output is the line sink for an instance's container output: its log ring, then OnLog (else the debug log).
func (rt *Runtime) output(instID string) func(stream int, text string) {
	ring := rt.ring(instID)
	return func(stream int, text string) {
		name := "stdout"
		if stream == 2 {
			name = "stderr"
		}
		ring.add(remotev1.LogEntry{Time: time.Now().UTC().Format(time.RFC3339Nano), Stream: name, Message: text})
		if rt.cfg.OnLog != nil {
			rt.cfg.OnLog(instID, stream == 2, text)
			return
		}
		rt.log.Debug("docker: instance output", "instance", instID, "stderr", stream == 2, "line", text)
	}
}

// stopLocked stops the instance's container; restarting: the caller starts it again right away. The host hears an exit.
func (rt *Runtime) stopLocked(ctx context.Context, c *ctr, restarting bool) {
	if c.container == "" {
		return
	}
	defer rt.exit(runtimev1.Exit{InstanceID: c.inst, Restarting: restarting})
	if err := rt.e.stop(ctx, c.container, rt.cfg.StopGrace); err != nil {
		rt.log.Warn("docker: stop", "instance", c.inst, "error", err)
	}
	_ = rt.e.remove(ctx, c.container)
	c.container, c.port, c.hash, c.dirty = "", 0, "", false
	c.portNow.Store(0)
	c.running.Store(false)
	rt.watch.Remove(c.inst)
}

// Stop stops an instance's container (its folder stays), e.g. before maintenance on its files.
func (rt *Runtime) Stop(ctx context.Context, id string) {
	c := rt.get(id)
	c.mu.Lock()
	defer c.mu.Unlock()
	rt.stopLocked(ctx, c, false)
}

// Running reports whether an instance's container is running.
func (rt *Runtime) Running(id string) bool {
	rt.mu.Lock()
	c, ok := rt.ctrs[id]
	rt.mu.Unlock()
	return ok && c.running.Load()
}

// Sync implements [multisite.Syncer]: deleted instances (or ones moved to another edge) stop; changed ones restart on
// their next request if what they run changed.
func (rt *Runtime) Sync(ch multisite.Change, prev *multisite.Instance) {
	rt.mu.Lock()
	c, ok := rt.ctrs[ch.Instance.ID]
	rt.mu.Unlock()
	if !ok {
		return
	}
	if ch.Kind == multisite.Delete || ch.Instance.Remote != "" {
		go rt.Stop(context.Background(), ch.Instance.ID)
		return
	}
	if prev == nil || !reflect.DeepEqual(*prev, ch.Instance) {
		c.mu.Lock()
		c.dirty = true
		if c.hash == "" { // adopted from another manager: we can't tell what changed, so restart it
			c.hash = "unknown"
		}
		c.mu.Unlock()
	}
}

// reap stops containers idle for longer than IdleTTL.
func (rt *Runtime) reap() {
	every := rt.cfg.IdleTTL / 4
	every = min(max(every, time.Second), time.Minute)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-rt.bg.Done():
			return
		case <-t.C:
		}
		rt.mu.Lock()
		list := make([]*ctr, 0, len(rt.ctrs))
		for _, c := range rt.ctrs {
			list = append(list, c)
		}
		rt.mu.Unlock()
		for _, c := range list {
			if !c.running.Load() || c.inflight.Load() > 0 || time.Since(time.Unix(0, c.lastUsed.Load())) < rt.cfg.IdleTTL {
				continue
			}
			c.mu.Lock()
			if c.running.Load() && c.inflight.Load() == 0 && time.Since(time.Unix(0, c.lastUsed.Load())) >= rt.cfg.IdleTTL {
				rt.log.Info("docker: instance idle, stopping", "instance", c.inst)
				rt.stopLocked(rt.bg, c, false)
			}
			c.mu.Unlock()
		}
	}
}

// RunningCount implements [multisite.Statser].
func (rt *Runtime) RunningCount() int {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	n := 0
	for _, c := range rt.ctrs {
		if c.running.Load() {
			n++
		}
	}
	return n
}

// Shutdown implements [multisite.Shutdowner]: background work stops; containers keep running for the next edge process
// to adopt.
func (rt *Runtime) Shutdown() {
	rt.cancel()
	rt.watch.Close()
}
