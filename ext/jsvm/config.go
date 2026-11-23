package jsvm

import (
	"reflect"
	"time"

	"pocketone.dev/ext/extkit/js"
)

// Config is the engine's configuration. The hook and migration fields mean what they mean in PocketBase's
// plugins/jsvm.
type Config struct {
	// OnInit runs after the app's VM is set up (after the extensions' OnInit), to add globals.
	OnInit func(rt js.Runtime)

	// HooksWatch restarts the app when a file in HooksDir changes.
	HooksWatch bool
	// HooksDir is the hooks folder (default pb_data/../pb_hooks).
	HooksDir string
	// HooksFilesPattern picks the hook files (default `^.*(\.pb\.js|\.pb\.ts)$`).
	HooksFilesPattern string
	// HooksPoolSize is accepted for compatibility with plugins/jsvm and ignored: each app has one VM.
	HooksPoolSize int

	// MigrationsDir is the migrations folder (default pb_data/../pb_migrations).
	MigrationsDir string
	// MigrationsFilesPattern picks the migration files (default `^.*(\.js|\.ts)$`).
	MigrationsFilesPattern string

	// TypesDir is where types.d.ts (rewritten at every start) and tsconfig.json (written once, never overwritten)
	// go: default the project root, the folder holding HooksDir. With hooks from a js.HookFiler (embedded) and no
	// TypesDir, neither is written.
	TypesDir string

	// VM sets the engine's limits (memory, CPU and wall time per task, queue, async calls).
	VM VMConfig

	// Extensions are values with any of the extkit/js extension methods (js.Initer, js.Caller, js.HTTPClienter,
	// js.HookFiler, js.MigrationFiler, js.Moduler, js.Loader, js.Binder).
	Extensions []any
}

type VMConfig struct {
	// MemoryLimit caps JS heap + estimated Go memory held through handles, in bytes.
	MemoryLimit int64
	// StackSize caps how deep JS calls may nest, in QuickJS stack slots (about one per JS call frame; 0 = 5000).
	// Deeper recursion throws "InternalError: stack overflow".
	StackSize int
	// TaskBudget is the JS time one task (a hook, a request, a promise continuation) may use. Time spent in Go
	// calls and waiting for async results doesn't count.
	TaskBudget time.Duration
	// QueueSize caps the tasks waiting for the loop; more fail fast with ErrBusy.
	QueueSize int
	// WaitTimeout bounds how long a Go caller waits for a JS task's synchronous part (its time in the queue
	// included). Awaits after it are bounded by AwaitLimit instead.
	WaitTimeout time.Duration
	// AwaitLimit bounds how long a Go caller waits for an async handler (a request, a hook, a cron job) to settle
	// once its synchronous part has run (default 15 min). Time the handler spends awaiting async Go calls
	// (`.async`, sleep.async, provider calls) counts here, not toward TaskBudget or TaskWall, which bound each
	// stretch of JS between awaits. When the limit passes, or the request the handler serves is abandoned (the client
	// went away), the handler stops: async Go calls it started are dropped when they finish, so the code after its
	// await never runs, and new ones throw.
	AwaitLimit time.Duration
	// PumpSyncCalls runs blocking Go calls made from sync JS (DB, sleep) on their own goroutine and keeps serving
	// other tasks meanwhile, nested on the same stack, up to MaxPumpDepth deep.
	// TaskWall caps one top-level task's wall time, Go calls included (a loop over Go calls never hits TaskBudget).
	TaskWall time.Duration
	// MaxConcurrent caps the JS calls (hooks, routes, cron jobs) an app has in flight at once; 0 = no cap. A call waits
	// for a slot, at most TaskWall, then fails with ErrTooBusy. Calls in flight overlap at Go calls and awaits.
	MaxConcurrent int
	// MaxSleep caps sleep() (default 10 s).
	MaxSleep time.Duration
	// MaxTimers caps the timers (setTimeout, setInterval) pending per VM (default 1024).
	MaxTimers int
	// MaxAsync caps async Go calls in flight per VM.
	MaxAsync      int
	PumpSyncCalls bool
	MaxPumpDepth  int
	// Shared types are alive anyway (the app, DB pools): handles to them aren't charged for what they reach.
	Shared func(t reflect.Type) bool
	// Blocking says whether a Go method on a receiver of this type may block (I/O); only those get pumped.
	Blocking func(recv reflect.Type) bool
}

func (c *VMConfig) defaults() {
	if c.MemoryLimit == 0 {
		c.MemoryLimit = 64 << 20
	}
	if c.TaskBudget == 0 {
		c.TaskBudget = 5 * time.Second
	}
	if c.QueueSize == 0 {
		c.QueueSize = 1024
	}
	if c.WaitTimeout == 0 {
		c.WaitTimeout = 30 * time.Second
	}
	if c.AwaitLimit == 0 {
		c.AwaitLimit = 15 * time.Minute
	}
	if c.TaskWall == 0 {
		c.TaskWall = 30 * time.Second
	}
	if c.MaxSleep == 0 {
		c.MaxSleep = 10 * time.Second
	}
	if c.MaxTimers == 0 {
		c.MaxTimers = 1024
	}
	if c.MaxAsync == 0 {
		c.MaxAsync = 256
	}
	if c.MaxPumpDepth == 0 {
		c.MaxPumpDepth = 16
	}
}
