# LLM changelog

Changes to `ext/llm`, newest first. A user-visible change adds its line under `## Unreleased` in the same
commit. Each PocketOne release raises this extension's `version:` when there's something under `## Unreleased` and
renames the section to that version ([Changelogs](../../docs/extensions.md#changelogs)).

## Unreleased

### Upgrading

- Guest calls (`$llm.for(e)` for a visitor who isn't signed in) are refused until Settings → LLM has a limit for all guests together. An app that answers visitors who aren't signed in adds one there (say $1 a day) after upgrading. Plain `$llm` calls aren't guest calls and aren't affected.
- A failed `$llm` call's `err.message` no longer starts with its code (`provider: `, `unknown_model: `...). Code that matched on that text checks `err.code` instead.

### Added

- Limits in Settings → LLM, checked before every call: the whole app, each signed-in user, each guest, or all guests together, at most so many US dollars, output tokens, input tokens or calls a UTC day or month, optionally for one kind or one model. A used-up limit refuses the call with code `limit` (429), naming the limit and when it resets ([Limits](docs/guides/limits.md)).
- `$llm.for(e)`: the same API as `$llm`, with every call counted for the request's signed-in user, or else its guest (a signed cookie, by IP until the browser sends it back), so per-user and per-guest limits apply. Pass a page's `ctx` or a route's `e`.
- Every call is recorded in the `_llm_calls` collection (kind, model, whom it was for, tokens, seconds, cost, and which limit refused it); Settings → LLM shows today's and this month's spend. Rows are kept 13 months; a music or video job's cost is filled in once the job is done ([Spend and the ledger](docs/guides/spend.md)).
- `$llm.search`: buckets of the app's content (files, folders, zips, web pages, Git repositories, collections) with hybrid search (meaning and keywords), through llm.search/v1 (the new LLM Search extension); `await $llm.search.searchAsync({ query, buckets })` ([Search and RAG](docs/guides/search.md)).
- `enrich` on `$llm.text.completeAsync` and `streamAsync` (llm.text/v1), retrieval-augmented generation (RAG) in one option: name buckets (`enrich: ["help"]`, or `{ buckets, limit, query, viewer }`) and the best passages are searched with the prompt and sent after it; the answer's `passages` lists them.
- `$llm.ledger` and the `llm.ledger/v1` contract read the ledger without SQL: `remaining()` (what's left of each limit for a user or guest, tightest first), `spent()` (one subject's totals for a UTC day, month or range), `summary()` (totals by model, kind, subject or day) and `calls()` (the rows, filtered and paged). Over HTTP, `remaining` and `spent` answer a signed-in user about themselves ([Spend and the ledger](docs/guides/spend.md)).
- Custom limits in code: `$llm.onCall` runs before every call that reaches a provider and may change its model, input or subject, or refuse it (`throw e.refuse("...")`, code `limit`); `$llm.onCallDone` runs after with what it used and cost. The limits in Settings → LLM are the built-in handler, run after yours. In Go: `llm.OnCall(app)` and `llm.OnCallDone(app)` in the shared contract package ([Custom limits](docs/guides/custom-limits.md)).

### Changed

- An `$llm` call that fails throws an `Error` with the failure's `code` (`limit`, `provider`, `unknown_model`, ...) in `err.code` and the message alone in `err.message`. Before, the message began with the code (`provider: vultr: 401: Invalid API key`). Code that matched on that text checks `err.code` instead.

- Settings → LLM is kept in the app's key-value store (kv/v1) under the private name `_llm/settings`; the old
  `_params` row `llm` moves there by itself the first time the settings are read. Nothing to do.

### Fixed

- A failed read of the guest signing key no longer makes and saves a new one (which gave every guest a new id): that
  request counts guests by IP and the stored key stays.
- Settings → LLM shows the error when the ledger's spend can't be read, instead of zeros.

## 0.0.1 (2026-11-22)

### Added

- New standard extension, replacing Agent: cloud models for your app, one contract per kind (`llm.text/v1`, `llm.image/v1`, `llm.speech/v1`, `llm.transcribe/v1`, `llm.align/v1`, `llm.music/v1`, `llm.video/v1`, `llm.embed/v1`) and `$llm.<kind>` in page code, where every call that reaches a provider is async and named so (`await $llm.text.completeAsync({...})`, `generateAsync`, `speakAsync`...): it runs off the app's JS VM, so the app keeps serving other requests while a model works. Providers are added in Settings → LLM with your own keys: Vultr, Cloudflare Workers AI, OpenRouter, or any OpenAI-compatible API. Every call names its model as `provider/model`. Music and video are jobs (`createAsync`, `getAsync`, `contentAsync`, or `wait`); `$llm.file()` turns a generated file into a PocketBase file. Every result's `usage.costMicroUSD` is what the call cost, in millionths of a US dollar (a whole number, `0` when free or not known): the provider's figure when it reports one, else estimated from the model's listed prices (`usage.costEstimated`).

### Upgrading

- Agent is gone, with its built-in model, `pocketone.yaml`'s `agent:` section and a multisite host's shared model. Replace `$agent.complete(opts)` with `await $llm.text.completeAsync({ model: "<provider>/<model>", ...opts })` (in an async handler) after adding a provider in Settings → LLM; `$agent.stream` is `await $llm.text.streamAsync`. There are no blocking versions. `thread`, `wait`, `warm`, `forget`, `threads` and `status` have no replacement (they were the built-in model's).

