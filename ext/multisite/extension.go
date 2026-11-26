package multisite

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"sync"

	gatev1 "pocketone.dev/ext/contracts/gate/v1"
	wrapv1 "pocketone.dev/ext/contracts/http/wrap/v1"
	lookupv1 "pocketone.dev/ext/contracts/instance/lookup/v1"
	sourcev1 "pocketone.dev/ext/contracts/instance/source/v1"
	statusv1 "pocketone.dev/ext/contracts/instance/status/v1"
	runtimev1 "pocketone.dev/ext/contracts/runtime/v1"
	tlsv1 "pocketone.dev/ext/contracts/tls/v1"
	"pocketone.dev/ext/extkit"
)

// Extension is a host extension: it works on the multisite process, whatever runs its instances. RegisterHost
// provides its contracts on the host's scope (h.Scope): runtime/v1 (pocker, docker), gate/v1 (firewall), tls/v1
// (autotls), http.wrap/v1 (servertiming), instance.source/v1. It may also add background work (h.Background). An
// extension with nothing to do on this host (its section absent, turned off) provides nothing. An extension that
// also implements Close(ctx) error is closed once the host has stopped every app.
type Extension interface {
	Name() string
	RegisterHost(h *Host) error
}

var (
	linkMu sync.Mutex
	linked []Extension
)

// Link makes e part of every host this binary builds from config (FromConfig), e.g. from the init of a package that
// turns itself on from its own config sections (ext/firewall). Linked extensions register before [Options] Extensions.
func Link(e Extension) {
	linkMu.Lock()
	defer linkMu.Unlock()
	linked = append(linked, e)
}

// Linked lists the extensions linked with [Link], in link order.
func Linked() []Extension {
	linkMu.Lock()
	defer linkMu.Unlock()
	return append([]Extension(nil), linked...)
}

// Use registers host extensions on h, in order (the order of a Many contract's providers). Call it before [Host.Setup].
func (h *Host) Use(exts ...Extension) error {
	s := h.scope()
	for _, e := range exts {
		prev := s.Registering(e.Name())
		err := e.RegisterHost(h)
		s.Registering(prev)
		if err != nil {
			return fmt.Errorf("%s: %w", e.Name(), err)
		}
		if c, ok := e.(interface{ Close(context.Context) error }); ok {
			h.Closers = append(h.Closers, c.Close)
		}
	}
	return nil
}

// Setup seals h's contracts, with `providers:` choices for the host-scope ones (`runtime/v1: docker`), and takes h's
// source, runtime, gates, wrappers and TLS from them (a runtime set on h directly is used when none is provided). h.Source, if set, is the default source; gates and
// wrappers set on h directly come after the contracts' (inside them). It returns the startup lines, also logged.
func (h *Host) Setup(choices extkit.Choices) ([]string, error) {
	s := h.scope()
	if h.Source != nil {
		prev := s.Registering(defaultName(h.Source))
		sourcev1.Contract.ProvideDefaultIn(s, h.Source)
		s.Registering(prev)
	}
	prev := s.Registering("multisite")
	statusv1.Contract.ProvideDefaultIn(s, h.Statuses())
	s.Registering(prev)
	if err := s.Seal(choices); err != nil {
		return nil, err
	}
	if src, ok := sourcev1.Contract.In(s); ok {
		h.Source = src
	}
	if l, ok := lookupv1.Contract.In(s); ok {
		h.Lookup = l
	}
	if rt, ok := runtimev1.Contract.In(s); ok {
		h.Runtime = rt
	}
	h.Gates = append(gatev1.Contract.AllIn(s), h.Gates...)
	var wraps []func(http.Handler) http.Handler
	for _, w := range wrapv1.Contract.AllIn(s) {
		wraps = append(wraps, w.Wrap)
	}
	h.Wrappers = append(wraps, h.Wrappers...)
	if t, ok := tlsv1.Contract.In(s); ok {
		h.TLS = t
	}
	lines := make([]string, 0, len(s.Lines))
	for _, l := range s.Lines {
		lines = append(lines, "multisite: "+l)
		h.log().Info("multisite: " + l)
	}
	return lines, nil
}

func defaultName(src InstanceSource) string {
	if _, ok := src.(*YAMLSource); ok {
		return "instances file"
	}
	return "host"
}

func (h *Host) scope() *extkit.Scope {
	if h.Scope == nil {
		h.Scope = extkit.NewScope("multisite")
	}
	return h.Scope
}

// HostGate is a gate that runs h's gates as they are when a request comes (a runtime built before [Host.Setup], or
// before host code appends gates, still sees all of them).
func HostGate(h *Host) Gate { return hostGate{h} }

type hostGate struct{ h *Host }

func (g hostGate) chain() Gate {
	if c := chainGates(g.h.Gates); c != nil {
		return c
	}
	return gateChain(nil)
}
func (g hostGate) AllowPeer(r *http.Request) bool { return g.chain().AllowPeer(r) }
func (g hostGate) Admit(w http.ResponseWriter, r *http.Request, inst Instance) (func(), bool) {
	return g.chain().Admit(w, r, inst)
}
func (g hostGate) ClientIP(r *http.Request) netip.Addr {
	if cr, ok := g.chain().(ClientIPResolver); ok {
		return cr.ClientIP(r)
	}
	return netip.Addr{}
}

// chainGates combines gates: AllowPeer needs every gate, Admit runs them in order and releases in reverse.
func chainGates(gates []Gate) Gate {
	switch len(gates) {
	case 0:
		return nil
	case 1:
		return gates[0]
	}
	return gateChain(gates)
}

type gateChain []Gate

func (c gateChain) AllowPeer(r *http.Request) bool {
	for _, g := range c {
		if !g.AllowPeer(r) {
			return false
		}
	}
	return true
}

func (c gateChain) Admit(w http.ResponseWriter, r *http.Request, inst Instance) (func(), bool) {
	var releases []func()
	releaseAll := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, g := range c {
		rel, ok := g.Admit(w, r, inst)
		if !ok {
			releaseAll()
			return nil, false
		}
		releases = append(releases, rel)
	}
	return releaseAll, true
}

// ClientIP is the first gate's answer that implements [ClientIPResolver] (the zero Addr when none does).
func (c gateChain) ClientIP(r *http.Request) netip.Addr {
	for _, g := range c {
		if cr, ok := g.(ClientIPResolver); ok {
			return cr.ClientIP(r)
		}
	}
	return netip.Addr{}
}

// ChainGates combines gates into one (nil when there are none).
func ChainGates(gates ...Gate) Gate { return chainGates(gates) }

// Provides is a host extension that only provides contracts on the host's scope: fn runs in RegisterHost, e.g.
// `multisite.Provides("cache", func(s *extkit.Scope) { cachev1.Contract.ProvideIn(s, hostCache) })`.
func Provides(name string, fn func(s *extkit.Scope)) Extension { return provides{name, fn} }

type provides struct {
	name string
	fn   func(*extkit.Scope)
}

func (p provides) Name() string { return p.name }

func (p provides) RegisterHost(h *Host) error {
	p.fn(h.Scope)
	return nil
}
