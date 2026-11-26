package multisite

import (
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	statusv1 "pocketone.dev/ext/contracts/instance/status/v1"
	runtimev1 "pocketone.dev/ext/contracts/runtime/v1"
)

// healthRT answers health requests: 503 for the first `failFirst` calls, then `code` (with X-Powered-By when powered).
type healthRT struct {
	failFirst int32
	code      int
	powered   bool
	anyStatus bool
	calls     atomic.Int32
	paths     chan string
}

func (rt *healthRT) Name() string                      { return "health" }
func (rt *healthRT) Serves(Instance) bool              { return true }
func (rt *healthRT) Ready() bool                       { return true }
func (rt *healthRT) HealthCheck(string) (string, bool) { return "", rt.anyStatus }
func (rt *healthRT) ServeInstance(w http.ResponseWriter, r *http.Request, _ Instance) {
	if rt.paths != nil {
		select {
		case rt.paths <- r.Host + r.URL.Path:
		default:
		}
	}
	if rt.calls.Add(1) <= rt.failFirst {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	if rt.powered {
		w.Header().Set("X-Powered-By", "PocketBase 0.22.21")
	}
	w.WriteHeader(rt.code)
}

// recordStates subscribes and returns a func that waits for the states seen for id to equal want.
func recordStates(t *testing.T, s *Supervisor) func(id string, want ...statusv1.State) {
	t.Helper()
	var mu sync.Mutex
	got := map[string][]statusv1.State{}
	cancel := s.Subscribe(func(st statusv1.Status) {
		mu.Lock()
		got[st.InstanceID] = append(got[st.InstanceID], st.State)
		mu.Unlock()
	})
	t.Cleanup(cancel)
	return func(id string, want ...statusv1.State) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for {
			mu.Lock()
			seen := append([]statusv1.State(nil), got[id]...)
			mu.Unlock()
			if equalStates(seen, want) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: states %v, want %v", id, seen, want)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func equalStates(a, b []statusv1.State) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func newTestSupervisor(rt Runtime) *Supervisor {
	s := NewSupervisor(rt, nil)
	s.Interval = 5 * time.Millisecond
	s.StartTimeout = 2 * time.Second
	return s
}

// An instance is starting until its health endpoint answers 200, asked on its own hostname, then running; more
// requests don't start it again.
func TestSupervisorRunningOnceHealthy(t *testing.T) {
	rt := &healthRT{failFirst: 3, code: http.StatusOK, paths: make(chan string, 16)}
	s := newTestSupervisor(rt)
	states := recordStates(t, s)
	inst := Instance{ID: "app", Hosts: []string{"app.example.com"}}
	s.Observe(inst)
	s.Observe(inst)
	states("app", statusv1.Starting, statusv1.Running)
	if p := <-rt.paths; p != "app.example.com/api/health" {
		t.Errorf("health request went to %q", p)
	}
	s.Observe(inst)
	time.Sleep(30 * time.Millisecond)
	states("app", statusv1.Starting, statusv1.Running)
	if got := s.State("app").State; got != statusv1.Running {
		t.Errorf("State = %s", got)
	}
}

// An instance whose health endpoint never answers 200 fails after the start timeout; the next request tries again.
func TestSupervisorFailsAfterTimeout(t *testing.T) {
	rt := &healthRT{failFirst: 1 << 30}
	s := newTestSupervisor(rt)
	s.StartTimeout = 100 * time.Millisecond
	states := recordStates(t, s)
	inst := Instance{ID: "slow"}
	s.Observe(inst)
	states("slow", statusv1.Starting, statusv1.Failed)
	if r := s.State("slow").Reason; r == "" {
		t.Error("a failed state carries no reason")
	}
	s.Observe(inst)
	states("slow", statusv1.Starting, statusv1.Failed, statusv1.Starting, statusv1.Failed)
}

// An exit signal during start fails it at once and ends the health requests.
func TestSupervisorExitDuringStart(t *testing.T) {
	rt := &healthRT{failFirst: 1 << 30}
	s := newTestSupervisor(rt)
	states := recordStates(t, s)
	s.Observe(Instance{ID: "crash"})
	states("crash", statusv1.Starting)
	s.Exited(runtimev1.Exit{InstanceID: "crash", Err: errors.New("exited during startup")})
	states("crash", statusv1.Starting, statusv1.Failed)
	n := rt.calls.Load()
	time.Sleep(50 * time.Millisecond)
	if rt.calls.Load() > n+1 {
		t.Errorf("health requests went on after the exit: %d → %d", n, rt.calls.Load())
	}
	if got := s.State("crash").Reason; got != "exited during startup" {
		t.Errorf("Reason = %q", got)
	}
}

// A running instance that the runtime stops is idle; one it restarts is starting, then running again.
func TestSupervisorExitAndRestart(t *testing.T) {
	rt := &healthRT{code: http.StatusOK}
	s := newTestSupervisor(rt)
	states := recordStates(t, s)
	inst := Instance{ID: "app"}
	s.Observe(inst)
	states("app", statusv1.Starting, statusv1.Running)
	s.Exited(runtimev1.Exit{InstanceID: "app", Restarting: true})
	states("app", statusv1.Starting, statusv1.Running, statusv1.Starting, statusv1.Running)
	s.Exited(runtimev1.Exit{InstanceID: "app"})
	states("app", statusv1.Starting, statusv1.Running, statusv1.Starting, statusv1.Running, statusv1.Idle)
	if len(s.Current()) != 0 {
		t.Errorf("Current() = %v, want none (idle)", s.Current())
	}
	s.Exited(runtimev1.Exit{InstanceID: "app"}) // already idle: no change
	time.Sleep(20 * time.Millisecond)
	states("app", statusv1.Starting, statusv1.Running, statusv1.Starting, statusv1.Running, statusv1.Idle)
}

// With anyStatus (a release without a health endpoint), any answer from the instance counts, but the runtime's own
// error page doesn't.
func TestSupervisorAnyStatus(t *testing.T) {
	rt := &healthRT{failFirst: 2, code: http.StatusNotFound, powered: true, anyStatus: true}
	s := newTestSupervisor(rt)
	states := recordStates(t, s)
	s.Observe(Instance{ID: "legacy"})
	states("legacy", statusv1.Starting, statusv1.Running)
	if rt.calls.Load() < 3 {
		t.Errorf("running after %d health requests; the runtime's 503s must not count", rt.calls.Load())
	}

	plain := &healthRT{code: http.StatusNotFound} // no anyStatus: a 404 isn't healthy
	s2 := newTestSupervisor(plain)
	s2.StartTimeout = 80 * time.Millisecond
	states2 := recordStates(t, s2)
	s2.Observe(Instance{ID: "nohealth"})
	states2("nohealth", statusv1.Starting, statusv1.Failed)
}

// Instances adopted at start are running, and Current lists them.
func TestSupervisorAdopted(t *testing.T) {
	s := newTestSupervisor(&healthRT{code: http.StatusOK})
	states := recordStates(t, s)
	s.Adopted([]string{"a", "b"})
	states("a", statusv1.Running)
	states("b", statusv1.Running)
	if n := len(s.Current()); n != 2 {
		t.Errorf("Current() has %d, want 2", n)
	}
	s.Exited(runtimev1.Exit{InstanceID: "a", Restarting: true}) // never seen a request: no instance to ask, so idle
	states("a", statusv1.Running, statusv1.Idle)
}

// The host provides instance.status/v1 on its scope, and its handler reports the requests it hands the runtime.
func TestHostProvidesStatuses(t *testing.T) {
	rt := &healthRT{code: http.StatusOK}
	h := &Host{Runtime: rt, Source: &YAMLSource{}}
	if _, err := h.Setup(nil); err != nil {
		t.Fatal(err)
	}
	st, ok := statusv1.Contract.In(h.Scope)
	if !ok || st != statusv1.Statuses(h.Statuses()) {
		t.Fatalf("instance.status/v1 on the host scope = %v, %v", st, ok)
	}
	h.Statuses().Interval = 5 * time.Millisecond
	states := recordStates(t, h.Statuses())
	h.Handler().Status.Observe(Instance{ID: "app"})
	states("app", statusv1.Starting, statusv1.Running)
}

// Health requests use the instance as the directory has it now (a restart for new settings must not start it from the
// old ones); one removed meanwhile is idle.
func TestSupervisorLooksUpCurrentInstance(t *testing.T) {
	rt := &healthRT{failFirst: 1, code: http.StatusOK, paths: make(chan string, 16)}
	s := newTestSupervisor(rt)
	s.Lookup = func(id string) (Instance, bool) {
		if id == "gone" {
			return Instance{}, false
		}
		return Instance{ID: id, Hosts: []string{"new.example.com"}}, true
	}
	states := recordStates(t, s)
	s.Observe(Instance{ID: "app", Hosts: []string{"old.example.com"}})
	states("app", statusv1.Starting, statusv1.Running)
	if p := <-rt.paths; p != "new.example.com/api/health" {
		t.Errorf("health request went to %q, want the current instance's host", p)
	}
	s.Observe(Instance{ID: "gone"})
	states("gone", statusv1.Starting, statusv1.Idle)
}
