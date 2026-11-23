package jsvm

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const timerHooksJS = `
let ticks = 0
let moduleFired = false
setTimeout(() => { moduleFired = true }, 20)

const wait = (ms, v) => new Promise((resolve) => setTimeout(resolve, ms, v))

routerAdd("GET", "/timeout", async (e) => e.string(200, await wait(50, "late")))
routerAdd("GET", "/order", async (e) => {
	const seen = []
	setTimeout(() => seen.push("timeout"), 0)
	queueMicrotask(() => seen.push("microtask"))
	Promise.resolve().then(() => seen.push("promise"))
	seen.push("sync")
	await wait(30)
	return e.string(200, seen.join(","))
})
routerAdd("GET", "/interval", async (e) => {
	let n = 0
	await new Promise((resolve) => {
		const id = setInterval(() => { if (++n === 5) { clearInterval(id); resolve() } }, 10)
	})
	await wait(60) // a cleared interval runs no more
	return e.string(200, String(n))
})
routerAdd("GET", "/cleared", async (e) => {
	let ran = false
	const id = setTimeout(() => { ran = true }, 20)
	clearTimeout(id)
	clearTimeout(undefined)
	clearTimeout(12345)
	await wait(60)
	return e.string(200, String(ran))
})
routerAdd("GET", "/left-behind", (e) => {
	setTimeout(() => { ticks += 100 }, 30)
	return e.string(200, "answered")
})
routerAdd("GET", "/stream", async (e) => {
	setInterval(() => { ticks++ }, 20)
	await new Promise(() => {}) // until the client leaves
})
routerAdd("GET", "/throws", async (e) => {
	setTimeout(() => { throw new Error("boom") }, 0)
	return e.string(200, await wait(30, "still here"))
})
routerAdd("GET", "/too-many", (e) => {
	try {
		for (let i = 0; i < 10; i++) setTimeout(() => {}, 1000)
	} catch (err) {
		return e.string(200, String(err.message))
	}
	return e.string(200, "no limit")
})
routerAdd("GET", "/bad", (e) => {
	try { setTimeout("ticks++", 0) } catch (err) { return e.string(200, err.name) }
	return e.string(200, "accepted a string")
})
routerAdd("GET", "/state", (e) => e.string(200, ticks + " " + moduleFired))
`

func TestTimers(t *testing.T) {
	_, vm, h := pbApp(t, VMConfig{}, timerHooksJS)
	var mu sync.Mutex
	var logs []string
	vm.Log = func(a ...any) {
		mu.Lock()
		defer mu.Unlock()
		for _, x := range a {
			if s, ok := x.(string); ok {
				logs = append(logs, s)
			}
		}
	}
	for _, c := range []struct{ path, want string }{
		{"/timeout", "late"},
		{"/order", "sync,microtask,promise,timeout"},
		{"/interval", "5"},
		{"/cleared", "false"},
		{"/bad", "TypeError"},
		{"/throws", "still here"},
	} {
		if code, body := get(t, h, "GET", c.path); code != 200 || body != c.want {
			t.Errorf("%s: %d %q, want %q", c.path, code, body, c.want)
		}
	}
	mu.Lock()
	if !strings.Contains(strings.Join(logs, "\n"), "boom") {
		t.Errorf("a throwing callback wasn't logged: %q", logs)
	}
	mu.Unlock()
	if _, body := get(t, h, "GET", "/state"); body != "0 true" {
		t.Errorf("state %q, want the module-scope timer to have fired and no ticks", body)
	}
}

// A request's timers are dropped once it has its response, and when its client goes away.
func TestRequestTimersEndWithRequest(t *testing.T) {
	_, _, h := pbApp(t, VMConfig{}, timerHooksJS)
	if code, body := get(t, h, "GET", "/left-behind"); code != 200 || body != "answered" {
		t.Fatalf("/left-behind: %d %q", code, body)
	}
	time.Sleep(80 * time.Millisecond)
	if _, body := get(t, h, "GET", "/state"); !strings.HasPrefix(body, "0 ") {
		t.Fatalf("a timer outlived its request's response: %q", body)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(150 * time.Millisecond); cancel() }()
	start := time.Now()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/stream", nil).WithContext(ctx))
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("/stream returned after %s, want ~150ms", d)
	}
	_, at := get(t, h, "GET", "/state")
	if strings.HasPrefix(at, "0 ") {
		t.Fatalf("the interval never ran: %q", at)
	}
	time.Sleep(100 * time.Millisecond)
	if _, after := get(t, h, "GET", "/state"); after != at {
		t.Fatalf("the interval kept running after the client left: %q, then %q", at, after)
	}
}

// While a request waits on a timer, the VM serves other requests.
func TestTimerDoesNotHoldTheLoop(t *testing.T) {
	_, _, h := pbApp(t, VMConfig{}, `
		routerAdd("GET", "/slow", async (e) => e.string(200, await new Promise((r) => setTimeout(r, 500, "slow"))))
		routerAdd("GET", "/fast", (e) => e.string(200, "fast"))
	`)
	done := make(chan string, 1)
	go func() { _, body := get(t, h, "GET", "/slow"); done <- body }()
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	if _, body := get(t, h, "GET", "/fast"); body != "fast" {
		t.Fatalf("/fast: %q", body)
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("/fast waited %s behind a pending timer", d)
	}
	if body := <-done; body != "slow" {
		t.Fatalf("/slow: %q", body)
	}
}

func TestMaxTimers(t *testing.T) {
	_, _, h := pbApp(t, VMConfig{MaxTimers: 5}, timerHooksJS)
	if _, body := get(t, h, "GET", "/too-many"); !strings.Contains(body, "too many timers") {
		t.Fatalf("/too-many: %q", body)
	}
}
