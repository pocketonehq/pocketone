package llmsearch

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/filesystem"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/types"

	searchv1 "pocketone.dev/ext/contracts/llm/search/v1"
	"pocketone.dev/ext/extkit"
)

// Source statuses.
const (
	SourceReading = "reading"
	SourceReady   = "ready"
	SourceError   = "error"
)

// MaxZipBytes is the largest zip upload.
const MaxZipBytes = 100 << 20

// spec is a source's settings, stored as JSON.
type spec struct {
	URL        string   `json:"url,omitempty"`
	Every      string   `json:"every,omitempty"`
	Commits    int      `json:"commits,omitempty"`
	Exclude    []string `json:"exclude,omitempty"`
	Collection string   `json:"collection,omitempty"`
	Fields     []string `json:"fields,omitempty"`
	Filter     string   `json:"filter,omitempty"`
	Path       string   `json:"path,omitempty"`
	Include    []string `json:"include,omitempty"`
	PageURL    string   `json:"pageURL,omitempty"`
}

func specOf(r *core.Record) spec {
	var sp spec
	_ = json.Unmarshal([]byte(r.GetString("spec")), &sp)
	return sp
}

func setSpec(r *core.Record, sp spec) {
	b, _ := json.Marshal(sp)
	r.Set("spec", types.JSONRaw(b))
}

func (s *service) findSource(bucketID, name string) (*core.Record, error) {
	r, err := s.app.FindFirstRecordByFilter(SourcesCollection, "bucket = {:b} && name = {:n}", dbx.Params{"b": bucketID, "n": name})
	if err != nil {
		return nil, searchv1.ErrNoSource
	}
	return r, nil
}

type sourceStats struct {
	Src       string `db:"src"`
	Embedded  int    `db:"-"`
	Documents int    `db:"documents"`
	Bytes     int64  `db:"bytes"`
	Chunks    int    `db:"chunks"`
	Pending   int    `db:"pending"`
	Cost      int64  `db:"cost"`
}

func (s *service) sourceStats(bucketID string) map[string]sourceStats {
	var rows []sourceStats
	_ = s.app.DB().Select("src", "count(*) AS documents", "coalesce(sum(size),0) AS bytes", "coalesce(sum(chunks),0) AS chunks",
		"coalesce(sum(status IN ('queued','embedding')),0) AS pending", "coalesce(sum(cost),0) AS cost").
		From(DocumentsCollection).Where(dbx.HashExp{"bucket": bucketID}).GroupBy("src").All(&rows)
	out := map[string]sourceStats{}
	for _, r := range rows {
		out[r.Src] = r
	}
	for src, n := range s.embeddedBy(bucketID, "d.src") {
		st := out[src]
		st.Embedded = n
		out[src] = st
	}
	return out
}

// embeddedBy counts a bucket's chunks with the app's model's vectors, grouped by a documents column (d.src, d.id).
func (s *service) embeddedBy(bucketID, column string) map[string]int {
	out := map[string]int{}
	model := s.knownModel()
	if model == "" {
		return out
	}
	var rows []struct {
		Key string `db:"k"`
		N   int    `db:"n"`
	}
	_ = s.app.DB().Select(column+" AS k", "count(*) AS n").From(ChunksCollection+" c").
		InnerJoin(DocumentsCollection+" d", dbx.NewExp("d.id = c.document")).
		Where(dbx.And(dbx.HashExp{"c.bucket": bucketID, "c.model": model}, dbx.NewExp("c.vector != ''"))).
		GroupBy(column).All(&rows)
	for _, r := range rows {
		out[r.Key] = r.N
	}
	return out
}

func timePtr(d types.DateTime) *time.Time {
	if d.IsZero() {
		return nil
	}
	t := d.Time()
	return &t
}

func sourceOf(bucket string, r *core.Record, st sourceStats) searchv1.Source {
	sp := specOf(r)
	return searchv1.Source{Bucket: bucket, Name: r.GetString("name"), Kind: r.GetString("kind"), URL: sp.URL, Every: sp.Every,
		Commits: sp.Commits, Exclude: sp.Exclude, Collection: sp.Collection, Fields: sp.Fields, Filter: sp.Filter,
		Path: sp.Path, Include: sp.Include, PageURL: sp.PageURL,
		Hash: r.GetString("hash"), Status: r.GetString("status"), Error: r.GetString("error"), Documents: st.Documents,
		Chunks: st.Chunks, Embedded: st.Embedded, Bytes: st.Bytes, Pending: st.Pending, CostMicroUSD: st.Cost,
		Refreshed: timePtr(r.GetDateTime("refreshed")), Next: timePtr(r.GetDateTime("next")), Created: r.GetDateTime("created").Time()}
}

func (s *service) sourceResult(b, src *core.Record) (searchv1.Source, error) {
	fresh, err := s.app.FindRecordById(SourcesCollection, src.Id)
	if err != nil {
		return searchv1.Source{}, err
	}
	return sourceOf(b.GetString("name"), fresh, s.sourceStats(b.Id)[fresh.Id]), nil
}

// Sources implements search/v1.
func (s *service) Sources(_ context.Context, _ extkit.Auth, in searchv1.BucketRef) (searchv1.SourceList, error) {
	b, err := s.findBucket(in.Bucket)
	if err != nil {
		return searchv1.SourceList{}, err
	}
	recs, err := s.app.FindRecordsByFilter(SourcesCollection, "bucket = {:b}", "name", 0, 0, dbx.Params{"b": b.Id})
	if err != nil {
		return searchv1.SourceList{}, err
	}
	st := s.sourceStats(b.Id)
	out := searchv1.SourceList{Sources: []searchv1.Source{}}
	for _, r := range recs {
		out.Sources = append(out.Sources, sourceOf(in.Bucket, r, st[r.Id]))
	}
	return out, nil
}

// putSource finds the source named name in the bucket (or replace, renamed to name) and sets its kind and spec,
// making it when it's missing. A source that changes kind loses its documents.
func (s *service) putSource(b *core.Record, name, replace, kind string, sp spec) (*core.Record, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 2000 {
		return nil, badRequest("give the source a name")
	}
	src, err := s.findSource(b.Id, name)
	if err != nil && replace != "" && replace != name {
		if old, err2 := s.findSource(b.Id, replace); err2 == nil {
			src, err = old, nil // a new version under a new name
		}
	}
	if err != nil {
		col, err := s.app.FindCachedCollectionByNameOrId(SourcesCollection)
		if err != nil {
			return nil, err
		}
		src = core.NewRecord(col)
		src.Set("bucket", b.Id)
	} else if src.GetString("kind") != kind {
		if err := s.clearSource(b.Id, src); err != nil {
			return nil, err
		}
	}
	src.Set("name", name)
	src.Set("kind", kind)
	setSpec(src, sp)
	if src.GetString("status") == "" {
		src.Set("status", SourceReady)
	}
	if err := s.app.Save(src); err != nil {
		return nil, err
	}
	return src, nil
}

// clearSource deletes a source's documents.
func (s *service) clearSource(bucketID string, src *core.Record) error {
	docs, err := s.app.FindAllRecords(DocumentsCollection, dbx.HashExp{"src": src.Id})
	if err != nil {
		return err
	}
	for _, d := range docs {
		if err := s.removeDoc(bucketID, d); err != nil {
			return err
		}
	}
	return nil
}

// syncDocs makes a source hold exactly docs: each added (unchanged ones cost nothing), the rest removed. A document
// that can't be added doesn't stop the others: they're reported together as a partial error (the source stays
// usable); only the bucket's limits stop it.
func (s *service) syncDocs(b, src *core.Record, docs []docIn, force bool) error {
	keep := map[string]bool{}
	var failed []string
	for _, d := range docs {
		keep[strings.TrimSpace(d.name)] = true
		if _, _, err := s.addDoc(b, src, d, addOpts{force: force}); err != nil {
			if isLimit(err) {
				return err
			}
			failed = append(failed, d.name+": "+errText(err))
		}
	}
	if err := s.prune(b, src, keep); err != nil {
		return err
	}
	if len(failed) > 0 {
		return &partialError{failed: failed, of: len(docs)}
	}
	return nil
}

// partialError: some of a source's documents couldn't be added; the rest are there.
type partialError struct {
	failed []string
	of     int
}

func (e *partialError) Error() string {
	msg := fmt.Sprintf("%d of %d files not added: %s", len(e.failed), e.of, e.failed[0])
	if len(e.failed) > 1 {
		msg += fmt.Sprintf(" (and %d more)", len(e.failed)-1)
	}
	return msg
}

func isLimit(err error) bool {
	e, ok := err.(*extkit.Error)
	return ok && e.Code == searchv1.ErrTooLarge.Code
}

// prune removes a source's documents that aren't in keep.
func (s *service) prune(b, src *core.Record, keep map[string]bool) error {
	docs, err := s.app.FindAllRecords(DocumentsCollection, dbx.HashExp{"src": src.Id})
	if err != nil {
		return err
	}
	for _, d := range docs {
		if !keep[d.GetString("name")] {
			if err := s.removeDoc(b.Id, d); err != nil {
				return err
			}
		}
	}
	return nil
}

// finish records how a read went: ready (or error), when, what was read, and when it's due again.
func (s *service) finish(src *core.Record, readErr error, state string) {
	fresh, err := s.app.FindRecordById(SourcesCollection, src.Id)
	if err != nil {
		return // removed meanwhile
	}
	var partial *partialError
	if errors.As(readErr, &partial) {
		// usable: ready, with a note of what's missing
		fresh.Set("status", SourceReady)
		fresh.Set("error", partial.Error())
		if state != "" {
			fresh.Set("state", state)
		}
	} else if readErr != nil {
		fresh.Set("status", SourceError)
		msg := errText(readErr)
		if len(msg) > 2000 {
			msg = msg[:2000]
		}
		fresh.Set("error", msg)
	} else {
		fresh.Set("status", SourceReady)
		fresh.Set("error", "")
		if state != "" {
			fresh.Set("state", state)
		}
	}
	now := time.Now().UTC()
	fresh.Set("refreshed", now)
	d := interval(specOf(fresh).Every)
	if fresh.GetString("kind") == searchv1.KindDir {
		d = DirEvery
	}
	if d > 0 {
		fresh.Set("next", now.Add(d))
	} else {
		fresh.Set("next", "")
	}
	if err := s.app.Save(fresh); err != nil {
		s.app.Logger().Warn("llm-search: saving a source failed", "source", fresh.GetString("name"), "error", err)
	}
}

func interval(every string) time.Duration {
	switch every {
	case searchv1.EveryHour:
		return time.Hour
	case searchv1.EveryDay:
		return 24 * time.Hour
	case searchv1.EveryWeek:
		return 7 * 24 * time.Hour
	}
	return 0
}

// ---- files, text, folders, zips

// Add implements search/v1.
func (s *service) Add(_ context.Context, _ extkit.Auth, in searchv1.AddInput) (searchv1.Source, error) {
	kind := in.Kind
	if kind == "" {
		kind = searchv1.KindFile
	}
	if kind != searchv1.KindFile && kind != searchv1.KindText {
		return searchv1.Source{}, badRequest("kind is file or text")
	}
	return s.addFile(in.Bucket, in.Name, in.Replace, kind, in.Text)
}

func (s *service) addFile(bucket, name, replace, kind, text string) (searchv1.Source, error) {
	b, err := s.findBucket(bucket)
	if err != nil {
		return searchv1.Source{}, err
	}
	if !isText([]byte(text)) {
		return searchv1.Source{}, searchv1.ErrNotText
	}
	src, err := s.putSource(b, name, replace, kind, spec{})
	if err != nil {
		return searchv1.Source{}, err
	}
	src.Set("hash", sha(text))
	if err := s.app.Save(src); err != nil {
		return searchv1.Source{}, err
	}
	err = s.syncDocs(b, src, []docIn{{name: src.GetString("name"), text: text}}, false)
	s.finish(src, err, "")
	var partial *partialError
	if errors.As(err, &partial) {
		return searchv1.Source{}, badRequest(partial.failed[0])
	} else if err != nil {
		return searchv1.Source{}, err
	}
	return s.sourceResult(b, src)
}

// Upload implements search/v1.
func (s *service) Upload(_ context.Context, _ extkit.Auth, in extkit.RawIn) (searchv1.Source, error) {
	if in.Body == nil {
		return searchv1.Source{}, badRequest("send the file as the request body")
	}
	name := strings.TrimSpace(in.Query.Get("name"))
	isZip := strings.HasSuffix(strings.ToLower(name), ".zip")
	limit := int64(MaxFileBytes)
	if isZip {
		limit = MaxZipBytes
	}
	data, err := io.ReadAll(io.LimitReader(in.Body, limit+1))
	if err != nil {
		return searchv1.Source{}, err
	}
	if int64(len(data)) > limit {
		return searchv1.Source{}, tooLarge(fmt.Sprintf("%s is over %d MB", name, limit>>20))
	}
	if !isZip {
		return s.addFile(in.Query.Get("bucket"), name, in.Query.Get("replace"), searchv1.KindFile, string(data))
	}
	b, err := s.findBucket(in.Query.Get("bucket"))
	if err != nil {
		return searchv1.Source{}, err
	}
	docs, err := zipDocs(data)
	if err != nil {
		return searchv1.Source{}, badRequest(name + ": " + err.Error())
	}
	src, err := s.putSource(b, name, in.Query.Get("replace"), searchv1.KindZip, spec{})
	if err != nil {
		return searchv1.Source{}, err
	}
	file, err := filesystem.NewFileFromBytes(data, "source.zip")
	if err != nil {
		return searchv1.Source{}, err
	}
	src.Set("original", file)
	src.Set("hash", sha(string(data)))
	src.Set("status", SourceReading)
	if err := s.app.Save(src); err != nil {
		return searchv1.Source{}, err
	}
	err = s.syncDocs(b, src, docs, false)
	s.finish(src, err, "")
	if err != nil && isLimit(err) {
		return searchv1.Source{}, err
	}
	return s.sourceResult(b, src)
}

// zipDocs is the text files in a zip, by path (a single top folder they all share, as a repository download has,
// is dropped), skipping what multi-file sources always skip.
func zipDocs(data []byte) ([]docIn, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("not a zip file")
	}
	// macOS adds __MACOSX/ (resource forks) beside the files; it isn't content, and it would hide the shared top folder
	junk := func(name string) bool { return strings.HasPrefix(name, "__MACOSX/") }
	var names []string
	for _, f := range zr.File {
		if !f.FileInfo().IsDir() && !junk(f.Name) {
			names = append(names, f.Name)
		}
	}
	top := commonTop(names)
	var docs []docIn
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || junk(f.Name) {
			continue
		}
		p := strings.TrimPrefix(f.Name, top)
		if skipPath(p, nil) || int64(f.UncompressedSize64) > MaxMultiFileBytes {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(rc, MaxMultiFileBytes+1))
		rc.Close()
		if err != nil || !isText(b) {
			continue
		}
		docs = append(docs, docIn{name: p, text: string(b)})
	}
	return docs, nil
}

// commonTop is "dir/" when every path starts with the same top folder.
func commonTop(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	first, _, ok := strings.Cut(paths[0], "/")
	if !ok {
		return ""
	}
	for _, p := range paths[1:] {
		if !strings.HasPrefix(p, first+"/") {
			return ""
		}
	}
	return first + "/"
}

// Put implements search/v1.
func (s *service) Put(_ context.Context, _ extkit.Auth, in searchv1.PutInput) (searchv1.Document, error) {
	b, err := s.findBucket(in.Bucket)
	if err != nil {
		return searchv1.Document{}, err
	}
	src, err := s.findSource(b.Id, in.Source)
	if err != nil || src.GetString("kind") != searchv1.KindFolder {
		if src, err = s.putSource(b, in.Source, in.Replace, searchv1.KindFolder, spec{}); err != nil {
			return searchv1.Document{}, err
		}
	}
	if src.GetString("status") != SourceReading {
		src.Set("status", SourceReading)
		if err := s.app.Save(src); err != nil {
			return searchv1.Document{}, err
		}
	}
	doc, _, err := s.addDoc(b, src, docIn{name: in.Path, text: in.Text}, addOpts{})
	if err != nil {
		return searchv1.Document{}, err
	}
	return documentOf(b.GetString("name"), src.GetString("name"), doc), nil
}

// Prune implements search/v1.
func (s *service) Prune(_ context.Context, _ extkit.Auth, in searchv1.PruneInput) (searchv1.Source, error) {
	b, err := s.findBucket(in.Bucket)
	if err != nil {
		return searchv1.Source{}, err
	}
	src, err := s.findSource(b.Id, in.Source)
	if err != nil {
		return searchv1.Source{}, err
	}
	keep := map[string]bool{}
	for _, p := range in.Keep {
		keep[strings.TrimSpace(p)] = true
	}
	err = s.prune(b, src, keep)
	s.finish(src, err, "")
	if err != nil {
		return searchv1.Source{}, err
	}
	return s.sourceResult(b, src)
}

// ---- URLs and collections

// parseURL takes an http or https URL.
func parseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, badRequest("give an http or https URL")
	}
	return u, nil
}

// DetectURL implements search/v1.
func (s *service) DetectURL(ctx context.Context, _ extkit.Auth, in searchv1.URLRef) (searchv1.URLKind, error) {
	if _, err := parseURL(in.URL); err != nil {
		return searchv1.URLKind{}, err
	}
	return searchv1.URLKind{Kind: detectKind(ctx, strings.TrimSpace(in.URL))}, nil
}

// detectKind asks the server the Git protocol's first question (its refs, as git ls-remote does): a repository
// answers it on any host, with or without .git; anything else is read as a page.
func detectKind(ctx context.Context, raw string) string {
	ctx, cancel := context.WithTimeout(ctx, DetectTimeout)
	defer cancel()
	if _, err := remoteHead(ctx, raw); err == nil {
		return searchv1.KindGit
	}
	return searchv1.KindPage
}

// AddURL implements search/v1.
func (s *service) AddURL(_ context.Context, _ extkit.Auth, in searchv1.URLInput) (searchv1.Source, error) {
	b, err := s.findBucket(in.Bucket)
	if err != nil {
		return searchv1.Source{}, err
	}
	raw := strings.TrimSpace(in.URL)
	if _, err := parseURL(raw); err != nil {
		return searchv1.Source{}, err
	}
	kind := in.Kind
	if kind == "" {
		kind = detectKind(context.Background(), raw)
	}
	if kind != searchv1.KindPage && kind != searchv1.KindGit {
		return searchv1.Source{}, badRequest("kind is page or git")
	}
	if in.Every != "" && interval(in.Every) == 0 {
		return searchv1.Source{}, badRequest("every is hour, day or week")
	}
	sp := spec{URL: raw, Every: in.Every, Exclude: in.Exclude}
	if kind == searchv1.KindGit {
		sp.Commits = in.Commits
	}
	src, err := s.putSource(b, raw, "", kind, sp)
	if err != nil {
		return searchv1.Source{}, err
	}
	s.read(b, src, false)
	return s.sourceResult(b, src)
}

// AddFolder implements search/v1.
func (s *service) AddFolder(_ context.Context, _ extkit.Auth, in searchv1.FolderInput) (searchv1.Source, error) {
	b, err := s.findBucket(in.Bucket)
	if err != nil {
		return searchv1.Source{}, err
	}
	rel, _, err := s.appFolder(in.Path)
	if err != nil {
		return searchv1.Source{}, err
	}
	if in.URL != "" && !strings.Contains(in.URL, "{path}") {
		return searchv1.Source{}, badRequest("url needs {path} where the file's path goes (\"/docs/{path}\")")
	}
	sp := spec{Path: rel, Include: in.Include, Exclude: in.Exclude, PageURL: in.URL}
	src, err := s.putSource(b, rel, "", searchv1.KindDir, sp)
	if err != nil {
		return searchv1.Source{}, err
	}
	s.read(b, src, false)
	return s.sourceResult(b, src)
}

// Follow implements search/v1.
func (s *service) Follow(_ context.Context, _ extkit.Auth, in searchv1.FollowInput) (searchv1.Source, error) {
	b, err := s.findBucket(in.Bucket)
	if err != nil {
		return searchv1.Source{}, err
	}
	sp := spec{Collection: in.Collection, Fields: in.Fields, Filter: in.Filter}
	if err := s.checkCollection(&sp); err != nil {
		return searchv1.Source{}, err
	}
	src, err := s.putSource(b, sp.Collection, "", searchv1.KindCollection, sp)
	if err != nil {
		return searchv1.Source{}, err
	}
	s.loadFollowers()
	s.read(b, src, false)
	return s.sourceResult(b, src)
}

// Refresh implements search/v1.
func (s *service) Refresh(_ context.Context, _ extkit.Auth, in searchv1.SourceRef) (searchv1.Source, error) {
	b, err := s.findBucket(in.Bucket)
	if err != nil {
		return searchv1.Source{}, err
	}
	src, err := s.findSource(b.Id, in.Source)
	if err != nil {
		return searchv1.Source{}, err
	}
	s.read(b, src, true)
	return s.sourceResult(b, src)
}

// read reads a source (in the background on a server): fetches a page or repository, rereads a collection,
// resplits a zip or a file's kept text. force reads even what looks unchanged.
func (s *service) read(b, src *core.Record, force bool) {
	src.Set("status", SourceReading)
	src.Set("error", "")
	if err := s.app.Save(src); err != nil {
		// the read still runs; finish saves its outcome
		s.app.Logger().Warn("llm-search: mark source reading", "source", src.Id, "error", err)
	}
	run := func() {
		state, err := s.readNow(b, src, force)
		s.finish(src, err, state)
	}
	if !background {
		run()
		return
	}
	go run()
}

// readNow does a source's reading and returns what it read (a page's ETag, a repository's commit).
func (s *service) readNow(b, src *core.Record, force bool) (string, error) {
	switch src.GetString("kind") {
	case searchv1.KindPage:
		return s.readPage(b, src, force)
	case searchv1.KindGit:
		return s.readGit(b, src, force)
	case searchv1.KindCollection:
		return "", s.syncCollection(b, src)
	case searchv1.KindDir:
		return s.readDir(b, src, force)
	case searchv1.KindZip:
		fsys, err := s.app.NewFilesystem()
		if err != nil {
			return "", err
		}
		defer fsys.Close()
		r, err := fsys.GetReader(src.BaseFilesPath() + "/" + src.GetString("original"))
		if err != nil {
			return "", fmt.Errorf("the zip wasn't kept; upload it again")
		}
		data, err := io.ReadAll(io.LimitReader(r, MaxZipBytes+1))
		r.Close()
		if err != nil {
			return "", err
		}
		docs, err := zipDocs(data)
		if err != nil {
			return "", err
		}
		return "", s.syncDocs(b, src, docs, force)
	default: // file, text, folder: split each document's kept text again
		fsys, err := s.app.NewFilesystem()
		if err != nil {
			return "", err
		}
		defer fsys.Close()
		docs, err := s.app.FindAllRecords(DocumentsCollection, dbx.HashExp{"src": src.Id})
		if err != nil {
			return "", err
		}
		for _, d := range docs {
			text, err := readText(fsys, d)
			if err != nil {
				return "", fmt.Errorf("%s: %w", d.GetString("name"), err)
			}
			if _, _, err := s.addDoc(b, src, docIn{name: d.GetString("name"), text: text, kind: d.GetString("kind"), record: d.GetString("record")}, addOpts{force: true}); err != nil {
				return "", err
			}
		}
		return "", nil
	}
}

// Remove implements search/v1.
func (s *service) Remove(_ context.Context, _ extkit.Auth, in searchv1.RemoveInput) (searchv1.Removed, error) {
	b, err := s.findBucket(in.Bucket)
	if err != nil {
		return searchv1.Removed{}, err
	}
	if len(in.Sources) == 0 {
		return searchv1.Removed{}, badRequest("name the sources to remove")
	}
	n := 0
	for _, name := range in.Sources {
		src, err := s.findSource(b.Id, name)
		if err != nil {
			continue
		}
		if err := s.clearSource(b.Id, src); err != nil {
			return searchv1.Removed{Count: n}, err
		}
		if err := s.app.Delete(src); err != nil {
			return searchv1.Removed{Count: n}, err
		}
		n++
	}
	s.loadFollowers()
	return searchv1.Removed{Count: n}, nil
}

// resumeReads reads again every source a restart (or crash) interrupted mid-read.
func (s *service) resumeReads() {
	stuck, err := s.app.FindRecordsByFilter(SourcesCollection, "status = 'reading'", "updated", 0, 0)
	if err != nil {
		return
	}
	for _, src := range stuck {
		b, err := s.app.FindRecordById(BucketsCollection, src.GetString("bucket"))
		if err != nil {
			continue
		}
		if src.GetString("kind") == searchv1.KindFolder {
			s.finish(src, nil, "") // a folder upload the browser didn't finish: keep what arrived
			continue
		}
		s.read(b, src, false)
	}
}

// refreshDue reads every source whose interval has come round.
func (s *service) refreshDue() {
	due, err := s.app.FindRecordsByFilter(SourcesCollection, "next != '' && next <= {:now} && status != 'reading'", "next", 20, 0,
		dbx.Params{"now": types.NowDateTime().String()})
	if err != nil {
		return
	}
	for _, src := range due {
		b, err := s.app.FindRecordById(BucketsCollection, src.GetString("bucket"))
		if err != nil {
			continue
		}
		state, err := s.readNow(b, src, false)
		s.finish(src, err, state)
	}
}
