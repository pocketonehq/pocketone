package pocker

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	runtimev1 "pocketone.dev/ext/contracts/runtime/v1"
)

func waitExit(t *testing.T, exits <-chan runtimev1.Exit, want func(runtimev1.Exit) bool, what string) runtimev1.Exit {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case x := <-exits:
			if want(x) {
				return x
			}
		case <-deadline:
			t.Fatalf("no exit signal: %s", what)
		}
	}
}

// Pocker's exit signal (runtime/v1's ExitNotifier): a recycle is a restart, a stop for a blocked instance is an exit,
// a failed start is an exit with its error; RunningIDs lists running apps.
func TestExitSignal(t *testing.T) {
	d := t.TempDir()
	if err := os.MkdirAll(filepath.Join(d, "pb_hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeHook(t, d, "a1")
	bad := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bad, "pb_hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "pb_hooks", "main.pb.js"), []byte("this is not javascript ("), 0o644); err != nil {
		t.Fatal(err)
	}
	a := Instance{ID: "a", Hosts: []string{"a.test"}, Dir: d}
	b := Instance{ID: "b", Hosts: []string{"b.test"}, Dir: bad}
	m := newTestManager(t, false, a, b)
	defer m.Shutdown()
	exits := make(chan runtimev1.Exit, 16)
	m.OnExit(func(x runtimev1.Exit) { exits <- x })

	if err := m.StartInstance(context.Background(), "a"); err != nil {
		t.Fatal(err)
	}
	if ids := m.RunningIDs(); !slices.Contains(ids, "a") {
		t.Fatalf("RunningIDs = %v, want a", ids)
	}

	m.recycle("a", "test")
	x := waitExit(t, exits, func(x runtimev1.Exit) bool { return x.InstanceID == "a" }, "recycle")
	if !x.Restarting || x.Err != nil {
		t.Errorf("recycle exit = %+v, want Restarting", x)
	}

	blocked := a
	blocked.Blocked = &Blocked{Status: 503, Message: "powered off"}
	m.Sync(Change{Kind: Upsert, Instance: blocked}, &a)
	x = waitExit(t, exits, func(x runtimev1.Exit) bool { return x.InstanceID == "a" }, "blocked")
	if x.Restarting || x.Err != nil {
		t.Errorf("blocked exit = %+v, want a plain stop", x)
	}
	if ids := m.RunningIDs(); slices.Contains(ids, "a") {
		t.Errorf("RunningIDs = %v after the stop", ids)
	}

	if err := m.StartInstance(context.Background(), "b"); err == nil {
		t.Fatal("a broken hook started")
	}
	x = waitExit(t, exits, func(x runtimev1.Exit) bool { return x.InstanceID == "b" }, "failed start")
	if x.Err == nil {
		t.Errorf("failed start exit = %+v, want an error", x)
	}
}
