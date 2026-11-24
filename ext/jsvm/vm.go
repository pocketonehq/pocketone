// Package jsvm is PocketOne's JS engine: one async QuickJS VM per app, with a generic reflection bridge to Go values.
//
// QuickJS here is modernc.org/libquickjs, QuickJS translated to pure Go: no cgo (see capi.go).
//
// Each VM is owned by one goroutine, its loop. Everything that touches the VM runs on that goroutine: Go code elsewhere
// posts jobs (Do) and waits. Code already on the loop (a Go call made from JS that triggers a hook) runs inline, which
// is what makes Go → JS → Go → JS re-entrancy work.
package jsvm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"sync"

	"pocketone.dev/ext/extkit/js"
	"reflect"
	"sync/atomic"
	"time"
)

var (
	ErrBusy    = errors.New("asyncvm: too many queued tasks")
	ErrTimeout = errors.New("asyncvm: timed out waiting for JS")
	ErrClosed  = errors.New("asyncvm: closed")
	// ErrAborted is the cause of a Go function's context when JS aborted the call with an AbortSignal
	// (context.Cause(ctx)); its message ends with the signal's reason.
	ErrAborted = errors.New("jsvm: aborted by its AbortSignal")
	// ErrTooBusy: the app already has MaxConcurrent calls in flight and no slot freed up in time.
	ErrTooBusy = errors.New("too many concurrent JS calls for this app; retry shortly")
)

type kind uint8

const (
	kindObj kind = iota + 1
	kindFn
	kindNative
)

type nativeFn func(vm *VM, this JSValue, args []JSValue, ctor bool) JSValue

type entry struct {
	blocking bool
	v        reflect.Value
	bytes    int64
	kind     kind
	native   nativeFn
	ctor     func(args []reflect.Value) any // for `new X(...)` on Go factories
}

type VM struct {
	cfg  VMConfig
	a    *AVM
	ctx  *JSContext
	self uintptr // id in vmRegistry

	jobs  chan *task
	posts chan func() // completions from async Go calls; never refused
	quit  chan struct{}
	done  chan struct{}

	// base is cancelled when the VM closes: every context the engine hands a Go function ends with it.
	base     context.Context
	stopBase context.CancelCauseFunc

	// only touched on the loop thread
	handles []entry
	free    []uint32
	goBytes int64

	// stats, readable anywhere
	statGoBytes   atomic.Int64
	statHandles   atomic.Int64
	statTasks     atomic.Int64
	statPeakQueue atomic.Int64

	pumpDepth     int
	asyncInFlight int
	statMaxPump   atomic.Int64
	statPumped    atomic.Int64

	timers   map[int64]*jsTimer // setTimeout/setInterval, by id
	timerSeq int64

	asyncSeq     uint64
	asyncCancels map[uint64]context.CancelCauseFunc // async Go calls an AbortSignal can cancel, by id

	settleSeq   uint64
	settles     map[uint64]chan settled
	settleTypes map[uint64]reflect.Type
	refs        map[*jsRef]struct{}

	// cur is the call whose JS is running now (nil outside handlers); async Go calls started now belong to it.
	cur *owner

	Log func(args ...any)

	// HTTPClient is what $http.send uses (nil = http.DefaultClient).
	HTTPClient *http.Client

	// Hider, when set, hides Go members from JS (an extension's js.Hider, e.g. the sandbox's).
	Hider js.Hider

	sliceProto JSValue               // prototype of Go slices: Array methods
	objProto   JSValue               // prototype of Go objects (toJSON, toString, ...); constructors' prototypes inherit it
	mods       *moduleState          // require() and import resolution (set by bindNode)
	srcMaps    map[string]*sourceMap // TypeScript files by the name in stack traces

	slotsOnce sync.Once
	slots     chan struct{} // MaxConcurrent

	// OnTask, when set, wraps every JS handler call (hooks, routes, cron, migrations): an error refuses the call.
	OnTask func() (done func(), err error)
}

type task struct {
	fn        func()
	done      chan struct{}
	cancelled atomic.Bool
}

// owner is one JS call a Go caller waits for (a request, a hook, a cron job) together with the async work it
// awaits. The caller cancels it when it stops waiting (AwaitLimit passed, the request abandoned): async Go calls the
// owner started then never settle their promises (the code after its await never runs) and new ones throw, so the
// handler stops at its next await instead of running on with nobody waiting for it. A detached promise belongs to
// the call that started it and runs on after the call returns; only cancelling stops it.
type owner struct {
	mu  sync.Mutex
	err error
	// ended is set when the request the call served got its response: the call's timers are dropped then, since
	// the response they would write to is finished (its async Go calls still settle, as for any detached promise).
	ended atomic.Bool
	// ctx is the call's context, made when a Go function first asks for one (ownerCtx); cancelled with the owner.
	ctx  context.Context
	stop context.CancelCauseFunc
}

// live reports whether the call's timers may still fire: it wasn't given up, and the request it served (if any)
// hasn't ended. A nil owner (module scope, bootstrap) is always live.
func (o *owner) live() bool {
	return o == nil || (o.cause() == nil && !o.ended.Load())
}

func (o *owner) cancel(err error) {
	if o == nil {
		return
	}
	o.mu.Lock()
	if o.err == nil {
		o.err = err
	}
	stop, cause := o.stop, o.err
	o.mu.Unlock()
	if stop != nil {
		stop(cause) // Go functions running for the call stop too
	}
}

// ownerCtx is the context a Go function called for o gets: cancelled when o is given up, and when the VM closes
// for calls outside any handler (o nil). Not cancelled when o's request is answered: work it started runs on.
func (vm *VM) ownerCtx(o *owner) context.Context {
	if o == nil {
		return vm.base
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.ctx == nil {
		// not a child of vm.base: an owner that is never cancelled would stay in base's children for good
		o.ctx, o.stop = context.WithCancelCause(context.Background())
		if o.err != nil {
			o.stop(o.err)
		}
	}
	return o.ctx
}

// cause is why the owner was cancelled, nil while it is live.
func (o *owner) cause() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.err
}

// ErrAbandoned: the request a handler served ended (the client went away) before the handler settled.
var ErrAbandoned = errors.New("jsvm: request abandoned before the handler finished")

// awaitSettled waits off the loop for a call's promise: at most AwaitLimit, and no longer than ctx (the request it
// serves; nil for none). Giving up cancels o, which stops the handler at its next await.
func (vm *VM) awaitSettled(ch chan settled, o *owner, ctx context.Context) settled {
	t := time.NewTimer(vm.cfg.AwaitLimit)
	defer t.Stop()
	var gone <-chan struct{}
	if ctx != nil {
		gone = ctx.Done()
	}
	select {
	case s := <-ch:
		return s
	case <-t.C:
		err := fmt.Errorf("%w: the handler was still awaiting after %s (AwaitLimit)", ErrTimeout, vm.cfg.AwaitLimit)
		o.cancel(err)
		return settled{err: err}
	case <-gone:
		err := fmt.Errorf("%w: %v", ErrAbandoned, context.Cause(ctx))
		o.cancel(err)
		return settled{err: err}
	case <-vm.quit:
		o.cancel(ErrClosed)
		return settled{err: ErrClosed}
	}
}

type settled struct {
	ok  bool
	val any
	err error
}

// New starts a VM on its own locked OS thread. setup runs on the loop before New returns.
func New(cfg VMConfig, setup func(vm *VM) error) (*VM, error) {
	cfg.defaults()
	vm := &VM{
		cfg:         cfg,
		jobs:        make(chan *task, cfg.QueueSize),
		posts:       make(chan func(), 4096),
		quit:        make(chan struct{}),
		done:        make(chan struct{}),
		handles:     make([]entry, 1, 256), // handle 0 = none
		settles:     map[uint64]chan settled{},
		settleTypes: map[uint64]reflect.Type{},
		refs:        map[*jsRef]struct{}{},
		timers:      map[int64]*jsTimer{},
		asyncCancels: map[uint64]context.CancelCauseFunc{},
		Log:         func(args ...any) { fmt.Println(args...) },
	}
	vm.base, vm.stopBase = context.WithCancelCause(context.Background())
	vm.self = registerVM(vm)
	ready := make(chan error, 1)
	go vm.loop(setup, ready)
	if err := <-ready; err != nil {
		vm.Close()
		return nil, err
	}
	return vm, nil
}

func (vm *VM) loop(setup func(*VM) error, ready chan error) {
	defer close(vm.done)
	vm.a = C.avm_new(vm.self, uint64(vm.cfg.MemoryLimit), uint64(vm.cfg.StackSize))
	vm.ctx = vm.a.ctx
	err := vm.installPrelude()
	if err == nil && setup != nil {
		err = vm.guard(func() error { return setup(vm) })
	}
	ready <- err
	if err != nil {
		<-vm.quit
		vm.teardown()
		return
	}
	for {
		select {
		case t := <-vm.jobs:
			vm.runTask(t)
		case f := <-vm.posts:
			vm.runTask(&task{fn: f})
		case <-vm.quit:
			vm.teardown()
			return
		}
	}
}

func (vm *VM) teardown() {
	vm.clearTimers(func(*jsTimer) bool { return true })
	vm.free_(vm.sliceProto)
	vm.free_(vm.objProto)
	for r := range vm.refs {
		vm.free_(r.v)
	}
	vm.refs = nil
	C.JS_RunGC(vm.a)
	C.avm_free(vm.a)
	vm.a = nil
	unregisterVM(vm.self)
}

func (vm *VM) Close() {
	select {
	case <-vm.quit:
	default:
		vm.stopBase(ErrClosed)
		close(vm.quit)
	}
	<-vm.done
}

// guard runs f with a fresh CPU budget and turns panics into errors.
func (vm *VM) guard(f func() error) (err error) {
	prev := vm.a.deadline_ns
	prevWall := vm.a.wall_ns
	now := C.avm_now_ns()
	vm.a.deadline_ns = now + int64(vm.cfg.TaskBudget)
	if prevWall == 0 {
		vm.a.wall_ns = now + int64(vm.cfg.TaskWall)
	}
	defer func() {
		vm.a.deadline_ns = prev
		vm.a.wall_ns = prevWall
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return f()
}

func (vm *VM) runTask(t *task) {
	if t.cancelled.Load() {
		return
	}
	C.avm_set_interrupt(vm.a, 0)
	vm.statTasks.Add(1)
	prevOwner := vm.cur // t.fn may set it (a handler, an async completion) for itself and the jobs it queues
	_ = vm.guard(func() error {
		t.fn()
		vm.runJobs()
		return nil
	})
	vm.cur = prevOwner
	if t.done != nil {
		close(t.done)
		vm.drainPosts()
	}
}

// drainPosts runs the posts queued so far. A job (a request, a hook) leaves several: the frees of the JS values Go
// dropped (finalizers post them) and async completions. The loop takes a job or a post at random, one at a time,
// so without this it ran about one post per job and the queue stayed full: the process's finalizer goroutine then
// blocked in post, no finalizer ran anywhere, and the JS values those frees would release filled the heap
// ("out of memory" on every request after ~25k pages at 50 concurrent). Posts are short; a job waits for at most
// the ones queued before it finished.
func (vm *VM) drainPosts() {
	for n := len(vm.posts); n > 0; n-- {
		select {
		case f := <-vm.posts:
			vm.runTask(&task{fn: f})
		default:
			return
		}
	}
}

func (vm *VM) runJobs() {
	for C.avm_run_jobs(vm.a) > 0 {
	}
}

func (vm *VM) onLoop() bool { return vm.a != nil && C.avm_on_thread(vm.a) != 0 }

// Do runs f on the loop and waits. On the loop already (re-entrant), f runs inline.
func (vm *VM) Do(f func()) error {
	if vm.onLoop() {
		f()
		return nil
	}
	t := &task{fn: f, done: make(chan struct{})}
	select {
	case vm.jobs <- t:
	case <-vm.quit:
		return ErrClosed
	default:
		return ErrBusy
	}
	if q := int64(len(vm.jobs)); q > vm.statPeakQueue.Load() {
		vm.statPeakQueue.Store(q)
	}
	timer := time.NewTimer(vm.cfg.WaitTimeout)
	defer timer.Stop()
	select {
	case <-t.done:
		return nil
	case <-timer.C:
		t.cancelled.Store(true)
		C.avm_set_interrupt(vm.a, 1) // stop it if it is running now
		return ErrTimeout
	case <-vm.quit:
		return ErrClosed
	}
}

// post queues a completion; used by async Go calls. Never refused (the work is already done).
func (vm *VM) post(f func()) {
	select {
	case vm.posts <- f:
	case <-vm.quit:
	}
}

// waitSettled waits on the loop for a settle channel, running other tasks meanwhile (re-entrant pump).
// The CPU budget is paused while waiting.
func (vm *VM) waitSettled(ch chan settled) settled {
	timer := time.NewTimer(vm.cfg.WaitTimeout)
	defer timer.Stop()
	for {
		vm.runJobs()
		select {
		case s := <-ch:
			return s
		default:
		}
		saved := vm.a.deadline_ns
		vm.a.deadline_ns = 0
		start := C.avm_now_ns()
		var run func()
		var t *task
		select {
		case s := <-ch:
			vm.a.deadline_ns = saved
			return s
		case t = <-vm.jobs:
		case run = <-vm.posts:
		case <-timer.C:
			vm.a.deadline_ns = saved
			return settled{err: ErrTimeout}
		}
		if saved != 0 {
			saved += C.avm_now_ns() - start
		}
		vm.a.deadline_ns = saved
		if t != nil {
			vm.runTask(t)
		} else {
			vm.runTask(&task{fn: run})
		}
	}
}

// pumped runs a blocking Go call on its own goroutine and serves other tasks until it returns.
func (vm *VM) pumped(call func() []reflect.Value) ([]reflect.Value, error) {
	type res struct {
		out []reflect.Value
		err error
	}
	ch := make(chan res, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- res{err: fmt.Errorf("%v", r)}
			}
		}()
		ch <- res{out: call()}
	}()
	vm.pumpDepth++
	vm.statPumped.Add(1)
	if d := int64(vm.pumpDepth); d > vm.statMaxPump.Load() {
		vm.statMaxPump.Store(d)
	}
	defer func() { vm.pumpDepth-- }()
	for {
		vm.runJobs()
		select {
		case r := <-ch:
			return r.out, r.err
		case t := <-vm.jobs:
			vm.runTask(t)
		case f := <-vm.posts:
			vm.runTask(&task{fn: f})
		}
	}
}

// ---- handles ----

func (vm *VM) newHandle(e entry) (uint32, error) {
	if vm.goBytes+e.bytes > vm.cfg.MemoryLimit {
		return 0, fmt.Errorf("out of memory: Go objects held by JS would exceed %d bytes", vm.cfg.MemoryLimit)
	}
	var h uint32
	if n := len(vm.free); n > 0 {
		h = vm.free[n-1]
		vm.free = vm.free[:n-1]
		vm.handles[h] = e
	} else {
		h = uint32(len(vm.handles))
		vm.handles = append(vm.handles, e)
	}
	vm.charge(e.bytes)
	vm.statHandles.Add(1)
	return h, nil
}

func (vm *VM) charge(n int64) {
	vm.goBytes += n
	vm.statGoBytes.Store(vm.goBytes)
	C.JS_SetMemoryLimit(vm.a, uint64(vm.jsLimit()))
}

// jsLimit is the QuickJS heap cap: the memory limit less what Go objects held by JS are charged.
func (vm *VM) jsLimit() int64 {
	limit := vm.cfg.MemoryLimit - vm.goBytes
	if limit < 1<<20 {
		limit = 1 << 20
	}
	return limit
}

// avmGoFinalize releases handle h when QuickJS frees its object.
func avmGoFinalize(id uintptr, h uint32) {
	vm := lookupVM(id)
	if vm == nil {
		return
	}
	e := vm.handles[h]
	vm.handles[h] = entry{}
	vm.free = append(vm.free, uint32(h))
	vm.statHandles.Add(-1)
	if e.bytes != 0 {
		vm.charge(-e.bytes)
	}
}

func vmOf(ctx *JSContext) *VM { return lookupVM(ctx.a.id) }

// ---- small helpers ----

func (vm *VM) free_(v JSValue) { C.JS_FreeValue(vm.ctx, v) }

func (vm *VM) str(s string) JSValue { return C.JS_NewStringLen(vm.ctx, s) }

func (vm *VM) goString(v JSValue) string { return C.goString(vm.ctx, v) }

func (vm *VM) throw(err error) JSValue {
	// A Go error stays reachable from JS as e.value, and comes back to Go unchanged if rethrown.
	msg := err.Error()
	ex := C.JS_NewError(vm.ctx)
	ms := vm.str(msg)
	cs := C.CString("message")
	C.JS_SetPropertyStr(vm.ctx, ex, cs, ms)
	if gv, e2 := vm.toJS(reflect.ValueOf(err)); e2 == nil {
		cv := C.CString("value")
		C.JS_SetPropertyStr(vm.ctx, ex, cv, gv)
		C.free(cv)
	}
	C.free(cs)
	return C.JS_Throw(vm.ctx, ex)
}

// exception takes the pending JS exception as a Go error.
func (vm *VM) exception() error {
	ex := C.JS_GetException(vm.ctx)
	defer vm.free_(ex)
	// Reading an out-of-memory error (its message, stack) allocates: give it 1 MiB above the cap while we do.
	C.JS_SetMemoryLimit(vm.a, uint64(vm.jsLimit()+1<<20))
	defer C.JS_SetMemoryLimit(vm.a, uint64(vm.jsLimit()))
	if C.avm_tag(ex) == C.JS_TAG_OBJECT {
		if h := C.avm_handle_of(vm.a, ex); h != 0 {
			if err, ok := vm.handles[h].v.Interface().(error); ok {
				return err
			}
		}
		cv := C.CString("value")
		val := C.JS_GetPropertyStr(vm.ctx, ex, cv)
		C.free(cv)
		if h := C.avm_handle_of(vm.a, val); h != 0 {
			if err, ok := vm.handles[h].v.Interface().(error); ok {
				vm.free_(val)
				return err
			}
		}
		vm.free_(val)
		cs := C.CString("stack")
		stack := C.JS_GetPropertyStr(vm.ctx, ex, cs)
		C.free(cs)
		defer vm.free_(stack)
		msg := vm.goString(ex)
		cm := C.CString("message")
		mv := C.JS_GetPropertyStr(vm.ctx, ex, cm)
		C.free(cm)
		short := ""
		if C.avm_tag(mv) == C.JS_TAG_STRING {
			short = vm.goString(mv)
		} else if C.avm_is_exception(mv) != 0 && !C.JS_HasException(vm.ctx) {
			// QuickJS stores JS_EXCEPTION as the message when it couldn't allocate the message string (out of memory)
			short = "out of memory"
			if msg == "" {
				msg = "InternalError: out of memory"
			}
		}
		vm.free_(mv)
		var thrown any
		if tv, err := vm.fromJS(ex, nil); err == nil && tv.IsValid() {
			thrown = tv.Interface()
		}
		if C.avm_tag(stack) == C.JS_TAG_STRING {
			return &JSError{Message: msg, Stack: vm.remapStack(vm.goString(stack)), Value: thrown, Msg: short}
		}
		return &JSError{Message: msg, Value: thrown, Msg: short}
	}
	if C.avm_tag(ex) == C.JS_TAG_NULL {
		// QuickJS throws null when it can't even allocate the error object
		return &JSError{Message: "InternalError: out of memory"}
	}
	return &JSError{Message: vm.goString(ex)}
}

// JSError is a JS exception as a Go error.
type JSError struct {
	Message, Stack string
	// Msg is an Error's message property, or Message for other thrown values.
	Msg string
	// Value is what JS threw, exported (an object's own enumerable properties as a map).
	Value any
}

// Error is the message, then where it was thrown (the top stack frame's file:line:col), like goja's exceptions.
func (e *JSError) Error() string {
	if at := topFrame(e.Stack); at != "" {
		return e.Message + " at " + at
	}
	return e.Message
}

// topFrame is the location of a stack's first frame: "pb_hooks/main.pb.js:4:9" from "    at <anonymous> (...)".
func topFrame(stack string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(stack), "\n")
	line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "at "))
	if i := strings.LastIndex(line, "("); i >= 0 && strings.HasSuffix(line, ")") {
		line = line[i+1 : len(line)-1]
	}
	if line == "" || line == "native" || !strings.Contains(line, ":") {
		return ""
	}
	return line
}

// ThrownValue implements extkit/js's Thrown.
func (e *JSError) ThrownValue() any { return e.Value }

// JSMessage implements extkit/js's Message.
func (e *JSError) JSMessage() string {
	if e.Msg != "" {
		return e.Msg
	}
	return e.Message
}

// Eval runs a script in the global scope (on the loop) and returns its exception, if any.
func (vm *VM) Eval(code, file string) error {
	var err error
	derr := vm.Do(func() {
		err = vm.evalHere(code, file)
	})
	if derr != nil {
		return derr
	}
	return err
}

func (vm *VM) evalHere(code, file string) error {
	cc := C.CString(code)
	cf := C.CString(file)
	defer C.free(cc)
	defer C.free(cf)
	r := C.avm_eval(vm.ctx, cc, uint64(len(code)), cf)
	if C.avm_is_exception(r) != 0 {
		return vm.exception()
	}
	vm.free_(r)
	vm.runJobs()
	return nil
}

func (vm *VM) setGlobal(name string, v JSValue) {
	g := C.JS_GetGlobalObject(vm.ctx)
	cs := C.CString(name)
	C.JS_SetPropertyStr(vm.ctx, g, cs, v)
	C.free(cs)
	vm.free_(g)
}

// Set exposes a Go value as a global (on the loop).
func (vm *VM) Set(name string, v any) error {
	var err error
	derr := vm.Do(func() {
		var jv JSValue
		jv, err = vm.toJS(reflect.ValueOf(v))
		if err == nil {
			vm.setGlobal(name, jv)
		}
	})
	if derr != nil {
		return derr
	}
	return err
}

// SetNative exposes a raw native function as a global.
func (vm *VM) setNative(name string, fn nativeFn) {
	h, _ := vm.newHandle(entry{kind: kindNative, native: fn})
	vm.setGlobal(name, C.avm_new_func(vm.a, uint32(h), 0))
}

// Stats is a snapshot readable from any goroutine.
type Stats struct {
	JSBytes, GoBytes, Handles, Tasks, PeakQueue int64
}

func (vm *VM) Stats() Stats {
	s := Stats{GoBytes: vm.statGoBytes.Load(), Handles: vm.statHandles.Load(), Tasks: vm.statTasks.Load(), PeakQueue: vm.statPeakQueue.Load()}
	_ = vm.Do(func() { s.JSBytes = int64(C.avm_mem_used(vm.a)) })
	return s
}

// GC frees what Go and JS no longer use: a Go collection (dropped js.Value wrappers post their frees to the loop),
// the frees posted so far, then QuickJS's cycle collector. Finalizers run on their own goroutine, so a free may land
// after GC returns; call it again to catch those. For tests and diagnostics; the engine collects on its own.
func (vm *VM) GC() {
	runtime.GC()
	_ = vm.Do(func() {
		for {
			select {
			case f := <-vm.posts:
				vm.runTask(&task{fn: f})
				continue
			default:
			}
			break
		}
		C.JS_RunGC(vm.a)
	})
}

// Interrupts is how many times the interrupt handler stopped JS.
func (vm *VM) Interrupts() int64 {
	var n int64
	_ = vm.Do(func() { n = int64(vm.a.interrupts) })
	return n
}

// Result is a JS value exported to Go, as RunString returns it.
type Result struct{ v any }

// Export is the value converted to Go (int64 for integers, as goja does).
func (r Result) Export() any { return r.v }

func (r Result) String() string { return fmt.Sprint(r.v) }

// RunString evaluates code in the global scope and returns its completion value, exported.
func (vm *VM) RunString(code string) (Result, error) {
	var res Result
	var err error
	derr := vm.Do(func() {
		cc := C.CString(code)
		cf := C.CString("<eval>")
		defer C.free(cc)
		defer C.free(cf)
		r := C.avm_eval(vm.ctx, cc, uint64(len(code)), cf)
		if C.avm_is_exception(r) != 0 {
			err = vm.exception()
			return
		}
		vm.runJobs()
		gv, e := vm.fromJS(r, nil)
		vm.free_(r)
		if e != nil {
			err = e
			return
		}
		if gv.IsValid() {
			res.v = gv.Interface()
		}
	})
	if derr != nil {
		return res, derr
	}
	return res, err
}

// acquire takes a call slot (MaxConcurrent) on the caller's goroutine; the returned func gives it back.
func (vm *VM) acquire() (func(), error) {
	if vm.cfg.MaxConcurrent <= 0 || vm.onLoop() {
		return func() {}, nil
	}
	vm.slotsOnce.Do(func() { vm.slots = make(chan struct{}, vm.cfg.MaxConcurrent) })
	t := time.NewTimer(vm.cfg.TaskWall)
	defer t.Stop()
	select {
	case vm.slots <- struct{}{}:
		return func() { <-vm.slots }, nil
	case <-t.C:
		return nil, ErrTooBusy
	case <-vm.quit:
		return nil, ErrClosed
	}
}
