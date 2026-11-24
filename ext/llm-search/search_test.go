package llmsearch

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tests"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/types"

	llmc "pocketone.dev/ext/contracts/llm"
	embedv1 "pocketone.dev/ext/contracts/llm/embed/v1"
	searchv1 "pocketone.dev/ext/contracts/llm/search/v1"
	"pocketone.dev/ext/extkit"
	"pocketone.dev/ext/extkit/js"
	"pocketone.dev/ext/jsvm"
	"pocketone.dev/ext/llm"
)

func init() {
	background = false
	RequestInterval = 0
	retryWait = 0
}

// fakeEmbed makes a bag-of-words vector (each word hashed to one of 64 dimensions), so texts that share words are
// close; it remembers every input and fails while fail is set. It lists two models: "p/cheap" and "p/dear".
type fakeEmbed struct {
	mu     sync.Mutex
	inputs []string
	models []string // the model of each input
	calls  int
	fail   error
	none   bool // list no models
}

func (f *fakeEmbed) Embed(_ context.Context, _ extkit.Auth, in embedv1.EmbedInput) (embedv1.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail != nil {
		return embedv1.Result{}, f.fail
	}
	res := embedv1.Result{Model: in.Model, Usage: llmc.Usage{CostMicroUSD: int64(len(in.Input))}}
	for _, text := range in.Input {
		f.inputs = append(f.inputs, text)
		f.models = append(f.models, in.Model)
		v := make([]float64, 64)
		for _, w := range wordRe.FindAllString(strings.ToLower(text), -1) {
			h := fnv.New32a()
			_, _ = h.Write([]byte(w))
			v[h.Sum32()%64]++
		}
		res.Vectors = append(res.Vectors, v)
	}
	return res, nil
}

func (f *fakeEmbed) Models(context.Context, extkit.Auth, llmc.ModelsInput) (llmc.Models, error) {
	if f.none {
		return llmc.Models{}, nil
	}
	return llmc.Models{Models: []llmc.ModelInfo{
		{ID: "p/dear", Provider: "p", Kinds: []string{"embed"}, Prices: []llmc.Price{{Unit: "1M input tokens", USD: 0.13}}},
		{ID: "p/cheap", Provider: "p", Kinds: []string{"embed"}, Prices: []llmc.Price{{Unit: "1M input tokens", USD: 0.05}}},
	}}, nil
}

func (f *fakeEmbed) embedded() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.inputs)
}

type fakeExt struct{ e *fakeEmbed }

func (fakeExt) Name() string { return "fake-embed" }
func (fakeExt) Contracts() extkit.Contracts {
	return extkit.Contracts{Implements: extkit.IDs("llm.embed/v1")}
}
func (x fakeExt) Register(app core.App, _ extkit.Config) error {
	embedv1.Contract.Provide(app, x.e)
	return nil
}

func setup(t *testing.T) (core.App, *fakeEmbed, searchv1.Search) {
	t.Helper()
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)
	fe := &fakeEmbed{}
	if _, err := extkit.Setup(app, []extkit.Extension{fakeExt{fe}, New()}, nil, nil); err != nil {
		t.Fatal(err)
	}
	return app, fe, searchv1.Contract.Must(app)
}

var ctx = context.Background()

const guide = `# Lighthouse

The keeper climbs the stairs every night to light the lamp.

## The fox

A red fox lives in the dunes and visits the lighthouse at dawn.

## Boats

Fishing boats follow the beam home through the fog.
`

func drain(t *testing.T, app core.App) {
	t.Helper()
	if err := of(app).drain(ctx); err != nil {
		t.Fatal(err)
	}
}

func mustSearch(t *testing.T, s searchv1.Search, in searchv1.SearchInput) searchv1.Results {
	t.Helper()
	r, err := s.Search(ctx, extkit.Internal, in)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func add(t *testing.T, s searchv1.Search, bucket, name, text string) searchv1.Source {
	t.Helper()
	src, err := s.Add(ctx, extkit.Internal, searchv1.AddInput{Bucket: bucket, Name: name, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func docNames(t *testing.T, s searchv1.Search, bucket, source string) string {
	t.Helper()
	l, err := s.Documents(ctx, extkit.Internal, searchv1.DocumentsInput{Bucket: bucket, Source: source})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range l.Documents {
		out = append(out, d.Name)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

// An app ensures its bucket; ensuring again keeps its settings and fills only what was empty.
func TestEnsure(t *testing.T) {
	_, _, s := setup(t)
	b, err := s.Ensure(ctx, extkit.Internal, searchv1.EnsureInput{Name: "storytime", Owner: "Story Time"})
	if err != nil || b.MaxDocuments != DefaultMaxDocuments || b.Owner != "Story Time" {
		t.Fatalf("ensure: %+v %v", b, err)
	}
	b, err = s.Ensure(ctx, extkit.Internal, searchv1.EnsureInput{Name: "storytime", Title: "Library", Owner: "x"})
	if err != nil || b.Title != "Library" || b.Owner != "Story Time" {
		t.Fatalf("ensure again: %+v %v", b, err)
	}
	if _, err := s.Ensure(ctx, extkit.Internal, searchv1.EnsureInput{Name: "../x"}); !errors.Is(err, searchv1.ErrInvalidName) {
		t.Fatalf("bad name: %v", err)
	}
}

// A file is a source with one document, embedded in the background with the app's model (the cheapest, picked
// automatically); search ranks across the buckets asked for and names each result's bucket and source.
func TestAddEmbedSearch(t *testing.T) {
	app, fe, s := setup(t)
	_, _ = s.Ensure(ctx, extkit.Internal, searchv1.EnsureInput{Name: "kb"})
	_, _ = s.Ensure(ctx, extkit.Internal, searchv1.EnsureInput{Name: "other"})
	src := add(t, s, "kb", "lighthouse.md", guide)
	if src.Kind != searchv1.KindFile || src.Documents != 1 || src.Pending != 1 || src.Chunks != 3 || src.Status != SourceReady || src.Hash == "" {
		t.Fatalf("add: %+v", src)
	}
	add(t, s, "other", "owl.md", "# Owl\n\nThe old owl watches the fox from the tower.")
	drain(t, app)
	if fe.embedded() != 4 || fe.models[0] != "p/cheap" || !strings.HasPrefix(fe.inputs[1], "Lighthouse › The fox\n\n") {
		t.Fatalf("embedded: %q with %q", fe.inputs, fe.models)
	}
	if st, _ := s.Status(ctx, extkit.Internal); st.Model != "p/cheap" || st.Chunks != 4 || st.Embedded != 4 {
		t.Fatalf("status: %+v", st)
	}
	r := mustSearch(t, s, searchv1.SearchInput{Buckets: []string{"kb"}, Query: "red fox dunes", Limit: 2})
	if len(r.Results) == 0 || r.Results[0].Heading != "Lighthouse › The fox" || r.Results[0].Bucket != "kb" || r.Results[0].Source != "lighthouse.md" {
		t.Fatalf("one bucket: %+v", r)
	}
	all := mustSearch(t, s, searchv1.SearchInput{Query: "fox", Limit: 10})
	got := map[string]bool{}
	for _, x := range all.Results {
		got[x.Bucket] = true
	}
	if !got["kb"] || !got["other"] {
		t.Fatalf("every bucket: %+v", all)
	}
	if _, err := s.Search(ctx, extkit.Internal, searchv1.SearchInput{Buckets: []string{"nope"}, Query: "fox"}); err == nil {
		t.Fatal("an unknown bucket was searched")
	}
	if _, err := s.Search(ctx, extkit.Internal, searchv1.SearchInput{Bucket: "kb", Query: `fox" OR NEAR(*`}); err != nil {
		t.Fatal(err)
	}
	b, _ := s.Bucket(ctx, extkit.Internal, searchv1.Ref{Name: "kb"})
	if b.Sources != 1 || b.Documents != 1 || b.Chunks != 3 || b.Pending != 0 || b.Bytes != int64(len(guide)) {
		t.Fatalf("bucket: %+v", b)
	}
}

// The same file again costs nothing; a changed one embeds only its new chunks; a new model embeds everything.
func TestReplaceAndModel(t *testing.T) {
	app, fe, s := setup(t)
	_, _ = s.Ensure(ctx, extkit.Internal, searchv1.EnsureInput{Name: "kb"})
	add(t, s, "kb", "a.md", guide)
	drain(t, app)
	add(t, s, "kb", "a.md", guide)
	drain(t, app)
	if fe.embedded() != 3 {
		t.Fatalf("the same text was embedded again: %d", fe.embedded())
	}
	add(t, s, "kb", "a.md", strings.Replace(guide, "follow the beam", "trust the beam", 1))
	drain(t, app)
	if fe.embedded() != 4 || !strings.Contains(fe.inputs[3], "trust the beam") {
		t.Fatalf("re-embedded more than the changed chunk: %q", fe.inputs)
	}
	st, err := s.SetModel(ctx, extkit.Internal, searchv1.ModelInput{Model: "p/dear"})
	if err != nil || st.Model != "p/dear" || st.Embedded != 0 {
		t.Fatalf("set model: %+v %v", st, err)
	}
	if r := mustSearch(t, s, searchv1.SearchInput{Bucket: "kb", Query: "trust", Limit: 1}); len(r.Results) != 1 {
		t.Fatalf("during the change keywords still find it: %+v", r)
	}
	drain(t, app)
	if st, _ := s.Status(ctx, extkit.Internal); st.Embedded != 3 || fe.models[len(fe.models)-1] != "p/dear" {
		t.Fatalf("after the change: %+v", st)
	}
	if src, err := s.Refresh(ctx, extkit.Internal, searchv1.SourceRef{Bucket: "kb", Source: "a.md"}); err != nil || src.Status != SourceReady {
		t.Fatalf("refresh: %+v %v", src, err)
	}
	if _, err := s.SetModel(ctx, extkit.Internal, searchv1.ModelInput{Model: "nomodel"}); err == nil {
		t.Fatal("a model without a provider was accepted")
	}
}

// Without an embedding model documents wait, search says what's missing, and nothing is lost.
func TestNoModel(t *testing.T) {
	app, fe, s := setup(t)
	fe.none = true
	_, _ = s.Ensure(ctx, extkit.Internal, searchv1.EnsureInput{Name: "kb"})
	add(t, s, "kb", "notes", "The fox jumps.\n\nThe owl sleeps.")
	drain(t, app)
	if _, err := s.Search(ctx, extkit.Internal, searchv1.SearchInput{Bucket: "kb", Query: "fox"}); !errors.Is(err, searchv1.ErrNoModel) {
		t.Fatalf("llm-search: %v", err)
	}
	if st, _ := s.Status(ctx, extkit.Internal); st.Model != "" || st.Note == "" {
		t.Fatalf("status: %+v", st)
	}
	fe.none = false // a provider is added
	drain(t, app)
	if r := mustSearch(t, s, searchv1.SearchInput{Bucket: "kb", Query: "fox"}); len(r.Results) != 1 || fe.embedded() != 2 {
		t.Fatalf("after a provider: %+v", r)
	}
}

// A dropped folder arrives file by file; Prune removes what the folder no longer has.
func TestFolder(t *testing.T) {
	app, _, s := setup(t)
	_, _ = s.Ensure(ctx, extkit.Internal, searchv1.EnsureInput{Name: "kb"})
	for _, p := range []string{"a.md", "b.md", "sub/c.txt"} {
		if _, err := s.Put(ctx, extkit.Internal, searchv1.PutInput{Bucket: "kb", Source: "docs", Path: p, Text: "text of " + p}); err != nil {
			t.Fatal(err)
		}
	}
	src, err := s.Prune(ctx, extkit.Internal, searchv1.PruneInput{Bucket: "kb", Source: "docs", Keep: []string{"a.md", "sub/c.txt"}})
	if err != nil || src.Kind != searchv1.KindFolder || src.Documents != 2 || src.Status != SourceReady {
		t.Fatalf("prune: %+v %v", src, err)
	}
	drain(t, app)
	if got := docNames(t, s, "kb", "docs"); got != "a.md,sub/c.txt" {
		t.Fatalf("documents: %s", got)
	}
}

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, text := range files {
		w, _ := zw.Create(name)
		_, _ = w.Write([]byte(text))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func upload(t *testing.T, s searchv1.Search, bucket, name, replace string, data []byte) searchv1.Source {
	t.Helper()
	src, err := s.Upload(ctx, extkit.Internal, extkit.RawIn{Query: url.Values{"bucket": {bucket}, "name": {name}, "replace": {replace}},
		Body: bytes.NewReader(data)})
	if err != nil {
		t.Fatal(err)
	}
	return src
}

// A zip is one source: its text files (the shared top folder dropped; .git, node_modules, lockfiles and binaries
// skipped). Uploading it again replaces it: unchanged files cost nothing, missing ones go. A renamed new version
// replaces the old one when it says so. The same zip in another bucket embeds nothing new.
func TestZip(t *testing.T) {
	app, fe, s := setup(t)
	_, _ = s.Ensure(ctx, extkit.Internal, searchv1.EnsureInput{Name: "kb"})
	_, _ = s.Ensure(ctx, extkit.Internal, searchv1.EnsureInput{Name: "other"})
	v1 := zipOf(t, map[string]string{
		"repo/README.md": "# Readme\n\nHello fox.", "repo/src/main.go": "package main\n\nfunc main() {}\n",
		"repo/notes.txt": "Old notes.", "repo/.git/config": "[core]", "repo/node_modules/x/index.js": "x",
		"repo/package-lock.json": "{}", "repo/logo.png": "\x89PNG\x00\x01", "__MACOSX/repo/._README.md": "\x00\x05junk",
	})
	src := upload(t, s, "kb", "repo.zip", "", v1)
	if src.Kind != searchv1.KindZip || src.Documents != 3 || src.Hash == "" {
		t.Fatalf("upload: %+v", src)
	}
	drain(t, app)
	if got := docNames(t, s, "kb", "repo.zip"); got != "README.md,notes.txt,src/main.go" {
		t.Fatalf("documents: %s", got)
	}
	first := fe.embedded()

	v2 := zipOf(t, map[string]string{"repo/README.md": "# Readme\n\nHello fox.", "repo/src/main.go": "package main\n\nfunc main() { run() }\n"})
	upload(t, s, "kb", "repo.zip", "", v2)
	drain(t, app)
	if got := docNames(t, s, "kb", "repo.zip"); got != "README.md,src/main.go" || fe.embedded() != first+1 {
		t.Fatalf("after v2: %s, %d embedded (was %d)", got, fe.embedded(), first)
	}

	v3 := zipOf(t, map[string]string{"repo/README.md": "# Readme\n\nHello fox and owl."})
	src = upload(t, s, "kb", "repo-v3.zip", "repo.zip", v3)
	if l, _ := s.Sources(ctx, extkit.Internal, searchv1.BucketRef{Bucket: "kb"}); len(l.Sources) != 1 || l.Sources[0].Name != "repo-v3.zip" || src.Documents != 1 {
		t.Fatalf("renamed version: %+v", l)
	}
	drain(t, app)

	// a loose file with the same content as a file in the zip is ready at once: nothing to embed
	before := fe.embedded()
	if src := add(t, s, "kb", "copy-of-readme.md", "# Readme\n\nHello fox and owl."); src.Pending != 0 {
		t.Fatalf("the loose copy waits: %+v", src)
	}

	upload(t, s, "other", "repo.zip", "", v3) // same content, another bucket
	drain(t, app)
	if fe.embedded() != before {
		t.Fatalf("the same content in another bucket was embedded again: %d -> %d", before, fe.embedded())
	}
	if r := mustSearch(t, s, searchv1.SearchInput{Bucket: "other", Query: "owl"}); len(r.Results) != 1 || r.Results[0].Source != "repo.zip" {
		t.Fatalf("the other bucket: %+v", r)
	}
	// replacing it in one bucket leaves the other's alone
	upload(t, s, "kb", "repo-v3.zip", "", zipOf(t, map[string]string{"repo/README.md": "# Readme\n\nSomething else."}))
	if r := mustSearch(t, s, searchv1.SearchInput{Bucket: "other", Query: "owl"}); len(r.Results) != 1 {
		t.Fatalf("the other bucket changed: %+v", r)
	}
}

// Limits, removing, deleting, binaries and a failed embed.
func TestLimitsRemoveDelete(t *testing.T) {
	app, fe, s := setup(t)
	_, _ = s.Ensure(ctx, extkit.Internal, searchv1.EnsureInput{Name: "kb"})
	one := 1
	_, _ = s.Update(ctx, extkit.Internal, searchv1.UpdateInput{Name: "kb", MaxDocuments: &one})
	add(t, s, "kb", "a.md", "alpha")
	if _, err := s.Add(ctx, extkit.Internal, searchv1.AddInput{Bucket: "kb", Name: "b.md", Text: "beta"}); err == nil {
		t.Fatal("over max documents")
	}
	if _, err := s.Upload(ctx, extkit.Internal, extkit.RawIn{Query: url.Values{"bucket": {"kb"}, "name": {"x.bin"}},
		Body: strings.NewReader("a\x00b")}); !errors.Is(err, searchv1.ErrNotText) {
		t.Fatalf("binary upload: %v", err)
	}
	fe.fail = llmc.ErrUnknownModel
	drain(t, app)
	docs, _ := s.Documents(ctx, extkit.Internal, searchv1.DocumentsInput{Bucket: "kb"})
	if docs.Documents[0].Status != StatusError || docs.Documents[0].Error == "" || fe.calls != 1 {
		t.Fatalf("a failed embed: %+v (calls %d)", docs, fe.calls)
	}
	fe.fail = nil
	if _, err := s.Refresh(ctx, extkit.Internal, searchv1.SourceRef{Bucket: "kb", Source: "a.md"}); err != nil {
		t.Fatal(err)
	}
	drain(t, app)
	if docs, _ = s.Documents(ctx, extkit.Internal, searchv1.DocumentsInput{Bucket: "kb"}); docs.Documents[0].Status != StatusReady {
		t.Fatalf("after refresh: %+v", docs)
	}
	if n, _ := s.Remove(ctx, extkit.Internal, searchv1.RemoveInput{Bucket: "kb", Sources: []string{"a.md", "nope"}}); n.Count != 1 {
		t.Fatalf("remove: %+v", n)
	}
	add(t, s, "kb", "b.md", "beta")
	if _, err := s.Delete(ctx, extkit.Internal, searchv1.Ref{Name: "kb"}); err != nil {
		t.Fatal(err)
	}
	var left, chunks, srcs int
	_ = app.DB().Select("count(*)").From(FTSTable).Row(&left)
	_ = app.DB().Select("count(*)").From(ChunksCollection).Row(&chunks)
	_ = app.DB().Select("count(*)").From(SourcesCollection).Row(&srcs)
	if left != 0 || chunks != 0 || srcs != 0 {
		t.Fatalf("left behind: %d fts rows, %d chunks, %d sources", left, chunks, srcs)
	}
}

// A page source is fetched and kept as Markdown; an unchanged page (its ETag) costs nothing; a changed one is
// replaced; an interval reads it again when due.
func TestPage(t *testing.T) {
	app, fe, s := setup(t)
	_, _ = s.Ensure(ctx, extkit.Internal, searchv1.EnsureInput{Name: "kb"})
	var mu sync.Mutex
	body, etag, hits, notModified := `<html><head><title>Fox guide</title><script>var x</script></head><body><nav>Menu</nav>
<h1>Foxes</h1><p>The red <b>fox</b> lives in dunes.</p><ul><li>Naps</li><li>Hunts</li></ul><pre>code  here</pre></body></html>`, `"v1"`, 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits++
		if r.Header.Get("If-None-Match") == etag {
			notModified++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("ETag", etag)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	src, err := s.AddURL(ctx, extkit.Internal, searchv1.URLInput{Bucket: "kb", URL: srv.URL + "/guide", Every: "day"})
	if err != nil || src.Kind != searchv1.KindPage || src.Status != SourceReady || src.Documents != 1 || src.Next == nil {
		t.Fatalf("add url: %+v %v", src, err)
	}
	drain(t, app)
	r := mustSearch(t, s, searchv1.SearchInput{Bucket: "kb", Query: "red fox dunes", Limit: 1})
	if len(r.Results) != 1 || r.Results[0].Heading != "Fox guide › Foxes" || strings.Contains(r.Results[0].Text, "var x") || strings.Contains(r.Results[0].Text, "Menu") {
		t.Fatalf("page: %+v", r)
	}
	if _, err := s.Refresh(ctx, extkit.Internal, searchv1.SourceRef{Bucket: "kb", Source: srv.URL + "/guide"}); err != nil {
		t.Fatal(err)
	}
	n := fe.embedded()
	mu.Lock()
	body, etag = strings.Replace(body, "dunes", "woods", 1), `"v2"`
	mu.Unlock()
	// due now: the scheduled read picks it up
	if _, err := app.DB().NewQuery("UPDATE " + SourcesCollection + " SET next = '2000-01-01 00:00:00.000Z'").Execute(); err != nil {
		t.Fatal(err)
	}
	of(app).refreshDue()
	drain(t, app)
	if r := mustSearch(t, s, searchv1.SearchInput{Bucket: "kb", Query: "woods", Limit: 1}); len(r.Results) != 1 || !strings.Contains(r.Results[0].Text, "woods") || fe.embedded() == n {
		t.Fatalf("after a change: %+v", r)
	}
	mu.Lock()
	defer mu.Unlock()
	if notModified != 0 && hits < 3 {
		t.Fatalf("hits %d, not modified %d", hits, notModified)
	}
	if _, err := s.AddURL(ctx, extkit.Internal, searchv1.URLInput{Bucket: "kb", URL: "file:///etc/passwd"}); err == nil {
		t.Fatal("a file URL was accepted")
	}
}

// fakeGit answers the Git protocol's first question (/info/refs) like a repository whose HEAD is main.
func fakeGit(t *testing.T) *httptest.Server {
	t.Helper()
	pkt := func(line string) string { return fmt.Sprintf("%04x%s", len(line)+4, line) }
	sha := strings.Repeat("ab", 20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/info/refs") || r.URL.Query().Get("service") != "git-upload-pack" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = io.WriteString(w, pkt("# service=git-upload-pack\n")+"0000"+
			pkt(sha+" HEAD\x00symref=HEAD:refs/heads/main agent=fake\n")+pkt(sha+" refs/heads/main\n")+"0000")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A URL is a repository when its server answers the Git protocol, with or without .git; anything else is a page.
func TestDetectURL(t *testing.T) {
	_, _, s := setup(t)
	git := fakeGit(t)
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<h1>Hello</h1>")
	}))
	defer page.Close()
	for raw, want := range map[string]string{git.URL + "/owner/repo": "git", git.URL + "/owner/repo.git": "git", page.URL + "/owner/repo": "page",
		page.URL + "/x.git": "page"} {
		got, err := s.DetectURL(ctx, extkit.Internal, searchv1.URLRef{URL: raw})
		if err != nil || got.Kind != want {
			t.Errorf("%s: %+v %v, want %s", raw, got, err, want)
		}
	}
	if _, err := s.DetectURL(ctx, extkit.Internal, searchv1.URLRef{URL: "ftp://example.com/x"}); err == nil {
		t.Fatal("an ftp URL was accepted")
	}
	for p, skip := range map[string]bool{"src/a.go": false, ".git/HEAD": true, "a/node_modules/x.js": true, "go.sum": true,
		"docs/old/x.md": true, "x.min.js": true, ".github/ci.yml": true} {
		if got := skipPath(p, []string{"docs/old", "*.min.js"}); got != skip {
			t.Errorf("skip %s: %v", p, got)
		}
	}
}

// A Git source against a real repository (set SEARCH_GIT_TEST=1; it fetches over the network).
func TestGitNetwork(t *testing.T) {
	if os.Getenv("SEARCH_GIT_TEST") == "" {
		t.Skip("set SEARCH_GIT_TEST=1 to clone a public repository")
	}
	app, _, s := setup(t)
	_, _ = s.Ensure(ctx, extkit.Internal, searchv1.EnsureInput{Name: "kb"})
	src, err := s.AddURL(ctx, extkit.Internal, searchv1.URLInput{Bucket: "kb", URL: "https://github.com/benallfree/nollama.git", Commits: 20})
	if err != nil || src.Kind != searchv1.KindGit || src.Status != SourceReady || src.Documents < 20 {
		t.Fatalf("git: %+v %v", src, err)
	}
	names := docNames(t, s, "kb", src.Name)
	if !strings.Contains(names, "README.md") || !strings.Contains(names, "commit ") || strings.Contains(names, ".git/") {
		t.Fatalf("documents: %s", names)
	}
	again, _ := s.Refresh(ctx, extkit.Internal, searchv1.SourceRef{Bucket: "kb", Source: src.Name})
	if again.Documents != src.Documents {
		t.Fatalf("refresh: %+v", again)
	}
	_ = app
}

// A collection is a source: its records are documents kept in sync, two buckets following it embed each record once,
// and a search for a viewer leaves out what they can't view.
func TestFollowCollection(t *testing.T) {
	app, fe, s := setup(t)
	members := core.NewAuthCollection("members")
	if err := app.Save(members); err != nil {
		t.Fatal(err)
	}
	notes := core.NewBaseCollection("notes")
	notes.ViewRule = types.Pointer("owner = @request.auth.id")
	notes.Fields.Add(&core.TextField{Name: "title"}, &core.EditorField{Name: "body"}, &core.BoolField{Name: "published"},
		&core.RelationField{Name: "owner", CollectionId: members.Id, MaxSelect: 1})
	if err := app.Save(notes); err != nil {
		t.Fatal(err)
	}
	member := func(email string) *core.Record {
		r := core.NewRecord(members)
		r.SetEmail(email)
		r.SetPassword("password123")
		if err := app.Save(r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	ann, bob := member("ann@example.com"), member("bob@example.com")
	note := func(title, body, owner string, published bool) *core.Record {
		r := core.NewRecord(notes)
		r.Set("title", title)
		r.Set("body", body)
		r.Set("owner", owner)
		r.Set("published", published)
		if err := app.Save(r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	n1 := note("Fox facts", "<p>The <b>red fox</b> hunts at dawn.</p>", ann.Id, true) // before anything follows
	_, _ = s.Ensure(ctx, extkit.Internal, searchv1.EnsureInput{Name: "notes"})
	src, err := s.Follow(ctx, extkit.Internal, searchv1.FollowInput{Bucket: "notes", Collection: "notes", Fields: []string{"title", "body"}, Filter: "published = true"})
	if err != nil || src.Kind != searchv1.KindCollection || src.Name != "notes" || src.Documents != 1 {
		t.Fatalf("follow: %+v %v", src, err)
	}
	n2 := note("Fox dens", "<p>A fox den has many exits.</p>", bob.Id, true)
	_ = note("Fox secrets", "<p>Draft about the fox.</p>", ann.Id, false) // filtered out
	drain(t, app)
	if got := docNames(t, s, "notes", "notes"); got != "notes/"+min(n1.Id, n2.Id)+",notes/"+max(n1.Id, n2.Id) {
		t.Fatalf("documents: %s", got)
	}

	// a second bucket on the same collection embeds nothing new
	before := fe.embedded()
	_, _ = s.Ensure(ctx, extkit.Internal, searchv1.EnsureInput{Name: "kb"})
	if _, err := s.Follow(ctx, extkit.Internal, searchv1.FollowInput{Bucket: "kb", Collection: "notes", Fields: []string{"title", "body"}, Filter: "published = true"}); err != nil {
		t.Fatal(err)
	}
	drain(t, app)
	if fe.embedded() != before {
		t.Fatalf("the second bucket embedded again: %d -> %d", before, fe.embedded())
	}

	r := mustSearch(t, s, searchv1.SearchInput{Bucket: "notes", Query: "red fox hunts", Limit: 1})
	if len(r.Results) != 1 || r.Results[0].Record.ID != n1.Id || r.Results[0].Heading != "Fox facts" || strings.Contains(r.Results[0].Text, "<b>") {
		t.Fatalf("llm-search: %+v", r)
	}
	ids := func(r searchv1.Results) string {
		var out []string
		for _, x := range r.Results {
			out = append(out, x.Record.ID)
		}
		return strings.Join(out, ",")
	}
	all := mustSearch(t, s, searchv1.SearchInput{Bucket: "notes", Query: "fox"})
	asAnn := mustSearch(t, s, searchv1.SearchInput{Bucket: "notes", Query: "fox", Viewer: &searchv1.Viewer{Collection: "members", ID: ann.Id}})
	asGuest := mustSearch(t, s, searchv1.SearchInput{Bucket: "notes", Query: "fox", Viewer: &searchv1.Viewer{}})
	if len(all.Results) != 2 || ids(asAnn) != n1.Id || len(asGuest.Results) != 0 {
		t.Fatalf("viewers: all %s, ann %s, guest %s", ids(all), ids(asAnn), ids(asGuest))
	}

	// enrich follows the same rules: a guest gets no passages from these records, ann gets hers
	if _, p, err := searchv1.Enrich(ctx, app, &llmc.Enrich{Buckets: []string{"notes"}, Viewer: &llmc.Viewer{}}, "fox"); err != nil || len(p) != 0 {
		t.Fatalf("enrich as a guest: %+v %v", p, err)
	}
	if _, p, err := searchv1.Enrich(ctx, app, &llmc.Enrich{Buckets: []string{"notes"}, Viewer: &llmc.Viewer{Collection: "members", ID: ann.Id}}, "fox"); err != nil || len(p) != 1 {
		t.Fatalf("enrich as ann: %+v %v", p, err)
	}

	n1.Set("body", "<p>The red fox naps in the sun.</p>")
	_ = app.Save(n1)
	drain(t, app)
	if r := mustSearch(t, s, searchv1.SearchInput{Bucket: "kb", Query: "naps sun", Limit: 1}); len(r.Results) != 1 || !strings.Contains(r.Results[0].Text, "naps") {
		t.Fatalf("after an edit, in the second bucket: %+v", r)
	}
	n1.Set("published", false)
	_ = app.Save(n1)
	_ = app.Delete(n2)
	if got := docNames(t, s, "notes", "notes"); got != "" {
		t.Fatalf("after unpublish and delete: %s", got)
	}
	if _, err := s.Follow(ctx, extkit.Internal, searchv1.FollowInput{Bucket: "kb", Collection: "notes", Fields: []string{"nope"}}); err == nil {
		t.Fatal("a missing field was accepted")
	}
}

// Enrich searches the buckets and words the passages for a prompt.
func TestEnrich(t *testing.T) {
	app, _, s := setup(t)
	_, _ = s.Ensure(ctx, extkit.Internal, searchv1.EnsureInput{Name: "kb"})
	add(t, s, "kb", "a.md", guide)
	drain(t, app)
	var e llmc.Enrich
	if err := json.Unmarshal([]byte(`["kb"]`), &e); err != nil || len(e.Buckets) != 1 {
		t.Fatalf("shorthand: %+v %v", e, err)
	}
	text, passages, err := searchv1.Enrich(ctx, app, &llmc.Enrich{Buckets: []string{"kb"}, Limit: 2}, "where does the fox live?")
	if err != nil || len(passages) != 2 || passages[0].Heading != "Lighthouse › The fox" || !strings.Contains(text, "[1] a.md › Lighthouse › The fox\nA red fox") {
		t.Fatalf("enrich: %q %+v %v", text, passages, err)
	}
	if text, p, err := searchv1.Enrich(ctx, app, nil, "x"); text != "" || p != nil || err != nil {
		t.Fatal("enrich without buckets did something")
	}
}

const hooks = `onRecordCreate(async (e) => {
  const b = $llm.search.ensure({ name: "storytime", title: "Story Time library" })
  const src = $llm.search.add({ bucket: "storytime", name: "fox.md", text: "# Fox\n\nThe red fox naps in the dunes." })
  const r = await $llm.search.searchAsync({ buckets: "storytime", query: "fox", viewer: null })
  // operations whose path isn't their name (add-folder, add-url) are reached too: these fail on their input
  const reached = []
  for (const call of [() => $llm.search.addFolder({ bucket: "storytime", path: "no/such/folder" }), () => $llm.search.addURL({ bucket: "storytime", url: "ftp://x" })]) {
    try { call() } catch (err) { if (!String(err).includes("has no operation")) reached.push(true) }
  }
  e.record.set("result", JSON.stringify({ reached: reached.length,
    title: b.title, kind: src.kind, sources: $llm.search.sources("storytime").length, docs: $llm.search.documents("storytime").length,
    heading: r.results[0].heading, source: r.results[0].source, model: $llm.search.status().model,
    removed: $llm.search.remove("storytime", "fox.md"),
  }))
  e.next()
}, "probes")
`

func TestJS(t *testing.T) {
	app, _, _ := setup(t)
	c := core.NewBaseCollection("probes")
	c.Fields.Add(&core.TextField{Name: "result", Max: 10000})
	if err := app.Save(c); err != nil {
		t.Fatal(err)
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
	r := core.NewRecord(c)
	if err := app.Save(r); err != nil {
		t.Fatal(err)
	}
	var out struct {
		Title, Kind, Heading, Source, Model string
		Sources, Docs, Removed, Reached     int
	}
	if err := json.Unmarshal([]byte(r.GetString("result")), &out); err != nil {
		t.Fatalf("result %q: %v", r.GetString("result"), err)
	}
	if out.Title != "Story Time library" || out.Kind != "file" || out.Sources != 1 || out.Docs != 1 ||
		out.Heading != "Fox" || out.Source != "fox.md" || out.Model != "p/cheap" || out.Removed != 1 || out.Reached != 2 {
		t.Fatalf("from JS: %+v", out)
	}
}

// A file that can't be added doesn't fail its source: the rest go in and the source says what's missing. A read a
// restart cut off is picked up again.
func TestPartialAndResume(t *testing.T) {
	app, _, s := setup(t)
	_, _ = s.Ensure(ctx, extkit.Internal, searchv1.EnsureInput{Name: "kb"})
	src := upload(t, s, "kb", "mixed.zip", "", zipOf(t, map[string]string{"ok.md": "# Fine", strings.Repeat("x", 1001) + ".md": "too long a name"}))
	if src.Status != SourceReady || src.Documents != 1 || !strings.Contains(src.Error, "1 of 2 files not added") {
		t.Fatalf("partial: %+v", src)
	}
	if _, err := app.DB().NewQuery("UPDATE " + SourcesCollection + " SET status = 'reading'").Execute(); err != nil {
		t.Fatal(err)
	}
	of(app).resumeReads()
	if l, _ := s.Sources(ctx, extkit.Internal, searchv1.BucketRef{Bucket: "kb"}); l.Sources[0].Status != SourceReady {
		t.Fatalf("after resuming: %+v", l.Sources[0])
	}
}

// Sample draws chunks at random from the buckets named, without embedding anything.
func TestSample(t *testing.T) {
	app, fe, s := setup(t)
	_, _ = s.Ensure(ctx, extkit.Internal, searchv1.EnsureInput{Name: "kb"})
	_, _ = s.Ensure(ctx, extkit.Internal, searchv1.EnsureInput{Name: "other"})
	add(t, s, "kb", "lighthouse.md", guide)
	add(t, s, "other", "owl.md", "# Owl\n\nThe old owl watches the fox from the tower.")
	drain(t, app)
	before := fe.embedded()
	r, err := s.Sample(ctx, extkit.Internal, searchv1.SampleInput{Bucket: "kb", Count: 10})
	if err != nil || len(r.Results) != 3 || fe.embedded() != before {
		t.Fatalf("sample kb: %+v %v (embedded %d -> %d)", r, err, before, fe.embedded())
	}
	for _, x := range r.Results {
		if x.Bucket != "kb" || x.Document != "lighthouse.md" || x.Text == "" || x.Score != 0 {
			t.Fatalf("a sampled chunk: %+v", x)
		}
	}
	if r, _ := s.Sample(ctx, extkit.Internal, searchv1.SampleInput{Count: 1}); len(r.Results) != 1 {
		t.Fatalf("count: %+v", r)
	}
	if r, _ := s.Sample(ctx, extkit.Internal, searchv1.SampleInput{Prefix: "owl", Count: 10}); len(r.Results) != 1 || r.Results[0].Bucket != "other" {
		t.Fatalf("prefix: %+v", r)
	}
	if _, err := s.Sample(ctx, extkit.Internal, searchv1.SampleInput{Bucket: "nope"}); err == nil {
		t.Fatal("an unknown bucket was sampled")
	}
}
