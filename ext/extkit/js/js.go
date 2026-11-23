// Package js is what extensions see of an app's JS engine, whichever engine runs it (goja today, QuickJS next).
// Extensions bind their Go API to JS, read what JS passes them, keep JS functions to call later, and hook the
// engine's lifecycle, all through these types, never through an engine's own.
//
// The engine itself is the jsvm extension (ext/jsvm). It sets an app's [Runner] when it registers, and calls the
// optional extension methods below on every value in its Config.Extensions.
package js

import (
	"context"
	"errors"
	"net/http"
	"reflect"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
)

// Runtime is one JS VM of an app. It's only valid inside the call that handed it out (OnInit, a NativeFunc, Run);
// keep a [Func] to call JS later instead.
type Runtime interface {
	// Set sets a global. Go values cross live (JS sees their fields and methods, named as in PocketBase's JS API:
	// FindRecordById -> findRecordById); a [NativeFunc] becomes a JS function.
	Set(name string, v any) error
	// Get reads a global (undefined if missing).
	Get(name string) Value
	// NewObject makes an empty JS object, e.g. a namespace to fill with Set and expose as a global.
	NewObject() Object
	// Eval runs code in the global scope (name labels it in stack traces) and returns its completion value.
	Eval(code, name string) (Value, error)
	// Engine is the engine's own VM (*goja.Runtime under goja), for engine-specific code that has no neutral form
	// yet. Code that uses it must check the type and work without it.
	Engine() any
}

// NativeFunc is a Go function that sees its raw JS arguments. Use it when a plain Go func won't do (optional
// arguments, a JS function argument, an options object). A returned error is thrown in JS.
type NativeFunc func(c Call) (any, error)

// Call is one call of a NativeFunc.
type Call interface {
	// Arg is the i-th argument; undefined past the end.
	Arg(i int) Value
	// Len is how many arguments were passed.
	Len() int
	Runtime() Runtime
}

// Value is a JS value.
type Value interface {
	// Nullish reports undefined or null.
	Nullish() bool
	// Export converts it to Go: primitives, map[string]any, []any, or the Go value it wraps.
	Export() any
	// String converts it to a string the way JS does.
	String() string
	// Object is the value as an object, or nil.
	Object() Object
	// Func keeps the value as a function to call later, or nil if it isn't one.
	Func() Func
}

// Release frees v now, if the engine supports it, instead of when Go garbage-collects it; v must not be used after.
// Call it on values a Go closure the VM holds can reach (a NativeFunc capturing a struct that keeps the value): the
// VM keeps such closures alive, so a value they reach is otherwise only freed after both sides drop it.
func Release(v Value) {
	if r, ok := v.(interface{ Release() }); ok && r != nil {
		r.Release()
	}
}

// Object is a JS object.
type Object interface {
	Value
	Get(name string) Value
	Set(name string, v any) error
	Keys() []string
}

// Func is a JS function kept for later. Call is safe from any goroutine: the engine runs it on the VM it came from
// (it may use that file's top-level variables), one call at a time per VM, and waits for a returned promise. The
// result is exported to Go.
type Func interface {
	Call(args ...any) (any, error)
}

// CallContext calls f like Func.Call, for a caller serving ctx (an HTTP request's context). Call it from outside the
// engine's own calls (not from a NativeFunc), so the VM serves other work while a returned promise is pending. When
// ctx ends before the promise settles, the call is given up: it returns ctx's cause, and the code after the
// function's next await never runs. Its timers (setTimeout, setInterval) are cleared when it returns. Engines
// without this fall back to Call.
func CallContext(ctx context.Context, f Func, args ...any) (any, error) {
	if cf, ok := f.(interface {
		CallContext(ctx context.Context, args ...any) (any, error)
	}); ok {
		return cf.CallContext(ctx, args...)
	}
	return f.Call(args...)
}

// Thrown returns what JS threw, exported to Go (an object's own enumerable properties as a map), when err is a JS
// exception; ok is false for other errors. A thrown Go error comes back as itself, not through Thrown.
func Thrown(err error) (value any, ok bool) {
	var t interface{ ThrownValue() any }
	if errors.As(err, &t) {
		return t.ThrownValue(), true
	}
	return nil, false
}

// Message is what JS threw as a short message: an Error's message, or the thrown value as a string. For other
// errors it's err.Error().
func Message(err error) string {
	var m interface{ JSMessage() string }
	if errors.As(err, &m) {
		return m.JSMessage()
	}
	return err.Error()
}

// Runner runs Go code on one of an app's JS runtimes, the same ones hooks and routes use.
type Runner interface {
	Run(fn func(rt Runtime) error) error
}

const runnerKey = "__pocketone_js_runner"

// ErrNoEngine is returned by [Run] on an app without a JS engine (jsvm not registered).
var ErrNoEngine = errors.New("js: no JS engine on this app")

// SetRunner is called by the engine when it registers on app.
func SetRunner(app core.App, r Runner) { app.Store().Set(runnerKey, r) }

// Run calls fn with one of app's JS runtimes. fn must not keep rt after it returns.
func Run(app core.App, fn func(rt Runtime) error) error {
	r, _ := app.Store().Get(runnerKey).(Runner)
	if r == nil {
		return ErrNoEngine
	}
	return r.Run(fn)
}

// Extension methods. A value in jsvm's Config.Extensions implements any of them; the engine calls only those it has.
// Extensions run in list order (done funcs in reverse); for the file and module sources the first that has the
// method wins; error mappers apply in list order.
type (
	// Initer sets up each runtime, after the engine's standard globals.
	Initer interface{ OnInit(rt Runtime) }
	// Caller wraps every call into JS; an error refuses the call.
	Caller interface {
		OnCall(rt Runtime) (done func(), err error)
	}
	// HTTPClienter supplies the client $http.send uses on rt.
	HTTPClienter interface {
		HTTPClient(rt Runtime) *http.Client
	}
	// HookFiler supplies the hook files instead of HooksDir (name -> source).
	HookFiler interface {
		HookFiles(pattern string) (map[string][]byte, error)
	}
	// MigrationFiler supplies the migration files instead of MigrationsDir.
	MigrationFiler interface {
		MigrationFiles(pattern string) (map[string][]byte, error)
	}
	// Moduler supplies require()'s modules: root is __hooks; load returns a module's source by its path under root,
	// or [ErrNoModule].
	Moduler interface {
		Modules() (root string, load func(path string) ([]byte, error))
	}
	// Loader wraps loading one hook file. done runs after it; mapErr rewrites its errors (nil = unchanged).
	Loader interface {
		OnLoad(rt Runtime, hooksRoot, file string, content []byte) (done func(), mapErr func(error) error)
	}
	// Hider hides Go members from JS (e.g. a sandbox hiding $app.restart and raw database handles): a hidden field
	// or method reads as undefined. The engine asks once per type.
	Hider interface {
		HideField(t reflect.Type, f reflect.StructField) bool
		HideMethod(t reflect.Type, m reflect.Method) bool
	}
	// Binder exposes an extension's Go API to an app's JS (e.g. as a $name global), on each of the app's runtimes.
	// Under a multisite host, what it exposes must be safe for a hostile tenant to call on its own app.
	Binder interface {
		BindJS(app core.App, rt Runtime)
	}
	// Typer declares in TypeScript what the extension binds (its globals, their types), so editors and tsc know them.
	// The engine appends it to types.d.ts. Write it as a .d.ts script: `declare var $name: ...`, no
	// top-level import or export.
	Typer interface {
		TypeScript() string
	}
	// TypesFilter changes the app's finished types.d.ts (the engine's declarations plus every Typer's), for an
	// extension that takes away what JS could otherwise reach, so editors and tsc stop offering it. The engine runs
	// every TypesFilter in order and writes what the last one returns.
	TypesFilter interface {
		FilterTypes(decl string) string
	}
)

// ErrNoModule is what a [Moduler]'s load returns for a module it doesn't have.
var ErrNoModule = errors.New("js: no such module")
