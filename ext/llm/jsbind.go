package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/filesystem"

	llmc "pocketone.dev/ext/contracts/llm"
	alignv1 "pocketone.dev/ext/contracts/llm/align/v1"
	embedv1 "pocketone.dev/ext/contracts/llm/embed/v1"
	imagev1 "pocketone.dev/ext/contracts/llm/image/v1"
	ledgerv1 "pocketone.dev/ext/contracts/llm/ledger/v1"
	musicv1 "pocketone.dev/ext/contracts/llm/music/v1"
	searchv1 "pocketone.dev/ext/contracts/llm/search/v1"
	speechv1 "pocketone.dev/ext/contracts/llm/speech/v1"
	textv1 "pocketone.dev/ext/contracts/llm/text/v1"
	transcribev1 "pocketone.dev/ext/contracts/llm/transcribe/v1"
	videov1 "pocketone.dev/ext/contracts/llm/video/v1"
	"pocketone.dev/ext/extkit"
	"pocketone.dev/ext/extkit/js"
)

// BindJS exposes the llm.* contracts to the app's page code as $llm.<kind>.<operation>Async. Every call that reaches a
// provider is async only and says so in its name (await $llm.text.completeAsync({...})): the app has one JS VM, and a
// model call that held it for seconds would make every other request wait. Only local calls (count, file, the chat
// records) are plain. The Go side takes and answers JSON text; a small JS layer (jsPrelude) builds $llm over it and
// runs each provider call with the engine's .async, off the VM.
func (*Extension) BindJS(app core.App, rt js.Runtime) {
	invokers := map[string]func(op string, raw json.RawMessage) (any, error){
		llmc.KindText:       invoker(app, textv1.Contract, textv1.W),
		llmc.KindImage:      invoker(app, imagev1.Contract, imagev1.W),
		llmc.KindSpeech:     invoker(app, speechv1.Contract, speechv1.W),
		llmc.KindTranscribe: invoker(app, transcribev1.Contract, transcribev1.W),
		llmc.KindAlign:      invoker(app, alignv1.Contract, alignv1.W),
		llmc.KindMusic:      invoker(app, musicv1.Contract, musicv1.W),
		llmc.KindVideo:      invoker(app, videov1.Contract, videov1.W),
		llmc.KindEmbed:      invoker(app, embedv1.Contract, embedv1.W),
		// $llm.search: whoever provides llm.search/v1 (the llm-search extension); ext/llm imports only the contract
		"search": func(op string, raw json.RawMessage) (any, error) {
			if _, ok := searchv1.Contract.Of(app); !ok {
				return nil, searchv1.ErrNoSearch
			}
			return invoker(app, searchv1.Contract, searchv1.W)(op, raw)
		},
		"ledger": invoker(app, ledgerv1.Contract, ledgerv1.W),
	}
	n := rt.NewObject()
	// call(kind, op, opts): any operation that answers JSON; the prelude calls it with .async
	_ = n.Set("call", func(kind, op, opts string) (string, error) {
		inv, ok := invokers[kind]
		if !ok {
			return "", fmt.Errorf("$llm: no kind %q", kind)
		}
		res, err := inv(op, json.RawMessage(opts))
		if err != nil {
			return "", err
		}
		b, err := json.Marshal(res)
		return string(b), err
	})
	_ = n.Set("streamStart", func(kind, opts string) (string, error) { return streamStart(app, kind, opts) })
	_ = n.Set("streamNext", streamNext)
	_ = n.Set("streamStop", streamStop)
	// file(f, download): a generated file as a PocketBase file, for record.set("field", ...). Plain $llm.file takes only
	// a file with its bytes; fileAsync may download one the provider only gave a URL for.
	_ = n.Set("file", func(f string, download bool) (*filesystem.File, error) {
		var v llmc.File
		if err := json.Unmarshal([]byte(f), &v); err != nil {
			return nil, errors.New("$llm.file: " + err.Error())
		}
		if len(v.Data) > 0 {
			return filesystem.NewFileFromBytes(v.Data, orDefault(v.Name, "file"))
		}
		if v.URL == "" {
			return nil, errors.New("$llm.file: the file has no data")
		}
		if !download {
			return nil, errors.New("$llm.file: the file has only a URL; use await $llm.fileAsync(file) to download it")
		}
		return filesystem.NewFileFromURL(context.Background(), v.URL)
	})
	bindHooksJS(app, n) // onCall, onCallDone
	// fromRecord(record, field): a record's stored file as an LlmFile with its bytes, to send to a model (a voice clip
	// for speech's reference, audio to transcribe or align). Read through the app's file system, so S3 storage works.
	_ = n.Set("fromRecord", func(rec *core.Record, field string) (string, error) {
		f, err := fileFromRecord(app, rec, field)
		if err != nil {
			return "", errors.New("$llm.fromRecord: " + err.Error())
		}
		b, err := json.Marshal(f)
		return string(b), err
	})
	// subject(e): whom a request's calls are for ($llm.for(e)); sets the guest cookie when the request has none
	_ = n.Set("subject", js.NativeFunc(func(c js.Call) (any, error) {
		switch e := c.Arg(0).Export().(type) {
		case *core.RequestEvent:
			return SubjectOf(app, e), nil
		case core.RequestEvent:
			return SubjectOf(app, &e), nil
		case nil:
			return nil, errors.New("$llm.for: pass the request: a page's ctx or a route's e")
		default:
			return nil, fmt.Errorf("$llm.for: pass the request (a page's ctx or a route's e), not a %T", e)
		}
	}))
	bindChatJS(app, rt, n)
	if err := rt.Set(nativeGlobal, n); err != nil {
		panic(fmt.Errorf("llm: bind JS: %w", err))
	}
	if _, err := rt.Eval(jsPrelude, "ext/llm"); err != nil {
		panic(fmt.Errorf("llm: bind JS: %w", err))
	}
}

// invoker calls a contract's operation by name with JSON input, through the provider's guarded side (scope checks
// and action hooks run as for any call).
func invoker[T any](app core.App, c *extkit.Contract[T], w *extkit.Wire[T]) func(op string, raw json.RawMessage) (any, error) {
	return func(op string, raw json.RawMessage) (any, error) {
		impl, ok := c.Of(app)
		if !ok {
			return nil, llmc.ErrNotConfigured
		}
		for _, o := range w.Ops {
			if (o.Name != op && o.Path != op) || o.Out != extkit.OutJSON { // by name ("follow") or path ("add-url")
				continue
			}
			req := extkit.OpRequest{JSON: raw}
			if o.Method == "GET" {
				req.JSON, req.Query = nil, queryOf(raw)
			}
			return o.Invoke(subjectCtx(raw), impl, extkit.Internal, req)
		}
		return nil, fmt.Errorf("$llm: %s has no operation %q", c.ID(), op)
	}
}

// subjectCtx is a context carrying the subject $llm.for(e) put in a call's input as "__subject" (contract inputs
// ignore the key). Only page code calls $llm, and page code is the app's own, so the subject is trusted as given.
func subjectCtx[B ~string | ~[]byte](raw B) context.Context {
	var v struct {
		Subject string `json:"__subject"`
	}
	_ = json.Unmarshal([]byte(raw), &v)
	if v.Subject == "" {
		return context.Background()
	}
	return llmc.WithSubject(context.Background(), v.Subject)
}

// queryOf is a GET operation's input (a JSON object) as query values.
func queryOf(raw json.RawMessage) url.Values {
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	q := url.Values{}
	for k, v := range m {
		if v != nil && k != "__subject" {
			q.Set(k, fmt.Sprint(v))
		}
	}
	return q
}

const nativeGlobal = "__pocketone_llm"

// jsPrelude builds $llm over the Go functions, which it takes off the global scope.
var jsPrelude = `(() => {
  const n = globalThis.__pocketone_llm;
  delete globalThis.__pocketone_llm;
  const J = (s) => JSON.parse(s);
  // a contract's error arrives as "code: message"; throw it as an Error with that message and err.code set
  const clean = (err) => {
    const m = /^([a-z][a-z_]*): ([\s\S]*)$/.exec(err.message);
    if (!m || err.code) return err;
    const e = new Error(m[2]);
    e.code = m[1];
    return e;
  };
  const sync = (kind, op, s) => { try { return J(n.call(kind, op, s)); } catch (err) { throw clean(err); } };
  // build(sub) is $llm with every call made for subject sub ("" for nobody in particular): $llm.for(e) names it
  const build = (sub) => {
  const S = (o) => JSON.stringify(sub ? { ...(o ?? {}), __subject: sub } : (o ?? {}));
  // a provider call, off the VM
  const callAsync = async (kind, op, o) => { try { return J(await n.call.async(kind, op, S(o))); } catch (err) { throw clean(err); } };
  // enrich: the buckets, or { buckets, limit, query, viewer }; viewer is an auth record (ctx.auth) or null for a guest
  const enrichOf = (e) => {
    if (e == null || Array.isArray(e) || typeof e !== "object") return e;
    if (!("viewer" in e)) return e;
    const v = e.viewer;
    const viewer = v == null ? {} : typeof v.collection === "function" ? { collection: v.collection().name, id: v.id } : v;
    return { ...e, viewer };
  };
  const text = (o) => {
    if (!o) return o;
    const out = typeof o.schema === "string" ? { ...o, schema: JSON.parse(o.schema) } : { ...o };
    if (out.enrich !== undefined) out.enrich = enrichOf(out.enrich);
    return out;
  };
  const count = (x) => typeof x === "string" ? { text: x } : Array.isArray(x) ? { messages: x } : (x ?? {});
  const modelsAsync = (kind) => (o) => callAsync(kind, "models", o);
  const jobs = (kind) => Object.freeze({
    createAsync: (o) => callAsync(kind, "create", o),
    getAsync: (id) => callAsync(kind, "get", { id }),
    contentAsync: (id, index) => callAsync(kind, "content", { id, index: index ?? 0 }),
    modelsAsync: modelsAsync(kind),
  });
  // $llm.search: buckets of the app's content (llm.search/v1). Only searchAsync reaches a provider (it embeds the query).
  const sc = (op, o) => J(n.call("search", op, S(o)));
  const ref = (x) => (typeof x === "string" ? { name: x } : x);
  const names = (x) => (x == null ? [] : [].concat(x));
  const searchQuery = (o) => {
    const q = { ...o };
    if (typeof q.buckets === "string") q.buckets = [q.buckets];
    if ("viewer" in q) q.viewer = enrichOf({ viewer: q.viewer }).viewer;
    return q;
  };
  const search = Object.freeze({
    buckets: () => sc("buckets").buckets,
    bucket: (name) => sc("bucket", ref(name)),
    ensure: (o) => sc("ensure", ref(o)),
    update: (o) => sc("update", o),
    deleteBucket: (name) => sc("delete", ref(name)).count,
    sources: (bucket) => sc("sources", { bucket }).sources,
    add: (o) => sc("add", o),
    addURL: (o) => sc("add-url", o),
    follow: (o) => sc("follow", o),
    addFolder: (o) => sc("add-folder", o),
    refresh: (bucket, source) => sc("refresh", { bucket, source }),
    remove: (bucket, list) => sc("remove", { bucket, sources: names(list) }).count,
    documents: (x) => sc("documents", typeof x === "string" ? { bucket: x } : x).documents,
    status: () => sc("status"),
    // random chunks to draw from: local, no embedding
    sample: (o) => sc("sample", typeof o === "string" ? { bucket: o } : { ...o, buckets: o && typeof o.buckets === "string" ? [o.buckets] : o && o.buckets }).results,
    searchAsync: (o) => callAsync("search", "search", searchQuery(o)),
  });` + streamPrelude + chatPrelude + `
  return Object.freeze({
    text: Object.freeze({
      completeAsync: (o) => callAsync("text", "complete", text(o)),
      streamAsync: async (o, onText, onReasoning) => readStream(n.streamStart("text", S(text(o))), onText, onReasoning),
      count: (x) => sync("text", "count", S(count(x))).tokens,
      modelsAsync: modelsAsync("text"),
    }),
    image: Object.freeze({
      generateAsync: (o) => callAsync("image", "generate", o),
      modelsAsync: modelsAsync("image"),
    }),
    speech: Object.freeze({
      speakAsync: (o) => callAsync("speech", "speak", o),
      voicesAsync: async (model) => (await callAsync("speech", "voices", { model })).voices,
      modelsAsync: modelsAsync("speech"),
    }),
    transcribe: Object.freeze({
      transcribeAsync: (o) => callAsync("transcribe", "transcribe", o),
      modelsAsync: modelsAsync("transcribe"),
    }),
    align: Object.freeze({ alignAsync: (o) => callAsync("align", "align", o), modelsAsync: modelsAsync("align") }),
    music: jobs("music"),
    video: jobs("video"),
    embed: Object.freeze({ embedAsync: (o) => callAsync("embed", "embed", o), modelsAsync: modelsAsync("embed") }),
    file: (f) => n.file(S(f), false),
    fileAsync: (f) => n.file.async(S(f), true),
    fromRecord: (record, field) => J(n.fromRecord(record, field)),
    chat,
    search,
    // the ledger: local reads, so plain calls; on $llm.for(e) they answer about that request's user or guest
    ledger: Object.freeze({
      remaining: (o) => sync("ledger", "remaining", S(o)).limits,
      spent: (o) => sync("ledger", "spent", S(typeof o === "string" ? { window: o } : o)),
      summary: (o) => sync("ledger", "summary", S(o)).rows,
      calls: (o) => sync("ledger", "calls", S(o)),
    }),
  });
  };
  const base = build("");` + hooksPrelude + `
  // for(e): e is a request event (a pb_hooks route's e) or a page's ctx (whose request event is ctx.req.event)
  const eventOf = (e) => (e && e.req && e.req.event) || (e && e.event) || e;
  globalThis.$llm = Object.freeze({ ...base, for: (e) => build(n.subject(eventOf(e))), ...hooks });
})()`

// fileFromRecord reads the first file in a record's file field, with its name and media type.
func fileFromRecord(app core.App, rec *core.Record, field string) (llmc.File, error) {
	if rec == nil {
		return llmc.File{}, errors.New("no record")
	}
	name := rec.GetString(field)
	if names := rec.GetStringSlice(field); len(names) > 0 {
		name = names[0]
	}
	if name == "" {
		return llmc.File{}, fmt.Errorf("%s has no file in %q", rec.Id, field)
	}
	fsys, err := app.NewFilesystem()
	if err != nil {
		return llmc.File{}, err
	}
	defer fsys.Close()
	r, err := fsys.GetReader(rec.BaseFilesPath() + "/" + name)
	if err != nil {
		return llmc.File{}, err
	}
	defer r.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		return llmc.File{}, err
	}
	typ := mime.TypeByExtension(path.Ext(name))
	if typ == "" {
		typ = http.DetectContentType(data)
	}
	return llmc.File{Name: name, Type: typ, Data: data}, nil
}
