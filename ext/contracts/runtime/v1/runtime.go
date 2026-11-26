// Package runtimev1 is the runtime/v1 contract: what runs a multisite host's instances (ext/pocker as isolates,
// ext/docker in containers). A host runs exactly one: every instance runs on it. When more than one is registered,
// `providers: { runtime/v1: docker }` picks.
//
// A host-scope service contract: providers register on the host's scope (ProvideIn), not on an app.
package runtimev1

import (
	"context"
	"net/http"
	"pocketone.dev/ext/extkit"

	sourcev1 "pocketone.dev/ext/contracts/instance/source/v1"
)

// Runtime runs instances on this host.
type Runtime interface {
	// Name identifies the runtime ("pocker", "docker"), for logs and the host's startup lines.
	Name() string
	// Serves reports whether this runtime can run inst (e.g. its PocketBase version); the host refuses one it can't.
	Serves(inst sourcev1.Instance) bool
	// ServeInstance serves a request for inst, starting it on demand. The host has already resolved the instance,
	// admitted the request (gates) and decided it runs here.
	ServeInstance(w http.ResponseWriter, r *http.Request, inst sourcev1.Instance)
	// Ready reports whether the runtime can take requests.
	Ready() bool
}

// Optional runtime hooks: a runtime implements the ones it needs, and the host checks each with a type assertion.

// Prechecker runs before the gates admit a request for a local instance, e.g. to answer 503 for an instance under
// maintenance, without counting it against rate limits. false = answered.
type Prechecker interface {
	Precheck(w http.ResponseWriter, r *http.Request, inst sourcev1.Instance) bool
}

// Syncer hears directory changes (stop removed instances, restart changed ones).
type Syncer interface {
	Sync(c sourcev1.Change, prev *sourcev1.Instance)
}

// Starter runs once before the host listens, e.g. to adopt containers that kept running across a restart. An error
// stops the host from starting.
type Starter interface {
	Start(ctx context.Context) error
}

// Shutdowner is called when the host stops.
type Shutdowner interface {
	Shutdown()
}

// Statser reports how many instances it has running, for the health route.
type Statser interface {
	RunningCount() int
}

// Exit is an instance's process ending. Err is nil for a stop the runtime chose (idle, powered off, removed,
// restarting with new settings or files) and set when it ended on its own (crashed, exited during startup).
type Exit struct {
	InstanceID string
	Err        error
	// Restarting: the runtime starts the instance again right away (its pb_hooks or settings changed), so the host
	// reports it as starting and asks its health endpoint again instead of reporting it idle.
	Restarting bool
}

// ExitNotifier tells the host each time an instance stops running (or fails to start), so the host's supervisor knows
// without polling. The host calls OnExit once, before Start. The runtime calls fn synchronously, in order, possibly
// while it holds its own locks, so fn must return quickly and never call back into the runtime from that goroutine.
type ExitNotifier interface {
	OnExit(fn func(Exit))
}

// RunningLister lists the instances running now, including ones adopted at Start (containers that kept running across
// a host restart), so the host's supervisor can confirm them.
type RunningLister interface {
	RunningIDs() []string
}

// HealthChecker tells the host's supervisor how to ask a started instance whether it's up: the path (default
// /api/health) and whether any answer from the instance counts, not only 200 (releases without a health endpoint).
// Without it the host asks /api/health and wants 200.
type HealthChecker interface {
	HealthCheck(instanceID string) (path string, anyStatus bool)
}

// Contract is runtime/v1, on a host's scope.
var Contract = extkit.NewContract[Runtime]("runtime/v1", extkit.One)
