package llmchat_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tests"
	llmc "pocketone.dev/ext/contracts/llm"
	chatv1 "pocketone.dev/ext/contracts/llm/chat/v1"
	textv1 "pocketone.dev/ext/contracts/llm/text/v1"
	"pocketone.dev/ext/extkit"
	"pocketone.dev/ext/extkit/js"
	"pocketone.dev/ext/jsvm"
	"pocketone.dev/ext/llm"
	llmchat "pocketone.dev/ext/llm-chat"
)

// fakeText answers "answer N" (or "SUMMARY" when asked to summarize) and remembers every input.
type fakeText struct {
	mu        sync.Mutex
	inputs    []textv1.CompleteInput
	cancelled chan struct{} // a "stall" question waits for its context and sends here when it's cancelled
}

// stall reports whether in asks "stall" and, if so, waits until ctx is cancelled.
func (f *fakeText) stall(ctx context.Context, in textv1.CompleteInput) bool {
	if len(in.Messages) == 0 || !strings.HasPrefix(in.Messages[len(in.Messages)-1].Content, "stall") {
		return false
	}
	<-ctx.Done()
	f.cancelled <- struct{}{}
	return true
}

func (f *fakeText) answer(in textv1.CompleteInput) textv1.CompleteResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs = append(f.inputs, in)
	if strings.HasPrefix(in.System, "Update the summary") {
		return textv1.CompleteResult{Text: "SUMMARY"}
	}
	return textv1.CompleteResult{Text: "answer " + string(rune('0'+len(f.inputs)))}
}

// last is the last answer's input (summaries left out).
func (f *fakeText) last() textv1.CompleteInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.inputs) - 1; i >= 0; i-- {
		if !strings.HasPrefix(f.inputs[i].System, "Update the summary") {
			return f.inputs[i]
		}
	}
	return textv1.CompleteInput{}
}

func (f *fakeText) Complete(ctx context.Context, _ extkit.Auth, in textv1.CompleteInput) (textv1.CompleteResult, error) {
	if f.stall(ctx, in) {
		return textv1.CompleteResult{}, ctx.Err()
	}
	return f.answer(in), nil
}
func (f *fakeText) Stream(_ context.Context, _ extkit.Auth, in textv1.CompleteInput) (extkit.Stream, error) {
	return extkit.Stream{Run: func(ctx context.Context, send func(any) error) error {
		if f.stall(ctx, in) {
			return ctx.Err()
		}
		res := f.answer(in)
		for _, w := range strings.SplitAfter(res.Text, " ") {
			if err := send(textv1.Chunk{Text: w}); err != nil {
				return err
			}
		}
		return send(textv1.Chunk{Done: true, Result: &res})
	}}, nil
}
func (f *fakeText) Count(context.Context, extkit.Auth, textv1.CountInput) (textv1.CountResult, error) {
	return textv1.CountResult{}, nil
}
func (f *fakeText) Models(context.Context, extkit.Auth, llmc.ModelsInput) (llmc.Models, error) {
	return llmc.Models{}, nil
}

// fakeExt provides the fake as llm.text/v1.
type fakeExt struct{ a *fakeText }

func (fakeExt) Name() string { return "fake-text" }
func (fakeExt) Contracts() extkit.Contracts {
	return extkit.Contracts{Implements: extkit.IDs("llm.text/v1")}
}
func (e fakeExt) Register(app core.App, _ extkit.Config) error {
	textv1.Contract.Provide(app, e.a)
	return nil
}

func setup(t *testing.T) (core.App, *fakeText, chatv1.Chats) {
	t.Helper()
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)
	fa := &fakeText{cancelled: make(chan struct{}, 4)}
	if _, err := extkit.Setup(app, []extkit.Extension{fakeExt{fa}, llmchat.New()}, nil, nil); err != nil {
		t.Fatal(err)
	}
	return app, fa, chatv1.Contract.Must(app)
}

// A chat keeps its messages and sends them back each turn; once enough pile up past the window, the
// oldest are folded into the summary, which leads the next prompt.
func TestSendAndFold(t *testing.T) {
	_, fa, c := setup(t)
	ctx := context.Background()
	chat, err := c.New(ctx, extkit.Internal, chatv1.NewInput{Title: "Support", Meta: json.RawMessage(`{"user":"u1"}`)})
	if err != nil || chat.ID == "" || chat.Title != "Support" {
		t.Fatalf("new: %+v %v", chat, err)
	}
	send := func(text string) chatv1.SendResult {
		t.Helper()
		r, err := c.Send(ctx, extkit.Internal, chatv1.SendInput{ID: chat.ID, Text: text, System: "Be brief.", Window: 2,
			SummarizeEvery: 2, Context: "CTX for " + text, Model: "p/m"})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r1 := send("q1")
	in := fa.last()
	if in.Model != "p/m" || in.System != "Be brief." || len(in.Messages) != 1 || in.Messages[0].Content != "q1\n\nCTX for q1" {
		t.Fatalf("first turn's input: %+v", in)
	}
	if r1.Answer.Text == "" || r1.Summarized {
		t.Fatalf("first answer: %+v", r1)
	}
	send("q2") // 4 messages: 2 past the window of 2, which is SummarizeEvery: fold
	if got, _ := c.Get(ctx, extkit.Internal, chatv1.Ref{ID: chat.ID}); got.Summary != "SUMMARY" || got.Messages != 4 {
		t.Fatalf("after the fold: %+v", got)
	}
	var summary textv1.CompleteInput
	for _, x := range fa.inputs {
		if strings.HasPrefix(x.System, "Update the summary") {
			summary = x
		}
	}
	if summary.Model != "p/m" || summary.Reasoning != "off" {
		t.Fatalf("the summary didn't use the chat's model with reasoning off: %+v", summary)
	}
	send("q3")
	in = fa.last()
	if len(in.Messages) != 4 || !strings.Contains(in.Messages[0].Content, "SUMMARY") || in.Messages[1].Content != "q2\n\nCTX for q2" ||
		in.Messages[3].Content != "q3\n\nCTX for q3" {
		t.Fatalf("after the fold, the prompt is the summary, the turns since, and the question: %+v", in.Messages)
	}
	// history keeps every turn (the question without its context), oldest first, and pages with before
	h, _ := c.History(ctx, extkit.Internal, chatv1.HistoryInput{ID: chat.ID})
	if len(h.Messages) != 6 || h.Messages[0].Content != "q1" || h.Messages[0].Role != "user" || h.Messages[1].Role != "assistant" {
		t.Fatalf("history: %+v", h.Messages)
	}
	page, _ := c.History(ctx, extkit.Internal, chatv1.HistoryInput{ID: chat.ID, Limit: 2, Before: h.Messages[4].ID})
	if len(page.Messages) != 2 || page.Messages[0].Content != "q2" {
		t.Fatalf("page: %+v", page.Messages)
	}
}

// Streaming sends the model's chunks and saves both turns before the last one.
func TestSendStream(t *testing.T) {
	_, _, c := setup(t)
	ctx := context.Background()
	chat, _ := c.New(ctx, extkit.Internal, chatv1.NewInput{})
	s, err := c.SendStream(ctx, extkit.Internal, chatv1.SendInput{ID: chat.ID, Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	var pieces []string
	var done bool
	err = s.Run(ctx, func(e any) error {
		ch := e.(textv1.Chunk)
		if ch.Done {
			done = true
			if got, _ := c.Get(ctx, extkit.Internal, chatv1.Ref{ID: chat.ID}); got.Messages != 2 {
				t.Errorf("not saved before the last chunk: %+v", got)
			}
			return nil
		}
		pieces = append(pieces, ch.Text)
		return nil
	})
	if err != nil || !done || strings.Join(pieces, "") != "answer 1" {
		t.Fatalf("stream: %v %v %q", err, done, pieces)
	}
}

// Chats are found by their meta, updated, and forgotten one at a time or by meta.
func TestListUpdateForget(t *testing.T) {
	_, _, c := setup(t)
	ctx := context.Background()
	a, _ := c.New(ctx, extkit.Internal, chatv1.NewInput{Meta: json.RawMessage(`{"user":"u1","kind":"support"}`)})
	b, _ := c.New(ctx, extkit.Internal, chatv1.NewInput{Meta: json.RawMessage(`{"user":"u1"}`)})
	_, _ = c.New(ctx, extkit.Internal, chatv1.NewInput{Meta: json.RawMessage(`{"user":"u2"}`)})
	if l, _ := c.List(ctx, extkit.Internal, chatv1.ListInput{Meta: map[string]any{"user": "u1"}}); len(l.Chats) != 2 {
		t.Fatalf("u1's chats: %+v", l)
	}
	if l, _ := c.List(ctx, extkit.Internal, chatv1.ListInput{Meta: map[string]any{"user": "u1", "kind": "support"}}); len(l.Chats) != 1 || l.Chats[0].ID != a.ID {
		t.Fatalf("u1's support chats: %+v", l)
	}
	if _, err := c.List(ctx, extkit.Internal, chatv1.ListInput{Meta: map[string]any{"x) || 1=1 || (": "y"}}); err == nil {
		t.Fatal("a bad meta key was accepted")
	}
	title := "Renamed"
	if u, err := c.Update(ctx, extkit.Internal, chatv1.UpdateInput{ID: b.ID, Title: &title}); err != nil || u.Title != "Renamed" || !strings.Contains(string(u.Meta), "u1") {
		t.Fatalf("update: %+v %v", u, err)
	}
	if f, err := c.ForgetWhere(ctx, extkit.Internal, chatv1.ListInput{Meta: map[string]any{"user": "u1"}}); err != nil || f.Count != 2 {
		t.Fatalf("forget u1: %+v %v", f, err)
	}
	if l, _ := c.List(ctx, extkit.Internal, chatv1.ListInput{}); len(l.Chats) != 1 {
		t.Fatalf("left: %+v", l)
	}
	if _, err := c.Get(ctx, extkit.Internal, chatv1.Ref{ID: a.ID}); err == nil {
		t.Fatal("a forgotten chat is still there")
	}
}

const hooks = `onRecordCreate(async (e) => {
  const id = $llm.chat.new({ title: "From JS", meta: { user: "u9" } })
  const chat = $llm.chat.open(id, { model: "p/m", system: "Be brief.", context: (q) => "ctx:" + q })
  const a1 = await chat.sendAsync("hello")
  const pieces = []
  const a2 = await chat.sendAsync("again", { onText: (t) => { pieces.push(t) } })
  chat.title("Renamed")
  const out = {
    a1: a1.text, a2: a2.text, pieces: pieces.join("|"), history: chat.history().map((m) => m.role + ":" + m.content),
    title: chat.title(), meta: chat.meta(), listed: $llm.chat.list({ meta: { user: "u9" } }).length,
    syncGone: typeof chat.send === "undefined",
  }
  out.forgotten = $llm.chat.forget({ meta: { user: "u9" } })
  e.record.set("result", JSON.stringify(out))
  e.next()
}, "probes")
`

// $llm.chat from pb_hooks, over llm-chat (bound by ext/llm's BindJS whoever provides llm.text/v1).
func TestJS(t *testing.T) {
	app, fa, _ := setup(t)
	for _, name := range []string{"probes"} {
		c := core.NewBaseCollection(name)
		c.Fields.Add(&core.TextField{Name: "result", Max: 10000})
		if err := app.Save(c); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(t.TempDir(), "pb_hooks")
	_ = os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "main.pb.js"), []byte(hooks), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := jsvm.Register(app, jsvm.Config{HooksDir: dir, OnInit: func(rt js.Runtime) { llm.New().BindJS(app, rt) }}); err != nil {
		t.Fatal(err)
	}
	if err := app.OnBootstrap().Trigger(&core.BootstrapEvent{App: app}, func(*core.BootstrapEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}

	r := core.NewRecord(mustCollection(t, app, "probes"))
	if err := app.Save(r); err != nil {
		t.Fatal(err)
	}
	var out struct {
		A1, A2, Pieces, Title string
		History               []string
		Meta                  map[string]any
		Listed, Forgotten     int
		SyncGone              bool
	}
	if err := json.Unmarshal([]byte(r.GetString("result")), &out); err != nil {
		t.Fatalf("result %q: %v", r.GetString("result"), err)
	}
	if out.A1 != "answer 1" || out.A2 != "answer 2" || out.Pieces != "answer |2" || out.Title != "Renamed" ||
		strings.Join(out.History, ",") != "user:hello,assistant:answer 1,user:again,assistant:answer 2" ||
		out.Meta["user"] != "u9" || out.Listed != 1 || out.Forgotten != 1 || !out.SyncGone {
		t.Fatalf("from JS: %+v", out)
	}
	if q := fa.inputs[0].Messages[len(fa.inputs[0].Messages)-1].Content; q != "hello\n\nctx:hello" {
		t.Fatalf("context function: %q", q)
	}

}

const abortHooks = `onRecordCreate(async (e) => {
  const chat = $llm.chat.open($llm.chat.new({}), { model: "p/m" })
  const out = {}
  const run = async (name, f) => {
    const t = Date.now()
    try { await f(); out[name] = "answered" } catch (err) { out[name] = err.name + ":" + (Date.now() - t) }
  }
  await run("send", () => chat.sendAsync("stall", { signal: AbortSignal.timeout(200) }))
  const c = new AbortController()
  setTimeout(() => c.abort(), 100)
  await run("stream", () => chat.sendAsync("stall again", { onText: () => {}, signal: c.signal }))
  e.record.set("result", JSON.stringify(out))
  e.next()
}, "probes")
`

// chat.sendAsync takes an AbortSignal: the promise rejects with its reason, and the model call's context is
// cancelled, streamed or not.
func TestJSAbort(t *testing.T) {
	app, fa, _ := setup(t)
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
	if err := app.Save(r); err != nil {
		t.Fatal(err)
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(r.GetString("result")), &out); err != nil {
		t.Fatalf("result %q: %v", r.GetString("result"), err)
	}
	for k, name := range map[string]string{"send": "TimeoutError", "stream": "AbortError"} {
		var ms int
		if v := out[k]; !strings.HasPrefix(v, name+":") {
			t.Errorf("%s: want %s, got %q", k, name, v)
		} else if _, _ = fmt.Sscan(v[len(name)+1:], &ms); ms > 3000 {
			t.Errorf("%s: took %d ms", k, ms)
		}
	}
	for i := 0; i < 2; i++ {
		select {
		case <-fa.cancelled:
		case <-time.After(3 * time.Second):
			t.Fatalf("only %d of 2 model calls were cancelled", i)
		}
	}
}

func mustCollection(t *testing.T, app core.App, name string) *core.Collection {
	t.Helper()
	c, err := app.FindCollectionByNameOrId(name)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
