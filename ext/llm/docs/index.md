---
title: AI in your app
description: "$llm gives your pages and Go code cloud models: text, pictures, speech, transcription, music, video and embeddings, from Vultr, Cloudflare, OpenRouter or any OpenAI-compatible API, on your own keys."
section: AI
---

# AI in your app

**Your app can call cloud models.** `$llm` writes text, draws pictures, reads text aloud, writes down what's said in
audio, makes music and video, and turns text into vectors for search. You bring the provider accounts and keys;
PocketOne connects them to your pages' code and your Go code.

```ts
const r: LlmTextAnswer = await $llm.text.completeAsync({
  model: "vultr/glm-5.3",
  prompt: "Summarize in one sentence: " + record.getString("body"),
  reasoning: "off",
})
// r.text is the summary
```

```go
r, err := textv1.Contract.Must(app).Complete(ctx, extkit.Internal, textv1.CompleteInput{
	Model: "vultr/glm-5.3", Prompt: "Summarize in one sentence: " + record.GetString("body"), Reasoning: "off"})
// r.Text is the summary
```

The examples show each thing in TypeScript, as [Pages](../../pages/docs/overview.md) page code (endpoints
and a page's `.ts`), and in Go. The Go examples call the contract packages (`textv1` is
`pocketone.dev/ext/contracts/llm/text/v1`) with a `context.Context` named `ctx`.

## Add a provider

Open your dashboard, go to **Settings → LLM** and add a provider:

- **Vultr** (Serverless Inference): every kind, music and video included. Paste your API key.
- **Cloudflare Workers AI**: text, pictures, speech, transcription and embeddings, with a free daily allowance. Enter
  your account ID and a token with Workers AI permission.
- **OpenRouter**: one key for hundreds of models. Pictures need credits on your OpenRouter account; small text models
  run on its free allowance.
- **OpenAI-compatible API**: OpenAI, Groq, Together, or a model on your own machine with Ollama or llama-server. Give the
  endpoint with its version path (`http://localhost:11434/v1` for Ollama).

Press **New provider** and pick its type. Give it a short name (it's how your code picks it) and its key, press its
**Test**, then **Save changes**.

## Name a model

Every call names its model as `provider/model`: the name you gave the provider, a slash, and the provider's own id for
the model. With providers named `vultr`, `cf` and `or`:

| Model | Means |
|---|---|
| `vultr/glm-5.3` | Vultr's `glm-5.3` |
| `cf/@cf/meta/llama-3.3-70b-instruct-fp8-fast` | Workers AI's Llama 3.3 |
| `or/black-forest-labs/flux.2-klein-4b` | OpenRouter's FLUX.2 Klein |

To see what you can use, ask: `$llm.text.modelsAsync()`, `$llm.image.modelsAsync()` and so on list each provider's models of that
kind, with their prices where the provider lists them.

Keep model names in one place in your app (a `_private/models.ts` module, say), so changing provider is one edit.

## What you can do

- [Structured answers](guides/structured.md): get JSON back that matches a schema.
- [Streaming answers](guides/streaming.md): show an answer as it's written.
- [Pictures, speech and music](guides/media.md): generate files and save them on records.
- [Conversations](guides/conversations.md): chats that remember, with LLM Chat.
- [Search and RAG](guides/search.md): retrieval-augmented generation over your own files, web pages, Git repositories
  and records: knowledge bases (buckets), hybrid search with `$llm.search`, and `enrich` to ground any call in them.
- [Limits](guides/limits.md): cap what the app spends, per day or month, for the whole app, each user and each guest.
- [Custom limits](guides/custom-limits.md): your own rules in code with `$llm.onCall` and `$llm.onCallDone`: plan tiers, credits, team buckets.
- [Users and guests](guides/users-and-guests.md): `$llm.for(ctx)`, so per-user and per-guest limits apply.
- [Spend and the ledger](guides/spend.md): `$llm.ledger`: what's left of each limit, what someone spent, where the money goes.

Every member of `$llm`, with its options and types, is in the [LLM JavaScript API](/docs/reference/javascript/llm) reference, generated from the declarations your editor sees in `types.d.ts`.

## Thinking models

Many models think before they answer, and their thinking counts toward `maxTokens`. For short jobs (a label, a title,
some JSON) say `reasoning: "off"`; otherwise a short `maxTokens` can be spent entirely on thinking and the answer comes
back empty. For hard questions, `"high"` lets the model think longer. A model that doesn't think ignores it.

## When something goes wrong

A failed call throws an `Error` whose `code` says what went wrong, with a message you can show:

```ts
try {
  const r = await $llm.for(ctx).text.completeAsync({ model: "vultr/glm-5.3", prompt })
} catch (err: any) {
  if (err.code === "limit") return ctx.res.json(429, { error: err.message })   // "limit reached: ..., it resets at 00:00 UTC"
  throw err
}
```

| Code | Means |
|---|---|
| `not_configured` | No provider in Settings → LLM yet. |
| `unknown_model` | No model named, or no provider by that name. |
| `not_supported` | That kind of provider doesn't do this (music on Cloudflare, say). |
| `provider` | The provider said no: a wrong key, no credits, a model it doesn't have. Its message is kept, with your provider's name in front. |
| `timeout` | The provider didn't answer in time, and the request was cancelled. The message names the model and what ran out. Worth a retry. |
| `limit` | A limit refused the call before it reached the provider: one in Settings → LLM or your own ([Limits](guides/limits.md)). |

### Time limits

A provider sometimes drops a request and never answers. Calls don't wait on one for long:

- **Text** is always streamed from the provider, even from `completeAsync`, so a stalled answer is spotted as soon as
  the provider goes quiet: after 10 seconds with nothing from Vultr or OpenRouter (both send something every second
  while a model works, thinking included), 30 from Cloudflare, 2 minutes from other OpenAI-compatible APIs (OpenAI
  sends nothing while its reasoning models think, and a local server may take a while to read a long prompt).
- **Every call** also has an overall limit: 10 minutes for text, 3 for a picture, 2 for speech or alignment, 5 for a
  transcription, 1 for embeddings or starting a music or video job (following one with `wait` has its own limit).

Either one cancels the request and throws with code `timeout`: `vultr/glm-5.3: the provider sent nothing for 10s, so
the call was cancelled`, or `...: no answer in 3m0s, so the call was cancelled`. Go code wanting less passes a context
with an earlier deadline.

## Costs

Every call is billed by your provider, on your account. Every answer's `usage` says what it used, and
`usage.costMicroUSD` what it cost, in millionths of a US dollar (`1000000` is $1, so `usage.costMicroUSD / 1e6` is
dollars). It's a whole number, so costs add up exactly, and it's on every answer of every kind: what the provider
charged when it says (OpenRouter always does; Vultr does for media), else worked out from the model's listed prices,
with `usage.costEstimated` true, else `0` (free, or no prices PocketOne can count).
