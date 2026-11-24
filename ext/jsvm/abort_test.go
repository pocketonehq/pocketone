package jsvm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const abortHooksJS = `
let bg = "pending"
const why = (e) => e && e.name ? e.name + " " + (e instanceof DOMException) + " " + e.code : String(e)
const run = async (e, f) => { try { return e.string(200, "resolved " + await f()) } catch (err) { return e.string(200, why(err)) } }

routerAdd("GET", "/timeout", (e) => run(e, () => block.asyncWith({ signal: AbortSignal.timeout(50) }, "t")))
routerAdd("GET", "/background", (e) => {
	;(async () => {
		try { await block.asyncWith({ signal: AbortSignal.timeout(50) }, "bg"); bg = "resolved" } catch (err) { bg = why(err) }
	})()
	return e.string(200, "answered")
})
routerAdd("GET", "/bg", (e) => e.string(200, bg))
routerAdd("GET", "/hang", async (e) => e.string(200, await block.async("hang")))
routerAdd("GET", "/controller", (e) => {
	const c = new AbortController()
	setTimeout(() => c.abort(), 30)
	return run(e, () => block.asyncWith({ signal: c.signal }, "c"))
})
routerAdd("GET", "/reason", (e) => {
	const c = new AbortController()
	setTimeout(() => c.abort("changed my mind"), 30)
	return run(e, () => block.asyncWith(c, "r")) // a controller-shaped object with .signal works too
})
routerAdd("GET", "/already", (e) => run(e, () => block.asyncWith({ signal: AbortSignal.abort() }, "a")))
routerAdd("GET", "/fast", (e) => run(e, () => double.asyncWith({ signal: AbortSignal.timeout(1000) }, 21)))
routerAdd("GET", "/plain", (e) => run(e, async () => double(21) + " " + await double.async(21) + " " + await double.asyncWith(undefined, 21)))
routerAdd("GET", "/own-ctx", (e) => run(e, () => double.async(e.request.context(), 21)))
routerAdd("GET", "/no-ctx", (e) => run(e, () => plain.asyncWith({ signal: AbortSignal.timeout(30) }, 100)))
routerAdd("GET", "/signals", (e) => {
	const out = []
	const c = new AbortController()
	c.signal.onabort = (ev) => out.push("onabort:" + ev.type)
	const l = () => out.push("listener")
	c.signal.addEventListener("abort", l)
	c.signal.addEventListener("abort", l) // once per listener, as in browsers
	const gone = () => out.push("removed")
	c.signal.addEventListener("abort", gone)
	c.signal.removeEventListener("abort", gone)
	const any = AbortSignal.any([new AbortController().signal, c.signal])
	out.push("before:" + c.signal.aborted + "," + any.aborted)
	c.abort("x")
	c.abort("y") // a second abort does nothing
	out.push("after:" + c.signal.reason + "," + any.aborted + "," + any.reason)
	try { c.signal.throwIfAborted() } catch (err) { out.push("threw:" + err) }
	try { new AbortSignal() } catch (err) { out.push(err.name) }
	out.push(String(new AbortController().signal), Object.prototype.toString.call(new AbortController()))
	out.push(AbortSignal.any([AbortSignal.abort("first")]).reason)
	try { AbortSignal.timeout(-1) } catch (err) { out.push(err.name) }
	return e.string(200, out.join("|"))
})
`

// blocker is a Go function that blocks until its context ends and reports why.
type blocker struct {
	started chan string
	causes  chan error
}

func newBlocker() *blocker {
	return &blocker{started: make(chan string, 16), causes: make(chan error, 16)}
}

func (b *blocker) fn(ctx context.Context, label string) (string, error) {
	b.started <- label
	select {
	case <-ctx.Done():
		b.causes <- context.Cause(ctx)
		return "", ctx.Err()
	case <-time.After(10 * time.Second):
		b.causes <- nil
		return "never cancelled", nil
	}
}

func (b *blocker) cause(t *testing.T) error {
	t.Helper()
	select {
	case err := <-b.causes:
		if err == nil {
			t.Fatal("the Go function's context was never cancelled")
		}
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("the Go function's context was never cancelled")
	}
	return nil
}

func abortApp(t *testing.T, cfg VMConfig) (*blocker, *VM, func(string) (int, string), http.Handler) {
	t.Helper()
	_, vm, h := pbApp(t, cfg, abortHooksJS)
	b := newBlocker()
	if err := vm.Set("block", b.fn); err != nil {
		t.Fatal(err)
	}
	if err := vm.Set("double", func(ctx context.Context, x int) int {
		if ctx == nil {
			return -1
		}
		return x * 2
	}); err != nil {
		t.Fatal(err)
	}
	if err := vm.Set("plain", func(ms int) int { time.Sleep(time.Duration(ms) * time.Millisecond); return ms }); err != nil {
		t.Fatal(err)
	}
	return b, vm, func(u string) (int, string) { return get(t, h, "GET", u) }, h
}

// AbortSignal.timeout rejects an asyncWith call with a TimeoutError on time, and cancels the Go function's context.
func TestAbortSignalTimeout(t *testing.T) {
	b, _, q, _ := abortApp(t, VMConfig{})
	start := time.Now()
	code, body := q("/timeout")
	if code != 200 || body != "TimeoutError true 23" {
		t.Fatalf("/timeout: %d %q", code, body)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("rejected after %s, want ~50ms", d)
	}
	if err := b.cause(t); !errors.Is(err, ErrAborted) || !strings.Contains(err.Error(), "TimeoutError") {
		t.Fatalf("Go saw %v, want ErrAborted with the TimeoutError", err)
	}
}

// AbortSignal.timeout still fires after the request was answered, where setTimeout is refused.
func TestAbortSignalTimeoutAfterResponse(t *testing.T) {
	b, _, q, _ := abortApp(t, VMConfig{})
	if code, body := q("/background"); code != 200 || body != "answered" {
		t.Fatalf("/background: %d %q", code, body)
	}
	if err := b.cause(t); !errors.Is(err, ErrAborted) {
		t.Fatalf("Go saw %v, want ErrAborted", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, body := q("/bg")
		if body == "TimeoutError true 23" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background call: %q, want a TimeoutError", body)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// When the request a handler serves is given up (the client left), the Go function's context is cancelled.
func TestOwnerGiveUpCancelsContext(t *testing.T) {
	b, _, _, h := abortApp(t, VMConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-b.started
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/hang", nil).WithContext(ctx))
	if err := b.cause(t); !errors.Is(err, ErrAbandoned) {
		t.Fatalf("Go saw %v, want ErrAbandoned", err)
	}
}

// Past AwaitLimit the Go function's context is cancelled too.
func TestAwaitLimitCancelsContext(t *testing.T) {
	b, _, q, _ := abortApp(t, VMConfig{AwaitLimit: 100 * time.Millisecond})
	if code, _ := q("/hang"); code == 200 {
		t.Fatal("/hang succeeded")
	}
	if err := b.cause(t); !errors.Is(err, ErrTimeout) {
		t.Fatalf("Go saw %v, want ErrTimeout", err)
	}
}

func TestAbortController(t *testing.T) {
	b, _, q, _ := abortApp(t, VMConfig{})
	if _, body := q("/controller"); body != "AbortError true 20" {
		t.Fatalf("/controller: %q", body)
	}
	if err := b.cause(t); !errors.Is(err, ErrAborted) {
		t.Fatalf("Go saw %v, want ErrAborted", err)
	}
	if _, body := q("/reason"); body != "changed my mind" {
		t.Fatalf("/reason: %q, want the abort reason itself", body)
	}
	if err := b.cause(t); !strings.Contains(err.Error(), "changed my mind") {
		t.Fatalf("Go saw %v, want the reason in the cause", err)
	}
}

// A signal that has already aborted rejects without starting the Go function.
func TestAbortSignalAlreadyAborted(t *testing.T) {
	b, _, q, _ := abortApp(t, VMConfig{})
	if _, body := q("/already"); body != "AbortError true 20" {
		t.Fatalf("/already: %q", body)
	}
	select {
	case l := <-b.started:
		t.Fatalf("the Go function started (%q)", l)
	default:
	}
}

// A context.Context first parameter is supplied by the engine, sync and async; a Go context JS passes is used.
func TestContextParameter(t *testing.T) {
	_, vm, q, _ := abortApp(t, VMConfig{})
	for path, want := range map[string]string{
		"/fast":    "resolved 42",
		"/plain":   "resolved 42 42 42",
		"/own-ctx": "resolved 42",
	} {
		if _, body := q(path); body != want {
			t.Errorf("%s: %q, want %q", path, body, want)
		}
	}
	// a Go function without a context: the promise rejects on time, the call finishes on its own
	start := time.Now()
	if _, body := q("/no-ctx"); body != "TimeoutError true 23" {
		t.Errorf("/no-ctx: %q", body)
	}
	if d := time.Since(start); d > 90*time.Millisecond {
		t.Errorf("/no-ctx rejected after %s, want ~30ms", d)
	}
	time.Sleep(150 * time.Millisecond)
	var left int
	_ = vm.Do(func() { left = len(vm.asyncCancels) })
	if left != 0 {
		t.Errorf("%d cancel funcs left behind", left)
	}
}

func TestAbortSignalSemantics(t *testing.T) {
	_, _, q, _ := abortApp(t, VMConfig{})
	want := "before:false,false|onabort:abort|listener|after:x,true,x|threw:x|TypeError|[object AbortSignal]|[object AbortController]|first|TypeError"
	if _, body := q("/signals"); body != want {
		t.Fatalf("/signals:\n got %s\nwant %s", body, want)
	}
}

// AbortSignal.timeout counts against MaxTimers like setTimeout.
func TestAbortSignalTimeoutMaxTimers(t *testing.T) {
	_, _, h := pbApp(t, VMConfig{MaxTimers: 3}, `
		routerAdd("GET", "/many", (e) => {
			try { for (let i = 0; i < 5; i++) AbortSignal.timeout(1000) } catch (err) { return e.string(200, err.message) }
			return e.string(200, "no limit")
		})
	`)
	if _, body := get(t, h, "GET", "/many"); !strings.Contains(body, "AbortSignal.timeout: too many timers") {
		t.Fatalf("/many: %q", body)
	}
}
