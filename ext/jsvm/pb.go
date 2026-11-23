package jsvm

import (
	"context"
	"net/http"
	"reflect"
	"strings"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
)

func init() {
	// A record held by JS keeps its field data alive: charge it.
	RegisterSizer(reflect.TypeOf(&core.Record{}), func(v reflect.Value) int64 {
		r := v.Interface().(*core.Record)
		if r == nil || r.Collection() == nil {
			return 0
		}
		n := int64(512)
		for k, val := range r.FieldsData() {
			n += 64 + int64(len(k))
			if s, ok := val.(string); ok {
				n += int64(len(s))
			}
		}
		return n
	})
}

var appType = reflect.TypeOf((*core.App)(nil)).Elem()

// PBBlocking marks methods that may do I/O: anything on an app, and DB queries.
func PBBlocking(recv reflect.Type) bool {
	if recv.Implements(appType) {
		return true
	}
	n := recv.String()
	return strings.HasPrefix(n, "*dbx.")
}

// PBShared: the app and its DB handles are alive no matter what JS holds.
func PBShared(t reflect.Type) bool {
	if t.Implements(appType) {
		return true
	}
	n := t.String()
	return strings.HasPrefix(n, "*dbx.") || strings.HasPrefix(n, "*sql.") || strings.HasPrefix(n, "*sync.") || strings.Contains(n, "context.")
}

type errString string

func (e errString) Error() string { return string(e) }

var errType = reflect.TypeOf((*error)(nil)).Elem()

// runHandler calls a JS handler with a Go event and waits for it (and its promise). While the synchronous part
// runs, $app is the event's app (the transaction's app inside a transaction), as in plugins/jsvm.
func (vm *VM) runHandler(h *jsRef, app core.App, args ...any) error {
	release, err := vm.acquire()
	if err != nil {
		return err
	}
	defer release()
	var ch chan settled
	var imm *settled
	o := &owner{}
	if vm.onLoop() {
		o = vm.cur // a hook fired by a sync Go call from JS belongs to the call that made it
	} else if requestContext(args) != nil {
		defer vm.endRequest(o)
	}
	derr := vm.Do(func() {
		vm.cur = o
		if vm.OnTask != nil {
			done, err := vm.OnTask()
			if err != nil {
				imm = &settled{err: err}
				return
			}
			defer done()
		}
		appV, err := vm.toJS(reflect.ValueOf(app))
		if err != nil {
			imm = &settled{err: err}
			return
		}
		g := C.JS_GetGlobalObject(vm.ctx)
		cs := C.CString("$app")
		old := C.JS_GetPropertyStr(vm.ctx, g, cs)
		C.JS_SetPropertyStr(vm.ctx, g, cs, appV)
		in := make([]reflect.Value, len(args))
		for i, a := range args {
			in[i] = reflect.ValueOf(a)
		}
		s, wait := vm.callHere(h.v, in, anyType)
		C.JS_SetPropertyStr(vm.ctx, g, cs, old)
		C.free(cs)
		vm.free_(g)
		if wait != nil {
			ch = wait
		} else {
			imm = &s
		}
	})
	if derr != nil {
		o.cancel(derr)
		return derr
	}
	var s settled
	switch {
	case imm != nil:
		s = *imm
	case vm.onLoop():
		s = vm.waitSettled(ch)
	default:
		s = vm.awaitSettled(ch, o, requestContext(args))
	}
	if s.err != nil {
		return s.err
	}
	if rv, ok := s.val.(reflect.Value); ok {
		return asError(rv)
	}
	return nil
}

var httpRequestType = reflect.TypeOf((*http.Request)(nil))

// requestContext is the context of the HTTP request an event serves (a route's or a request hook's event: a
// *core.RequestEvent or an event embedding one), nil for other events. A handler stops when it ends.
func requestContext(args []any) context.Context {
	for _, a := range args {
		v := reflect.ValueOf(a)
		if v.Kind() != reflect.Pointer || v.IsNil() || v.Elem().Kind() != reflect.Struct {
			continue
		}
		f := v.Elem().FieldByName("Request")
		if f.IsValid() && f.Type() == httpRequestType && !f.IsNil() {
			return f.Interface().(*http.Request).Context()
		}
	}
	return nil
}
