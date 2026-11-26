package docker

import (
	"context"
	"log/slog"
	"testing"

	runtimev1 "pocketone.dev/ext/contracts/runtime/v1"
	"pocketone.dev/ext/multisite"
)

// Docker's exit signal without a Docker daemon: a container whose output ends (it stopped on its own) or that stops
// answering is dropped and reported once; a stale container id is ignored. Adopted containers take any health answer.
func TestExitSignalAndHealthHints(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := slog.Default()
	rt := &Runtime{log: log, ctrs: map[string]*ctr{}, bg: ctx, cancel: cancel}
	rt.watch = multisite.NewScriptWatcher(log, func(string) {})
	defer rt.watch.Close()
	var exits []runtimev1.Exit
	rt.OnExit(func(x runtimev1.Exit) { exits = append(exits, x) })

	c := rt.get("a")
	c.container, c.port = "c1", 41000
	c.portNow.Store(41000)
	c.running.Store(true)
	if path, any := rt.HealthCheck("a"); path != "" || !any {
		t.Errorf("HealthCheck for a container without a hint = %q, %v; want any answer", path, any)
	}
	c.health.Store(&healthHint{path: "/api/health"})
	if path, any := rt.HealthCheck("a"); path != "/api/health" || any {
		t.Errorf("HealthCheck = %q, %v", path, any)
	}
	if ids := rt.RunningIDs(); len(ids) != 1 || ids[0] != "a" {
		t.Errorf("RunningIDs = %v", ids)
	}

	rt.ended("a", "c0") // an older container's stream ending: not the current one
	if len(exits) != 0 {
		t.Fatalf("exit for a stale container: %v", exits)
	}
	rt.ended("a", "c1")
	if len(exits) != 1 || exits[0].InstanceID != "a" || exits[0].Err == nil || exits[0].Restarting {
		t.Fatalf("exits = %+v, want one with an error", exits)
	}
	if rt.Running("a") || len(rt.RunningIDs()) != 0 {
		t.Error("still running after it ended")
	}
	rt.ended("a", "c1") // reported once
	if len(exits) != 1 {
		t.Errorf("exits = %d, want 1", len(exits))
	}

	c.container, c.port = "c2", 41001
	c.portNow.Store(41001)
	c.running.Store(true)
	rt.forget(c, 41001)
	if len(exits) != 2 || exits[1].Err == nil {
		t.Fatalf("forget: exits = %+v", exits)
	}

	cancel() // shutting down: containers keep running, nothing is reported
	c.container = "c3"
	c.running.Store(true)
	rt.ended("a", "c3")
	if len(exits) != 2 {
		t.Errorf("an exit was reported during shutdown: %+v", exits)
	}
}
