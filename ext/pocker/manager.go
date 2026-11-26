package pocker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	lookupv1 "pocketone.dev/ext/contracts/instance/lookup/v1"
	sourcev1 "pocketone.dev/ext/contracts/instance/source/v1"
	runtimev1 "pocketone.dev/ext/contracts/runtime/v1"
	"pocketone.dev/ext/multisite"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/hook"
	"pocketone.dev/ext/extkit"
	"pocketone.dev/ext/extkit/js"
)

// HealthPath answers on any host with the engine's status (see [multisite.DefaultHealthPath]).
const HealthPath = multisite.DefaultHealthPath

// ValidID reports whether id is a safe instance id (see [multisite.ValidID]).
func ValidID(id string) bool { return multisite.ValidID(id) }

// DevID is the id dev mode gives an unmapped hostname (see [multisite.DevID]).
func DevID(h string) string { return multisite.DevID(h) }

// Manager runs instances as isolates in this process: an [multisite.Runtime] that starts each instance's app on its first request and
// keeps it running. Its instances come from instance.source/v1 on the host's scope, through an [multisite.Directory]:
// the host's (shared in [ManagerOptions.Directory], which the host feeds), or its own, fed by Run. As an http.Handler
// it serves the whole edge pipeline with itself as the only runtime.
type Manager struct {
	scope *extkit.Scope // the host's contracts: instance.source/v1
	opts  appOptions
	log   *slog.Logger

	dir     *multisite.Directory
	ownsDir bool

	lookupOnce sync.Once // picks up instance.lookup/v1 for ServeHTTP
	handler    *multisite.Handler

	defaults Limits
	onEvent  func(Event)
	hooks    *multisite.ScriptWatcher // restarts an app when its pb_hooks change
	gate     Gate

	held map[string]chan struct{} // by ID: instances under maintenance; requests wait for the channel to close

	mu         sync.RWMutex
	apps       map[string]*appSlot // by ID
	restarting map[string]bool     // by ID: the next stop is a restart (recycle), under mu

	onExit atomic.Pointer[func(runtimev1.Exit)] // runtime/v1's exit signal, set by the host
}

var (
	_ multisite.Runtime    = (*Manager)(nil)
	_ multisite.Prechecker = (*Manager)(nil)
	_ multisite.Syncer     = (*Manager)(nil)
	_ multisite.Shutdowner = (*Manager)(nil)
	_ multisite.Statser    = (*Manager)(nil)

	_ runtimev1.ExitNotifier  = (*Manager)(nil)
	_ runtimev1.RunningLister = (*Manager)(nil)
)

// OnExit implements runtime/v1's ExitNotifier: fn hears every app that stops or fails to start.
func (m *Manager) OnExit(fn func(runtimev1.Exit)) { m.onExit.Store(&fn) }

func (m *Manager) exit(x runtimev1.Exit) {
	if fn := m.onExit.Load(); fn != nil {
		(*fn)(x)
	}
}

// stopped is an app slot's stop: an exit, a restart when recycle asked for one.
func (m *Manager) stopped(id string) {
	m.mu.Lock()
	restarting := m.restarting[id]
	delete(m.restarting, id)
	m.mu.Unlock()
	m.exit(runtimev1.Exit{InstanceID: id, Restarting: restarting})
}

// RunningIDs implements runtime/v1's RunningLister: instances whose app is running.
func (m *Manager) RunningIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.apps))
	for id, s := range m.apps {
		select {
		case <-s.done:
			if s.app != nil {
				ids = append(ids, id)
			}
		default:
		}
	}
	return ids
}

// appSlot holds one app: a start in progress (done not yet closed) or a finished start (app or err set).
type appSlot struct {
	done chan struct{}
	app  *runningApp
	err  error

	mu       sync.Mutex
	stopping bool           // set by stop: new requests get 503 instead of reaching an app that's going away
	inflight sync.WaitGroup // requests the app is serving
	stopOnce sync.Once

	onStopped func() // called once its running app has stopped (the runtime's exit signal)
}

// enter counts a request in, unless the app is stopping.
func (s *appSlot) enter() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopping {
		return false
	}
	s.inflight.Add(1)
	return true
}

// stop stops the slot's app once: new requests are turned away, the ones it is serving get up to 10s to finish, then
// the app stops. Concurrent callers wait for the one stop.
func (s *appSlot) stop() {
	s.stopOnce.Do(func() {
		<-s.done
		s.mu.Lock()
		s.stopping = true
		s.mu.Unlock()
		drained := make(chan struct{})
		go func() { s.inflight.Wait(); close(drained) }()
		select {
		case <-drained:
		case <-time.After(10 * time.Second):
		}
		if s.app != nil {
			stopApp(s.app)
			if s.onStopped != nil {
				s.onStopped()
			}
		}
	})
}

// ManagerOptions configures a [Manager].
type ManagerOptions struct {
	// PBRoot is the folder that holds one PocketBase folder per instance (<PBRoot>/<instance id>).
	PBRoot string
	// Dev serves any hostname: a host the source doesn't map gets [DevID] as its id and its folder is created on
	// first use. Apps run in PocketBase dev mode.
	Dev bool
	// HooksPoolSize is kept for compatibility and ignored: each app has one JS VM (ext/jsvm).
	HooksPoolSize int
	// Defaults are the per-instance limits used where an instance doesn't set its own (zero = DefaultLimits).
	Defaults Limits
	// OnEvent receives instance events (started, stopped, restarts), e.g. to log them or notify the owner.
	OnEvent func(Event)
	// Gate, if set, admits requests: a peer check before routing, and per-instance admission (e.g. rate limits)
	// before an instance is served here or forwarded (see [Gate]).
	Gate Gate
	// Init runs host Go code for each app before it bootstraps (extensions implementing [AppIniter]).
	Init func(inst Instance, app core.App) error
	// InitJS adds globals to each JS runtime after the sandbox set it up (extensions implementing [JSIniter]).
	InitJS func(inst Instance, app core.App, rt js.Runtime)
	// Setup runs host Go code for each app after migrations and before it serves (see [Server.SetupApp]).
	Setup func(inst Instance, app core.App) error
	// Env adds host values to each app's environment, over its secrets (see [Server.Env]).
	Env func(inst Instance) map[string]string
	// Forward serves requests for instances with [Instance.Remote] set, after the firewall checks. Nil = a plain
	// reverse proxy to Remote.
	Forward func(w http.ResponseWriter, r *http.Request, inst Instance)
	// InstanceRoutes answer requests for instances run here, after the gates and before the app (see
	// multisite.Handler's InstanceRoutes): host-provided contracts such as remote/v1 with [Remote].
	InstanceRoutes []func(http.Handler) http.Handler
	// Directory, when set, is shared with the rest of the edge (the host feeds it); the Manager then doesn't load
	// the source itself and [Manager.Run] only waits.
	Directory *multisite.Directory
	// Logger defaults to slog.Default().
	Logger *slog.Logger
	// Standard is registered on every app (see [Server.Standard]). Required: without it no instance starts.
	Standard extkit.AppSet
	// Host is the host's contract scope (see [Server.Host]).
	Host *extkit.Scope
	// IsolateConfig is every instance's base config, one section per extension (see [Config.IsolateConfig]).
	IsolateConfig map[string]map[string]any
}

// NewManager creates a Manager that takes its instances from instance.source/v1 on scope, the host's contract scope
// (sealed before [Manager.Run]; sourcetest.Scope gives tests one). On a host, pass the host's scope and its
// [ManagerOptions.Directory]. Call [Manager.Run] to load instances and follow changes.
func NewManager(scope *extkit.Scope, opts ManagerOptions) *Manager {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	defaults := opts.Defaults.Over(DefaultLimits())
	m := &Manager{
		scope:    scope,
		opts:     appOptions{PBRoot: opts.PBRoot, HooksPoolSize: opts.HooksPoolSize, ForceDev: opts.Dev, Defaults: defaults, Init: opts.Init, InitJS: opts.InitJS, Setup: opts.Setup, Env: opts.Env, Standard: opts.Standard, Host: opts.Host, IsolateConfig: opts.IsolateConfig},
		defaults: defaults,
		onEvent:  opts.OnEvent,
		gate:     opts.Gate,
		held:     map[string]chan struct{}{},
		log:      opts.Logger,
		apps:     map[string]*appSlot{},
		dir:      opts.Directory,
	}
	if m.dir == nil {
		m.dir = multisite.NewDirectory(opts.Logger)
		m.ownsDir = true
		m.dir.Subscribe(m.Sync) // with a shared directory, the host subscribes its runtime
	}
	m.hooks = multisite.NewScriptWatcher(opts.Logger, m.hooksChanged)
	m.handler = &multisite.Handler{Dir: m.dir, Gate: opts.Gate, Runtime: m, Forward: opts.Forward,
		InstanceRoutes: opts.InstanceRoutes, Dev: opts.Dev, Logger: opts.Logger}
	return m
}

// errNoSource: the Manager's scope has no instance.source/v1 provider.
var errNoSource = errors.New("pocker: no instance.source/v1 provider on the host scope (the instances file, a control plane's mirror, or sourcetest in tests)")

// Directory is the instance directory the Manager reads.
func (m *Manager) Directory() *multisite.Directory { return m.dir }

// Run loads the snapshot, marks the manager ready and applies changes until ctx is done. With a shared directory
// ([ManagerOptions.Directory]) the host feeds it and Run only waits for ctx.
func (m *Manager) Run(ctx context.Context) error {
	if !m.ownsDir {
		<-ctx.Done()
		return nil
	}
	if m.scope == nil {
		return errNoSource
	}
	src, ok := sourcev1.Contract.In(m.scope)
	if !ok {
		return errNoSource
	}
	return m.dir.Run(ctx, src, nil)
}

// Sync implements [multisite.Syncer]: a changed instance (env, limits, dev mode, folder) is recycled if it's running
// (see recycle); a blocked or removed one is stopped.
func (m *Manager) Sync(c Change, prev *Instance) {
	switch c.Kind {
	case Upsert:
		// A blocked instance (powered off, suspended, unpaid) must not keep running: its cron jobs and other
		// background work stop with the app, and unblocking starts a fresh app (re-reading pb_hooks) on the next
		// request or StartInstance.
		blocked := c.Instance.Blocked != nil
		switch {
		case prev == nil:
		case blocked:
			if m.stopInstance(c.Instance.ID) {
				m.emit(Event{Kind: EventStopped, InstanceID: c.Instance.ID, Reason: c.Instance.Blocked.Message})
			}
		case !sameRuntime(*prev, c.Instance):
			go m.recycle(c.Instance.ID, "settings changed")
		}
		m.log.Info("pocker: instance updated", "id", c.Instance.ID)
	case Delete:
		m.mu.Lock()
		slot := m.apps[c.Instance.ID]
		delete(m.apps, c.Instance.ID)
		m.mu.Unlock()
		m.log.Info("pocker: instance removed", "id", c.Instance.ID)
		if slot != nil {
			go func() {
				slot.stop()
			}()
		}
	}
}

// Lookup returns the instance that serves host.
func (m *Manager) Lookup(host string) (Instance, bool) { return m.dir.Lookup(host) }

// Name implements [multisite.Runtime].
func (m *Manager) Name() string { return "pocker" }

// Serves implements [multisite.Runtime]: every instance (they all run this process's PocketBase).
func (m *Manager) Serves(Instance) bool { return true }

// app returns the instance's running app, starting it (once, shared by concurrent callers) if needed.
// A failed start is not cached: the next request tries again.
func (m *Manager) app(inst Instance) (*runningApp, error) {
	slot := m.slot(inst)
	return slot.app, slot.err
}

// slot returns the instance's app slot once its start has finished, starting the app (once, shared by concurrent
// callers) if needed.
func (m *Manager) slot(inst Instance) *appSlot {
	m.mu.Lock()
	slot, ok := m.apps[inst.ID]
	if !ok {
		id := inst.ID
		slot = &appSlot{done: make(chan struct{}), onStopped: func() { m.stopped(id) }}
		m.apps[inst.ID] = slot
		m.mu.Unlock()

		t := time.Now()
		slot.app, slot.err = startApp(m.opts, inst)
		close(slot.done)
		if slot.err != nil {
			m.mu.Lock()
			if m.apps[inst.ID] == slot {
				delete(m.apps, inst.ID)
			}
			m.mu.Unlock()
			m.log.Error("pocker: instance failed to start", "id", inst.ID, "error", slot.err)
			openInstanceLog(instanceDir(m.opts, inst)).Write("stderr", "Instance failed to start: "+slot.err.Error())
			m.emit(Event{Kind: EventFailed, InstanceID: inst.ID, Reason: slot.err.Error()})
			m.exit(runtimev1.Exit{InstanceID: inst.ID, Err: slot.err})
		} else {
			m.catchRestart(inst.ID, slot)
			m.watchHooks(inst, slot)
			m.log.Info("pocker: instance started", "id", inst.ID, "ms", time.Since(t).Milliseconds())
			m.emit(Event{Kind: EventStarted, InstanceID: inst.ID})
		}
		return slot
	}
	m.mu.Unlock()
	<-slot.done
	return slot
}

// ServeHTTP serves the edge pipeline (resolve by Host, gates, forwarding) with the Manager as the only runtime.
func (m *Manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.lookupOnce.Do(func() {
		// a source that doesn't hold every instance answers hostnames itself (instance.lookup/v1); the scope is
		// sealed by the first request
		if m.scope != nil {
			if l, ok := lookupv1.Contract.In(m.scope); ok {
				m.handler.Lookup = l
			}
		}
	})
	m.handler.ServeHTTP(w, r)
}

// Precheck implements [multisite.Prechecker]: instances under maintenance answer 503 before the gates.
func (m *Manager) Precheck(w http.ResponseWriter, r *http.Request, inst Instance) bool {
	if err := m.waitHeld(r.Context(), inst.ID); err != nil {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "this instance is being updated; retry shortly", http.StatusServiceUnavailable)
		return false
	}
	return true
}

// ServeInstance implements [multisite.Runtime]: start the app on demand and serve the request.
func (m *Manager) ServeInstance(w http.ResponseWriter, r *http.Request, inst Instance) {
	wakeStart := time.Now()
	slot := m.slot(inst)
	if slot.err != nil {
		http.Error(w, "instance failed to start: "+slot.err.Error(), http.StatusBadGateway)
		return
	}
	if !slot.enter() {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "this instance is restarting; retry shortly", http.StatusServiceUnavailable)
		return
	}
	defer slot.inflight.Done()
	app := slot.app
	readyAt := time.Now()
	Timing(r.Context(), func() []TimingEntry {
		return []TimingEntry{{Name: "origin", Dur: time.Since(readyAt)}, {Name: "wake", Dur: readyAt.Sub(wakeStart)}}
	})
	app.handler.ServeHTTP(w, r)
}

// Gate returns the manager's gate (nil if none), so host code in front of the manager (e.g. its own routes) can
// apply the same peer check.
func (m *Manager) Gate() Gate { return m.gate }

// LogInstance appends a line to the instance's log (<instance folder>/logs/exec.log, the same file its app writes),
// e.g. for a scheduled job's result. Unknown instances are ignored.
func (m *Manager) LogInstance(id, stream, message string) {
	dir, ok := m.InstanceDir(id)
	if !ok {
		return
	}
	openInstanceLog(dir).Write(stream, message)
}

// RunningCount implements [multisite.Statser]: apps currently running.
func (m *Manager) RunningCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	running := 0
	for _, s := range m.apps {
		select {
		case <-s.done:
			if s.app != nil {
				running++
			}
		default:
		}
	}
	return running
}

// watchHooks restarts the app when its pb_hooks change (PocketBase's HooksWatch, but only this app and with one
// shared watcher). Migrations are not watched: like PocketBase, they run when the app boots. The watch ends with this
// app, unless a newer app for the instance already took over.
func (m *Manager) watchHooks(inst Instance, slot *appSlot) {
	if m.hooks == nil {
		return
	}
	m.hooks.Add(inst.ID, filepath.Join(instanceDir(m.opts, inst), "pb_hooks"))
	slot.app.app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
		m.mu.RLock()
		cur := m.apps[inst.ID]
		m.mu.RUnlock()
		if cur == nil || cur == slot {
			m.hooks.Remove(inst.ID)
		}
		return e.Next()
	})
}

// hooksChanged recycles the instance after its pb_hooks change, as PocketBase's watcher restarts its process. A hook
// file that fails to load is skipped in dev mode; otherwise the instance doesn't start: the error goes to its log,
// requests get 502, and the next request after a fix starts it.
func (m *Manager) hooksChanged(id string) { go m.recycle(id, "pb_hooks changed") }

// recycle restarts the instance if its app is running, as PocketBase restarts its process: the app stops, then a
// fresh one starts with the current files and settings. An instance that isn't running is left alone. Requests that
// reach it while it restarts fail; two apps never have its folder open.
func (m *Manager) recycle(id, reason string) {
	m.mu.Lock()
	if m.restarting == nil {
		m.restarting = map[string]bool{}
	}
	m.restarting[id] = true
	m.mu.Unlock()
	if !m.stopInstance(id) {
		m.mu.Lock()
		delete(m.restarting, id)
		m.mu.Unlock()
		return
	}
	m.log.Info("pocker: "+reason+", restarting", "id", id)
	if dir, ok := m.InstanceDir(id); ok {
		openInstanceLog(dir).Write("stdout", reason+", restarting")
	}
	m.emit(Event{Kind: EventRestarted, InstanceID: id, Reason: reason})
	_ = m.StartInstance(context.Background(), id) // a start error is logged and reported by app()
}

// stopInstance stops the instance's running app and drops it; it reports whether an app was running.
func (m *Manager) stopInstance(id string) bool {
	m.mu.RLock()
	slot := m.apps[id]
	m.mu.RUnlock()
	if slot == nil {
		return false
	}
	slot.stop()
	m.mu.Lock()
	if m.apps[id] == slot {
		delete(m.apps, id)
	}
	m.mu.Unlock()
	return slot.app != nil
}

// Shutdown stops every running app.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	slots := m.apps
	m.apps = map[string]*appSlot{}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range slots {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.stop()
		}()
	}
	wg.Wait()
	m.hooks.Close()
}

// Event is something that happened to an instance, for logs or owner notifications.
type Event struct {
	Kind       EventKind
	InstanceID string
	Reason     string
}

// EventKind names an [Event].
type EventKind string

const (
	EventRestarted EventKind = "restarted" // settings changed: the app restarts on its next request
	EventStarted   EventKind = "started"   // the app started and serves requests
	EventFailed    EventKind = "failed"    // the app failed to start (Reason has the error)
	EventStopped   EventKind = "stopped"   // the instance became blocked (e.g. powered off): its app was stopped
)

func (m *Manager) emit(e Event) {
	if m.onEvent != nil {
		m.onEvent(e)
	}
}

// sameRuntime reports whether two versions of an instance can share a running app. Extension settings count: extensions
// read them once, when they're registered on the app.
func sameRuntime(a, b Instance) bool {
	return a.Remote == b.Remote && a.Dir == b.Dir && a.Dev == b.Dev && reflect.DeepEqual(a.Secrets, b.Secrets) && a.EncryptionKey == b.EncryptionKey &&
		reflect.DeepEqual(a.Access, b.Access) && reflect.DeepEqual(a.Limits, b.Limits) &&
		reflect.DeepEqual(a.Superusers, b.Superusers) && reflect.DeepEqual(a.Private, b.Private) &&
		reflect.DeepEqual(a.Extensions, b.Extensions)
}

// InstanceDir returns the folder of a known instance (<pbRoot>/<id> unless the instance sets Dir).
func (m *Manager) InstanceDir(id string) (string, bool) {
	m.mu.RLock()
	inst, ok := m.dir.Get(id)
	m.mu.RUnlock()
	if !ok {
		return "", false
	}
	return instanceDir(m.opts, inst), true
}

// WithInstanceStopped stops the instance's app (if running), holds its new requests, runs fn with the instance
// folder, then releases the held requests; the next request starts a fresh app. Use it to change an instance's
// files (deploys, restores) or to work on its databases while nothing has them open. Requests in flight when it
// starts are cut off, like stopping a process.
func (m *Manager) WithInstanceStopped(ctx context.Context, id string, fn func(dir string) error) error {
	m.mu.Lock()
	inst, ok := m.dir.Get(id)
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("instance %q not found", id)
	}
	if _, busy := m.held[id]; busy {
		m.mu.Unlock()
		return fmt.Errorf("instance %q is already being updated", id)
	}
	release := make(chan struct{})
	m.held[id] = release
	slot := m.apps[id]
	delete(m.apps, id)
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.held, id)
		m.mu.Unlock()
		close(release)
	}()
	if slot != nil {
		select {
		case <-slot.done:
		case <-ctx.Done():
			return ctx.Err()
		}
		slot.stop()
	}
	return fn(instanceDir(m.opts, inst))
}

// Ready reports whether the instance list has loaded (before that, requests get 503 "starting").
func (m *Manager) Ready() bool { return m.dir.Ready() }

// Running reports whether the instance's app is started (or starting).
func (m *Manager) Running(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.apps[id]
	return ok
}

// StartInstance starts the instance's app now, if it isn't running, and returns the start error (e.g. a broken
// hook or migration). Host code uses it after changing an instance's files to report the result right away instead
// of on the next request. A blocked instance isn't started.
func (m *Manager) StartInstance(ctx context.Context, id string) error {
	if err := m.waitHeld(ctx, id); err != nil {
		return err
	}
	m.mu.RLock()
	inst, ok := m.dir.Get(id)
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("instance %q not found", id)
	}
	if inst.Blocked != nil {
		return fmt.Errorf("instance %q is blocked: %s", id, inst.Blocked.Message)
	}
	_, err := m.app(inst)
	return err
}

// waitHeld blocks while the instance is under maintenance (WithInstanceStopped), up to a minute.
func (m *Manager) waitHeld(ctx context.Context, id string) error {
	m.mu.RLock()
	ch := m.held[id]
	m.mu.RUnlock()
	if ch == nil {
		return nil
	}
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	select {
	case <-ch:
		return nil
	case <-t.C:
		return fmt.Errorf("instance %q still being updated", id)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// catchRestart makes app.Restart() (PocketBase's backup restore, extensions) restart this one app instead of
// re-executing the whole process, which would restart every instance. Two handlers: the innermost one replaces the
// re-exec (close the databases, drop the slot); the outermost one starts a fresh app once every other OnTerminate
// handler has finished (e.g. an extension that detaches from the database after PocketBase closes it), so a start error
// is reported right away and the restored data is served next.
func (m *Manager) catchRestart(id string, slot *appSlot) {
	var caught bool
	slot.app.app.OnTerminate().Bind(&hook.Handler[*core.TerminateEvent]{
		Priority: math.MinInt,
		Func: func(e *core.TerminateEvent) error {
			err := e.Next()
			if caught {
				caught = false
				go func() {
					if err := m.StartInstance(context.Background(), id); err != nil {
						m.log.Error("pocker: instance failed to restart", "id", id, "error", err)
					}
				}()
			}
			return err
		},
	})
	slot.app.app.OnTerminate().Bind(&hook.Handler[*core.TerminateEvent]{
		Priority: math.MaxInt,
		Func: func(e *core.TerminateEvent) error {
			if !e.IsRestart {
				return e.Next()
			}
			caught = true
			m.mu.Lock()
			if m.apps[id] == slot {
				delete(m.apps, id)
			}
			m.mu.Unlock()
			m.log.Info("pocker: instance restarting (app.Restart)", "id", id)
			m.emit(Event{Kind: EventRestarted, InstanceID: id, Reason: "app restart"})
			m.exit(runtimev1.Exit{InstanceID: id, Restarting: true})
			return e.App.ResetBootstrapState()
		},
	})
}
