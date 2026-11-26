// Package statusv1 is the instance.status/v1 contract: what a multisite host knows about each instance it runs
// (starting, running, idle, failed), as it changes. The host's supervisor decides it: an instance is running once its
// health endpoint answers, and idle or failed when its runtime reports that it ended (runtime/v1's ExitNotifier).
//
// A host-scope service contract; ext/multisite provides it by default.
package statusv1

import (
	"time"

	"pocketone.dev/ext/extkit"
)

// State is where an instance is in its run.
type State string

const (
	Starting State = "starting" // launched; its health endpoint hasn't answered yet
	Running  State = "running"  // its health endpoint answered
	Idle     State = "idle"     // not running (stopped by the runtime, or never started)
	Failed   State = "failed"   // it didn't come up, or it ended on its own (Reason has why)
)

// Status is an instance's state as of At.
type Status struct {
	InstanceID string
	State      State
	Reason     string // why it failed; empty otherwise
	At         time.Time
}

// Statuses reports instance states as they change.
type Statuses interface {
	// Subscribe calls fn for every change from now on, in order per instance, from the host's own goroutine (fn must
	// not block for long). The returned func stops the calls.
	Subscribe(fn func(Status)) (cancel func())
	// Current lists every instance that isn't idle (starting, running, failed), e.g. to resend the full picture after
	// a control plane reconnects.
	Current() []Status
}

// Contract is instance.status/v1, on a host's scope.
var Contract = extkit.NewContract[Statuses]("instance.status/v1", extkit.One)
