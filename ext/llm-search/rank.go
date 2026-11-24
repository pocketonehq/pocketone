package llmsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"pocketone.dev/ext/contracts/llm"
	embedv1 "pocketone.dev/ext/contracts/llm/embed/v1"
	searchv1 "pocketone.dev/ext/contracts/llm/search/v1"
	"pocketone.dev/ext/extkit"
)

// Search sizes.
const (
	DefaultLimit = 5
	MaxLimit     = 50
	rrfK         = 60 // reciprocal rank fusion: score = Σ 1/(rrfK + rank)
	maxQueries   = 256
)

// vecCache is a bucket's vectors from one model, as of a version.
type vecCache struct {
	version int64
	model   string
	ids     []string
	names   []string // each chunk's document name, for Prefix
	vecs    [][]float32
}

type vecRow struct {
	ID     string `db:"id"`
	Name   string `db:"name"`
	Vector string `db:"vector"`
}

// vectors returns a bucket's vectors from model, loading them when they changed.
func (s *service) vectors(bucketID, model string) (*vecCache, error) {
	s.mu.Lock()
	version := s.versions[bucketID]
	c := s.vecs[bucketID]
	s.mu.Unlock()
	if c != nil && c.version == version && c.model == model {
		return c, nil
	}
	var rows []vecRow
	err := s.app.DB().Select("c.id AS id", "d.name AS name", "c.vector AS vector").
		From(ChunksCollection+" c").InnerJoin(DocumentsCollection+" d", dbx.NewExp("d.id = c.document")).
		Where(dbx.And(dbx.HashExp{"c.bucket": bucketID, "c.model": model}, dbx.NewExp("c.vector != ''"))).All(&rows)
	if err != nil {
		return nil, err
	}
	c = &vecCache{version: version, model: model}
	for _, r := range rows {
		if v := decodeVector(r.Vector); len(v) > 0 {
			c.ids, c.names, c.vecs = append(c.ids, r.ID), append(c.names, r.Name), append(c.vecs, v)
		}
	}
	s.mu.Lock()
	s.vecs[bucketID] = c
	s.mu.Unlock()
	return c, nil
}

// queryVector embeds a query (cached).
func (s *service) queryVector(ctx context.Context, model, q string) ([]float32, llm.Usage, error) {
	key := model + "|" + q
	s.mu.Lock()
	v, ok := s.queries[key]
	s.mu.Unlock()
	if ok {
		return v, llm.Usage{}, nil
	}
	embed, ok := embedv1.Contract.Of(s.app)
	if !ok {
		return nil, llm.Usage{}, searchv1.ErrNoModel
	}
	res, err := embed.Embed(ctx, extkit.Internal, embedv1.EmbedInput{Model: model, Input: []string{q}})
	if err != nil {
		return nil, llm.Usage{}, err
	}
	if len(res.Vectors) != 1 {
		return nil, llm.Usage{}, fmt.Errorf("llm-search: the model returned %d vectors for one query", len(res.Vectors))
	}
	f := make([]float32, len(res.Vectors[0]))
	for i, x := range res.Vectors[0] {
		f[i] = float32(x)
	}
	v = normalize(f)
	s.mu.Lock()
	if len(s.queries) >= maxQueries {
		clear(s.queries)
	}
	s.queries[key] = v
	s.mu.Unlock()
	return v, res.Usage, nil
}

type hit struct {
	id    string
	score float64
}

var wordRe = regexp.MustCompile(`[\p{L}\p{N}_]+`)

// ftsQuery is a query as FTS5 terms: each word quoted (no operators from the user), any of them matching.
func ftsQuery(q string) string {
	words := wordRe.FindAllString(q, 32)
	for i, w := range words {
		words[i] = `"` + w + `"`
	}
	return strings.Join(words, " OR ")
}

// keyword ranks the buckets' chunks by BM25.
func (s *service) keyword(bucketIDs []string, q, prefix string, n int) ([]hit, error) {
	m := ftsQuery(q)
	if m == "" {
		return nil, nil
	}
	params := dbx.Params{"m": m, "n": n}
	in := make([]string, len(bucketIDs))
	for i, id := range bucketIDs {
		k := fmt.Sprintf("b%d", i)
		params[k] = id
		in[i] = "{:" + k + "}"
	}
	sql := "SELECT chunk, bm25(" + FTSTable + ") AS r FROM " + FTSTable + " WHERE " + FTSTable + " MATCH {:m} AND bucket IN (" +
		strings.Join(in, ",") + ")"
	if prefix != "" {
		sql += " AND substr(name, 1, {:pl}) = {:p}"
		params["p"], params["pl"] = prefix, len(prefix)
	}
	sql += " ORDER BY r LIMIT {:n}"
	var rows []struct {
		Chunk string  `db:"chunk"`
		R     float64 `db:"r"`
	}
	if err := s.app.DB().NewQuery(sql).Bind(params).All(&rows); err != nil {
		return nil, err
	}
	out := make([]hit, len(rows))
	for i, r := range rows {
		out[i] = hit{r.Chunk, -r.R} // bm25() is lower for better matches
	}
	return out, nil
}

// semantic ranks the caches' chunks by cosine similarity to qv.
func semantic(caches []*vecCache, qv []float32, prefix string, n int) []hit {
	var out []hit
	for _, c := range caches {
		for i, v := range c.vecs {
			if len(v) != len(qv) || (prefix != "" && !strings.HasPrefix(c.names[i], prefix)) {
				continue
			}
			var dot float32
			for j := range v {
				dot += v[j] * qv[j]
			}
			out = append(out, hit{c.ids[i], float64(dot)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].score > out[j].score })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

type chunkRow struct {
	ID        string `db:"id"`
	Text      string `db:"text"`
	Heading   string `db:"heading"`
	Anchor    string `db:"anchor"`
	StartLine int    `db:"start_line"`
	EndLine   int    `db:"end_line"`
	Name      string `db:"name"`
	Record    string `db:"record"`
	Bucket    string `db:"bucket"`
	Source    string `db:"source"`
	Kind      string `db:"kind"`
	Spec      string `db:"spec"`
}

// searchBuckets is the bucket records a search names (every bucket when it names none).
func (s *service) searchBuckets(in searchv1.SearchInput) ([]*core.Record, error) {
	names := append([]string(nil), in.Buckets...)
	if in.Bucket != "" {
		names = append(names, in.Bucket)
	}
	if len(names) == 0 {
		return s.app.FindRecordsByFilter(BucketsCollection, "", "name", 0, 0)
	}
	var out []*core.Record
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			continue
		}
		seen[n] = true
		b, err := s.findBucket(n)
		if err != nil {
			return nil, &extkit.Error{Code: searchv1.ErrNoBucket.Code, Status: searchv1.ErrNoBucket.Status, Message: "no bucket named " + n}
		}
		out = append(out, b)
	}
	return out, nil
}

// Search implements search/v1.
func (s *service) Search(ctx context.Context, _ extkit.Auth, in searchv1.SearchInput) (searchv1.Results, error) {
	out := searchv1.Results{Results: []searchv1.Result{}}
	q := strings.TrimSpace(in.Query)
	if q == "" {
		return out, badRequest("give a query")
	}
	buckets, err := s.searchBuckets(in)
	if err != nil || len(buckets) == 0 {
		return out, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	limit = min(limit, MaxLimit)
	pool := max(limit*4, 20)
	viewer, err := s.viewer(in.Viewer)
	if err != nil {
		return out, err
	}
	if viewer != nil {
		pool *= 2 // some matches may be records the viewer can't see
	}

	model, err := s.currentModel(ctx)
	if err != nil {
		return out, err
	}
	ids := make([]string, len(buckets))
	nameOf := map[string]string{}
	for i, b := range buckets {
		ids[i] = b.Id
		nameOf[b.Id] = b.GetString("name")
	}
	kw, err := s.keyword(ids, q, in.Prefix, pool)
	if err != nil {
		return out, err
	}
	qv, usage, err := s.queryVector(ctx, model, q)
	if err != nil {
		return out, err
	}
	out.Usage, out.Model = usage, model
	caches := make([]*vecCache, 0, len(ids))
	for _, id := range ids {
		c, err := s.vectors(id, model)
		if err != nil {
			return out, err
		}
		caches = append(caches, c)
	}
	sem := semantic(caches, qv, in.Prefix, pool)

	// reciprocal rank fusion: keywords catch exact names an embedding blurs, meaning catches a reworded question
	score := map[string]float64{}
	for rank, h := range kw {
		score[h.id] += 1 / float64(rrfK+rank+1)
	}
	for rank, h := range sem {
		score[h.id] += 1 / float64(rrfK+rank+1)
	}
	ranked := make([]string, 0, len(score))
	for id := range score {
		ranked = append(ranked, id)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if score[ranked[i]] != score[ranked[j]] {
			return score[ranked[i]] > score[ranked[j]]
		}
		return ranked[i] < ranked[j]
	})
	if len(ranked) == 0 {
		return out, nil
	}

	args := make([]any, len(ranked))
	for i, id := range ranked {
		args[i] = id
	}
	var rows []chunkRow
	err = s.app.DB().Select("c.id AS id", "c.text AS text", "c.heading AS heading", "c.anchor AS anchor", "c.start_line AS start_line",
		"c.end_line AS end_line", "d.name AS name", "d.record AS record", "c.bucket AS bucket", "s.name AS source",
		"s.kind AS kind", "s.spec AS spec").
		From(ChunksCollection+" c").InnerJoin(DocumentsCollection+" d", dbx.NewExp("d.id = c.document")).
		InnerJoin(SourcesCollection+" s", dbx.NewExp("s.id = d.src")).
		Where(dbx.In("c.id", args...)).All(&rows)
	if err != nil {
		return out, err
	}
	rowByID := map[string]chunkRow{}
	for _, r := range rows {
		rowByID[r.ID] = r
	}
	allowed := map[string]bool{} // record ref -> the viewer may see it
	for _, id := range ranked {
		if len(out.Results) == limit {
			break
		}
		r, ok := rowByID[id]
		if !ok {
			continue // removed meanwhile
		}
		ref := recordRef(r.Record)
		if viewer != nil && ref != nil {
			ok, seen := allowed[r.Record]
			if !seen {
				ok = s.canView(viewer, ref)
				allowed[r.Record] = ok
			}
			if !ok {
				continue
			}
		}
		out.Results = append(out.Results, searchv1.Result{Bucket: nameOf[r.Bucket], Source: r.Source, Document: r.Name, Text: r.Text,
			Heading: r.Heading, StartLine: r.StartLine, EndLine: r.EndLine, Record: ref, URL: resultURL(r), Score: score[id]})
	}
	return out, nil
}

// viewerAuth is who a search is for: auth is nil for a guest.
type viewerAuth struct{ auth *core.Record }

func (s *service) viewer(v *searchv1.Viewer) (*viewerAuth, error) {
	if v == nil {
		return nil, nil
	}
	if v.ID == "" {
		return &viewerAuth{}, nil
	}
	r, err := s.app.FindRecordById(v.Collection, v.ID)
	if err != nil || !r.Collection().IsAuth() {
		return nil, badRequest("viewer: no auth record " + v.Collection + "/" + v.ID)
	}
	return &viewerAuth{auth: r}, nil
}

// canView checks the record's collection's view rule for the viewer, as PocketBase's record API does.
func (s *service) canView(v *viewerAuth, ref *searchv1.RecordRef) bool {
	rec, err := s.app.FindRecordById(ref.Collection, ref.ID)
	if err != nil {
		return false
	}
	if v.auth != nil && v.auth.IsSuperuser() {
		return true
	}
	ok, err := s.app.CanAccessRecord(rec, &core.RequestInfo{Auth: v.auth}, rec.Collection().ViewRule)
	return err == nil && ok
}

// resultURL is where a result is published: a web page's URL, or a server folder's URL pattern for the file, with the
// section's anchor; "" when its source doesn't know.
func resultURL(r chunkRow) string {
	switch r.Kind {
	case searchv1.KindPage:
		return r.Name
	case searchv1.KindDir:
		var sp spec
		_ = json.Unmarshal([]byte(r.Spec), &sp)
		u := pageURL(sp.PageURL, r.Name)
		if u != "" && r.Anchor != "" {
			u += "#" + r.Anchor
		}
		return u
	}
	return ""
}

// pageURL fills a URL pattern's {path} with a file's path without its extension; an index file is its folder
// ("guides/index.md" is "guides", the folder's own "index.md" is ""). "" when there's no pattern.
func pageURL(pattern, file string) string {
	if pattern == "" {
		return ""
	}
	p := strings.TrimSuffix(file, path.Ext(file))
	if p == "index" {
		p = ""
	} else {
		p = strings.TrimSuffix(p, "/index")
	}
	u := strings.ReplaceAll(pattern, "{path}", p)
	if p == "" && strings.Contains(pattern, "/{path}") {
		u = strings.Replace(pattern, "/{path}", "", 1)
		if u == "" {
			u = "/"
		}
	}
	return u
}

// Sample implements search/v1: chunks picked at random from the buckets named (every bucket when none), no query and
// no embedding.
func (s *service) Sample(_ context.Context, _ extkit.Auth, in searchv1.SampleInput) (searchv1.Results, error) {
	out := searchv1.Results{Results: []searchv1.Result{}}
	buckets, err := s.searchBuckets(searchv1.SearchInput{Buckets: in.Buckets, Bucket: in.Bucket})
	if err != nil || len(buckets) == 0 {
		return out, err
	}
	count := in.Count
	if count <= 0 {
		count = DefaultLimit
	}
	count = min(count, MaxLimit)
	ids := make([]any, len(buckets))
	nameOf := map[string]string{}
	for i, b := range buckets {
		ids[i] = b.Id
		nameOf[b.Id] = b.GetString("name")
	}
	where := dbx.And(dbx.In("c.bucket", ids...), dbx.NewExp("c.text != ''"))
	if in.Prefix != "" {
		where = dbx.And(where, dbx.Like("d.name", in.Prefix).Match(false, true))
	}
	var rows []chunkRow
	err = s.app.DB().Select("c.id AS id", "c.text AS text", "c.heading AS heading", "c.anchor AS anchor", "c.start_line AS start_line",
		"c.end_line AS end_line", "d.name AS name", "d.record AS record", "c.bucket AS bucket", "s.name AS source",
		"s.kind AS kind", "s.spec AS spec").
		From(ChunksCollection+" c").InnerJoin(DocumentsCollection+" d", dbx.NewExp("d.id = c.document")).
		InnerJoin(SourcesCollection+" s", dbx.NewExp("s.id = d.src")).
		Where(where).OrderBy("RANDOM()").Limit(int64(count)).All(&rows)
	if err != nil {
		return out, err
	}
	for _, r := range rows {
		out.Results = append(out.Results, searchv1.Result{Bucket: nameOf[r.Bucket], Source: r.Source, Document: r.Name, Text: r.Text,
			Heading: r.Heading, StartLine: r.StartLine, EndLine: r.EndLine, Record: recordRef(r.Record), URL: resultURL(r)})
	}
	return out, nil
}
