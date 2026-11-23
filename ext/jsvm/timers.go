package jsvm

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// Timers: setTimeout, setInterval, clearTimeout, clearInterval. A timer is a Go time.Timer that posts its callback to
// the loop, where it runs as a task of its own (with a fresh TaskBudget and TaskWall). It belongs to the call that set
// it (vm.cur), like an async Go call: it is dropped when that call is given up (the client went away, AwaitLimit) and
// when the request the call served got its response. Timers set outside a call (module scope) belong to the VM and
// run until cleared or the VM closes.

// maxTimerDelay is the longest delay, as in browsers and Node: a longer (or negative, or NaN) one runs as soon as it
// can.
const maxTimerDelay = math.MaxInt32 * time.Millisecond

type jsTimer struct {
	fn    JSValue
	args  []JSValue
	every time.Duration // 0 for setTimeout
	owner *owner
	t     *time.Timer
}

// timerBytes is what a pending timer is charged against MemoryLimit, beside what its callback holds in the JS heap.
const timerBytes = handleBase

func (vm *VM) installTimers() {
	native := func(name string, fn nativeFn) { // like console: globals, but not among the global object's keys
		h, _ := vm.newHandle(entry{kind: kindNative, native: fn})
		vm.setHidden(name, C.avm_new_func(vm.a, uint32(h), 0))
	}
	native("setTimeout", func(vm *VM, this JSValue, args []JSValue, ctor bool) JSValue {
		return vm.setTimer("setTimeout", args, false)
	})
	native("setInterval", func(vm *VM, this JSValue, args []JSValue, ctor bool) JSValue {
		return vm.setTimer("setInterval", args, true)
	})
	clear := func(vm *VM, this JSValue, args []JSValue, ctor bool) JSValue {
		if len(args) > 0 && C.avm_tag(args[0]) != C.JS_TAG_UNDEFINED && C.avm_tag(args[0]) != C.JS_TAG_NULL {
			var f float64
			C.JS_ToFloat64(vm.ctx, &f, args[0])
			if t := vm.timers[int64(f)]; t != nil {
				t.t.Stop()
				vm.dropTimer(int64(f))
			}
		}
		return C.avm_undefined()
	}
	native("clearTimeout", clear)
	native("clearInterval", clear)
}

func (vm *VM) setTimer(name string, args []JSValue, repeat bool) JSValue {
	if len(args) == 0 || C.avm_is_function(vm.ctx, args[0]) == 0 {
		return C.throwType(vm.ctx, name+": the callback must be a function")
	}
	o := vm.cur
	if err := o.cause(); err != nil {
		return vm.throw(err) // the call this code runs for was given up: start nothing new
	}
	if o != nil && o.ended.Load() {
		return vm.throw(fmt.Errorf("%s: the request this code served has already been answered", name))
	}
	if len(vm.timers) >= vm.cfg.MaxTimers {
		return vm.throw(fmt.Errorf("%s: too many timers pending (%d)", name, vm.cfg.MaxTimers))
	}
	if vm.goBytes+timerBytes > vm.cfg.MemoryLimit {
		return vm.throw(fmt.Errorf("out of memory: Go objects held by JS would exceed %d bytes", vm.cfg.MemoryLimit))
	}
	d := time.Duration(0)
	if len(args) > 1 {
		var ms float64
		C.JS_ToFloat64(vm.ctx, &ms, args[1])
		if ms > 0 && !math.IsNaN(ms) {
			d = time.Duration(ms * float64(time.Millisecond))
		}
	}
	if d > maxTimerDelay {
		d = 0
	}
	d = max(d, time.Millisecond)

	t := &jsTimer{fn: C.avm_dup(vm.ctx, args[0]), owner: o}
	for _, a := range args[min(2, len(args)):] {
		t.args = append(t.args, C.avm_dup(vm.ctx, a))
	}
	if repeat {
		t.every = d
	}
	vm.timerSeq++
	id := vm.timerSeq
	vm.timers[id] = t
	vm.charge(timerBytes)
	t.t = time.AfterFunc(d, func() { vm.post(func() { vm.fireTimer(id) }) })
	return C.avm_int(vm.ctx, id)
}

// fireTimer runs on the loop when timer id is due.
func (vm *VM) fireTimer(id int64) {
	t := vm.timers[id]
	if t == nil {
		return // cleared after it fired
	}
	if !t.owner.live() {
		vm.dropTimer(id)
		return
	}
	if t.every == 0 {
		delete(vm.timers, id) // clearTimeout(id) in its own callback is a no-op; freed below
	}
	vm.cur = t.owner // the callback and what it starts belong to the call that set the timer (runTask restores cur)
	// hold the function and its arguments for the call: clearTimeout/clearInterval inside it frees the timer's own
	fn := C.avm_dup(vm.ctx, t.fn)
	args := make([]JSValue, len(t.args))
	for i, a := range t.args {
		args[i] = C.avm_dup(vm.ctx, a)
	}
	var argp *JSValue
	if len(args) > 0 {
		argp = &args[0]
	}
	ret := C.avm_call(vm.ctx, fn, C.avm_undefined(), int32(len(args)), argp)
	vm.free_(fn)
	for _, a := range args {
		vm.free_(a)
	}
	if C.avm_is_exception(ret) != 0 {
		vm.Log(fmt.Sprintf("timer callback failed: %v", vm.exception()))
	} else {
		vm.free_(ret)
	}
	if t.every == 0 {
		vm.freeTimer(t)
		return
	}
	if vm.timers[id] == t { // still set: the next run is armed once this one is done, so runs never pile up
		t.t.Reset(t.every)
	}
}

func (vm *VM) dropTimer(id int64) {
	if t := vm.timers[id]; t != nil {
		delete(vm.timers, id)
		vm.freeTimer(t)
	}
}

func (vm *VM) freeTimer(t *jsTimer) {
	vm.free_(t.fn)
	for _, a := range t.args {
		vm.free_(a)
	}
	t.fn, t.args = C.avm_undefined(), nil
	vm.charge(-timerBytes)
}

// clearTimers stops and frees the timers match picks (on the loop).
func (vm *VM) clearTimers(match func(*jsTimer) bool) {
	for id, t := range vm.timers {
		if match(t) {
			t.t.Stop()
			vm.dropTimer(id)
		}
	}
}

// endRequest marks a call that served a request as answered: its pending timers are dropped (a late callback would
// write to a finished response). Called off the loop when the call returns; it waits for the loop, so a callback that
// was running when the request ended has finished before the request's goroutine moves on.
func (vm *VM) endRequest(o *owner) {
	o.ended.Store(true)
	clear := func() {
		if vm.a != nil {
			vm.clearTimers(func(t *jsTimer) bool { return t.owner == o })
		}
	}
	if err := vm.Do(clear); errors.Is(err, ErrBusy) {
		vm.post(clear) // a full queue: the timers still never fire (ended is set), they're just freed later
	}
}
