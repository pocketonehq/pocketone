package pages

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A route that streams from a timer runs off the VM's loop: other requests are served meanwhile, and the stream
// stops when its client goes away.
func TestStreamFromTimer(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"stream/+api.ts": `export async function get(ctx: any) {
			const s = ctx.res.sse()
			setInterval(() => { globalThis.ticks = (globalThis.ticks || 0) + 1; s.send('tick', String(globalThis.ticks)) }, 20)
			await new Promise(() => {}) // until the client leaves
		}`,
		"count/+api.js": `module.exports = { get(ctx) { return ctx.res.text(String(globalThis.ticks || 0)) } }`,
	})
	h, _ := newServer(t, dir)
	count := func() string {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/count", nil))
		b, _ := io.ReadAll(rec.Result().Body)
		return string(b)
	}

	ctx, cancel := context.WithCancel(context.Background())
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/stream", nil).WithContext(ctx))
	}()
	time.Sleep(120 * time.Millisecond)
	start := time.Now()
	if n := count(); n == "0" {
		t.Fatalf("the stream's interval hasn't run: %s", n)
	}
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Fatalf("another request waited %s behind the open stream", d)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the stream didn't end when its client left")
	}
	if body := rec.Body.String(); !strings.Contains(body, "event: tick\ndata: 1\n") {
		t.Fatalf("stream body:\n%s", body)
	}
	at := count()
	time.Sleep(100 * time.Millisecond)
	if after := count(); after != at {
		t.Fatalf("the interval kept running after the client left: %s, then %s", at, after)
	}
}
