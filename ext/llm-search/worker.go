package llmsearch

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/core"

	"pocketone.dev/ext/contracts/llm"
	embedv1 "pocketone.dev/ext/contracts/llm/embed/v1"
	"pocketone.dev/ext/extkit"
)

// Embedding pace: chunks per request, and the least time between requests (Vultr allows 120 a minute).
var (
	BatchSize       = 64
	Parallel        = 4     // requests at once
	BatchChars      = 48000 // and at most this much text a request (about 12K tokens): big requests are slow
	RequestTimeout  = 90 * time.Second
	RequestInterval = 550 * time.Millisecond
	retryWait       = 3 * time.Second
	// background: false leaves queued documents for drain (tests).
	background = true
)

// kickWorker starts the worker if it isn't running and tells it there's work.
func (s *service) kickWorker() {
	if !background {
		return
	}
	s.workerOnce.Do(func() { go s.worker() })
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

func (s *service) stopWorker() { s.stopOnce.Do(func() { close(s.stop) }) }

func (s *service) worker() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-s.stop; cancel() }()
	for {
		select {
		case <-s.stop:
			return
		case <-s.kick:
		case <-time.After(time.Minute):
		}
		_ = s.drain(ctx)
	}
}

// drain embeds queued documents until none are left (or ctx ends).
func (s *service) drain(ctx context.Context) error {
	for ctx.Err() == nil {
		more, err := s.embedNext(ctx)
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
	}
	return ctx.Err()
}

type pendingChunk struct {
	ID       string `db:"id"`
	Hash     string `db:"hash"`
	Heading  string `db:"heading"`
	Text     string `db:"text"`
	Document string `db:"document"`
	Bucket   string `db:"bucket"`
}

// embedNext embeds the next round: chunks without the model's vectors, from the documents waiting longest, across
// documents (so a source of many small files fills each request), in up to Parallel requests at once of at most
// BatchSize chunks and BatchChars of text each. False when nothing is waiting.
//
// It holds s.work only while it writes, never across a model call, so adding or removing documents (page code, on the
// app's one JS VM) never waits for the network. A vector written for a chunk replaced meanwhile updates no row.
func (s *service) embedNext(ctx context.Context) (bool, error) {
	s.drainMu.Lock()
	defer s.drainMu.Unlock()
	model, err := s.currentModel(ctx)
	if err != nil {
		return false, nil // no embedding model yet: documents wait, and Status says why
	}
	embed, ok := embedv1.Contract.Of(s.app)
	if !ok {
		return false, nil
	}
	stale := dbx.Or(dbx.HashExp{"c.vector": ""}, dbx.Not(dbx.HashExp{"c.model": model}))
	var rows []pendingChunk
	if err := s.app.DB().Select("c.id AS id", "c.hash AS hash", "c.heading AS heading", "c.text AS text",
		"c.document AS document", "c.bucket AS bucket").
		From(ChunksCollection+" c").InnerJoin(DocumentsCollection+" d", dbx.NewExp("d.id = c.document")).
		Where(dbx.And(stale, dbx.In("d.status", StatusQueued, StatusEmbedding))).
		OrderBy("d.updated", "d.id", "c.seq").Limit(int64(BatchSize * Parallel)).All(&rows); err != nil {
		return false, err
	}
	if len(rows) == 0 {
		return false, s.markDone(model)
	}

	// content embedded meanwhile (the same file added elsewhere) is reused; the rest is split into requests, a repeat
	// going once
	hashes := make([]string, len(rows))
	for i, c := range rows {
		hashes[i] = c.Hash
	}
	vecs := s.vectorsByHash(model, hashes)
	type request struct {
		in     embedv1.EmbedInput
		hashes []string
		chars  int
		docs   map[string]bool
	}
	var reqs []*request
	assigned := map[string]*request{}
	var batch []pendingChunk // the chunks this round covers
	for _, c := range rows {
		if _, done := vecs[c.Hash]; done {
			batch = append(batch, c)
			continue
		}
		if r := assigned[c.Hash]; r != nil {
			r.docs[c.Document] = true
			batch = append(batch, c)
			continue
		}
		text := embedText(piece{Heading: c.Heading, Text: c.Text})
		var r *request
		if n := len(reqs); n > 0 && len(reqs[n-1].hashes) < BatchSize && reqs[n-1].chars+len(text) <= BatchChars {
			r = reqs[n-1]
		} else if len(reqs) < Parallel {
			r = &request{in: embedv1.EmbedInput{Model: model}, docs: map[string]bool{}}
			reqs = append(reqs, r)
		} else {
			break // the rest wait for the next round
		}
		r.in.Input = append(r.in.Input, text)
		r.hashes = append(r.hashes, c.Hash)
		r.chars += len(text)
		r.docs[c.Document] = true
		assigned[c.Hash] = r
		batch = append(batch, c)
	}
	docs := map[string]int{} // document -> its chunks this round
	for _, c := range batch {
		docs[c.Document]++
		if _, err := s.app.DB().Update(DocumentsCollection, dbx.Params{"status": StatusEmbedding}, dbx.HashExp{"id": c.Document, "status": StatusQueued}).Execute(); err != nil {
			return true, err
		}
	}

	type answer struct {
		vecs [][]float64
		cost int64
		err  error
	}
	answers := make([]answer, len(reqs))
	var wg sync.WaitGroup
	for i, r := range reqs {
		wg.Add(1)
		go func(i int, r *request) {
			defer wg.Done()
			// spaced out, to stay under the provider's request rate
			select {
			case <-ctx.Done():
				answers[i].err = ctx.Err()
				return
			case <-time.After(time.Duration(i) * RequestInterval):
			}
			res, err := s.embedOnce(ctx, embed, r.in)
			if err == nil && len(res.Vectors) != len(r.in.Input) {
				err = errors.New("the model returned a different number of vectors")
			}
			answers[i] = answer{vecs: res.Vectors, cost: res.Usage.CostMicroUSD, err: err}
		}(i, r)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return false, ctx.Err() // shutting down: stays queued
	}
	if len(reqs) > 0 {
		if wait := time.Duration(len(reqs))*RequestInterval - time.Since(s.lastRequest); wait > 0 {
			time.Sleep(wait)
		}
		s.lastRequest = time.Now()
	}
	var cost int64
	failed := map[string]string{} // document -> why its request failed
	for i, r := range reqs {
		if answers[i].err != nil {
			for d := range r.docs {
				failed[d] = errText(answers[i].err)
			}
			continue
		}
		for j, h := range r.hashes {
			vecs[h] = encodeVector(answers[i].vecs[j])
		}
		cost += answers[i].cost
	}

	s.work.Lock()
	err = s.tx(func(tx core.App) error {
		for _, c := range batch {
			v, ok := vecs[c.Hash]
			if !ok {
				continue
			}
			if _, err := tx.DB().Update(ChunksCollection, dbx.Params{"vector": v, "model": model}, dbx.HashExp{"id": c.ID}).Execute(); err != nil {
				return err
			}
		}
		// the round's cost, shared by the documents in it, by chunks
		for id, n := range docs {
			share := cost * int64(n) / int64(len(batch))
			if _, err := tx.DB().NewQuery("UPDATE " + DocumentsCollection + " SET cost = cost + {:c} WHERE id = {:id}").
				Bind(dbx.Params{"c": share, "id": id}).Execute(); err != nil {
				return err
			}
		}
		return nil
	})
	s.work.Unlock()
	if err != nil {
		return true, err
	}
	for id, why := range failed {
		if d, err := s.app.FindRecordById(DocumentsCollection, id); err == nil {
			_ = s.fail(d, why)
		}
	}
	buckets := map[string]bool{}
	for _, c := range batch {
		buckets[c.Bucket] = true
	}
	for b := range buckets {
		s.bump(b)
	}
	return true, s.markDone(model)
}

// markDone marks ready every waiting document whose chunks all have the model's vectors.
func (s *service) markDone(model string) error {
	s.work.Lock()
	defer s.work.Unlock()
	_, err := s.app.DB().NewQuery("UPDATE " + DocumentsCollection + " SET status = 'ready', error = '' " +
		"WHERE status IN ('queued', 'embedding') AND NOT EXISTS (SELECT 1 FROM " + ChunksCollection + " c " +
		"WHERE c.document = " + DocumentsCollection + ".id AND (c.vector = '' OR c.model != {:m}))").
		Bind(dbx.Params{"m": model}).Execute()
	return err
}

// embedOnce calls the model with a time limit, retrying once after a failure a retry might fix.
func (s *service) embedOnce(ctx context.Context, embed embedv1.Embed, in embedv1.EmbedInput) (embedv1.Result, error) {
	var res embedv1.Result
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		reqCtx, cancel := context.WithTimeout(ctx, RequestTimeout)
		res, err = embed.Embed(reqCtx, extkit.Internal, in)
		if err != nil && ctx.Err() == nil && errors.Is(reqCtx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("the embedding model didn't answer in %s", RequestTimeout)
		}
		cancel()
		if err == nil || isPermanent(err) || ctx.Err() != nil {
			return res, err
		}
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(retryWait):
		}
	}
	return res, err
}

// isPermanent: errors a retry won't fix.
func isPermanent(err error) bool {
	var e *extkit.Error
	if errors.As(err, &e) {
		switch e.Code {
		case llm.ErrNotConfigured.Code, llm.ErrUnknownModel.Code, llm.ErrNotSupported.Code, "bad_request", "forbidden":
			return true
		}
	}
	return false
}

func errText(err error) string {
	var e *extkit.Error
	if errors.As(err, &e) {
		return e.Message
	}
	return err.Error()
}

func (s *service) setStatus(doc *core.Record, status, msg string) error {
	_, err := s.app.DB().Update(DocumentsCollection, dbx.Params{"status": status, "error": msg}, dbx.HashExp{"id": doc.Id}).Execute()
	return err
}

func (s *service) fail(doc *core.Record, msg string) error {
	if len(msg) > 2000 {
		msg = msg[:2000]
	}
	s.app.Logger().Warn("llm-search: embedding failed", "document", doc.GetString("name"), "error", msg)
	return s.setStatus(doc, StatusError, msg)
}

// encodeVector packs a vector as little-endian float32s in base64.
func encodeVector(v []float64) string {
	b := make([]byte, 4*len(v))
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(float32(f)))
	}
	return base64.StdEncoding.EncodeToString(b)
}

// decodeVector unpacks a vector and scales it to length 1, so a dot product is the cosine.
func decodeVector(s string) []float32 {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b)%4 != 0 {
		return nil
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return normalize(v)
}

func normalize(v []float32) []float32 {
	var sum float64
	for _, f := range v {
		sum += float64(f) * float64(f)
	}
	if sum == 0 {
		return v
	}
	n := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= n
	}
	return v
}
