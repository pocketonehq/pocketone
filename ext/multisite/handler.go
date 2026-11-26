package multisite

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	lookupv1 "pocketone.dev/ext/contracts/instance/lookup/v1"
	"sync"
	"time"
)

// DefaultHealthPath answers on any host with the host's status. It sits under /.well-known/, the one root a
// host may answer for every hostname (an instance's own API is under /api/).
const DefaultHealthPath = "/.well-known/health"

// Handler is the edge's request pipeline: health → peer check → resolve the Host to an instance (directory) →
// blocked → owned by another edge? admit and forward : precheck, admit, the instance routes, then the instance's
// runtime.
type Handler struct {
	Dir     *Directory
	Gate    Gate    // nil = admit everything
	Runtime Runtime // runtime/v1: runs every instance this host serves

	// Forward serves a request for an instance another edge runs ([Instance.Remote] set), after the gates. Nil = a
	// plain reverse proxy to Remote as a URL.
	Forward func(w http.ResponseWriter, r *http.Request, inst Instance)

	// InstanceRoutes answer requests for instances this host runs, after the gates admitted them and before the
	// runtime (the first is the outermost): host-provided contracts (remote/v1 for docker's stock images) go here, so
	// the firewall, rate limits and the blocked answer apply to them. [InstanceOf] gives the resolved instance; next
	// is the runtime. Not run for instances another server runs ([Instance.Remote]).
	InstanceRoutes []func(next http.Handler) http.Handler

	// Dev serves any hostname: one the directory doesn't map becomes an instance with id [DevID] and Dev set.
	Dev bool

	// Lookup, when set (instance.lookup/v1: a source that doesn't hold every instance), answers every request's
	// hostname instead of the directory: from its cache, or by asking. A failed lookup is 503, not 404.
	Lookup lookupv1.Lookup

	HealthPath string // "" = DefaultHealthPath
	Logger     *slog.Logger

	// Status, when set, is told about every request handed to the runtime, so it launches and watches instances that
	// aren't running yet (instance.status/v1).
	Status *Supervisor

	routesOnce sync.Once
	routes     http.Handler
}

func (h *Handler) log() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

// Ready reports whether the directory has loaded and the runtime is ready.
func (h *Handler) Ready() bool {
	return h.Dir.Ready() && h.Runtime != nil && h.Runtime.Ready()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	hp := h.HealthPath
	if hp == "" {
		hp = DefaultHealthPath
	}
	if r.URL.Path == hp {
		h.serveHealth(w)
		return
	}
	if h.Gate != nil && !h.Gate.AllowPeer(r) {
		http.Error(w, "forbidden: "+PeerAddr(r).String(), http.StatusForbidden)
		return
	}
	if !h.Ready() {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}

	inst, status, msg := h.resolve(r)
	if status != 0 {
		http.Error(w, msg, status)
		return
	}
	if inst.Blocked != nil {
		status := inst.Blocked.Status
		if status == 0 {
			status = http.StatusForbidden
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(inst.Blocked.Message))
		return
	}

	if inst.Remote != "" {
		if h.Gate != nil {
			release, ok := h.Gate.Admit(w, r, inst)
			if !ok {
				return
			}
			defer release()
		}
		if h.Forward != nil {
			h.Forward(w, r, inst)
			return
		}
		h.proxyRemote(w, r, inst)
		return
	}

	rt := h.Runtime
	if rt == nil || !rt.Serves(inst) {
		http.Error(w, "this server can't run "+inst.ID+versionNote(inst), http.StatusServiceUnavailable)
		return
	}
	if pc, ok := rt.(Prechecker); ok && !pc.Precheck(w, r, inst) {
		return
	}
	if h.Gate != nil {
		release, ok := h.Gate.Admit(w, r, inst)
		if !ok {
			return
		}
		defer release() // long-lived responses (realtime) hold their admission until they end
	}
	if cr, ok := h.Gate.(ClientIPResolver); ok {
		r = withClientAddr(r, cr.ClientIP(r))
	}
	if h.Status != nil {
		h.Status.Observe(inst)
	}
	if len(h.InstanceRoutes) == 0 {
		rt.ServeInstance(w, r, inst)
		return
	}
	h.routesOnce.Do(func() {
		var next http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s := r.Context().Value(servingKey{}).(serving)
			s.rt.ServeInstance(w, r, s.inst)
		})
		for i := len(h.InstanceRoutes) - 1; i >= 0; i-- {
			next = h.InstanceRoutes[i](next)
		}
		h.routes = next
	})
	h.routes.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), servingKey{}, serving{inst, rt})))
}

type servingKey struct{}

type serving struct {
	inst Instance
	rt   Runtime
}

// InstanceOf is the instance a request is for, inside [Handler.InstanceRoutes] (resolved, not blocked, admitted).
func InstanceOf(r *http.Request) (Instance, bool) {
	s, ok := r.Context().Value(servingKey{}).(serving)
	return s.inst, ok
}

// InstanceID is [InstanceOf]'s id, in the shape extkit's host handlers take (remotev1.W.HostHandler,
// extkit.HostRoutes).
func InstanceID(r *http.Request) (string, bool) {
	inst, ok := InstanceOf(r)
	return inst.ID, ok
}

// withClientAddr returns r with RemoteAddr set to client (keeping the peer's port), or r itself when client is
// invalid or already the peer.
func withClientAddr(r *http.Request, client netip.Addr) *http.Request {
	if !client.IsValid() || client == PeerAddr(r) {
		return r
	}
	_, port, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		port = "0"
	}
	r2 := r.WithContext(r.Context())
	r2.RemoteAddr = net.JoinHostPort(client.String(), port)
	return r2
}

func versionNote(inst Instance) string {
	if inst.Version == "" {
		return ""
	}
	return " (PocketBase " + inst.Version + ")"
}

// resolve maps the request's Host to an instance. In dev mode an unmapped host becomes a new instance whose id is
// the hostname ([DevID]). A non-zero status means the request can't be routed.
func (h *Handler) resolve(r *http.Request) (Instance, int, string) {
	host := NormalizeHost(r.Host)
	if h.Lookup != nil {
		res, err := h.Lookup.Lookup(r.Context(), host)
		switch {
		case err != nil:
			h.log().Warn("edge: instance lookup failed", "host", host, "error", err)
			return Instance{}, http.StatusServiceUnavailable, "instance lookup failed for " + host + "; try again shortly"
		case !res.Found:
			return Instance{}, http.StatusNotFound, host + " not found"
		}
		return res.Instance, 0, ""
	}
	if inst, ok := h.Dir.Lookup(host); ok {
		return inst, 0, ""
	}
	if h.Dev {
		id := DevID(host)
		if id == "" {
			return Instance{}, http.StatusBadRequest, "hostname can't be used as an instance id: " + host
		}
		if inst, ok := h.Dir.Get(id); ok {
			return inst, 0, ""
		}
		inst := Instance{ID: id, Hosts: []string{host}, Dev: true}
		h.Dir.Put(inst)
		h.log().Info("edge: dev instance", "host", host, "id", id)
		return inst, 0, ""
	}
	return Instance{}, http.StatusNotFound, host + " not found"
}

// proxyRemote is the default forwarder: Remote is the other server's URL.
func (h *Handler) proxyRemote(w http.ResponseWriter, r *http.Request, inst Instance) {
	target, err := url.Parse(inst.Remote)
	if err != nil || target.Host == "" {
		http.Error(w, "bad remote for "+inst.ID, http.StatusBadGateway)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = forwardTransport
	proxy.ErrorLog = slog.NewLogLogger(h.log().Handler(), slog.LevelWarn)
	proxy.ServeHTTP(w, r)
}

// forwardTransport is the default forwarder's own transport: http.DefaultTransport may carry an egress policy that
// blocks private addresses, where other servers usually are.
var forwardTransport = &http.Transport{
	DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	MaxIdleConnsPerHost: 64,
	IdleConnTimeout:     90 * time.Second,
}

func (h *Handler) serveHealth(w http.ResponseWriter) {
	running := 0
	if s, ok := h.Runtime.(Statser); ok {
		running = s.RunningCount()
	}
	status := http.StatusOK
	body := map[string]any{"status": "ok", "instances": h.Dir.Len(), "running": running}
	if !h.Ready() {
		status = http.StatusServiceUnavailable
		body["status"] = "starting"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// PeerAddr is the address of the connecting peer (RemoteAddr), without port.
func PeerAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, _ := netip.ParseAddr(host)
	return a.Unmap()
}
