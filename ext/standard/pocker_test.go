package standard_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"pocketone.dev/ext/contracts/instance/source/v1/sourcetest"
	remotev1 "pocketone.dev/ext/contracts/remote/v1"
	"pocketone.dev/ext/extkit"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"pocketone.dev/ext/multisite"
	"pocketone.dev/ext/pocker"
	"pocketone.dev/ext/standard"
)

// Every Pocker instance gets the standard set, with its own extensions.<name> settings, and pb_hooks sees its JS APIs.
func TestPockerInstancesGetTheStandardSet(t *testing.T) {
	root := t.TempDir()
	hooks := filepath.Join(root, "a", "pb_hooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	probe := `routerAdd("GET", "/probe", (e) => e.json(200, {mcp: typeof $mcp, tokens: typeof $apiTokens}))`
	if err := os.WriteFile(filepath.Join(hooks, "probe.pb.js"), []byte(probe), 0o644); err != nil {
		t.Fatal(err)
	}

	set := standard.New()
	var mu sync.Mutex
	apps := map[string]core.App{}
	a := pocker.Instance{ID: "a", Hosts: []string{"a.test"}}
	b := pocker.Instance{ID: "b", Hosts: []string{"b.test"}, Extensions: map[string]any{"mcp": map[string]any{"enabled": false}}}
	m := pocker.NewManager(sourcetest.ScopeOf(a, b), pocker.ManagerOptions{
		PBRoot:   root,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Standard: set,
		Setup: func(inst pocker.Instance, app core.App) error {
			mu.Lock()
			apps[inst.ID] = app
			mu.Unlock()
			return nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	defer m.Shutdown()
	for deadline := time.Now().Add(10 * time.Second); !m.Ready(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("manager not ready")
		}
	}
	do := func(method, url string) (int, string, http.Header) {
		req := httptest.NewRequest(method, url, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		m.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String(), rec.Header()
	}

	code, _, hdr := do(http.MethodPost, "http://a.test/mcp")
	if code != http.StatusUnauthorized || !strings.Contains(hdr.Get("WWW-Authenticate"), "resource_metadata") {
		t.Fatalf("a /mcp: %d %q", code, hdr.Get("WWW-Authenticate"))
	}
	if code, body, _ := do(http.MethodGet, "http://a.test/.well-known/oauth-authorization-server"); code != 200 || !strings.Contains(body, "authorization_endpoint") {
		t.Fatalf("a oauth metadata: %d %s", code, body)
	}
	if code, body, _ := do(http.MethodGet, "http://a.test/probe"); code != 200 || !strings.Contains(body, `"mcp":"object"`) || !strings.Contains(body, `"tokens":"object"`) {
		t.Fatalf("a /probe: %d %q", code, body)
	}
	if code, _, _ := do(http.MethodPost, "http://b.test/mcp"); code == http.StatusUnauthorized {
		t.Fatalf("b has extensions.mcp.enabled: false, but /mcp answered %d", code)
	}

	// each instance's startup lines are in its own log
	logA, _ := os.ReadFile(filepath.Join(root, "a", "logs", "exec.log"))
	logB, _ := os.ReadFile(filepath.Join(root, "b", "logs", "exec.log"))
	if !strings.Contains(string(logA), "standard: on: settings, templates, cache, ") || !strings.Contains(string(logA), " api-tokens, remotes, mcp,") {
		t.Errorf("a's log:\n%s", logA)
	}
	if !strings.Contains(string(logB), "mcp off: enabled: false") {
		t.Errorf("b's log:\n%s", logB)
	}
}

// A standard member listed in a Pocker server's Extensions too is refused.
func TestPockerRefusesAMemberInExtensions(t *testing.T) {
	set := standard.New()
	srv := &pocker.Server{Config: pocker.DefaultConfig(), Standard: set, Extensions: []pocker.Extension{set.PITR}}
	srv.Config.PBRoot = t.TempDir()
	srv.Source = sourcetest.New()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := srv.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "extension pitr is in Standard already") {
		t.Fatalf("err = %v", err)
	}
}

// A host provides remote/v1 for its Pocker instances (with its own auth, as PocketHost's mothership tokens): the host
// answers whoami, status, restart and logs on each instance's hostname, after its gates, and the instance's own
// remote steps aside.
func TestHostProvidesRemoteForPockerInstances(t *testing.T) {
	root := t.TempDir()
	host := extkit.NewScope("multisite")
	var mu sync.Mutex
	starts := map[string]int{}
	auth := func(r *http.Request) extkit.Auth {
		if r.Header.Get("Authorization") == "ph_pat_ok" {
			return extkit.Auth{Kind: "token", ID: "pat1", Scopes: []string{"remote", "remote:read"}}
		}
		return extkit.Auth{}
	}
	gate := &countGate{}
	m := pocker.NewManager(sourcetest.ScopeOf(pocker.Instance{ID: "a", Hosts: []string{"a.test"}}), pocker.ManagerOptions{
		PBRoot:   root,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Standard: standard.New(),
		Host:     host,
		Gate:     gate,
		InstanceRoutes: []func(http.Handler) http.Handler{func(next http.Handler) http.Handler {
			return remotev1.W.HostHandler(host, multisite.InstanceID, auth, next)
		}},
		Setup: func(inst pocker.Instance, app core.App) error {
			mu.Lock()
			starts[inst.ID]++
			mu.Unlock()
			return nil
		},
	})
	host.Registering("phremote")
	remotev1.Contract.ProvideForInstances(host, pocker.Remote(m, host))
	edge := m // the whole request front: gates, then the instance routes, then the app

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	defer m.Shutdown()
	for deadline := time.Now().Add(10 * time.Second); !m.Ready(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("manager not ready")
		}
	}
	if err := m.StartInstance(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	// in memory: Pocker's egress policy (process-wide) refuses loopback connections
	hc := &http.Client{Transport: handlerTransport{edge, "a.test"}}
	c := remotev1.NewClient("http://a.test", "ph_pat_ok", hc)

	before := gate.n.Load()
	w, err := c.Whoami(ctx, extkit.Auth{})
	if err != nil || w.Via != "token" || w.TokenID != "pat1" || len(w.Contracts) == 0 || w.Contracts[0] != "remote/v1" {
		t.Fatalf("whoami %+v %v", w, err)
	}
	if gate.n.Load() == before {
		t.Fatal("remote/v1 bypassed the host's gate")
	}
	s, err := c.Status(ctx, extkit.Auth{})
	if err != nil || !s.Running || s.Uptime == "" {
		t.Fatalf("status %+v %v", s, err)
	}
	if _, err := remotev1.NewClient("http://a.test", "", hc).Status(ctx, extkit.Auth{}); err == nil {
		t.Fatal("anonymous status answered")
	}
	// the instance's own remote stepped aside for the host's
	logA, _ := os.ReadFile(filepath.Join(root, "a", "logs", "exec.log"))
	if !strings.Contains(string(logA), "remote/v1 ← host (phremote); stepped aside in the app: remotes") {
		t.Fatalf("a's log:\n%s", logA)
	}
	// logs: the instance's exec.log, from the host's provider (the SSE path is covered in ext/remotes)
	p, _ := remotev1.Contract.ForInstance(host, "a")
	st, err := p.Logs(ctx, extkit.Auth{Kind: "host"}, remotev1.LogsInput{N: 50})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range st.Initial {
		if m, _ := e.(map[string]any); strings.Contains(fmt.Sprint(m["message"]), "standard: on:") {
			found = true
		}
	}
	if !found {
		t.Fatal("the log stream didn't replay the instance's startup lines")
	}
	// restart: only that instance, through the Manager
	r, err := c.Restart(ctx, extkit.Auth{})
	if err != nil || !r.Restarting {
		t.Fatalf("restart %+v %v", r, err)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		mu.Lock()
		n := starts["a"]
		mu.Unlock()
		if n >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("instance a started %d times, want 2", n)
		}
	}
}

// countGate admits everything and counts admissions.
type countGate struct{ n atomic.Int64 }

func (*countGate) AllowPeer(*http.Request) bool { return true }
func (g *countGate) Admit(http.ResponseWriter, *http.Request, pocker.Instance) (func(), bool) {
	g.n.Add(1)
	return func() {}, true
}

// handlerTransport serves requests with h in memory, as if sent to host.
type handlerTransport struct {
	h    http.Handler
	host string
}

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Host = t.host
	rec := httptest.NewRecorder()
	t.h.ServeHTTP(rec, r)
	return rec.Result(), nil
}
