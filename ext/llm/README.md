---
owners: ["@benallfree"]
version: 0.0.1
title: "LLM"
group: binary
summary: "Cloud models for your app: text, pictures, speech, transcription, alignment, music, video and embeddings from Vultr, Cloudflare, OpenRouter or any OpenAI-compatible API."
---

# LLM

LLM gives your app cloud models to call, one kind at a time: text, pictures, speech, transcription, music, video and
embeddings. You add providers in Settings → LLM, each with your own key, and every call names its model as
`provider/model`:

```ts
const r: LlmTextAnswer = await $llm.text.completeAsync({ model: "vultr/glm-5.3", prompt: "Summarize: " + body, reasoning: "off" })
const pic = await $llm.image.generateAsync({ model: "or/black-forest-labs/flux.2-klein-4b", prompt: "a lighthouse at dusk, watercolor" })
record.set("cover", $llm.file(pic.files[0]))
```

```go
r, err := textv1.Contract.Must(app).Complete(ctx, extkit.Internal, textv1.CompleteInput{
	Model: "vultr/glm-5.3", Prompt: "Summarize: " + body, Reasoning: "off"})
```

`vultr` is the name you gave a provider; what follows the first slash is that provider's own id for the model. Any
call can name any model of any provider, so one app can use two picture providers, or a cheap text model for labels and
a large one for writing.

Providers come in four types:

| Type | What it reaches |
|---|---|
| `vultr` | [Vultr Serverless Inference](https://docs.vultr.com/products/compute/serverless-inference): every kind, music, video and alignment included. |
| `cloudflare` | [Workers AI](https://developers.cloudflare.com/workers-ai/) on your Cloudflare account: text, pictures, speech, transcription and embeddings. |
| `openrouter` | [OpenRouter](https://openrouter.ai): hundreds of models behind one key; text, pictures, speech, transcription, video and embeddings. |
| `openai` | Any API shaped like OpenAI's: OpenAI itself, Groq, Together, or Ollama and llama-server on localhost. Add as many as you like. |

The answers come from your provider accounts, on your keys; PocketOne adds no limits or charges.

## Install

LLM is built into `pocketone` and on in every app and every instance of a host (a standard member). It does nothing
until you add a provider. In your own Go build:

```go
extkit.Setup(app, []extkit.Extension{llm.New()}, nil, nil)
```

The module is `pocketone.dev/ext/llm`.

## Configure

**Settings → LLM** (superusers) lists your providers. Press **New provider**, pick its type, give it a name (the part
before the slash in a model) and enter its key: an endpoint for `openai` providers (with its version path, like
`https://api.groq.com/openai/v1`), the account ID for Cloudflare (with a token that has Workers AI permission). Vultr,
Cloudflare and OpenRouter providers can't be saved without a key; an `openai` one can (a local Ollama has none). Each
provider's **Test** checks its key, typed or saved, without spending anything, and its check mark turns green when it
passes. Saving applies at once.

Keys stay on the server: the screen never shows a saved key again, and a key is never sent to an endpoint other than the
one it was entered for (change a provider's endpoint and you enter its key again). The settings are in the app's
[key-value store](../kv/README.md), under the private name `_llm/settings` (only Go code reaches it; the kv/v1 API
and `$kv` don't).

Under the providers, **Limits** cap what the app spends: the whole app, each signed-in user, each guest or all guests
together, at most so many US dollars, output tokens, input tokens or calls a UTC day or month, optionally for one kind
or model. Every call passes them before it reaches a provider, and a used-up limit refuses it with code `limit` (429).
Page code says whom a call is for with `$llm.for(ctx)` (a page's `ctx` or a route's `e`): its signed-in user, or else a
guest told apart by a signed cookie. Guest calls are refused until there's a limit for all guests together. Every call
is recorded in the `_llm_calls` collection, and the screen shows today's and this month's spend from it
([Limits](docs/guides/limits.md), [Users and guests](docs/guides/users-and-guests.md)). `$llm.ledger` reads it: what's left of
each limit, what someone spent, totals by model or person ([Spend and the ledger](docs/guides/spend.md)). The screen's
limits are the built-in handler on `$llm.onCall`; your own handlers there add plan tiers, credits or anything else
([Custom limits](docs/guides/custom-limits.md)).

There's no pocketone.yaml configuration besides the switch: `llm: {enabled: false}` turns it off on a single app (on a
host, an instance's `extensions.llm`), and with it `$llm` and the `llm.*` contracts.

## Use

In page code it's `$llm.<kind>`. Every call that reaches a provider is async, and its name says so: await it in an async
handler (`await $llm.text.completeAsync({...})`). It runs off the app's JS VM, so the app keeps serving other requests
while a model works; there are no blocking versions to reach for. Each call names its `model`:

| Call | What it does |
|---|---|
| `$llm.search.searchAsync({ query, buckets?, limit?, viewer? })` and the rest of `$llm.search` | Hybrid search over knowledge bases (buckets) of your content, through the [LLM Search](../llm-search/README.md) extension ([Search and RAG](docs/guides/search.md)). |
| `$llm.text.completeAsync({ model, system?, prompt \| messages, schema?, maxTokens?, temperature?, stop?, reasoning?, enrich?, options? })` | Answers: `{ text, data, reasoning, stopReason, model, usage, passages }`. With `schema`, `data` is the parsed JSON. With `enrich` (RAG: buckets to search, `["help"]`), the best passages from them are sent after the prompt and listed in `passages` ([LLM Search](../llm-search/README.md)). |
| `$llm.text.streamAsync(opts, onText, onReasoning?)` | The same, calling `onText` with each piece as it's written (`false` stops it). |
| `$llm.text.count(text \| messages)` | About how many tokens (four characters each). Local, so not async. |
| `$llm.image.generateAsync({ model, prompt, references?, n?, size?, aspectRatio? })` | Pictures: `{ files, model, usage }`. `references` are pictures to draw from, on models with the feature `reference images`. |
| `$llm.speech.speakAsync({ model, text, voice?, format?, speed?, instructions?, reference? })`, `voicesAsync(model)` | Audio: `{ file, model, usage }`. `reference: { audio, text? }` reads in the voice of that clip (voice cloning). |
| `$llm.transcribe.transcribeAsync({ model, audio, language?, prompt? })` | Text from audio: `{ text, language, model, usage }`. |
| `$llm.align.alignAsync({ model, audio, text, mode?, language? })` | When each line and word of `text` is said in `audio`: `{ duration, lines, words, unaligned, model, usage }`, in seconds. For captions timed to a voice. |
| `$llm.music.createAsync({ model, prompt, lyrics?, seconds?, wait? })`, `getAsync(id)`, `contentAsync(id, index)` | Music, as a job (below). |
| `$llm.video.createAsync({ model, prompt, image?, seconds?, size?, aspectRatio?, wait? })`, `getAsync(id)`, `contentAsync(id, index)` | Video, as a job. |
| `$llm.embed.embedAsync({ model, input, dimensions? })` | One vector per text: `{ vectors, model, usage }`. |
| `$llm.<kind>.modelsAsync({ provider? })` | The models your providers offer of that kind, with their prices, size, features and languages where known (below). |
| `$llm.file(file)`, `$llm.fileAsync(file)` | A generated file as a PocketBase file, for `record.set("field", ...)`; `fileAsync` also downloads one the provider gave only a URL for. |
| `$llm.fromRecord(record, field)` | The other way: a record's stored file as a file with its bytes, to send to a model (a voice clip, audio to transcribe). |
| `$llm.chat.*` | Stored conversations, from the [LLM Chat](../llm-chat/README.md) extension. |

**`reasoning`** is how much a reasoning model thinks before answering: `"off"`, `"low"`, `"medium"`, `"high"`, or left
out for the model's own default. Many models think by default, and the thinking counts toward `maxTokens`: for short
jobs (labels, titles, JSON) say `"off"`, or the answer can come back empty with `stopReason: "length"`.

**`options`** passes a provider's own fields through unchanged (`seed`, `steps`, OpenRouter's `provider` routing...), for
what the common fields don't cover.

**Files** come back as `{ name, type, data, url }`: `data` is the bytes (base64 in JSON), `url` the provider's own copy
when it keeps one (it may expire). Audio to transcribe is sent the same way.

**Jobs:** music and video take a while. `createAsync` starts one and returns `{ id, status }`; follow it with `getAsync(id)` until
`status` is `done`, then fetch each of its `outputs` with `contentAsync(id, index)`. Or pass `wait: "5m"`: `createAsync` then
resolves once the job is done, its `files` included. The id names the provider
(`vultr:music_02a2…`), so `get` finds it again after a restart; the provider keeps the job, not your app.

**Cost:** every result's `usage.costMicroUSD` is what the call cost, in millionths of a US dollar (`1000000` is $1), a
whole number on every answer of every kind: what the provider charged when it says (OpenRouter on every call, Vultr for
media), else worked out from the model's listed prices and what the call used (tokens, cached tokens, pictures,
seconds, characters), with `usage.costEstimated` true, else `0` (free, or no prices it can count).

**Models and prices:** each model in a `models` answer carries what its provider says about it: `contextTokens`,
`size` ("27B", "30B (3B active)"), `features` (`tools`, `structured output`, `vision`, `audio input`, `voice cloning`,
`voice design`, `built-in voices`, `lyrics`), `languages`, and `prices`. A price is a rate on one meter, in US dollars
per `unit`, and every provider's are given in the same units: `1M input tokens`, `1M output tokens`,
`1M cached input tokens`, `second` (of audio or video made, or read), `megapixel`, `image`, `1K characters` (read
aloud), `request`. Its `source` is `listed` (the provider's own list) or `measured`: what this app's calls to the model
were actually billed, from the ledger, over `calls` calls (text in `1M tokens`, in and out together, since a bill
doesn't split them; pictures per `request`). Costs worked out from listed prices never count toward a measured one.
No prices means the price isn't known. Extras a call may not use (web search, cache writes, images sent in) aren't
listed, and a model with a price in a unit that can't be put in these (per inference step) lists none rather than
look cheaper than it is.

Settings → LLM lists every model of each kind across your providers under **Models**, searchable and cheapest first.
Other dashboard screens use the same pieces through `window.pocketone.llm`: `select({ kind, value, onchange })` is a
searchable dropdown of a kind's models with their prices (Story Time's settings use it). Give it the work the model is
for, `job: () => ({ "1M input tokens": 0.006, "1M output tokens": 0.002 })` (how much of each price unit the work uses)
and `per: "a story"`, and each model also shows what that work costs on it ("≈ $0.0012 a story", "≥" when it lists only
one side of the tokens), cheapest first. `browser({ kinds?, kind? })` is the whole list, and `models(kind)`,
`prices(model)`, `priceText(model)`, `rank(model)` and `costFor(model, job)` the data underneath.

**Errors** have a code: `not_configured` (no providers yet), `unknown_model` (no model named, or no provider by that
name), `not_supported` (that provider type doesn't do this kind), `provider` (the provider's own error, its message
kept, with the provider's name in front), `timeout` (the provider didn't answer in time; the request was cancelled).
Text calls always stream from the provider, so a stalled answer fails after a few seconds of silence (10 s on Vultr and
OpenRouter), and every call has an overall limit by kind ([Time limits](docs/index.md#time-limits)).

**Over HTTP** each kind is a contract at `/api/_c/llm.<kind>/v1/` (`llm.text/v1/complete`, `llm.image/v1/generate`...),
for superusers and for API tokens with the `llm` scope (`llm:read` for `models`, `voices`, `get` and `content`); see
[llm.text/v1](../contracts/llm/text/v1/README.md) and the others in the [catalogue](../contracts/README.md).

What each provider type covers:

| | Text | Pictures | Speech | Transcription | Music | Video | Embeddings |
|---|---|---|---|---|---|---|---|
| `vultr` | yes | yes (and references) | yes, with voices and cloning | yes | yes (job) | yes (job) | yes |
| `cloudflare` | yes | yes | yes | yes | | | yes |
| `openrouter` | yes | yes (and references) | yes, with voices and cloning | yes | | yes (job) | yes |
| `openai` | yes | yes | yes | yes | | | yes |

An `openai` provider's model list names only ids, so each model's kind is guessed from its name (`whisper` is
transcription, `tts` is speech, `embed` is embeddings, and so on; anything else is text).

## Integrations

| With | What happens |
|---|---|
| [LLM Chat](../llm-chat/README.md) | Stored conversations on top of `llm.text/v1`: `$llm.chat.new()`, `$llm.chat.open(id).sendAsync(text)`. |
| [Settings](../settings/README.md) | Settings → LLM is its `settings/v1` section. |
| [API Tokens](../api-tokens/README.md) | A token with the `llm` scope can call the contracts over HTTP, and MCP clients get them as tools. |
| [Multisite](../multisite/README.md) | Each instance has its own Settings → LLM, providers and keys. |

## Example

The example is a PocketBase app with LLM that, once you've added a provider named `vultr` in Settings → LLM, answers
one question at start-up and logs it.

```go
// Command example is a PocketBase app with the llm extension. Add a provider named "vultr" in Settings → LLM (or
// change the model below), run `go run . serve`, and watch the log for "llm: answer".
package main

import (
	"context"
	"log"

	"ganigeorgiev.com/pocketbase/pocketbase"
	"ganigeorgiev.com/pocketbase/pocketbase/core"

	textv1 "pocketone.dev/ext/contracts/llm/text/v1"
	"pocketone.dev/ext/devusers"
	"pocketone.dev/ext/extkit"
	"pocketone.dev/ext/llm"
)

func main() {
	app := pocketbase.New()
	devusers.Register(app)
	if _, err := extkit.Setup(app, []extkit.Extension{llm.New()}, nil, nil); err != nil {
		log.Fatal(err)
	}
	app.OnServe().BindFunc(func(e *core.ServeEvent) error {
		go func() {
			res, err := textv1.Contract.Must(app).Complete(context.Background(), extkit.Internal, textv1.CompleteInput{
				Model: "vultr/glm-5.3", Prompt: "In one sentence: what is PocketBase?", MaxTokens: 200, Reasoning: "off"})
			app.Logger().Info("llm: answer", "text", res.Text, "model", res.Model, "error", err)
		}()
		return e.Next()
	})
	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
```

The [ask starter kit](../pages/starters/ask/README.md) (`pocketone pages create mydocs --starter ask`) is a whole PocketOne app
(no Go) on `$llm`: it answers questions from a folder of Markdown docs kept in an LLM Search bucket, citing its pages.

## License

MIT
