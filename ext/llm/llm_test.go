package llm_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tests"

	llmc "pocketone.dev/ext/contracts/llm"
	alignv1 "pocketone.dev/ext/contracts/llm/align/v1"
	embedv1 "pocketone.dev/ext/contracts/llm/embed/v1"
	imagev1 "pocketone.dev/ext/contracts/llm/image/v1"
	musicv1 "pocketone.dev/ext/contracts/llm/music/v1"
	speechv1 "pocketone.dev/ext/contracts/llm/speech/v1"
	textv1 "pocketone.dev/ext/contracts/llm/text/v1"
	settingsv1 "pocketone.dev/ext/contracts/settings/v1"
	"pocketone.dev/ext/extkit"
	"pocketone.dev/ext/extkit/conformance"
	"pocketone.dev/ext/extkit/js"
	"pocketone.dev/ext/jsvm"
	"pocketone.dev/ext/llm"
)

func TestConformance(t *testing.T) {
	conformance.Run(t, func() extkit.Extension { return llm.New() })
}

// fakeCloud is an OpenAI-shaped API (and Vultr's job shape for music): it records each request's path and body.
type fakeCloud struct {
	mu     sync.Mutex
	bodies map[string]map[string]any
	polls  int
}

func (f *fakeCloud) body(path string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[path]
}

func (f *fakeCloud) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	f.mu.Lock()
	if f.bodies == nil {
		f.bodies = map[string]map[string]any{}
	}
	f.bodies[r.URL.Path] = body
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer k1" {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"error":{"message":"bad key"}}`)
		return
	}
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG\r\n\x1a\nxxxx"))
	switch r.URL.Path {
	case "/v1/models":
		_, _ = io.WriteString(w, `{"data":[{"id":"chat-1"},{"id":"chat-2"},{"id":"text-embedding-3-small"},{"id":"gpt-image-1"}]}`)
	case "/v1/chat/completions":
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			pieces := []string{"Hel", "lo"}
			if body["response_format"] != nil {
				pieces = []string{`{\"label\":`, `\"billing\"}`}
			}
			for _, p := range pieces {
				_, _ = io.WriteString(w, `data: {"choices":[{"delta":{"content":"`+p+`"}}]}`+"\n\n")
			}
			_, _ = io.WriteString(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`+"\n\ndata: [DONE]\n\n")
			return
		}
		text := "Hello"
		if body["response_format"] != nil {
			text = `{"label":"billing"}`
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":`+jsonString(text)+`},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2}}`)
	case "/v1/images/generations":
		_, _ = io.WriteString(w, `{"data":[{"b64_json":"`+png+`"}]}`)
	case "/v1/embeddings":
		_, _ = io.WriteString(w, `{"data":[{"index":1,"embedding":[0.2]},{"index":0,"embedding":[0.1]}]}`)
	default:
		w.WriteHeader(404)
	}
}

func jsonString(s string) string { b, _ := json.Marshal(s); return string(b) }

// setup is an app with ext/llm and one OpenAI-compatible provider "fake" at a fake cloud.
func setup(t *testing.T) (core.App, *fakeCloud) {
	t.Helper()
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)
	if _, err := extkit.Setup(app, []extkit.Extension{llm.New()}, nil, nil); err != nil {
		t.Fatal(err)
	}
	return app, withFakeCloud(t, app)
}

// withFakeCloud adds a provider named "fake" that a fakeCloud answers.
func withFakeCloud(t *testing.T, app core.App) *fakeCloud {
	t.Helper()
	fc := &fakeCloud{}
	srv := httptest.NewServer(fc)
	t.Cleanup(srv.Close)
	if err := llm.SaveSettings(app, llm.Settings{
		Providers: []llm.Provider{{Name: "fake", Type: llm.TypeOpenAI, Endpoint: srv.URL + "/v1", Key: "k1"}},
	}); err != nil {
		t.Fatal(err)
	}
	return fc
}

func TestText(t *testing.T) {
	app, fc := setup(t)
	ctx := context.Background()
	tx := textv1.Contract.Must(app)

	r, err := tx.Complete(ctx, extkit.Internal, textv1.CompleteInput{Model: "fake/chat-1", Prompt: "hi", System: "Be brief.", Reasoning: "off"})
	if err != nil || r.Text != "Hello" || r.Model != "fake/chat-1" || r.Usage.InputTokens != 3 {
		t.Fatalf("complete: %+v %v", r, err)
	}
	b := fc.body("/v1/chat/completions")
	if b["model"] != "chat-1" || b["reasoning_effort"] != "none" || len(b["messages"].([]any)) != 2 {
		t.Fatalf("sent: %v", b)
	}

	r, err = tx.Complete(ctx, extkit.Internal, textv1.CompleteInput{Model: "fake/chat-2", Prompt: "label",
		Schema: json.RawMessage(`{"type":"object"}`)})
	if err != nil || string(r.Data) != `{"label":"billing"}` || r.Model != "fake/chat-2" {
		t.Fatalf("schema: %+v %v", r, err)
	}

	s, err := tx.Stream(ctx, extkit.Internal, textv1.CompleteInput{Model: "fake/chat-1", Prompt: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	var pieces []string
	var done *textv1.CompleteResult
	if err := s.Run(ctx, func(e any) error {
		ch := e.(textv1.Chunk)
		if ch.Done {
			done = ch.Result
		} else {
			pieces = append(pieces, ch.Text)
		}
		return nil
	}); err != nil || strings.Join(pieces, "|") != "Hel|lo" || done == nil || done.Text != "Hello" {
		t.Fatalf("stream: %v %q %+v", err, pieces, done)
	}

	for ref, code := range map[string]string{"nobody/x": "unknown_model", "noslash": "unknown_model", "": "unknown_model"} {
		_, err := tx.Complete(ctx, extkit.Internal, textv1.CompleteInput{Model: ref, Prompt: "hi"})
		var xe *extkit.Error
		if !asError(err, &xe) || xe.Code != code {
			t.Errorf("%s: %v", ref, err)
		}
	}

	ms, err := tx.Models(ctx, extkit.Internal, llmc.ModelsInput{})
	if err != nil || len(ms.Models) != 2 || ms.Models[0].ID != "fake/chat-1" || ms.Models[0].Provider != "fake" {
		t.Fatalf("models: %+v %v", ms, err)
	}
}

func asError(err error, xe **extkit.Error) bool {
	e, ok := err.(*extkit.Error)
	*xe = e
	return ok
}

func TestMediaAndErrors(t *testing.T) {
	app, _ := setup(t)
	ctx := context.Background()
	im, err := imagev1.Contract.Must(app).Generate(ctx, extkit.Internal, imagev1.GenerateInput{Model: "fake/gpt-image-1", Prompt: "cat"})
	if err != nil || len(im.Files) != 1 || im.Files[0].Type != "image/png" || im.Files[0].Name != "image-1.png" {
		t.Fatalf("image: %+v %v", im, err)
	}
	if _, err := imagev1.Contract.Must(app).Generate(ctx, extkit.Internal, imagev1.GenerateInput{Prompt: "cat"}); err == nil ||
		!strings.Contains(err.Error(), "name the image model as provider/model") {
		t.Fatalf("no model: %v", err)
	}
	em, err := embedv1.Contract.Must(app).Embed(ctx, extkit.Internal, embedv1.EmbedInput{Model: "fake/text-embedding-3-small", Input: []string{"a", "b"}})
	if err != nil || len(em.Vectors) != 2 || em.Vectors[0][0] != 0.1 {
		t.Fatalf("embed: %+v %v", em, err)
	}
	// music on an OpenAI-compatible API: not supported, said plainly
	_, err = musicv1.Contract.Must(app).Create(ctx, extkit.Internal, musicv1.CreateInput{Model: "fake/x", Prompt: "lullaby"})
	if xe, ok := err.(*extkit.Error); !ok || xe.Code != "not_supported" {
		t.Fatalf("music: %v", err)
	}
	// alignment: inputs checked first, then an OpenAI-compatible API says it can't
	al := alignv1.Contract.Must(app)
	if _, err := al.Align(ctx, extkit.Internal, alignv1.AlignInput{Model: "fake/x", Audio: llmc.File{Data: []byte("a")}}); err == nil ||
		!strings.Contains(err.Error(), "text") {
		t.Fatalf("align without text: %v", err)
	}
	_, err = al.Align(ctx, extkit.Internal, alignv1.AlignInput{Model: "fake/x", Audio: llmc.File{Data: []byte("a")}, Text: "hi"})
	if xe, ok := err.(*extkit.Error); !ok || xe.Code != "not_supported" {
		t.Fatalf("align: %v", err)
	}
	// voice cloning: the clip and its rules are checked for every provider, then an OpenAI-compatible API says it can't
	sp := speechv1.Contract.Must(app)
	clip := &speechv1.VoiceReference{Audio: llmc.File{Name: "me.wav", Data: []byte("RIFF")}, Text: "Once upon a time"}
	if _, err := sp.Speak(ctx, extkit.Internal, speechv1.SpeakInput{Model: "fake/tts", Text: "hi", Reference: &speechv1.VoiceReference{}}); err == nil ||
		!strings.Contains(err.Error(), "clip") {
		t.Fatalf("clone without a clip: %v", err)
	}
	if _, err := sp.Speak(ctx, extkit.Internal, speechv1.SpeakInput{Model: "fake/tts", Text: "hi", Reference: clip, Instructions: "softly"}); err == nil ||
		!strings.Contains(err.Error(), "no instructions") {
		t.Fatalf("clone with instructions: %v", err)
	}
	_, err = sp.Speak(ctx, extkit.Internal, speechv1.SpeakInput{Model: "fake/tts", Text: "hi", Reference: clip})
	if xe, ok := err.(*extkit.Error); !ok || xe.Code != "not_supported" || !strings.Contains(xe.Message, "voice cloning") {
		t.Fatalf("clone on an OpenAI-compatible API: %v", err)
	}
	// a wrong key: the provider's error, with its name
	s, _ := llm.LoadSettings(app)
	s.Providers[0].Key = "wrong"
	_ = llm.SaveSettings(app, s)
	_, err = textv1.Contract.Must(app).Complete(ctx, extkit.Internal, textv1.CompleteInput{Model: "fake/chat-1", Prompt: "hi"})
	if xe, ok := err.(*extkit.Error); !ok || xe.Code != "provider" || !strings.Contains(xe.Message, "fake: ") || !strings.Contains(xe.Message, "bad key") {
		t.Fatalf("bad key: %v", err)
	}
}

// Settings → LLM: the key never comes back, and an empty key keeps the saved one for the same endpoint only.
func TestSettingsSection(t *testing.T) {
	app, _ := setup(t)
	ctx := context.Background()
	sec, ok := settingsv1.SectionOf(app, "llm")
	if !ok {
		t.Fatal("no llm settings section")
	}
	st := sec.(settingsv1.Saver)
	raw, err := sec.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "k1") {
		t.Fatal("the key came back")
	}
	var view struct {
		Providers []struct {
			Name     string `json:"name"`
			Endpoint string `json:"endpoint"`
			KeySet   bool   `json:"keySet"`
		} `json:"providers"`
	}
	_ = json.Unmarshal(raw, &view)
	if len(view.Providers) != 1 || !view.Providers[0].KeySet {
		t.Fatalf("view: %s", raw)
	}
	endpoint := view.Providers[0].Endpoint
	save := func(endpoint string) llm.Settings {
		t.Helper()
		vals, _ := json.Marshal(map[string]any{"providers": []any{map[string]any{"name": "fake", "type": "openai", "endpoint": endpoint}}})
		if _, err := st.Save(ctx, vals); err != nil {
			t.Fatal(err)
		}
		s, _ := llm.LoadSettings(app)
		return s
	}
	if s := save(endpoint); s.Providers[0].Key != "k1" {
		t.Fatal("an empty key didn't keep the saved one")
	}
	if s := save("https://elsewhere.example/v1"); s.Providers[0].Key != "" {
		t.Fatal("the key followed the provider to another endpoint")
	}
	bad, _ := json.Marshal(map[string]any{"providers": []any{map[string]any{"name": "a/b", "type": "openai"}}})
	if _, err := st.Save(ctx, bad); err == nil {
		t.Fatal("a provider name with a slash was accepted")
	}
	nokey, _ := json.Marshal(map[string]any{"providers": []any{map[string]any{"name": "vultr", "type": "vultr"}}})
	if _, err := st.Save(ctx, nokey); err == nil || !strings.Contains(err.Error(), "enter its API key") {
		t.Fatalf("a new cloud provider without a key: %v", err)
	}
}

const hooks = `onRecordCreate(async (e) => {
  const a = await $llm.text.completeAsync({ model: "fake/chat-1", prompt: "hi" })
  const pieces = []
  const s = await $llm.text.streamAsync({ model: "fake/chat-1", prompt: "hi" }, (t) => { pieces.push(t) })
  const stopped = await $llm.text.streamAsync({ model: "fake/chat-1", prompt: "hi" }, () => false)
  const img = await $llm.image.generateAsync({ model: "fake/gpt-image-1", prompt: "cat" })
  const f = $llm.file(img.files[0])
  const models = await $llm.text.modelsAsync()
  let syncGone = typeof $llm.text.complete === "undefined" && typeof $llm.text.stream === "undefined"
  e.record.set("result", JSON.stringify({ a: a.text, model: a.model, s: s.text, pieces: pieces.join("|"),
    stopped: stopped.stopReason + ":" + stopped.text, models: models.models.length, img: img.files[0].type,
    file: f.originalName, tokens: $llm.text.count("abcdefgh"), syncGone }))
  e.next()
}, "probes")
`

func TestJS(t *testing.T) {
	app, _ := setup(t)
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
		A, Model, S, Pieces, Stopped, Img, File string
		Models, Tokens                          int
		SyncGone                                bool
	}
	if err := json.Unmarshal([]byte(r.GetString("result")), &out); err != nil {
		t.Fatalf("result %q: %v", r.GetString("result"), err)
	}
	if out.A != "Hello" || out.Model != "fake/chat-1" || out.S != "Hello" || out.Pieces != "Hel|lo" || out.Models != 2 ||
		out.Stopped != "cancelled:Hel" || out.Img != "image/png" || out.File != "image-1.png" || out.Tokens != 2 || !out.SyncGone {
		t.Fatalf("from JS: %+v", out)
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
