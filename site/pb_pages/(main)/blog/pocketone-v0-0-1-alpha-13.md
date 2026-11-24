---
title: "PocketOne alpha.13: cloud models in your app"
description: PocketOne v0.0.1-alpha.13 replaces Agent with LLM, adds search buckets and a key-value store, and routes Pages by file extension.
date: 2026-11-24
author: Ben Allfree
tags: release
---

This one is mostly about AI. Agent is gone and LLM takes its place, and there's a lot riding along with it.

**LLM replaces Agent.** Agent ran one built-in model. LLM talks to the clouds instead, on your own keys: Vultr, Cloudflare Workers AI, OpenRouter, or anything that speaks the OpenAI API. Add a provider in Settings → LLM, hit Test, and call it from your pages:

```js
const r = await $llm.text.completeAsync({
  model: 'vultr/glm-5.3',
  prompt: 'Summarize in one sentence: ' + record.getString('body'),
})
// r.text is the summary
```

It's not just text. `$llm` draws pictures, reads text aloud (in a cloned voice if you give it a clip), writes down what's said in audio, makes music and video, and turns text into embeddings. Every call runs off the JS VM, so your app keeps serving while a model works.

I didn't want anybody waking up to a surprise bill, so every call is recorded with what it cost, and Settings → LLM has limits: for the whole app, each signed-in user, each guest, or all guests together, by dollars, tokens or calls a day or a month. If you need something fancier, `$llm.onCall` lets you write your own. More in [AI in your app](/docs/extensions/llm).

**Search buckets.** LLM Search keeps named buckets of your content (files, folders, zips, web pages, Git repos, collections) and searches them by meaning and keywords. Add `enrich: ["help"]` to a completion or a chat and the best passages go along with the question. The new `ask` [starter kit](/docs/starters/ask) puts it together into an "ask my docs" app: `pocketone pages create mydocs --starter ask`. The search box on pocketone.dev works the same way now.

**Pages routes by file extension.** This one is like PHP. `about.ts` sitting next to `about.ets` is that page's code, and a `.ts` with no template is an endpoint. `+page.ts` and `+api.ts` are gone, so you'll have some renaming to do (steps below). Async routes can now wait up to 15 minutes, `setTimeout` and `setInterval` work, and `ctx.realtime.subscribe()` streams record changes to the visitor with the collection's rules applied.

**A key-value store.** `$kv.get`, `set`, `delete` and `list` keep named JSON values without a collection or a migration. It's on by default.

To upgrade, run `pocketone upgrade`. If you used `$agent`, add a provider in Settings → LLM and switch to `await $llm.text.completeAsync({ model: 'provider/model', ... })`. In Pages, rename each `+page.ts` to the name of the page it serves and each `+api.ts` to `<folder>.ts`. API Tokens, LLM Chat and Remotes have a few steps too. The [release notes](https://github.com/pocketonehq/pocketone/releases/tag/v0.0.1-alpha.13) have all of them.
