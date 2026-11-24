package jsvm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"pocketone.dev/ext/extkit/js"
)

// ---- reflection cache: JS names for a Go type's fields and methods ----

type members struct {
	methods map[string]int   // js name -> method index on the type
	fields  map[string][]int // js name -> field index path (on the struct type)
	keys    []string         // enumerable: field names
}

var memberCache sync.Map // memberKey -> *members

type memberKey struct {
	t    reflect.Type
	hide reflect.Type // the VM's js.Hider type (rules are per type), nil for none
}

func membersOf(t reflect.Type) *members { return membersFor(t, nil) }

func (vm *VM) membersOf(t reflect.Type) *members { return membersFor(t, vm.Hider) }

func membersFor(t reflect.Type, hider js.Hider) *members {
	key := memberKey{t: t}
	if hider != nil {
		key.hide = reflect.TypeOf(hider)
	}
	if m, ok := memberCache.Load(key); ok {
		return m.(*members)
	}
	m := &members{methods: map[string]int{}, fields: map[string][]int{}}
	for i := 0; i < t.NumMethod(); i++ {
		meth := t.Method(i)
		if meth.IsExported() && (hider == nil || !hider.HideMethod(t, meth)) {
			m.methods[jsName(meth.Name)] = i
		}
	}
	st := t
	for st.Kind() == reflect.Pointer {
		st = st.Elem()
	}
	if st.Kind() == reflect.Struct {
		for _, f := range reflect.VisibleFields(st) {
			if !f.IsExported() || f.Anonymous || (hider != nil && hider.HideField(st, f)) {
				continue
			}
			n := jsName(f.Name)
			if _, dup := m.fields[n]; dup {
				continue
			}
			m.fields[n] = f.Index
			m.keys = append(m.keys, n)
		}
	}
	memberCache.Store(key, m)
	return m
}

// jsName is plugins/jsvm's convertGoToJSName (FindRecordById -> findRecordById, JSON -> json).
func jsName(name string) string {
	if name == "OAuth2" {
		return "oauth2"
	}
	upper := 0
	for _, c := range name {
		if c != '_' && !unicode.IsUpper(c) && !unicode.IsDigit(c) {
			break
		}
		upper++
	}
	if upper == len(name) {
		return strings.ToLower(name)
	}
	if upper > 1 {
		return strings.ToLower(name[0:upper-1]) + name[upper-1:]
	}
	if upper == 1 {
		return strings.ToLower(name[0:1]) + name[1:]
	}
	return name
}

// ---- Go -> JS ----

// nativeBox carries a native JS function through a Go value (e.g. a namespace map member).
type nativeBox struct{ fn nativeFn }

var nativeBoxType = reflect.TypeOf(nativeBox{})

var errorType = reflect.TypeOf((*error)(nil)).Elem()

// toJS converts a Go value: primitives are copied, everything else becomes a live handle.
func (vm *VM) toJS(v reflect.Value) (JSValue, error) { return vm.toJSWith(v, false) }

// toJSDeep is for fresh results of Go calls: the handle is charged for everything reachable from the value.
func (vm *VM) toJSDeep(v reflect.Value) (JSValue, error) { return vm.toJSWith(v, true) }

func (vm *VM) toJSWith(v reflect.Value, deep bool) (JSValue, error) {
	for v.IsValid() && v.Kind() == reflect.Interface {
		if v.IsNil() {
			return C.avm_null(), nil
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return C.avm_null(), nil
	}
	if jv, ok := v.Interface().(*jsValue); ok && jv != nil && jv.vm == vm {
		// a JS value held by Go passes back as itself
		return C.avm_dup(vm.ctx, jv.v), nil
	}
	if v.Type() == nativeBoxType {
		h, err := vm.newHandle(entry{kind: kindNative, native: v.Interface().(nativeBox).fn})
		if err != nil {
			return C.avm_undefined(), err
		}
		return C.avm_new_func(vm.a, uint32(h), 0), nil
	}
	switch v.Kind() {
	case reflect.Bool:
		if v.Bool() {
			return C.avm_bool(1), nil
		}
		return C.avm_bool(0), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return C.avm_int(vm.ctx, int64(v.Int())), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return C.avm_float(float64(v.Uint())), nil
	case reflect.Float32, reflect.Float64:
		return C.avm_float(float64(v.Float())), nil
	case reflect.String:
		return vm.str(v.String()), nil
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func:
		if v.IsNil() {
			return C.avm_null(), nil
		}

	case reflect.Struct:
		// keep it addressable so pointer methods work and writes stick
		p := reflect.New(v.Type())
		p.Elem().Set(v)
		v = p
	}
	if v.Kind() == reflect.Func {
		if f, ok := v.Interface().(js.NativeFunc); ok {
			return vm.jsFromAny(f)
		}
		h, err := vm.newHandle(entry{v: v, kind: kindFn, bytes: handleBase})
		if err != nil {
			return C.avm_undefined(), err
		}
		return C.avm_new_func(vm.a, uint32(h), 0), nil
	}
	bytes := estimate(v)
	if deep {
		if d := handleBase + deepSize(v, vm.cfg.MemoryLimit-vm.goBytes, vm.cfg.Shared); d > bytes {
			bytes = d
		}
	}
	h, err := vm.newHandle(entry{v: v, kind: kindObj, bytes: bytes})
	if err != nil {
		return C.avm_undefined(), err
	}
	o := C.avm_new_object(vm.a, uint32(h))
	if d := deref(v); (d.Kind() == reflect.Slice || d.Kind() == reflect.Array) && C.avm_tag(vm.sliceProto) == C.JS_TAG_OBJECT {
		C.avm_set_proto(vm.ctx, o, vm.sliceProto)
	}
	return o, nil
}

// Sizer lets a Go type say how much memory a JS handle to it keeps alive.
type Sizer interface{ JSSize() int64 }

var sizers sync.Map // reflect.Type -> func(reflect.Value) int64

// RegisterSizer sets the estimate for values of t held by JS.
func RegisterSizer(t reflect.Type, f func(reflect.Value) int64) { sizers.Store(t, f) }

func estimate(v reflect.Value) int64 {
	const base = handleBase
	if f, ok := sizers.Load(v.Type()); ok {
		return base + f.(func(reflect.Value) int64)(v)
	}
	if s, ok := v.Interface().(Sizer); ok {
		return base + s.JSSize()
	}
	d := v
	for d.Kind() == reflect.Pointer && !d.IsNil() {
		d = d.Elem()
	}
	switch d.Kind() {
	case reflect.Slice, reflect.Array:
		n := int64(d.Len())
		per := int64(d.Type().Elem().Size())
		if et := d.Type().Elem(); n > 0 {
			if f, ok := sizers.Load(et); ok {
				// sample the first element for the per-element estimate
				per += f.(func(reflect.Value) int64)(d.Index(0))
			}
		}
		return base + n*per
	case reflect.Map:
		return base + int64(d.Len())*64
	case reflect.Struct:
		return base + int64(d.Type().Size())
	}
	return base
}

// ---- JS -> Go ----

func (vm *VM) fromJS(v JSValue, t reflect.Type) (reflect.Value, error) {
	if t == nil {
		t = anyType
	}
	if h := C.avm_handle_of(vm.a, v); h != 0 {
		e := vm.handles[h]
		if e.kind == kindNative {
			// a native JS function stored into a Go slot (a namespace member): it stays the same function
			nb := reflect.ValueOf(nativeBox{fn: e.native})
			if nb.Type().AssignableTo(t) {
				return nb, nil
			}
			return reflect.Value{}, fmt.Errorf("cannot pass a built-in function to Go as %s", t)
		}
		gv := e.v
		switch {
		case gv.Type().AssignableTo(t):
			return gv, nil
		case gv.Kind() == reflect.Pointer && gv.Elem().Type().AssignableTo(t):
			return gv.Elem(), nil
		case gv.Type().ConvertibleTo(t):
			return gv.Convert(t), nil
		case t.Kind() == reflect.Pointer && t.Elem().Kind() == reflect.Struct:
			// as goja: a fresh value of the wanted type, filled from the source's fields by name (an embedded field
			// gets the whole source when it fits, e.g. a *dbx.DB built around the app's dbx.Builder)
			return vm.exportStruct(gv, t.Elem()).Addr(), nil
		}
		return reflect.Value{}, fmt.Errorf("cannot use %s as %s", gv.Type(), t)
	}
	switch tag := C.avm_tag(v); {
	case t.Kind() == reflect.Pointer && t.Elem().Kind() != reflect.Struct && tag != C.JS_TAG_UNDEFINED &&
		tag != C.JS_TAG_NULL && tag != C.JS_TAG_OBJECT:
		// a primitive where Go wants a pointer to one (a *string collection rule): allocate it, as goja does
		ev, err := vm.fromJS(v, t.Elem())
		if err != nil {
			return reflect.Value{}, err
		}
		p := reflect.New(t.Elem())
		p.Elem().Set(ev)
		return p, nil
	}
	switch C.avm_tag(v) {
	case C.JS_TAG_UNDEFINED, C.JS_TAG_NULL:
		return reflect.Zero(t), nil
	case C.JS_TAG_BOOL:
		b := C.JS_ToBool(vm.ctx, v) != 0
		return convertPrim(reflect.ValueOf(b), t)
	case C.JS_TAG_INT:
		var i int64
		C.JS_ToInt64(vm.ctx, &i, v)
		return convertPrim(reflect.ValueOf(int64(i)), t)
	case C.JS_TAG_FLOAT64:
		var f float64
		C.JS_ToFloat64(vm.ctx, &f, v)
		if t.Kind() == reflect.Interface && float64(f) == float64(int64(f)) && !math.Signbit(float64(f)) {
			return convertPrim(reflect.ValueOf(int64(f)), t)
		}
		return convertPrim(reflect.ValueOf(float64(f)), t)
	case C.JS_TAG_STRING, C.JS_TAG_STRING_ROPE:
		s := vm.goString(v)
		if t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
			return reflect.ValueOf([]byte(s)).Convert(t), nil
		}
		return convertPrim(reflect.ValueOf(s), t)
	case C.JS_TAG_OBJECT:
		if t.Kind() == reflect.Func && C.avm_is_function(vm.ctx, v) != 0 {
			return vm.makeFunc(v, t), nil
		}
		if C.avm_is_array(vm.ctx, v) != 0 {
			return vm.arrayToGo(v, t)
		}
		if t.Kind() == reflect.Interface || t.Kind() == reflect.Map {
			return vm.objectToGo(v, t)
		}
		// a struct from an object holding Go values ({ name, fs: $os.dirFS(...) } for e.uiExtensions): field by field,
		// so the Go values stay themselves instead of turning into JSON strings
		if t.Kind() == reflect.Struct && vm.holdsGoValue(v) {
			return vm.objectToStruct(v, t)
		}
		// structs and anything else: through JSON, like goja's export + PocketBase's own unmarshal
		js := C.JS_JSONStringify(vm.ctx, v, C.avm_undefined(), C.avm_undefined())
		if C.avm_is_exception(js) != 0 {
			return reflect.Value{}, vm.exception()
		}
		s := vm.goString(js)
		vm.free_(js)
		p := reflect.New(t)
		if err := json.Unmarshal([]byte(s), p.Interface()); err != nil {
			return reflect.Value{}, err
		}
		return p.Elem(), nil
	}
	return reflect.Zero(t), nil
}

var anyType = reflect.TypeOf((*any)(nil)).Elem()

func convertPrim(v reflect.Value, t reflect.Type) (reflect.Value, error) {
	if v.Type().AssignableTo(t) {
		r := reflect.New(t).Elem()
		r.Set(v)
		return r, nil
	}
	if v.Type().ConvertibleTo(t) && t.Kind() != reflect.String {
		return v.Convert(t), nil
	}
	if t.Kind() == reflect.String {
		return reflect.ValueOf(fmt.Sprint(v.Interface())).Convert(t), nil
	}
	return reflect.Value{}, fmt.Errorf("cannot use %s as %s", v.Type(), t)
}

func (vm *VM) arrayToGo(v JSValue, t reflect.Type) (reflect.Value, error) {
	n := int(C.avm_length(vm.ctx, v))
	if n < 0 {
		return reflect.Value{}, errors.New("array length can't be read")
	}
	// a sparse array's length costs JS nothing, but the Go slice is allocated in full (outside the memory limit)
	// and filled in a loop the interrupt handler can't stop
	if limit := vm.maxArrayWalk(); int64(n) > limit {
		return reflect.Value{}, fmt.Errorf("array length %d is over this VM's limit of %d", n, limit)
	}
	et := anyType
	if t.Kind() == reflect.Slice {
		et = t.Elem()
	}
	out := reflect.MakeSlice(reflect.SliceOf(et), n, n)
	for i := 0; i < n; i++ {
		ev := C.avm_array_get(vm.ctx, v, uint32(i))
		gv, err := vm.fromJS(ev, et)
		vm.free_(ev)
		if err != nil {
			return reflect.Value{}, err
		}
		out.Index(i).Set(gv)
	}
	if t.Kind() == reflect.Interface {
		r := reflect.New(t).Elem()
		r.Set(out)
		return r, nil
	}
	return out, nil
}

func (vm *VM) objectToGo(v JSValue, t reflect.Type) (reflect.Value, error) {
	mt := reflect.TypeOf(map[string]any{})
	if t.Kind() == reflect.Map {
		mt = t
	}
	keys := C.avm_object_keys(vm.ctx, v)
	if C.avm_is_exception(keys) != 0 {
		return reflect.Value{}, vm.exception()
	}
	defer vm.free_(keys)
	n := int(C.avm_length(vm.ctx, keys))
	out := reflect.MakeMapWithSize(mt, n)
	for i := 0; i < n; i++ {
		kv := C.avm_array_get(vm.ctx, keys, uint32(i))
		k := vm.goString(kv)
		ck := C.CString(k)
		ev := C.avm_get_str(vm.ctx, v, ck)
		C.free(ck)
		vm.free_(kv)
		gv, err := vm.fromJS(ev, mt.Elem())
		vm.free_(ev)
		if err != nil {
			return reflect.Value{}, err
		}
		out.SetMapIndex(reflect.ValueOf(k).Convert(mt.Key()), gv)
	}
	if t.Kind() == reflect.Interface {
		r := reflect.New(t).Elem()
		r.Set(out)
		return r, nil
	}
	return out, nil
}

// holdsGoValue reports whether one of a plain object's own property values is a Go value.
func (vm *VM) holdsGoValue(v JSValue) bool {
	keys := C.avm_object_keys(vm.ctx, v)
	if C.avm_is_exception(keys) != 0 {
		_ = vm.exception() // clears it
		return false
	}
	defer vm.free_(keys)
	n := int(C.avm_length(vm.ctx, keys))
	for i := 0; i < n; i++ {
		kv := C.avm_array_get(vm.ctx, keys, uint32(i))
		ck := C.CString(vm.goString(kv))
		ev := C.avm_get_str(vm.ctx, v, ck)
		C.free(ck)
		vm.free_(kv)
		h := C.avm_handle_of(vm.a, ev)
		vm.free_(ev)
		if h != 0 {
			return true
		}
	}
	return false
}

// objectToStruct makes a t from a plain object, each exported field from the property of its JS name (as JS sees
// the struct's fields), converted like any other value; properties the struct doesn't have are ignored.
func (vm *VM) objectToStruct(v JSValue, t reflect.Type) (reflect.Value, error) {
	dst := reflect.New(t).Elem()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() || f.Anonymous {
			continue
		}
		ck := C.CString(jsName(f.Name))
		ev := C.avm_get_str(vm.ctx, v, ck)
		C.free(ck)
		if tag := C.avm_tag(ev); tag == C.JS_TAG_UNDEFINED {
			vm.free_(ev)
			continue
		}
		gv, err := vm.fromJS(ev, f.Type)
		vm.free_(ev)
		if err != nil {
			return reflect.Value{}, fmt.Errorf("%s: %w", jsName(f.Name), err)
		}
		dst.Field(i).Set(gv)
	}
	return dst, nil
}

// exportStruct makes a new st and fills its exported fields from src, as goja exports an object to a struct.
func (vm *VM) exportStruct(src reflect.Value, st reflect.Type) reflect.Value {
	dst := reflect.New(st).Elem()
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Anonymous {
			if src.Type().AssignableTo(f.Type) {
				dst.Field(i).Set(src)
			}
			continue
		}
		v, ok := vm.lookup(src, jsName(f.Name))
		if !ok || !v.IsValid() {
			continue
		}
		switch {
		case v.Type().AssignableTo(f.Type):
			dst.Field(i).Set(v)
		case v.Type().ConvertibleTo(f.Type):
			dst.Field(i).Set(v.Convert(f.Type))
		}
	}
	return dst
}

// isPlainObject: a JS object that isn't a Go value, an array or a function.
func (vm *VM) isPlainObject(v JSValue) bool {
	return C.avm_tag(v) == C.JS_TAG_OBJECT && C.avm_handle_of(vm.a, v) == 0 && C.avm_is_array(vm.ctx, v) == 0 &&
		C.avm_is_function(vm.ctx, v) == 0
}

// ---- property access from JS ----

func deref(v reflect.Value) reflect.Value {
	for (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && !v.IsNil() {
		v = v.Elem()
	}
	return v
}

// lookup finds name on a Go value. ok=false means "not ours" (JS then tries the prototype).
func (vm *VM) lookup(v reflect.Value, name string) (reflect.Value, bool) {
	if !v.IsValid() {
		return reflect.Value{}, false
	}
	m := vm.membersOf(v.Type())
	if i, ok := m.methods[name]; ok {
		return v.Method(i), true
	}
	d := deref(v)
	switch d.Kind() {
	case reflect.Struct:
		if idx, ok := vm.membersOf(d.Type()).fields[name]; ok {
			f, err := d.FieldByIndexErr(idx)
			if err != nil {
				return reflect.Value{}, true // nil embedded pointer
			}
			if (f.Kind() == reflect.Struct || f.Kind() == reflect.Slice) && f.CanAddr() {
				return f.Addr(), true
			}
			return f, true
		}
		if d.CanAddr() {
			pm := vm.membersOf(d.Addr().Type())
			if i, ok := pm.methods[name]; ok {
				return d.Addr().Method(i), true
			}
		}
	case reflect.Map:
		if d.Type().Key().Kind() == reflect.String {
			mv := d.MapIndex(reflect.ValueOf(name).Convert(d.Type().Key()))
			if mv.IsValid() {
				return mv, true
			}
		}
	case reflect.Slice, reflect.Array:
		if name == "length" {
			return reflect.ValueOf(d.Len()), true
		}
		if i, err := strconv.Atoi(name); err == nil {
			if i < 0 || i >= d.Len() {
				return reflect.Value{}, true
			}
			e := d.Index(i)
			if e.Kind() == reflect.Struct && e.CanAddr() {
				return e.Addr(), true
			}
			return e, true
		}
	}
	return reflect.Value{}, false
}

// avmGoGet reads property key of the Go object behind handle h into out: 1 found, 0 not found, -1 thrown.
func avmGoGet(ctx *JSContext, h uint32, key string, out *JSValue) (rc int32) {
	vm := vmOf(ctx)
	defer func() {
		if r := recover(); r != nil {
			vm.throw(fmt.Errorf("%v", r))
			rc = -1
		}
	}()
	recv := vm.handles[h].v
	v, ok := vm.lookup(recv, key)
	if !ok {
		return 0
	}
	if v.Kind() == reflect.Func && vm.cfg.Blocking != nil && vm.cfg.Blocking(recv.Type()) {
		hh, err := vm.newHandle(entry{v: v, kind: kindFn, bytes: handleBase, blocking: true})
		if err != nil {
			vm.throw(err)
			return -1
		}
		*out = C.avm_new_func(vm.a, uint32(hh), 0)
		return 1
	}
	jv, err := vm.toJS(v)
	if err != nil {
		vm.throw(err)
		return -1
	}
	*out = jv
	return 1
}

func avmGoHas(ctx *JSContext, h uint32, key string) int32 {
	vm := vmOf(ctx)
	if _, ok := vm.lookup(vm.handles[h].v, key); ok {
		return 1
	}
	return 0
}

func avmGoSet(ctx *JSContext, h uint32, key string, val JSValue) (rc int32) {
	vm := vmOf(ctx)
	defer func() {
		if r := recover(); r != nil {
			vm.throw(fmt.Errorf("%v", r))
			rc = -1
		}
	}()
	d := deref(vm.handles[h].v)
	switch d.Kind() {
	case reflect.Struct:
		if idx, ok := vm.membersOf(d.Type()).fields[key]; ok {
			f := d.FieldByIndex(idx)
			if !f.CanSet() {
				vm.throw(fmt.Errorf("field %s is read-only", key))
				return -1
			}
			// a plain object assigned to a struct field merges into it, as goja does (o.otp = {enabled: false}
			// keeps OTP's other settings)
			if target := f; vm.isPlainObject(val) && (target.Kind() == reflect.Struct || (target.Kind() == reflect.Pointer &&
				target.Type().Elem().Kind() == reflect.Struct)) {
				if target.Kind() == reflect.Pointer {
					if target.IsNil() {
						target.Set(reflect.New(target.Type().Elem()))
					}
				} else {
					target = target.Addr()
				}
				obj, err := vm.toJS(target)
				if err != nil {
					vm.throw(err)
					return -1
				}
				vm.loadInto(obj, val)
				vm.free_(obj)
				return 1
			}
			gv, err := vm.fromJS(val, f.Type())
			if err != nil {
				vm.throw(err)
				return -1
			}
			f.Set(gv)
			return 1
		}
	case reflect.Map:
		gv, err := vm.fromJS(val, d.Type().Elem())
		if err != nil {
			vm.throw(err)
			return -1
		}
		d.SetMapIndex(reflect.ValueOf(key).Convert(d.Type().Key()), gv)
		return 1
	case reflect.Slice:
		if i, err := strconv.Atoi(key); err == nil && i >= 0 {
			gv, err := vm.fromJS(val, d.Type().Elem())
			if err != nil {
				vm.throw(err)
				return -1
			}
			if i >= d.Len() {
				// grow, as goja does for a slice it can set (one behind a pointer)
				if !d.CanSet() || i > 1<<24 {
					vm.throw(fmt.Errorf("cannot set index %d on %s", i, d.Type()))
					return -1
				}
				grown := reflect.MakeSlice(d.Type(), i+1, i+1)
				reflect.Copy(grown, d)
				d.Set(grown)
			}
			d.Index(i).Set(gv)
			return 1
		}
		if key == "length" {
			n, err := vm.fromJS(val, reflect.TypeOf(0))
			if err == nil && d.CanSet() && int(n.Int()) <= d.Len() {
				d.Set(d.Slice(0, int(n.Int())))
				return 1
			}
		}
	}
	vm.throw(fmt.Errorf("cannot set %q on %s", key, vm.handles[h].v.Type()))
	return -1
}

// avmGoKeys is the enumerable keys of the Go object behind handle h.
func avmGoKeys(ctx *JSContext, h uint32) []string {
	vm := vmOf(ctx)
	d := deref(vm.handles[h].v)
	var keys []string
	switch d.Kind() {
	case reflect.Struct:
		keys = vm.membersOf(d.Type()).keys
	case reflect.Map:
		for _, k := range d.MapKeys() {
			keys = append(keys, fmt.Sprint(k.Interface()))
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < d.Len(); i++ {
			keys = append(keys, strconv.Itoa(i))
		}
	}
	return keys
}

// ---- calls from JS into Go ----

// avmGoCall calls the Go function behind handle h.
func avmGoCall(ctx *JSContext, h uint32, this JSValue, args []JSValue, ctor bool) (ret JSValue) {
	vm := vmOf(ctx)
	e := vm.handles[h]
	// Go time doesn't count against the JS budget.
	start := C.avm_now_ns()
	defer func() {
		if vm.a.deadline_ns != 0 {
			vm.a.deadline_ns += C.avm_now_ns() - start
		}
		if r := recover(); r != nil {
			ret = vm.throw(fmt.Errorf("%v", r))
		}
	}()
	if e.kind == kindNative {
		return e.native(vm, this, args, ctor)
	}
	in, err := vm.goArgs(e.v.Type(), args)
	if err != nil {
		return vm.throw(err)
	}
	if e.blocking && vm.cfg.PumpSyncCalls && vm.pumpDepth < vm.cfg.MaxPumpDepth {
		saved := vm.a.deadline_ns
		out, err := vm.pumped(func() []reflect.Value { return e.v.Call(in) })
		vm.a.deadline_ns = saved // the deferred extension above adds the time spent here
		if err != nil {
			return vm.throw(err)
		}
		return vm.goResults(out)
	}
	out := e.v.Call(in)
	return vm.goResults(out)
}

// goArgs converts JS arguments to ft's parameters; missing ones are zero values.
//
// A function whose first parameter is a context.Context gets the context of the call this code runs for
// (ownerCtx: cancelled when the call is given up) unless JS passes a Go context itself; JS passes the other arguments.
func (vm *VM) goArgs(ft reflect.Type, args []JSValue) ([]reflect.Value, error) {
	if vm.suppliesCtx(ft, args) {
		return vm.goArgsFrom(ft, 1, args, reflect.ValueOf(vm.ownerCtx(vm.cur)))
	}
	return vm.goArgsFrom(ft, 0, args)
}

var contextType = reflect.TypeOf((*context.Context)(nil)).Elem()

// suppliesCtx reports whether the engine supplies ft's first argument: it is a context.Context and JS's first
// argument isn't a Go context.
func (vm *VM) suppliesCtx(ft reflect.Type, args []JSValue) bool {
	if ft.NumIn() == 0 || ft.In(0) != contextType {
		return false
	}
	return len(args) == 0 || !vm.isGoContext(args[0])
}

func (vm *VM) isGoContext(v JSValue) bool {
	h := C.avm_handle_of(vm.a, v)
	if h == 0 {
		return false
	}
	gv := vm.handles[h].v
	return gv.IsValid() && gv.Type().Implements(contextType)
}

// goArgsFrom converts args to ft's parameters from index skip on, after the values in pre (one per skipped one).
func (vm *VM) goArgsFrom(ft reflect.Type, skip int, args []JSValue, pre ...reflect.Value) ([]reflect.Value, error) {
	nIn := ft.NumIn()
	in := pre
	for i := skip; i < nIn; i++ {
		at := ft.In(i)
		k := i - skip // the JS argument for parameter i
		if ft.IsVariadic() && i == nIn-1 {
			for j := k; j < len(args); j++ {
				gv, err := vm.fromJS(args[j], at.Elem())
				if err != nil {
					return nil, fmt.Errorf("argument %d: %w", j, err)
				}
				in = append(in, gv)
			}
			break
		}
		if k < len(args) {
			gv, err := vm.fromJS(args[k], at)
			if err != nil {
				return nil, fmt.Errorf("argument %d: %w", k, err)
			}
			in = append(in, gv)
		} else {
			in = append(in, reflect.Zero(at))
		}
	}
	return in, nil
}

// goResults maps Go results to JS the way goja does: a non-nil trailing error throws; one value returns it.
func (vm *VM) goResults(out []reflect.Value) JSValue {
	if n := len(out); n > 0 && out[n-1].Type() == errorType {
		if !out[n-1].IsNil() {
			return vm.throw(out[n-1].Interface().(error))
		}
		out = out[:n-1]
	}
	switch len(out) {
	case 0:
		return C.avm_undefined()
	case 1:
		jv, err := vm.toJSDeep(out[0])
		if err != nil {
			return vm.throw(err)
		}
		return jv
	}
	arr := C.avm_new_array(vm.ctx)
	for i, o := range out {
		jv, err := vm.toJSDeep(o)
		if err != nil {
			vm.free_(arr)
			return vm.throw(err)
		}
		C.avm_array_push(vm.ctx, arr, uint32(i), jv)
	}
	return arr
}

// ---- JS functions called from Go ----

// jsRef keeps a JS value alive for Go. Freed on the loop.
type jsRef struct {
	vm *VM
	v  JSValue
}

func (vm *VM) ref(v JSValue) *jsRef {
	r := &jsRef{vm: vm, v: C.avm_dup(vm.ctx, v)}
	vm.refs[r] = struct{}{}
	return r
}

// makeFunc wraps a JS function as a Go func of type t.
func (vm *VM) makeFunc(v JSValue, t reflect.Type) reflect.Value {
	r := vm.ref(v)
	return reflect.MakeFunc(t, func(args []reflect.Value) []reflect.Value {
		res, err := vm.CallRef(r, args, t)
		return finishResults(t, res, err)
	})
}

func finishResults(t reflect.Type, res reflect.Value, err error) []reflect.Value {
	out := make([]reflect.Value, t.NumOut())
	for i := range out {
		ot := t.Out(i)
		switch {
		case ot == errorType:
			if err != nil {
				out[i] = reflect.ValueOf(&err).Elem()
			} else {
				out[i] = reflect.Zero(ot)
			}
		case i == 0 && res.IsValid() && err == nil:
			out[i] = res
		default:
			out[i] = reflect.Zero(ot)
		}
	}
	return out
}

// CallRef calls a JS function with Go args and waits for its result, awaiting it if it returns a promise.
// Works from any goroutine; from the loop itself it runs inline and pumps the loop until the promise settles.
func (vm *VM) CallRef(r *jsRef, args []reflect.Value, ft reflect.Type) (reflect.Value, error) {
	return vm.callRef(nil, r, args, ft)
}

// callRef is CallRef for a call serving ctx (nil for none): off the loop, a pending promise is given up when ctx
// ends, and the call's timers are dropped when it returns (the request got its response).
func (vm *VM) callRef(ctx context.Context, r *jsRef, args []reflect.Value, ft reflect.Type) (reflect.Value, error) {
	var resT reflect.Type
	if ft != nil {
		for i := 0; i < ft.NumOut(); i++ {
			if ft.Out(i) != errorType {
				resT = ft.Out(i)
				break
			}
		}
	}
	var ch chan settled
	var immediate *settled
	o := &owner{}
	if vm.onLoop() {
		o = vm.cur // re-entrant: it belongs to the call it runs for
	} else if ctx != nil {
		defer vm.endRequest(o)
	}
	derr := vm.Do(func() {
		vm.cur = o
		s, wait := vm.callHere(r.v, args, resT)
		if wait != nil {
			ch = wait
		} else {
			immediate = &s
		}
	})
	if derr != nil {
		return reflect.Value{}, derr
	}
	var s settled
	if immediate != nil {
		s = *immediate
	} else if vm.onLoop() {
		s = vm.waitSettled(ch)
	} else {
		s = vm.awaitSettled(ch, o, ctx)
	}
	if s.err != nil {
		return reflect.Value{}, s.err
	}
	if rv, ok := s.val.(reflect.Value); ok {
		return rv, nil
	}
	return reflect.Value{}, nil
}

// callHere runs on the loop. If the function returns a pending promise, it returns a channel that gets the result.
func (vm *VM) callHere(fn JSValue, args []reflect.Value, resT reflect.Type) (settled, chan settled) {
	jsArgs := make([]JSValue, len(args))
	for i, a := range args {
		jv, err := vm.toJS(a)
		if err != nil {
			for _, x := range jsArgs[:i] {
				vm.free_(x)
			}
			return settled{err: err}, nil
		}
		jsArgs[i] = jv
	}
	var argp *JSValue
	if len(jsArgs) > 0 {
		argp = &jsArgs[0]
	}
	ret := C.avm_call(vm.ctx, fn, C.avm_undefined(), int32(len(jsArgs)), argp)
	for _, x := range jsArgs {
		vm.free_(x)
	}
	if C.avm_is_exception(ret) != 0 {
		return settled{err: vm.exception()}, nil
	}
	if C.avm_is_promise(vm.ctx, ret) == 0 {
		return vm.settleValue(ret, resT), nil
	}
	return vm.settlePromise(ret, resT)
}

// settlePromise takes ownership of the promise ret: its settled value now, or a channel that gets it once it settles.
func (vm *VM) settlePromise(ret JSValue, resT reflect.Type) (settled, chan settled) {
	vm.runJobs()
	switch C.avm_promise_state(vm.ctx, ret) {
	case C.JS_PROMISE_FULFILLED:
		res := C.avm_promise_result(vm.ctx, ret)
		vm.free_(ret)
		return vm.settleValue(res, resT), nil
	case C.JS_PROMISE_REJECTED:
		res := C.avm_promise_result(vm.ctx, ret)
		vm.free_(ret)
		C.JS_Throw(vm.ctx, res)
		return settled{err: vm.exception()}, nil
	}
	// pending: settle later through __settle(promise, id)
	vm.settleSeq++
	id := vm.settleSeq
	ch := make(chan settled, 1)
	vm.settles[id] = ch
	vm.settleTypes[id] = resT
	cs := C.CString("__settle")
	g := C.JS_GetGlobalObject(vm.ctx)
	settle := C.JS_GetPropertyStr(vm.ctx, g, cs)
	C.free(cs)
	vm.free_(g)
	sargs := []JSValue{ret, C.avm_float(float64(id))}
	r2 := C.avm_call(vm.ctx, settle, C.avm_undefined(), 2, &sargs[0])
	vm.free_(r2)
	vm.free_(settle)
	vm.free_(ret)
	return settled{}, ch
}

// settleValue takes ownership of v.
func (vm *VM) settleValue(v JSValue, resT reflect.Type) settled {
	defer vm.free_(v)
	if resT == nil {
		return settled{ok: true}
	}
	gv, err := vm.fromJS(v, resT)
	if err != nil {
		return settled{err: err}
	}
	return settled{ok: true, val: gv}
}

// checkError: a JS handler that returns a Go error value (e.g. `return new BadRequestError()`) fails the hook.
func asError(v reflect.Value) error {
	if v.IsValid() && v.Type().Implements(errorType) && !v.IsNil() {
		return v.Interface().(error)
	}
	return nil
}

var _ = time.Second

// globalRef returns a ref to a global (on the loop).
func (vm *VM) globalRef(name string) *jsRef {
	g := C.JS_GetGlobalObject(vm.ctx)
	cs := C.CString(name)
	v := C.JS_GetPropertyStr(vm.ctx, g, cs)
	C.free(cs)
	vm.free_(g)
	r := &jsRef{vm: vm, v: v}
	vm.refs[r] = struct{}{}
	return r
}
