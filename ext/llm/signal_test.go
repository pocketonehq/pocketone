package llm_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tests"
	"pocketone.dev/ext/extkit"
	"pocketone.dev/ext/extkit/js"
	"pocketone.dev/ext/jsvm"
	"pocketone.dev/ext/llm"
)

const abortHooks = `onRecordCreate(async (e) => {
  const out = {}
  const run = async (name, f) => {
    const t = Date.now()
    try { await f(); out[name] = "answered" } catch (err) { out[name] = err.name + ":" + (Date.now() - t) }
  }
  await run("complete", () => $llm.text.completeAsync({ model: "stall/chat-1", prompt: "hi", signal: AbortSignal.timeout(300) }))
  await run("stream", () => $llm.text.streamAsync({ model: "stall/chat-1", prompt: "hi", signal: AbortSignal.timeout(300) }, () => {}))
  const c = new AbortController()
  setTimeout(() => c.abort(), 100)
  await run("image", () => $llm.image.generateAsync({ model: "stall/gpt-image-1", prompt: "cat", signal: c.signal }))
  await run("already", () => $llm.text.completeAsync({ model: "stall/chat-1", prompt: "hi", signal: AbortSignal.abort() }))
  e.record.set("result", JSON.stringify(out))
  e.next()
}, "probes")
`

// Page code passes an AbortSignal to $llm: the promise rejects with the signal's reason when it fires, and the
// provider's HTTP request is cancelled (the stalled server sees each connection closed). chat.sendAsync's signal is
// tested in ext/llm-chat.
func TestJSAbortSignal(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	hungUp := make(chan struct{}, 8)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(io.Discard, conn); hungUp <- struct{}{} }()
		}
	}()
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Cleanup()
	if _, err := extkit.Setup(app, []extkit.Extension{llm.New()}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := llm.SaveSettings(app, llm.Settings{Providers: []llm.Provider{{Name: "stall", Type: llm.TypeOpenAI,
		Endpoint: "http://" + ln.Addr().String() + "/v1", Key: "k"}}}); err != nil {
		t.Fatal(err)
	}
	c := core.NewBaseCollection("probes")
	c.Fields.Add(&core.TextField{Name: "result", Max: 10000})
	if err := app.Save(c); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "pb_hooks")
	_ = os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "main.pb.js"), []byte(abortHooks), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := jsvm.Register(app, jsvm.Config{HooksDir: dir, OnInit: func(rt js.Runtime) { llm.New().BindJS(app, rt) }}); err != nil {
		t.Fatal(err)
	}
	if err := app.OnBootstrap().Trigger(&core.BootstrapEvent{App: app}, func(*core.BootstrapEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	r := core.NewRecord(c)
	start := time.Now()
	if err := app.Save(r); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("took %s", took)
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(r.GetString("result")), &out); err != nil {
		t.Fatalf("result %q: %v", r.GetString("result"), err)
	}
	t.Logf("%v", out)
	want := map[string]string{"complete": "TimeoutError", "stream": "TimeoutError", "image": "AbortError", "already": "AbortError"}
	for k, name := range want {
		var got string
		var ms int
		if v := out[k]; len(v) > len(name) && v[:len(name)+1] == name+":" {
			got = name
			_, _ = fmt.Sscan(v[len(name)+1:], &ms)
		}
		if got != name || ms > 3000 {
			t.Errorf("%s: want %s within 3 s, got %q", k, name, out[k])
		}
	}
	for i := 0; i < 3; i++ { // complete, stream, image reached the server; "already" never called it
		select {
		case <-hungUp:
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d of 3 provider requests were cancelled", i)
		}
	}
}
