package multisite

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	statusv1 "pocketone.dev/ext/contracts/instance/status/v1"
	runtimev1 "pocketone.dev/ext/contracts/runtime/v1"
)

// Supervisor decides each instance's state for the host and provides instance.status/v1. It doesn't take over the
// runtime's start: when the host sends an instance its first request, the supervisor launches it through runtime/v1
// too, with a health request (GET /api/health, sent straight to the runtime, past the gates), and repeats it until the
// instance answers (running), the start timeout passes or the runtime reports an exit (failed). The runtime's exit
// signal (runtime/v1's ExitNotifier) makes a running instance idle, or failed when it ended on its own.
type Supervisor struct {
	// StartTimeout is how long an instance may take to answer its health endpoint (default 90s: a cold container
	// downloads nothing but may run long migrations).
	StartTimeout time.Duration
	// Interval is the pause between health requests (default 250ms).
	Interval time.Duration
	Logger   *slog.Logger
	// Lookup, if set, returns an instance as the directory has it now: each health request uses it, so a restart for
	// new settings never starts the instance again from the settings it had when it was first seen. The host sets it.
	Lookup func(id string) (Instance, bool)

	rt Runtime

	mu      sync.Mutex // entries
	entries map[string]*supEntry
	events  chan statusv1.Status

	subMu   sync.Mutex // subs (never held with mu)
	subs    map[int]func(statusv1.Status)
	nextSub int
	once    sync.Once
}

type supEntry struct {
	status statusv1.Status
	inst   Instance
	known  bool   // inst is set (an adopted instance isn't, until its first request)
	gen    uint64 // bumped on every change; a health loop stops when its generation is gone
}

var _ statusv1.Statuses = (*Supervisor)(nil)

// NewSupervisor returns a supervisor for rt.
func NewSupervisor(rt Runtime, log *slog.Logger) *Supervisor {
	if log == nil {
		log = slog.Default()
	}
	return &Supervisor{rt: rt, Logger: log}
}

func (s *Supervisor) init() {
	s.once.Do(func() {
		if s.StartTimeout <= 0 {
			s.StartTimeout = 90 * time.Second
		}
		if s.Interval <= 0 {
			s.Interval = 250 * time.Millisecond
		}
		if s.Logger == nil {
			s.Logger = slog.Default()
		}
		s.entries = map[string]*supEntry{}
		s.subs = map[int]func(statusv1.Status){}
		s.events = make(chan statusv1.Status, 4096)
		go s.dispatch()
	})
}

// dispatch hands changes to subscribers in order, outside the supervisor's lock.
func (s *Supervisor) dispatch() {
	for st := range s.events {
		s.subMu.Lock()
		fns := make([]func(statusv1.Status), 0, len(s.subs))
		for _, fn := range s.subs {
			fns = append(fns, fn)
		}
		s.subMu.Unlock()
		for _, fn := range fns {
			fn(st)
		}
	}
}

// set changes an instance's state (under s.mu) and queues the change. It returns the new generation.
func (s *Supervisor) set(e *supEntry, state statusv1.State, reason string) uint64 {
	e.gen++
	e.status = statusv1.Status{InstanceID: e.status.InstanceID, State: state, Reason: reason, At: time.Now().UTC()}
	s.events <- e.status
	return e.gen
}

func (s *Supervisor) entry(id string) *supEntry {
	e, ok := s.entries[id]
	if !ok {
		e = &supEntry{status: statusv1.Status{InstanceID: id, State: statusv1.Idle}}
		s.entries[id] = e
	}
	return e
}

// Observe is called for every request the host hands its runtime: an instance that isn't running or starting is
// launched and watched until it answers.
func (s *Supervisor) Observe(inst Instance) {
	s.init()
	s.mu.Lock()
	e := s.entry(inst.ID)
	e.inst, e.known = inst, true
	if e.status.State == statusv1.Starting || e.status.State == statusv1.Running {
		s.mu.Unlock()
		return
	}
	gen := s.set(e, statusv1.Starting, "")
	s.mu.Unlock()
	go s.probe(inst, gen)
}

// probe asks the instance's health endpoint until it answers, the start timeout passes, or its generation is gone
// (an exit or another change came first).
func (s *Supervisor) probe(inst Instance, gen uint64) {
	path, anyStatus := "/api/health", false
	if hc, ok := s.rt.(runtimev1.HealthChecker); ok {
		if p, a := hc.HealthCheck(inst.ID); p != "" {
			path, anyStatus = p, a
		} else {
			anyStatus = a
		}
	}
	deadline := time.Now().Add(s.StartTimeout)
	for {
		if !s.current(inst.ID, gen) {
			return
		}
		if s.Lookup != nil {
			cur, ok := s.Lookup(inst.ID)
			if !ok { // removed meanwhile: nothing to start
				s.finish(inst.ID, gen, statusv1.Idle, "")
				return
			}
			inst = cur
		}
		host := inst.ID
		if len(inst.Hosts) > 0 {
			host = inst.Hosts[0]
		}
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+host+path, nil)
		req.Host = host
		req.Body = http.NoBody // as a server request has; routers close it
		req.RemoteAddr = "127.0.0.1:0"
		rec := &probeRecorder{header: http.Header{}}
		s.rt.ServeInstance(rec, req, inst)
		cancel()
		if rec.healthy(anyStatus) {
			s.finish(inst.ID, gen, statusv1.Running, "")
			return
		}
		if time.Now().After(deadline) {
			s.finish(inst.ID, gen, statusv1.Failed, "not healthy within "+s.StartTimeout.String())
			return
		}
		time.Sleep(s.Interval)
	}
}

func (s *Supervisor) current(id string, gen uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[id]
	return e != nil && e.gen == gen
}

// finish ends a health loop, unless something changed the instance meanwhile.
func (s *Supervisor) finish(id string, gen uint64, state statusv1.State, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[id]
	if e == nil || e.gen != gen {
		return
	}
	if state == statusv1.Failed {
		s.Logger.Warn("multisite: instance didn't come up", "instance", id, "reason", reason)
	}
	s.set(e, state, reason)
}

// Exited is the runtime's exit signal (runtime/v1's ExitNotifier).
func (s *Supervisor) Exited(x runtimev1.Exit) {
	s.init()
	s.mu.Lock()
	e := s.entry(x.InstanceID)
	switch {
	case x.Restarting && e.known:
		gen := s.set(e, statusv1.Starting, "")
		inst := e.inst
		s.mu.Unlock()
		go s.probe(inst, gen)
		return
	case x.Err != nil:
		s.set(e, statusv1.Failed, x.Err.Error())
	case e.status.State != statusv1.Idle:
		s.set(e, statusv1.Idle, "")
	}
	s.mu.Unlock()
}

// Adopted marks instances the runtime found already running at start (containers that kept running across a host
// restart) as running: they answered before the previous process left them, and an exit signal follows if they end.
func (s *Supervisor) Adopted(ids []string) {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		e := s.entry(id)
		if e.status.State != statusv1.Running {
			s.set(e, statusv1.Running, "")
		}
	}
}

// Subscribe implements [statusv1.Statuses].
func (s *Supervisor) Subscribe(fn func(statusv1.Status)) func() {
	s.init()
	s.subMu.Lock()
	id := s.nextSub
	s.nextSub++
	s.subs[id] = fn
	s.subMu.Unlock()
	return func() {
		s.subMu.Lock()
		delete(s.subs, id)
		s.subMu.Unlock()
	}
}

// Current implements [statusv1.Statuses]: every instance that isn't idle.
func (s *Supervisor) Current() []statusv1.Status {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]statusv1.Status, 0, len(s.entries))
	for _, e := range s.entries {
		if e.status.State != statusv1.Idle {
			out = append(out, e.status)
		}
	}
	return out
}

// State returns an instance's current state (idle when the supervisor hasn't seen it).
func (s *Supervisor) State(id string) statusv1.Status {
	s.init()
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[id]; ok {
		return e.status
	}
	return statusv1.Status{InstanceID: id, State: statusv1.Idle}
}

// probeRecorder takes a health answer: the status, the headers and a discarded body.
type probeRecorder struct {
	header http.Header
	code   int
}

func (r *probeRecorder) Header() http.Header { return r.header }
func (r *probeRecorder) WriteHeader(code int) {
	if r.code == 0 {
		r.code = code
	}
}
func (r *probeRecorder) Write(b []byte) (int, error) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return io.Discard.Write(b)
}
func (r *probeRecorder) Flush() {}

// healthy: 200, or with anyStatus any answer that came from the instance (a runtime's own error page, such as "failed
// to start" or "unavailable", carries no X-Powered-By; an instance's answer does, set by PocketBase or the runtime's
// proxy).
func (r *probeRecorder) healthy(anyStatus bool) bool {
	if r.code == http.StatusOK {
		return true
	}
	return anyStatus && r.code != 0 && r.header.Get("X-Powered-By") != ""
}
