---
owners: ["@benallfree"]
version: 0.1.0
title: "LLM Search"
group: binary
summary: "RAG and hybrid search: knowledge bases (buckets) of files, folders, zips, web pages, Git repositories and collections; $llm.search, and enrich to ground $llm calls in them."
---

# LLM Search

LLM Search is retrieval-augmented generation (RAG) for PocketOne apps, part of [AI in your app](../llm/README.md). It
keeps **buckets**, knowledge bases of your content, and runs the pipeline: **ingestion** (sources become documents,
documents become chunks that remember their section, lines and record), **embeddings** with the app's one embedding
model (picked for you from Settings → LLM), and **hybrid search** (semantic search by vector similarity and keyword
search by BM25, merged). Your app makes the bucket it needs; you fill it on the dashboard's **Search** screen with
**sources**: files, folders and zips you drop, pasted text, web pages and Git repositories it keeps current, or
collections it keeps in sync. **Chat** on that screen lets you try it out.

The usual use is grounding a model's answer in your content. Name the buckets on a model call (`enrich`) and the best
passages go into the prompt:

```ts
const answer = await $llm.text.completeAsync({ model: "vultr/glm-5.3", prompt: question, enrich: ["help"] })
// answer.passages: what was added, to show where the answer came from
```

Or search yourself:

```ts
$llm.search.ensure({ name: "storytime", title: "Story Time library", description: "Stories, characters and places to draw on." })
const found = await $llm.search.searchAsync({ buckets: ["storytime"], query: "a fox who lives by the sea", limit: 5 })
for (const r of found.results) console.log(r.bucket, r.document, r.heading, r.text)
```

In Go:

```go
s := searchv1.Contract.Must(app) // llm.search/v1, from pocketone.dev/ext/contracts/llm/search/v1
s.Ensure(ctx, extkit.Internal, searchv1.EnsureInput{Name: "storytime", Title: "Story Time library"})
res, err := s.Search(ctx, extkit.Internal, searchv1.SearchInput{Buckets: []string{"storytime"}, Query: "a fox who lives by the sea"})
```

## Install

LLM Search is built into `pocketone` and on by default, in every app and every instance of a host (a standard member). In
your own Go build, next to LLM:

```go
extkit.Setup(app, []extkit.Extension{llm.New(), llmsearch.New()}, nil, nil)
```

The module is `pocketone.dev/ext/llm-search`; page code reaches it as `$llm.search`, bound by the LLM extension. Its collections (`_llm_search_buckets`, `_llm_search_documents`, `_llm_search_chunks`)
and its keyword index (the SQLite FTS5 table `_llm_search_fts`) come from its own migration, `0001_buckets` in
[schema.go](schema.go), which runs on the first start. They stay empty until something makes a bucket.

## Configure

Searching needs an AI provider with embedding models in Settings → LLM (Vultr's `qwen3-embedding-4b`, say). There's
nothing else to set:

- **The embedding model** is one for the whole app, so every bucket can be searched together. The first time anything
  needs it, Search picks the cheapest embedding model your providers list and keeps it (in kv, `_llm_search/model`).
  Change it on the Search screen: every bucket is embedded again in the background, with its progress shown there, and
  search keeps working meanwhile (chunks not yet done are still found by their words). Content added before there's a
  provider waits, and is embedded once there is one.
- **Each bucket** has a title, a description of what belongs in it, and limits: at most 2,000 documents and 50 MB of
  text. A document is at most 10 MB, a zip 100 MB.

Turn the extension off on a single app with `llm-search: {enabled: false}` in pocketone.yaml (on a host, an instance's
`extensions.llm-search`); its collections and data stay.

## Use

**Buckets.** `$llm.search.ensure({ name, title?, description?, owner? })` makes a bucket if it's missing and
returns it; call it at startup for the bucket your app needs. An existing bucket keeps its settings (only an empty
title, description or owner is filled in), so your admins' changes stick. The description is what the Search
screen shows them: say what to load. A name is letters, digits, `-` and `_`, up to 64.

**Sources.** A bucket holds sources, each one tracked unit however many documents it brings:

| Source | Add it | Documents | Adding it again |
|---|---|---|---|
| File | drop it, or `$llm.search.add({ bucket, name, text })`, or `POST upload?bucket=&name=` with the file as the body | one | replaces it |
| Text | **paste text**, or `add` with `kind: "text"` | one | replaces it |
| Folder | drop it (sent file by file, then pruned) | one per text file, by its path in the folder | updates changed files, adds new ones, **removes missing ones** |
| Zip | drop it, or `upload` a `.zip` | one per text file inside (a shared top folder is dropped) | the same, from the new zip |
| Web page | **add a URL**, or `$llm.search.addURL({ bucket, url, every })` | one | read again on its interval |
| Git repository | add its URL, with or without `.git`: the server is asked whether it's a repository (the Git protocol's `info/refs`), on any host | one per text file on the default branch, plus one per recent commit (300; `commits`) | read again on its interval: one small request when nothing changed |
| Collection | **follow a collection**, or `$llm.search.follow({ bucket, collection, fields, filter })` | one per record the filter keeps | kept in sync as records are saved and deleted |
| Server folder | `$llm.search.addFolder({ bucket, path, include?, exclude?, url? })`: a folder of the app's own files | one per text file, by its path in the folder | read again every five minutes: changed files are embedded again, removed ones leave |

A source's name identifies it in its bucket: the file, folder or zip name, the URL, the collection, or the server
folder's path. Adding one of
the same name replaces it, and the screen asks first. A new version under another name (`notes-v2.zip` for
`notes-v1.zip`) replaces the old one when you say so: **Upload a new version** on its row, or `replace: "<old name>"`.
Dropping content that's already in the bucket under another name (same sha256) asks before adding it again. Each
bucket's sources are its own: replacing `foo.zip` in one bucket leaves a `foo.zip` in another as it was.

Nothing is embedded twice. A document whose text didn't change is left alone, a changed one embeds only its chunks
whose text changed, and a chunk whose content was embedded anywhere in the app (another source, another bucket) reuses
that vector. So two buckets following the same collection, or the same zip in two buckets, cost one embedding.

Folders, zips and repositories leave out binaries, files over 1 MB, dot folders and dotfiles (`.git`, `.github`),
`node_modules`, `vendor` and lockfiles; a repository also takes `exclude` patterns (`"docs/old"`, `"*.min.js"`). Markdown is
split at headings (each chunk carries its heading path, led by the front matter's `title`), code and text at blank
lines, into chunks of about 500 tokens; a page's HTML becomes Markdown (scripts, navigation and footers left out) and
a commit is its subject, body, author, date and the files it changed (no diffs). A collection's record is its first
field as the title and the others below it (editor fields' HTML as text); results from it name their `record`.

Everything is embedded in the background: 64 chunks to a request, under 120 requests a minute, picking up where it
left off after a restart. **Refresh** on a row (or `$llm.search.refresh(bucket, source)`) reads a source again now;
`$llm.search.remove(bucket, sources)` takes sources out; `$llm.search.deleteBucket(name)` removes a bucket. URL and repository
intervals (`every`: `hour`, `day`, `week`) are checked every five minutes. Fetches use Go's default HTTP client, so on
a multitenant host (Pocker) the host's outbound guard applies; only `http` and `https` URLs are taken, public
repositories only, with a 30-second limit per page and five minutes per repository.

**Server folders.** `addFolder` reads files that are already on the server: your docs in `pb_pages`, notes in
`pb_public`, or any folder you put beside them. `path` is relative to the app's own folder (the one holding `pb_data`,
`pb_pages` and `pb_public`) and must stay inside it: no absolute paths, no `..`, no links out of it, and never
`pb_data`. `include` keeps matching files (`["*.md"]`, against the name or the path in the folder), `exclude` leaves out
matches (`["_private", "drafts"]`), beside the usual skips. The folder is read in the background when added and every
five minutes after; an unchanged folder (same files, sizes and times) costs a walk of it. A deploy that changes files
shows up within five minutes, with no restart. `url` says where each file is published, with `{path}` for its path
without the extension (an index file is its folder): with `url: "/docs/{path}"`, `guides/install.md` links to
`/docs/guides/install`, and a result from its "macOS" section to `/docs/guides/install#macos`. Anchors are the ids
PocketOne's Pages (and most Markdown sites) give headings.

```ts
$llm.search.ensure({ name: "docs", title: "Our docs" })
$llm.search.addFolder({ bucket: "docs", path: "pb_pages/docs", include: ["*.md"], exclude: ["_private"], url: "/docs/{path}" })
const found = await $llm.search.searchAsync({ buckets: "docs", query: "install on a Mac" })
// found.results[0].url → "/docs/install#macos"
```

**Drawing at random.** `$llm.search.sample({ buckets?, count?, prefix? })` returns chunks picked at random from the buckets named, in the same shape as search results (no score): material to draw from, for a "surprise me" or a topic nobody has asked about. Nothing is embedded, so it's local and free.

**Searching.** `await $llm.search.searchAsync({ query, buckets?, limit?, prefix?, viewer? })` returns the best chunks across
the buckets named (every bucket when none are): `{ results: [{ bucket, document, text, heading, startLine, endLine,
record, url, score }], model, usage }`; `url` links to where the passage is published, for a web page or a server folder
with a `url` pattern. `limit` is 5 by default, at most 50; `prefix` keeps documents whose name starts with
it. Each search embeds the query once (a few tokens; a repeated query is cached), the only part that calls a provider,
so it's the only async call. Ranking merges similarity of meaning (a loop in Go over vectors cached in memory) with
keyword matching (BM25), so exact names like `onRecordCreate` or a character's name rank where they should.

**Who sees what.** A bucket holds a copy of its records' text, so pass `viewer` to apply the collection's view rules:
`viewer: ctx.auth` (or `e.auth`) leaves out matches from records that user can't view, and `null` is a signed-out
guest. Without `viewer` a search sees everything, as your own server code. Over HTTP and MCP only superusers and API
tokens with the `search` scope (or `search:read` to list and search) get in.

**Enriching a model call.** `enrich` on `$llm.text.completeAsync`, `streamAsync` and LLM Chat's `sendAsync` (or a
chat's options) searches the buckets with the prompt and sends the best passages after it, numbered so the model can
cite them; `answer.passages` lists them. Give the buckets (`enrich: ["help"]`) or `{ buckets, limit?, query?, viewer? }`
(default 5 passages, at most 20); `viewer: ctx.auth` applies collections' view rules, as for a search. LLM Chat saves the passages with the question, so later turns send the same thing.

**Cost.** Embedding goes through llm.embed/v1, billed to your provider account like any other `$llm` call. Each
document shows what embedding it cost. `qwen3-embedding-4b` on Vultr is $0.05 per million tokens, about 4 MB of text.

## Integrations

| With | What happens |
|---|---|
| [LLM](../llm/README.md) | Embeds chunks and queries (llm.embed/v1), and adds passages to text calls with `enrich`. Search's own HTTP and MCP API is the [llm.search/v1](../contracts/llm/search/v1/README.md) contract. |
| [LLM Chat](../llm-chat/README.md) | `enrich` on a chat or a send. |
| [Key-Value Store](../kv/README.md) | Keeps the app's embedding model. |
| [Sandbox](../sandbox/README.md) | On a Pocker host, its egress guard covers page and repository fetches. |
| [Multisite](../multisite/README.md) | Each instance keeps its own buckets and model in its own database. |

## Example

```go
// Command example is a PocketBase app with search buckets: the llm extension embeds, search keeps the buckets. Add a
// provider with embedding models in Settings → LLM, run `go run . serve`, open the dashboard's Search screen, make a
// bucket and drop Markdown files on it. Or over HTTP as a superuser:
//
//	POST /api/_c/search/v1/ensure   {"name": "docs"}
//	POST /api/_c/search/v1/add      {"bucket": "docs", "name": "install.md", "text": "# Install\n..."}
//	POST /api/_c/search/v1/search   {"buckets": ["docs"], "query": "how do I install it"}
//
// In page code the same is $search.ensure(...), $search.add(...) and await $search.searchAsync(...).
package main

import (
	"log"

	"ganigeorgiev.com/pocketbase/pocketbase"

	"pocketone.dev/ext/devusers"
	"pocketone.dev/ext/extkit"
	"pocketone.dev/ext/llm"
	llmsearch "pocketone.dev/ext/llm-search"
)

func main() {
	app := pocketbase.New()
	devusers.Register(app)
	if _, err := extkit.Setup(app, []extkit.Extension{llm.New(), llmsearch.New()}, nil, nil); err != nil {
		log.Fatal(err)
	}
	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
```

## License

MIT
