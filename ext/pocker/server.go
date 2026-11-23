package pocker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"path/filepath"
	fsv1 "pocketone.dev/ext/contracts/instance/fs/v1"
	tokensv1 "pocketone.dev/ext/contracts/instance/tokens/v1"
	runtimev1 "pocketone.dev/ext/contracts/runtime/v1"
	"strings"

	"pocketone.dev/ext/multisite"
	"pocketone.dev/ext/sandbox"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"pocketone.dev/ext/extkit"
)

// Server wires a [Config], an [InstanceSource] and a [Manager] into one HTTP server.
type Server struct {
	Config  Config
	Source  InstanceSource // nil = YAMLSource{Path: Config.InstancesFile}
	Logger  *slog.Logger
	Manager *Manager // set by Run

	// OnEvent, if set, receives instance events (started, stopped, restarts). An instance source that
	// implements [EventSink] receives them too.
	OnEvent func(Event)

	// SetupApp, if set, runs for each app after its migrations and before it serves: the place for a host
	// application's Go code (routes, settings) that uses Instance.Private instead of env.
	SetupApp func(inst Instance, app core.App) error

	// Env, if set, adds host values to each app's environment ($os.getenv, process.env) over the instance's own
	// secrets, so a secret can't override them (e.g. facts about the runtime the host wants every app to read).
	Env func(inst Instance) map[string]string

	// Wrap, if set, wraps the instance handler (e.g. firewall or rate-limit middleware from a host application).
	Wrap func(http.Handler) http.Handler

	// Forward, if set, serves requests for instances another server runs (see [ManagerOptions.Forward]).
	Forward func(w http.ResponseWriter, r *http.Request, inst Instance)

	// Gates every request passes first, in order (e.g. ext/firewall: allowed peers, client IPs, rate limits).
	Gates []multisite.Gate

	// Background work run until shutdown (e.g. an edge provider's refresh).
	Background []func(ctx context.Context)

	// Extensions extend the server (see [Extension]): their own settings, a request gate, TLS (e.g. the autotls extension), handler wrappers and background work. Order matters for wrappers: the
	// first listed is the outermost.
	Extensions []Extension

	// Standard is the per-app extension set every instance gets: ext/standard's Set, so every instance has tokens,
	// mcp, pitr and the rest, each turned off per instance with `extensions.<name>.enabled: false`. Required: Run
	// refuses to start without it, so no host can create an instance that lacks the standard extensions (pass
	// standard.New(); pocker can't make one itself, since extensions don't import each other). Its members'
	// Starters run like Extensions', and their Closers close once every app has stopped. A member also listed in Extensions is an error.
	Standard extkit.AppSet

	// Host is the host's contract scope: what it provides for its instances (extkit.ProvideForInstances, e.g.
	// remote/v1 with [Remote]); those contracts step aside inside each instance, and the host answers them in
	// InstanceRoutes. Nil: the multisite host's own scope (multisite.Host.Scope), where host extensions provide them.
	Host *extkit.Scope

	// InstanceRoutes answer requests for the instances after the gates and before the app (see
	// multisite.Host's InstanceRoutes): where the host's contracts for its instances are served, e.g.
	// remotev1.W.HostHandler(host, multisite.InstanceID, auth, next).
	InstanceRoutes []func(http.Handler) http.Handler

	// OnStart, if set, receives the Manager once Run has created it, before any request is served: the safe way for
	// host code running in other goroutines (scheduled jobs, maintenance) to get it.
	OnStart func(m *Manager)
}

// errNoStandard: a Server, Provider or Manager without Standard can't start instances.
var errNoStandard = errors.New("pocker: Standard is required: every instance gets the standard extensions (pass ext/standard's standard.New())")

// Run serves until ctx is done, then shuts down in-flight requests and running apps.
func (s *Server) Run(ctx context.Context) error {
	if s.Standard == nil {
		return errNoStandard
	}
	log := s.Logger
	if log == nil {
		log = slog.Default()
	}
	source := s.Source
	if source == nil {
		// production needs the host -> id map; dev mode works with or without it
		source = &YAMLSource{Path: s.Config.InstancesFile, Required: !s.Config.Dev}
	}
	first, err := source.Snapshot(ctx)
	if err != nil {
		return err // fail fast, e.g. missing instances file in production
	}
	h := &multisite.Host{
		Source: source, First: first, Forward: s.Forward, Dev: s.Config.Dev, Logger: log,
		Listeners: multisite.Listeners{Listen: s.Config.Listen, PlainListen: s.Config.PlainListen, TLS: s.Config.TLS, ShutdownTimeout: s.Config.ShutdownTimeout},
	}
	h.Gates = append(h.Gates, s.Gates...)
	if err := h.Use(s); err != nil {
		return err
	}
	if _, err := h.Setup(nil); err != nil {
		return err
	}
	return h.Run(ctx)
}

// Name implements multisite.Extension.
func (s *Server) Name() string { return "pocker" }

// RegisterHost implements multisite.Extension: it makes h run instances as isolates. It registers the host extensions
// among s.Extensions on h (gates, TLS, wrappers), installs the egress policy and provides the Manager as runtime/v1;
// its app extensions go to every instance. h's Source, Forward, Dev, Listeners and Logger are already set.
func (s *Server) RegisterHost(h *multisite.Host) error {
	log := h.Logger
	if log == nil {
		log = slog.Default()
		h.Logger = log
	}
	var allow []netip.Prefix
	for _, c := range s.Config.EgressAllow {
		pfx, err := netip.ParsePrefix(c)
		if err != nil {
			a, aerr := netip.ParseAddr(c)
			if aerr != nil {
				return fmt.Errorf("egress_allow: %q is not an IP or CIDR", c)
			}
			pfx = netip.PrefixFrom(a, a.BitLen())
		}
		allow = append(allow, pfx)
	}
	sandbox.InstallEgressPolicy(sandbox.EgressPolicy{Allow: allow})
	if h.DataRoot == "" {
		h.DataRoot = s.Config.PBRoot
	}
	pool := 2
	if s.Config.HooksPoolSize != nil {
		pool = *s.Config.HooksPoolSize
	}
	var (
		starters  []Starter
		initers   []AppIniter
		jsIniters []JSIniter
	)
	standardNames := map[string]bool{}
	for _, e := range s.Standard.Extensions() {
		standardNames[e.Name()] = true
		if st, ok := e.(Starter); ok {
			starters = append(starters, st)
		}
	}
	for _, p := range s.Extensions {
		if standardNames[p.Name()] {
			return fmt.Errorf("extension %s is in Standard already: configure the member there instead of listing it in Extensions", p.Name())
		}
		if cp, ok := p.(extkit.Extension); ok {
			// a PocketOne extension: register it on every instance, with that instance's settings
			if _, own := p.(AppIniter); !own {
				hp := hostExtension(cp, s.Config.IsolateConfig[cp.Name()])
				initers = append(initers, hp)
				jsIniters = append(jsIniters, hp)
			}
		}
		if st, ok := p.(Starter); ok {
			starters = append(starters, st)
		}
		if in, ok := p.(AppIniter); ok {
			initers = append(initers, in)
		}
		if js, ok := p.(JSIniter); ok {
			jsIniters = append(jsIniters, js)
		}
	}
	// isolate_config: a section for an extension the instances don't run is a typo, not a no-op
	for name := range s.Config.IsolateConfig {
		known := standardNames[name]
		for _, p := range s.Extensions {
			if _, ok := p.(extkit.Extension); ok && p.Name() == name {
				known = true
			}
		}
		if !known {
			return fmt.Errorf("isolate_config: unknown section %q (sections are the extensions every instance runs: %s)", name, strings.Join(instanceExtensionNames(s), ", "))
		}
	}
	// host extensions among s.Extensions (servertiming, autotls, ...): the same as on any runtime
	var hostExts []multisite.Extension
	for _, p := range s.Extensions {
		if he, ok := p.(multisite.Extension); ok {
			hostExts = append(hostExts, he)
		}
	}
	for _, e := range s.Standard.Extensions() {
		if c, ok := e.(extkit.Closer); ok {
			h.Closers = append(h.Closers, c.Close)
		}
	}
	if err := h.Use(hostExts...); err != nil {
		return err
	}
	if h.Directory == nil {
		h.Directory = multisite.NewDirectory(log)
	}
	host := s.Host
	if host == nil {
		host = h.Scope // where the host's extensions provide contracts for its instances (multisite.Host.Scope)
	}
	if host != nil {
		// caches shared by every instance (ext/cache's store) go in the host's own pb_cache, next to the instances'
		// folders (instance IDs have no underscore, so none is named pb_cache)
		host.SetCacheRoot(filepath.Join(h.DataRoot, extkit.CacheFolder))
	}
	s.Manager = NewManager(h.Scope, ManagerOptions{
		PBRoot:        s.Config.PBRoot,
		Dev:           s.Config.Dev,
		HooksPoolSize: pool,
		Logger:        log,
		Defaults:      s.Config.Limits,
		Gate:          multisite.HostGate(h), // the host's gates as they are once it's set up
		OnEvent:       s.onEvent(h.Source, log),
		Init:          chainIniters(initers),
		InitJS:        chainJSIniters(jsIniters),
		Setup:         s.SetupApp,
		Env:           s.Env,
		Standard:      s.Standard,
		IsolateConfig: s.Config.IsolateConfig,
		Host:          host,
		Forward:       h.Forward,
		Directory:     h.Directory,
	})
	if s.OnStart != nil {
		s.OnStart(s.Manager)
	}
	runtimev1.Contract.ProvideIn(h.Scope, s.Manager)
	fsv1.Contract.ProvideIn(h.Scope, s.Manager.Files())      // instance.fs/v1: its instances' code files
	tokensv1.Contract.ProvideIn(h.Scope, s.Manager.Tokens()) // instance.tokens/v1: its instances' own API tokens
	if s.Wrap != nil {                                       // the host application's wrapper, inside the host extensions'
		h.Wrappers = append(h.Wrappers, s.Wrap)
	}
	h.InstanceRoutes = append(h.InstanceRoutes, s.InstanceRoutes...)
	h.Background = append(h.Background, s.Background...)
	for _, st := range starters {
		h.Background = append(h.Background, func(ctx context.Context) { st.Start(ctx, s.Manager) })
	}
	h.OnListen = func(addr string) {
		log.Info("pocker: listening", "addr", addr, "pb_root", s.Config.PBRoot, "dev", s.Config.Dev)
	}
	return nil
}

// EventSink is implemented by instance sources that want instance events (e.g. to notify the owner).
type EventSink interface {
	InstanceEvent(Event)
}

func (s *Server) onEvent(source InstanceSource, log *slog.Logger) func(Event) {
	sink, _ := source.(EventSink)
	return func(e Event) {
		log.Info("pocker: event", "kind", e.Kind, "id", e.InstanceID, "reason", e.Reason)
		if s.OnEvent != nil {
			s.OnEvent(e)
		}
		if sink != nil {
			sink.InstanceEvent(e)
		}
	}
}

// instanceExtensionNames lists the extensions every instance runs: the standard set's, then the PocketOne extensions
// among s.Extensions.
func instanceExtensionNames(s *Server) []string {
	var out []string
	for _, e := range s.Standard.Extensions() {
		out = append(out, e.Name())
	}
	for _, p := range s.Extensions {
		if _, ok := p.(extkit.Extension); ok {
			out = append(out, p.Name())
		}
	}
	return out
}
