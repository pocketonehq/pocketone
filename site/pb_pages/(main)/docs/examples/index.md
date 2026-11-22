---
title: Example apps
description: PocketOne apps you can try live and read end to end - Bedtime Story and the Pages starter kits - what each does and how it's built.
---

# Example apps

Small, complete apps that show what a PocketOne app looks like. Each is a PocketOne app with no Go: a `pb_pages`
folder (and sometimes migrations and hooks) run by the stock `pocketone` binary. Their source is in the PocketOne
repository, ready to copy.

## Apps

An app built on [`$llm`](/docs/extensions/llm), PocketOne's cloud models, running live on PocketHost:

| App | Try it | Source |
|---|---|---|
| [**Story Time**](/docs/examples/story-time): a story on any topic, read aloud with pictures and music; set up at sleep.pocketone.dev as Bedtime Story | [sleep.pocketone.dev](https://sleep.pocketone.dev) | [`ext/llm/examples/storytime`](https://github.com/pocketonehq/pocketone/tree/main/ext/llm/examples/storytime) |

To run it yourself:

```bash
cd ext/llm/examples/storytime
pocketone superuser upsert admin@example.com password --dir=pb_data
pocketone serve --dir=pb_data
```

Open the dashboard at `http://127.0.0.1:8090/_/`, add a provider in Settings → LLM (it defaults to Vultr models under a
provider named `vultr`), then open `http://127.0.0.1:8090`. To put your copy online, `pocketone link` your own
app and `pocketone deploy` ([Deploy and live sync](/docs/features/deploy)).

## Starter kits

To start an app of your own instead, begin from a [starter kit](/docs/starters): `pocketone pages create myapp --starter
<kit>`.
