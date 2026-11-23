package mothership

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"hash/fnv"
	"log/slog"
	"sync"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/core"

	sourcev1 "pocketone.dev/ext/contracts/instance/source/v1"
	resolvev1 "pocketone.dev/ext/contracts/instance/resolve/v1"
	watchv1 "pocketone.dev/ext/contracts/instance/watch/v1"
	"pocketone.dev/ext/extkit"
)

// ServerName is the watch server's extension name.
const ServerName = "mothership"

// Server is the mothership side of a lazy fleet: it provides instance.watch/v1. Each edge opens one stream; each
// hostname it asks for is resolved (instance.resolve/v1) and watched on that stream; after every write to a record
// an answer was built from, the answer is resolved again and sent if it changed. Watches live in memory, per stream:
// a restart drops every stream, and every edge starts over.
//
// It holds no schema: whoever provides instance.resolve/v1 (the default [Schema], or a control plane's own) decides
// what a hostname serves.
type Server struct {
	// Enabled forces it on (true) or off (false) whatever pocketone.yaml says; nil follows `mothership.serve`.
	Enabled *bool
	// Shards is how many resolves run at once (default 8). Answers for one hostname always resolve in order.
	Shards int
	Logger *slog.Logger
}

var _ extkit.Extension = (*Server)(nil)

// NewServer returns the watch server, on when pocketone.yaml's mothership section says `serve: true`.
func NewServer() *Server { return &Server{} }

// Name implements [extkit.Extension].
func (s *Server) Name() string { return ServerName }

// Contracts implements [extkit.Declarer] (nothing while it's off).
func (s *Server) Contracts() extkit.Contracts {
	if !s.on() {
		return extkit.Contracts{}
	}
	return extkit.Contracts{Implements: extkit.IDs("instance.watch/v1"), Requires: extkit.IDs("instance.resolve/v1")}
}

func (s *Server) on() bool {
	if s.Enabled != nil {
		return *s.Enabled
	}
	fc, _ := Section.Get()
	return fc.Serve
}

// Register implements [extkit.Extension]: it provides instance.watch/v1 and follows the app's record writes.
func (s *Server) Register(app core.App, _ extkit.Config) error {
	if !s.on() {
		return nil
	}
	log := s.Logger
	if log == nil {
		log = app.Logger().With("extension", ServerName)
	}
	w := newWatchServer(app, log, s.Shards)
	watchv1.Contract.Provide(app, w)
	written := func(e *core.RecordEvent) error {
		w.written(e.Record)
		return e.Next()
	}
	app.OnRecordAfterCreateSuccess().BindFunc(written)
	app.OnRecordAfterUpdateSuccess().BindFunc(written)
	app.OnRecordAfterDeleteSuccess().BindFunc(written)
	app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
		w.close()
		return e.Next()
	})
	return nil
}

// ---------------------------------------------------------------------------------------------------------------

// target is one thing a connection follows: a hostname's answer, its standing set, or the edges list.
type target struct {
	c    *conn
	kind string // watchv1.KindHost, KindStanding, KindEdges
	host string
}

func (t target) shardKey() string {
	if t.kind == watchv1.KindHost {
		return "h:" + t.host
	}
	return t.kind + ":" + t.c.id
}

// follow is a target's state: what it was last built from and what was last sent.
type follow struct {
	deps []string
	last []byte
}

type conn struct {
	id, edge string

	mu      sync.Mutex
	seq     uint64
	queue   []any
	wake    chan struct{}
	closed  bool
	follows map[target]*follow // guarded by watchServer.mu
}

func (c *conn) push(ev watchv1.Event) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.seq++
	ev.Seq = c.seq
	c.queue = append(c.queue, ev)
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *conn) drain() []any {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.queue
	c.queue = nil
	return out
}

// write is one committed record write, remembered briefly so a resolve that read before it can tell.
type write struct {
	seq  uint64
	deps []string
}

// recentWrites is how many writes are remembered; a resolve that took longer than that many writes resolves again.
const recentWrites = 4096

type watchServer struct {
	app core.App
	log *slog.Logger

	mu     sync.Mutex
	conns  map[string]*conn
	index  map[string]map[target]struct{} // dep -> targets built from it
	wseq   uint64                         // writes seen
	recent []write                        // the last recentWrites writes, oldest first

	shards []*shard
	stop   chan struct{}
}

func newWatchServer(app core.App, log *slog.Logger, n int) *watchServer {
	if n <= 0 {
		n = 8
	}
	w := &watchServer{app: app, log: log, conns: map[string]*conn{}, index: map[string]map[target]struct{}{}, stop: make(chan struct{})}
	for i := 0; i < n; i++ {
		sh := &shard{w: w, pending: map[target]bool{}, signal: make(chan struct{}, 1)}
		w.shards = append(w.shards, sh)
		go sh.run()
	}
	return w
}

func (w *watchServer) close() {
	select {
	case <-w.stop:
	default:
		close(w.stop)
	}
}

// resolver is instance.resolve/v1, which the server Requires.
func (w *watchServer) resolver() resolvev1.Resolver { return resolvev1.Contract.Must(w.app) }

// shard resolves targets one at a time, in the order asked; a target asked for again before it ran runs once.
type shard struct {
	w       *watchServer
	mu      sync.Mutex
	order   []target
	pending map[target]bool // target -> force (send even if unchanged)
	signal  chan struct{}
}

func (w *watchServer) enqueue(t target, force bool) {
	h := fnv.New32a()
	_, _ = h.Write([]byte(t.shardKey()))
	sh := w.shards[int(h.Sum32())%len(w.shards)]
	sh.mu.Lock()
	if f, ok := sh.pending[t]; ok {
		sh.pending[t] = f || force
	} else {
		sh.pending[t] = force
		sh.order = append(sh.order, t)
	}
	sh.mu.Unlock()
	select {
	case sh.signal <- struct{}{}:
	default:
	}
}

func (sh *shard) run() {
	for {
		select {
		case <-sh.w.stop:
			return
		case <-sh.signal:
		}
		for {
			sh.mu.Lock()
			if len(sh.order) == 0 {
				sh.mu.Unlock()
				break
			}
			t := sh.order[0]
			sh.order = sh.order[1:]
			force := sh.pending[t]
			delete(sh.pending, t)
			sh.mu.Unlock()
			sh.w.refresh(t, force)
		}
	}
}

// resolved is a target's payload and deps, as of write seq.
type resolved struct {
	payload []byte
	event   watchv1.Event
	deps    []string
	seq     uint64
}

// build resolves t from the records. It notes the write count first: a write the read may have missed has a higher
// one (see commit).
func (w *watchServer) build(ctx context.Context, t target) (resolved, error) {
	w.mu.Lock()
	seq := w.wseq
	w.mu.Unlock()
	r := w.resolver()
	out := resolved{seq: seq, event: watchv1.Event{Kind: t.kind}}
	switch t.kind {
	case watchv1.KindHost:
		res, err := r.Resolve(ctx, resolvev1.Query{Edge: t.c.edge, Host: t.host})
		if err != nil {
			return out, err
		}
		a := &watchv1.Answer{Found: res.Instance != nil, Instance: res.Instance}
		out.event.Host, out.event.Answer, out.deps = t.host, a, append(res.Deps, resolvev1.Host(t.host))
	case watchv1.KindStanding:
		st, err := r.Standing(ctx, t.c.edge)
		if err != nil {
			return out, err
		}
		out.event.Standing, out.deps = st.Instances, st.Deps
		if out.event.Standing == nil {
			out.event.Standing = []sourcev1.Instance{} // "none" is sent as an empty list, not left out
		}
	case watchv1.KindEdges:
		el, err := r.Edges(ctx)
		if err != nil {
			return out, err
		}
		out.event.Edges, out.deps = el.Edges, el.Deps
		if out.event.Edges == nil {
			out.event.Edges = []watchv1.Edge{}
		}
	}
	p, _ := json.Marshal(out.event)
	out.payload = p
	return out, nil
}

// commit indexes t under its new deps (with w.mu held). It reports whether a write since res.seq touched any of
// them, so the answer may be stale and t must resolve again.
func (w *watchServer) commitLocked(t target, f *follow, res resolved) (stale bool) {
	for _, d := range f.deps {
		if m := w.index[d]; m != nil {
			delete(m, t)
			if len(m) == 0 {
				delete(w.index, d)
			}
		}
	}
	f.deps = res.deps
	deps := make(map[string]bool, len(res.deps))
	for _, d := range res.deps {
		deps[d] = true
		m := w.index[d]
		if m == nil {
			m = map[target]struct{}{}
			w.index[d] = m
		}
		m[t] = struct{}{}
	}
	if res.seq == w.wseq {
		return false
	}
	if len(w.recent) == 0 || w.recent[0].seq > res.seq+1 {
		return true // older than what's remembered: assume touched
	}
	for i := len(w.recent) - 1; i >= 0 && w.recent[i].seq > res.seq; i-- {
		for _, d := range w.recent[i].deps {
			if deps[d] {
				return true
			}
		}
	}
	return false
}

// refresh resolves t again and sends its event when it changed (or when forced: a watch's first answer).
func (w *watchServer) refresh(t target, force bool) {
	w.mu.Lock()
	f := t.c.follows[t]
	w.mu.Unlock()
	if f == nil {
		return // unwatched or closed meanwhile
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := w.build(ctx, t)
	if err != nil {
		w.log.Warn("mothership: resolve failed", "edge", t.c.edge, "kind", t.kind, "host", t.host, "error", err)
		if force && t.kind == watchv1.KindHost {
			// the edge waits for a first answer: tell it nothing is known rather than leave it hanging (not cached
			// as "not found": the error answer has no Found and no Instance, and the edge treats it as an error)
			t.c.push(watchv1.Event{Kind: watchv1.KindHost, Host: t.host})
		}
		return
	}
	w.mu.Lock()
	if t.c.follows[t] != f {
		w.mu.Unlock()
		return
	}
	stale := w.commitLocked(t, f, res)
	changed := force || string(f.last) != string(res.payload)
	if changed {
		f.last = res.payload
	}
	w.mu.Unlock()
	if changed {
		t.c.push(res.event)
	}
	if stale {
		w.enqueue(t, false)
	}
}

// written is called after every committed record write: the targets built from the record resolve again.
func (w *watchServer) written(rec *core.Record) {
	w.mu.Lock()
	idle := len(w.conns) == 0
	w.mu.Unlock()
	if idle {
		return
	}
	// a failed Touches still refreshes the record's own deps; the hosts it would have named wait for their next write
	touched, err := w.resolver().Touches(context.Background(), rec)
	if err != nil {
		w.log.Warn("mothership: touches failed", "collection", rec.Collection().Name, "record", rec.Id, "error", err)
	}
	deps := resolvev1.RecordDeps(rec, touched...)
	var targets []target
	w.mu.Lock()
	w.wseq++
	w.recent = append(w.recent, write{seq: w.wseq, deps: deps})
	if len(w.recent) > recentWrites {
		w.recent = append(w.recent[:0:0], w.recent[len(w.recent)-recentWrites:]...)
	}
	seen := map[target]bool{}
	for _, d := range deps {
		for t := range w.index[d] {
			if !seen[t] {
				seen[t] = true
				targets = append(targets, t)
			}
		}
	}
	w.mu.Unlock()
	for _, t := range targets {
		w.enqueue(t, false)
	}
}

func (w *watchServer) dropLocked(t target) {
	f := t.c.follows[t]
	if f == nil {
		return
	}
	for _, d := range f.deps {
		if m := w.index[d]; m != nil {
			delete(m, t)
			if len(m) == 0 {
				delete(w.index, d)
			}
		}
	}
	delete(t.c.follows, t)
}

// ---------------------------------------------------------------------------------------------------------------
// instance.watch/v1

var _ watchv1.Watcher = (*watchServer)(nil)

func newID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Stream implements [watchv1.Watcher].
func (w *watchServer) Stream(ctx context.Context, a extkit.Auth, in watchv1.StreamInput) (extkit.Stream, error) {
	if in.Edge == "" {
		return extkit.Stream{}, &extkit.Error{Code: "bad_request", Message: "edge is required", Status: 400}
	}
	c := &conn{id: newID(), edge: in.Edge, wake: make(chan struct{}, 1), follows: map[target]*follow{}}
	tEdges, tStanding := target{c: c, kind: watchv1.KindEdges}, target{c: c, kind: watchv1.KindStanding}
	edges, err := w.build(ctx, tEdges)
	if err != nil {
		return extkit.Stream{}, err
	}
	standing, err := w.build(ctx, tStanding)
	if err != nil {
		return extkit.Stream{}, err
	}
	hello := watchv1.Event{Kind: watchv1.KindHello, Conn: c.id, Edges: edges.event.Edges, Standing: standing.event.Standing}
	for i := range hello.Edges {
		if hello.Edges[i].Name == in.Edge {
			e := hello.Edges[i]
			hello.Self = &e
		}
	}
	fe, fs := &follow{last: edges.payload}, &follow{last: standing.payload}
	w.mu.Lock()
	w.conns[c.id] = c
	c.follows[tEdges], c.follows[tStanding] = fe, fs
	staleEdges := w.commitLocked(tEdges, fe, edges)
	staleStanding := w.commitLocked(tStanding, fs, standing)
	c.push(hello) // first, before anything a resolve below could send
	n := len(w.conns)
	w.mu.Unlock()
	if staleEdges {
		w.enqueue(tEdges, false)
	}
	if staleStanding {
		w.enqueue(tStanding, false)
	}
	w.log.Info("mothership: edge connected", "edge", in.Edge, "conn", c.id, "streams", n, "standing", len(hello.Standing))
	return extkit.Stream{
		Run: func(ctx context.Context, send func(any) error) error {
			ping := time.NewTicker(watchv1.PingEvery)
			defer ping.Stop()
			for {
				for _, ev := range c.drain() {
					if err := send(ev); err != nil {
						return nil // the edge went away
					}
				}
				select {
				case <-ctx.Done():
					return nil
				case <-w.stop:
					return nil
				case <-c.wake:
				case <-ping.C:
					c.push(watchv1.Event{Kind: watchv1.KindPing})
				}
			}
		},
		Close: func() {
			c.mu.Lock()
			c.closed = true
			c.queue = nil
			c.mu.Unlock()
			w.mu.Lock()
			for t := range c.follows {
				w.dropLocked(t)
			}
			delete(w.conns, c.id)
			n := len(w.conns)
			w.mu.Unlock()
			w.log.Info("mothership: edge disconnected", "edge", c.edge, "conn", c.id, "streams", n)
		},
	}, nil
}

// Watch implements [watchv1.Watcher].
func (w *watchServer) Watch(ctx context.Context, a extkit.Auth, in watchv1.WatchInput) (watchv1.WatchResult, error) {
	if in.Host == "" {
		return watchv1.WatchResult{}, &extkit.Error{Code: "bad_request", Message: "host is required", Status: 400}
	}
	w.mu.Lock()
	c := w.conns[in.Conn]
	if c != nil {
		t := target{c: c, kind: watchv1.KindHost, host: in.Host}
		_, had := c.follows[t]
		if !had {
			c.follows[t] = &follow{}
		}
		w.mu.Unlock()
		w.enqueue(t, true) // the answer goes on the stream, after anything already queued for this host
		return watchv1.WatchResult{Registered: true}, nil
	}
	w.mu.Unlock()
	edge := in.Edge
	if edge == "" {
		return watchv1.WatchResult{}, &extkit.Error{Code: "bad_request", Message: "edge is required without a live conn", Status: 400}
	}
	res, err := w.build(ctx, target{c: &conn{edge: edge}, kind: watchv1.KindHost, host: in.Host})
	if err != nil {
		return watchv1.WatchResult{}, err
	}
	return watchv1.WatchResult{Answer: res.event.Answer}, nil
}

// Unwatch implements [watchv1.Watcher].
func (w *watchServer) Unwatch(ctx context.Context, a extkit.Auth, in watchv1.UnwatchInput) (watchv1.UnwatchResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	c := w.conns[in.Conn]
	if c == nil {
		return watchv1.UnwatchResult{}, watchv1.ErrUnknownConn
	}
	t := target{c: c, kind: watchv1.KindHost, host: in.Host}
	_, had := c.follows[t]
	w.dropLocked(t)
	return watchv1.UnwatchResult{Removed: had}, nil
}
