package jsvm

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

const preludeJS = `
(() => {
	const hide = (name, value) => Object.defineProperty(globalThis, name, { value, writable: true, configurable: true })
	hide("__settle", (p, id) => p.then(v => __done(id, true, v), e => __done(id, false, e)))
	const obj = Object.create(Object.prototype)
	Object.defineProperty(obj, "toJSON", { value: function () { return __goJSON(this) } })
	Object.defineProperty(obj, "toString", { value: function () { return __goString(this) } })
	// a pointer to a primitive (*string, *int64, ...) compares and prints as its value, as in goja
	Object.defineProperty(obj, Symbol.toPrimitive, { value: function (hint) { return __goPrim(this, hint) } })
	Object.defineProperty(obj, Symbol.iterator, { value: function* () { for (let i = 0; i < this.length; i++) yield this[i] } })
	// Go slices get the generic Array methods (map, forEach, filter, ...), as in goja
	const arr = Object.create(Array.prototype)
	Object.defineProperty(arr, "toJSON", { value: function () { return __goJSON(this) } })
	Object.defineProperty(arr, "toString", { value: function () { return Array.prototype.join.call(this, ",") } })
	// DOMException, AbortSignal and AbortController as in browsers (the parts that matter without a DOM).
	// AbortSignal.timeout runs on a Go timer that outlives the request (__abortAfter), unlike setTimeout.
	const report = (what, e) => __log(what + " failed: " + ((e && e.stack) || e))
	const codes = { AbortError: 20, TimeoutError: 23 }
	class DOMException extends Error {
		#name
		constructor(message = "", name = "Error") { super(String(message)); this.#name = String(name) }
		get name() { return this.#name }
		get code() { return codes[this.#name] ?? 0 }
	}
	const KEY = Symbol()
	let abort
	class AbortSignal {
		#aborted = false
		#reason = undefined
		#onabort = null
		#listeners = []
		constructor(key) { if (key !== KEY) throw new TypeError("Illegal constructor") }
		get aborted() { return this.#aborted }
		get reason() { return this.#reason }
		get onabort() { return this.#onabort }
		set onabort(fn) { this.#onabort = typeof fn === "function" ? fn : null }
		throwIfAborted() { if (this.#aborted) throw this.#reason }
		addEventListener(type, fn, opts) {
			if (type !== "abort" || fn == null || this.#aborted) return
			if (this.#listeners.some((l) => l.fn === fn)) return
			this.#listeners.push({ fn })
		}
		removeEventListener(type, fn) {
			if (type !== "abort") return
			const i = this.#listeners.findIndex((l) => l.fn === fn)
			if (i >= 0) this.#listeners.splice(i, 1)
		}
		get [Symbol.toStringTag]() { return "AbortSignal" }
		static {
			abort = (s, reason) => {
				if (s.#aborted) return
				s.#aborted = true
				s.#reason = reason === undefined ? new DOMException("This operation was aborted", "AbortError") : reason
				const event = { type: "abort", target: s, currentTarget: s }
				const listeners = s.#listeners
				s.#listeners = []
				const call = (fn) => {
					try { typeof fn === "function" ? fn.call(s, event) : fn.handleEvent(event) } catch (e) { report("abort listener", e) }
				}
				if (s.#onabort) call(s.#onabort)
				for (const l of listeners) call(l.fn)
			}
		}
		static abort(reason) { const s = new AbortSignal(KEY); abort(s, reason); return s }
		static timeout(ms) {
			ms = Number(ms)
			if (!(ms >= 0) || ms > Number.MAX_SAFE_INTEGER) throw new TypeError("AbortSignal.timeout: ms must be a non-negative number")
			const s = new AbortSignal(KEY)
			__abortAfter(() => abort(s, new DOMException("The operation timed out.", "TimeoutError")), ms)
			return s
		}
		static any(signals) {
			const s = new AbortSignal(KEY)
			const list = [...signals]
			for (const x of list) {
				if (!(x instanceof AbortSignal)) throw new TypeError("AbortSignal.any: every item must be an AbortSignal")
				if (x.aborted) { abort(s, x.reason); return s }
			}
			const onAbort = (e) => {
				for (const x of list) x.removeEventListener("abort", onAbort)
				abort(s, e.target.reason)
			}
			for (const x of list) x.addEventListener("abort", onAbort)
			return s
		}
	}
	class AbortController {
		#signal = new AbortSignal(KEY)
		get signal() { return this.#signal }
		abort(reason) { abort(this.#signal, reason) }
		get [Symbol.toStringTag]() { return "AbortController" }
	}
	hide("DOMException", DOMException)
	hide("AbortSignal", AbortSignal)
	hide("AbortController", AbortController)

	const fn = Object.create(Function.prototype)
	// Any Go function or method can run off the loop: await $app.findRecordById.async("posts", id)
	Object.defineProperty(fn, "async", { value: function (...args) { return __goAsync(this, args) } })
	// fn.asyncWith({ signal }, ...args) is fn.async(...args) that the signal can abort: the promise rejects with the
	// signal's reason, and the Go function's context (if it takes one) is cancelled.
	Object.defineProperty(fn, "asyncWith", { value: function (opts, ...args) {
		const signal = opts == null ? undefined : opts.signal
		if (signal == null) return __goAsync(this, args)
		if (!(signal instanceof AbortSignal)) throw new TypeError("asyncWith: signal must be an AbortSignal")
		if (signal.aborted) return Promise.reject(signal.reason)
		const [p, id] = __goAsync(this, args, true)
		return new Promise((resolve, reject) => {
			const onAbort = () => { __goCancel(id, signal.reason); reject(signal.reason) }
			signal.addEventListener("abort", onAbort)
			p.then(
				(v) => { signal.removeEventListener("abort", onAbort); resolve(v) },
				(e) => { signal.removeEventListener("abort", onAbort); reject(e) },
			)
		})
	} })
	hide("console", { log: __log, error: __log, warn: __log, info: __log, debug: __log, trace: __log })
	hide("__protos", [obj, fn, arr])
	hide("queueMicrotask", function queueMicrotask(cb) {
		if (typeof cb !== "function") throw new TypeError("queueMicrotask: the callback must be a function")
		Promise.resolve().then(cb).catch((e) => __log("queueMicrotask callback failed: " + ((e && e.stack) || e)))
	})
	// goja has no performance global; keep QuickJS's but out of the global's keys
	if ("performance" in globalThis) Object.defineProperty(globalThis, "performance", { enumerable: false })
	// Go slices count as arrays, as in goja
	const isArray = Array.isArray
	Object.defineProperty(Array, "isArray", { value: (v) => isArray(v) || __isGoSlice(v), writable: true, configurable: true })
})();
`

// arrayGuardJS caps the length the Array.prototype methods that loop over an array's holes may walk.
//
// QuickJS checks the interrupt handler on every JS call and loop back-edge, never inside a built-in's own C loop.
// Methods such as join, indexOf, slice or forEach walk every index up to length, and on a sparse array
// (new Array(1e9)) or an array-like ({ length: 2 ** 53 - 1 }) the holes cost time but no memory and call no JS:
// neither the memory limit nor TaskBudget stops them, and one call pins a CPU for minutes. Methods that allocate
// per element (fill, toSorted, Array.from) are stopped by the memory limit instead, and ones that call back into JS
// for every index (find*) by the interrupt handler. A dense array longer than MAX can't fit in the VM's memory, so
// the cap only refuses sparse arrays and array-likes.
//
// The originals stay in this closure: JS can replace the wrappers (in its own VM) but never reach the unguarded
// built-ins.
const arrayGuardJS = `
((MAX) => {
	"use strict" // a wrapper called on null or a primitive must not see globalThis or a boxed this
	const P = Array.prototype, isArray = Array.isArray, spreadable = Symbol.isConcatSpreadable
	const len = (o) => {
		if (o == null) return 0
		const n = Number(o.length)
		return n > 0 ? n : 0
	}
	const refuse = (name, n) => {
		throw new RangeError("Array.prototype." + name + ": length " + n + " is over this VM's limit of " + MAX)
	}
	const guard = (name, check) => {
		const orig = P[name]
		if (typeof orig !== "function") return
		const w = { [name](...args) { check(this, args); return orig.apply(this, args) } }[name]
		Object.defineProperty(w, "length", { value: orig.length })
		Object.defineProperty(P, name, { value: w, writable: true, configurable: true, enumerable: false })
	}
	const self = (name) => (o) => { const n = len(o); if (n > MAX) refuse(name, n) }
	for (const name of ["copyWithin", "every", "fill", "filter", "forEach", "includes", "indexOf", "join",
		"lastIndexOf", "map", "reduce", "reduceRight", "reverse", "shift", "slice", "some", "sort", "splice",
		"toLocaleString", "unshift"]) {
		guard(name, self(name))
	}
	// concat walks every spread argument too
	guard("concat", (o, args) => {
		let n = len(o)
		for (const a of args) {
			if (a !== null && typeof a === "object" && (a[spreadable] ?? isArray(a))) n += len(a)
		}
		if (n > MAX) refuse("concat", n)
	})
	// flat walks nested arrays natively: add up every array it will open, in JS (interruptible)
	const nested = (name, o, depth) => {
		let n = 0
		const walk = (a, d) => {
			const l = len(a)
			n += l
			if (n > MAX) refuse(name, n)
			if (d < 1) return
			for (let i = 0; i < l; i++) {
				if (i in a && isArray(a[i])) walk(a[i], d - 1)
			}
		}
		walk(o, depth)
	}
	guard("flat", (o, args) => {
		const d = args[0] === undefined ? 1 : Math.trunc(Number(args[0])) || 0
		nested("flat", o, d)
	})
	// flatMap opens each callback result natively, between JS calls: check each one
	const flatMap = P.flatMap
	if (typeof flatMap === "function") {
		const w = { flatMap(cb, thisArg) {
			const n = len(this)
			if (n > MAX) refuse("flatMap", n)
			if (typeof cb !== "function") return flatMap.call(this, cb, thisArg)
			return flatMap.call(this, function (...a) {
				const r = cb.apply(thisArg, a)
				if (isArray(r) && len(r) > MAX) refuse("flatMap", len(r))
				return r
			})
		} }.flatMap
		Object.defineProperty(w, "length", { value: flatMap.length })
		Object.defineProperty(P, "flatMap", { value: w, writable: true, configurable: true, enumerable: false })
	}
})
`

// maxArrayWalk is the longest array the guarded Array methods may walk: as many elements as a dense array could
// hold in the VM's memory (16 bytes per value), at least 1M.
func (vm *VM) maxArrayWalk() int64 {
	return max(vm.cfg.MemoryLimit/16, 1<<20)
}

func (vm *VM) installPrelude() error {
	hidden := func(name string, fn nativeFn) {
		h, _ := vm.newHandle(entry{kind: kindNative, native: fn})
		vm.setHidden(name, C.avm_new_func(vm.a, uint32(h), 0))
	}
	hidden("__done", nativeDone)
	hidden("__goAsync", nativeAsync)
	hidden("__goCancel", nativeCancel)
	hidden("__abortAfter", func(vm *VM, this JSValue, args []JSValue, ctor bool) JSValue {
		return vm.setTimerOpts("AbortSignal.timeout", args, false, true)
	})
	hidden("__goJSON", nativeJSON)
	hidden("__goString", nativeString)
	hidden("__goPrim", func(vm *VM, this JSValue, args []JSValue, ctor bool) JSValue {
		if h := C.avm_handle_of(vm.a, args[0]); h != 0 {
			d := deref(vm.handles[h].v)
			switch d.Kind() {
			case reflect.Bool, reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
				reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
				jv, err := vm.toJS(d)
				if err != nil {
					return vm.throw(err)
				}
				return jv
			}
		}
		return nativeString(vm, this, args, ctor)
	})
	hidden("__isGoSlice", func(vm *VM, this JSValue, args []JSValue, ctor bool) JSValue {
		if len(args) > 0 {
			if h := C.avm_handle_of(vm.a, args[0]); h != 0 && vm.handles[h].kind == kindObj {
				if d := deref(vm.handles[h].v); d.Kind() == reflect.Slice || d.Kind() == reflect.Array {
					return C.avm_bool(1)
				}
			}
		}
		return C.avm_bool(0)
	})
	hidden("__log", func(vm *VM, this JSValue, args []JSValue, ctor bool) JSValue {
		parts := make([]string, len(args))
		for i, a := range args {
			parts[i] = vm.goString(a)
		}
		vm.Log(strings.Join(parts, " "))
		return C.avm_undefined()
	})
	if err := vm.evalHere(preludeJS, "<prelude>"); err != nil {
		return err
	}
	if err := vm.evalHere(arrayGuardJS+fmt.Sprintf("(%d);", vm.maxArrayWalk()), "<prelude>"); err != nil {
		return err
	}
	g := C.JS_GetGlobalObject(vm.ctx)
	cs := C.CString("__protos")
	arr := C.JS_GetPropertyStr(vm.ctx, g, cs)
	C.free(cs)
	vm.free_(g)
	objP := C.avm_array_get(vm.ctx, arr, 0)
	fnP := C.avm_array_get(vm.ctx, arr, 1)
	vm.sliceProto = C.avm_array_get(vm.ctx, arr, 2)
	vm.free_(arr)
	vm.objProto = C.avm_dup(vm.ctx, objP) // kept for setCtor: each constructor's prototype inherits it
	C.avm_set_protos(vm.a, objP, fnP)     // takes ownership
	vm.installTimers()
	return nil
}

func numArg(vm *VM, v JSValue) uint64 {
	var f float64
	C.JS_ToFloat64(vm.ctx, &f, v)
	return uint64(f)
}

func nativeDone(vm *VM, this JSValue, args []JSValue, ctor bool) JSValue {
	id := numArg(vm, args[0])
	ch, ok := vm.settles[id]
	if !ok {
		return C.avm_undefined()
	}
	resT := vm.settleTypes[id]
	delete(vm.settles, id)
	delete(vm.settleTypes, id)
	var s settled
	if C.JS_ToBool(vm.ctx, args[1]) != 0 {
		s = vm.settleValue(C.avm_dup(vm.ctx, args[2]), resT)
	} else {
		C.JS_Throw(vm.ctx, C.avm_dup(vm.ctx, args[2]))
		s = settled{err: vm.exception()}
	}
	ch <- s
	return C.avm_undefined()
}

// nativeAsync runs a Go function on its own goroutine and returns a promise. Arguments are converted on the loop;
// the result is converted back on the loop when it is done.
//
// A function whose first parameter is a context.Context gets one of its own (JS passes the other arguments, or a Go
// context as the parent): cancelled when the call this code runs for is given up, when the VM closes, and when JS
// cancels it with __goCancel. With a third argument true (asyncWith with a signal), it returns [promise, id] for
// __goCancel instead of the promise.
func nativeAsync(vm *VM, this JSValue, args []JSValue, ctor bool) JSValue {
	h := C.avm_handle_of(vm.a, args[0])
	if h == 0 || vm.handles[h].kind != kindFn {
		return C.throwType(vm.ctx, "async: not a Go function")
	}
	o := vm.cur
	if err := o.cause(); err != nil {
		return vm.throw(err) // the call this code runs for was given up: start nothing new
	}
	if vm.asyncInFlight >= vm.cfg.MaxAsync {
		return vm.throw(fmt.Errorf("too many async Go calls in flight (%d)", vm.cfg.MaxAsync))
	}
	abortable := len(args) > 2 && C.JS_ToBool(vm.ctx, args[2]) != 0
	fn := vm.handles[h].v
	ft := fn.Type()
	n := int(C.avm_length(vm.ctx, args[1]))
	jsArgs := make([]JSValue, n)
	for i := range jsArgs {
		jsArgs[i] = C.avm_array_get(vm.ctx, args[1], uint32(i))
	}
	withCtx := ft.NumIn() > 0 && ft.In(0) == contextType
	var ownerCtx, parent context.Context
	var in []reflect.Value
	var err error
	if withCtx {
		ownerCtx = vm.ownerCtx(o)
		parent = ownerCtx
		rest := jsArgs
		if !vm.suppliesCtx(ft, jsArgs) {
			parent = vm.handles[C.avm_handle_of(vm.a, jsArgs[0])].v.Interface().(context.Context)
			rest = jsArgs[1:]
		}
		in, err = vm.goArgsFrom(ft, 1, rest, reflect.Value{}) // in[0] is set below
	} else {
		in, err = vm.goArgsFrom(ft, 0, jsArgs)
	}
	for _, a := range jsArgs {
		vm.free_(a)
	}
	if err != nil {
		return vm.throw(err)
	}
	cancel := func(error) {}
	var unlink []func() bool
	if withCtx {
		ctx, c := context.WithCancelCause(parent)
		cancel = c
		if parent != vm.base {
			unlink = append(unlink, context.AfterFunc(vm.base, func() { c(context.Cause(vm.base)) }))
		}
		if parent != ownerCtx {
			unlink = append(unlink, context.AfterFunc(ownerCtx, func() { c(context.Cause(ownerCtx)) }))
		}
		in[0] = reflect.ValueOf(ctx)
	}
	var id uint64
	if abortable {
		vm.asyncSeq++
		id = vm.asyncSeq
		vm.asyncCancels[id] = cancel
	}
	funcs := make([]JSValue, 2)
	p := C.avm_new_promise(vm.ctx, &funcs[0])
	resolve, reject := funcs[0], funcs[1]
	vm.asyncInFlight++
	go func() {
		var out []reflect.Value
		var perr error
		func() {
			defer func() {
				if r := recover(); r != nil {
					perr = fmt.Errorf("%v", r)
				}
			}()
			out = fn.Call(in)
		}()
		for _, u := range unlink {
			u()
		}
		cancel(nil) // done: frees the context
		vm.post(func() {
			vm.asyncInFlight--
			if abortable {
				delete(vm.asyncCancels, id)
			}
			defer vm.free_(resolve)
			defer vm.free_(reject)
			if o.cause() != nil {
				return // given up: the promise never settles, so the code after the await never runs
			}
			vm.cur = o // the continuation (run by runTask's runJobs) and the async calls it starts belong to o
			var v JSValue
			if perr != nil {
				v = vm.throw(perr)
			} else {
				v = vm.goResults(out)
			}
			target := resolve
			if C.avm_is_exception(v) != 0 {
				v = C.JS_GetException(vm.ctx)
				target = reject
			}
			r := C.avm_call(vm.ctx, target, C.avm_undefined(), 1, &v)
			vm.free_(v)
			vm.free_(r)
		})
	}()
	if !abortable {
		return p
	}
	arr := C.avm_new_array(vm.ctx)
	C.avm_array_push(vm.ctx, arr, 0, p)
	C.avm_array_push(vm.ctx, arr, 1, C.avm_int(vm.ctx, int64(id)))
	return arr
}

// nativeCancel cancels the context of the async Go call with id args[0] (from nativeAsync), for an AbortSignal
// whose reason is args[1]. A call that has finished is left alone.
func nativeCancel(vm *VM, this JSValue, args []JSValue, ctor bool) JSValue {
	id := numArg(vm, args[0])
	if c := vm.asyncCancels[id]; c != nil {
		delete(vm.asyncCancels, id)
		reason := "aborted"
		if len(args) > 1 {
			reason = vm.goString(args[1])
		}
		c(fmt.Errorf("%w: %s", ErrAborted, reason))
	}
	return C.avm_undefined()
}

func nativeJSON(vm *VM, this JSValue, args []JSValue, ctor bool) JSValue {
	h := C.avm_handle_of(vm.a, args[0])
	if h == 0 {
		return C.avm_undefined()
	}
	b, err := json.Marshal(vm.handles[h].v.Interface())
	if err != nil {
		return vm.throw(err)
	}
	cs := C.CString(string(b))
	defer C.free(cs)
	return C.avm_parse_json(vm.ctx, cs, uint64(len(b)))
}

func nativeString(vm *VM, this JSValue, args []JSValue, ctor bool) JSValue {
	h := C.avm_handle_of(vm.a, args[0])
	if h == 0 {
		return vm.str("[object]")
	}
	v := vm.handles[h].v.Interface()
	if s, ok := v.(fmt.Stringer); ok {
		return vm.str(s.String())
	}
	if e, ok := v.(error); ok {
		return vm.str(e.Error())
	}
	return vm.str(fmt.Sprintf("[Go %T]", v))
}
