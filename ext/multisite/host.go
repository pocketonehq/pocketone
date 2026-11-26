package multisite

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	lookupv1 "pocketone.dev/ext/contracts/instance/lookup/v1"
	runtimev1 "pocketone.dev/ext/contracts/runtime/v1"
	"pocketone.dev/ext/extkit"
	"time"
)

// Host serves many instances: it follows the instance source into a directory, runs the request front (health, peer
// check, resolve, blocked, then the forward hook or the instance routes and a runtime, with the gates) behind the
// wrappers, and listens.
// Build it from config ([FromConfig]), or fill it in (Use and Setup, or the fields directly), then call Run.
type Host struct {
	Source  InstanceSource // instance.source/v1 (required)
	Runtime Runtime        // runtime/v1: runs every instance (required)

	// Scope is the host's contract scope: host extensions provide runtime/v1, gate/v1, tls/v1, http.wrap/v1 and
	// instance.source/v1 on it ([Host.Use]); [Host.Setup] seals it. Its instances can be given contracts too
	// (extkit's ProvideForInstances).
	Scope *extkit.Scope

	// Directory is shared with the runtime when the runtime needs it at construction (e.g. pocker's Manager); nil =
	// a new one.
	Directory *Directory
	// DataRoot is where the first runtime keeps its instances' folders (set while it registers), for host extensions
	// that keep files next to them (autotls's certificates); read it once the host runs.
	DataRoot string
	// First, if set, is a snapshot already taken from Source (so it isn't loaded twice).
	First []Instance

	Gates    []Gate                            // in order; a ConfigurableGate is configured by its owner
	Wrappers []func(http.Handler) http.Handler // the first is the outermost
	// InstanceRoutes run after the gates, for instances this host runs (see [Handler.InstanceRoutes]): where
	// host-provided contracts answer. Add them before Run.
	InstanceRoutes []func(http.Handler) http.Handler
	TLS            TLSProvider                                                 // nil = Listeners.TLS cert files, or plain HTTP
	Forward        func(w http.ResponseWriter, r *http.Request, inst Instance) // instances another server runs (ext/edge); nil = plain proxy to Remote
	Dev            bool                                                        // also serve unknown hostnames (dev ids)
	// Lookup answers hostnames the source doesn't hold (instance.lookup/v1, taken from the scope by Setup); nil =
	// the directory alone.
	Lookup lookupv1.Lookup

	Listeners  Listeners
	HealthPath string // default [DefaultHealthPath]

	// Background work run until shutdown (e.g. the edge provider's refresh, extension jobs).
	Background []func(ctx context.Context)
	// OnListen is called for each address once it accepts connections.
	OnListen func(addr string)
	// OnShutdown runs after the listeners closed (in-flight requests done or cut), e.g. to stop running apps.
	OnShutdown func()
	// Closers release process-wide resources (extensions' stores) last, once the runtime has stopped every app.
	Closers []func(context.Context) error

	Logger *slog.Logger

	handler *Handler
	status  *Supervisor
}

// Statuses is the host's supervisor (instance.status/v1), created on first use. [Host.Setup] provides it on the
// host's scope by default.
func (h *Host) Statuses() *Supervisor {
	if h.status == nil {
		h.status = NewSupervisor(h.Runtime, h.log())
	}
	return h.status
}

// Handler returns the request front (before the wrappers), built on first use.
func (h *Host) Handler() *Handler {
	if h.handler == nil {
		h.handler = &Handler{Dir: h.dir(), Gate: ChainGates(h.Gates...), Runtime: h.Runtime, Forward: h.Forward,
			InstanceRoutes: h.InstanceRoutes, Dev: h.Dev, Lookup: h.Lookup, HealthPath: h.HealthPath, Logger: h.log(),
			Status: h.Statuses()}
	}
	return h.handler
}

func (h *Host) dir() *Directory {
	if h.Directory == nil {
		h.Directory = NewDirectory(h.log())
	}
	return h.Directory
}

func (h *Host) log() *slog.Logger {
	if h.Logger == nil {
		h.Logger = slog.Default()
	}
	return h.Logger
}

// Run serves until ctx is done or the source fails, then shuts down: the listeners (in-flight requests get
// ShutdownTimeout), background work, the runtime.
func (h *Host) Run(ctx context.Context) error {
	if h.Source == nil || h.Runtime == nil {
		return errors.New("host: a Source and a runtime are required")
	}
	log := h.log()
	dir := h.dir()
	if s, ok := h.Runtime.(Syncer); ok {
		dir.Subscribe(s.Sync)
	}
	var handler http.Handler = h.Handler()
	for i := len(h.Wrappers) - 1; i >= 0; i-- {
		handler = h.Wrappers[i](handler)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	sup := h.Statuses()
	sup.rt = h.Runtime // the runtime Setup chose, if it changed after the supervisor was made
	sup.Lookup = dir.Get
	if en, ok := h.Runtime.(runtimev1.ExitNotifier); ok {
		en.OnExit(sup.Exited)
	}
	if st, ok := h.Runtime.(RuntimeStarter); ok {
		if err := st.Start(runCtx); err != nil {
			return fmt.Errorf("host: runtime %s: %w", h.Runtime.Name(), err)
		}
	}
	if rl, ok := h.Runtime.(runtimev1.RunningLister); ok {
		sup.Adopted(rl.RunningIDs())
	}
	sourceErr := make(chan error, 1)
	go func() { sourceErr <- dir.Run(runCtx, h.Source, h.First) }()
	for _, bg := range h.Background {
		go bg(runCtx)
	}

	serveCtx, stopServe := context.WithCancel(ctx)
	defer stopServe()
	go func() {
		select {
		case <-serveCtx.Done():
		case err := <-sourceErr:
			if err != nil {
				log.Error("host: instance source stopped", "error", err)
			}
			stopServe()
		}
	}()
	served := func(host string) bool {
		if h.Lookup != nil {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			res, err := h.Lookup.Lookup(ctx, host)
			return err == nil && res.Found
		}
		_, ok := dir.Lookup(host)
		return ok
	}
	onListen := h.OnListen
	if onListen == nil {
		onListen = func(addr string) { log.Info("host: listening", "addr", addr) }
	}
	err := Serve(serveCtx, runCtx, h.Listeners, handler, h.TLS, served, log, onListen)
	cancel() // ends long-lived requests such as realtime SSE, and background work
	if h.OnShutdown != nil {
		h.OnShutdown()
	}
	if s, ok := h.Runtime.(Shutdowner); ok {
		s.Shutdown()
	}
	if len(h.Closers) > 0 {
		cctx, ccancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer ccancel()
		for _, c := range h.Closers {
			if cerr := c(cctx); cerr != nil && !errors.Is(cerr, context.Canceled) {
				log.Warn("host: close", "error", cerr)
			}
		}
	}
	return err
}
