# PocketOne changelog

PocketOne's own changes, newest first: core, the `pocketone` binary, the site, and anything that isn't one
extension's. Each extension keeps its own changelog in its folder (`ext/<name>/CHANGELOG.md`); a release here lists
every extension that got a new version, with a link to its changes. Upstream PocketBase's changes are in
[CHANGELOG.pocketbase.md](CHANGELOG.pocketbase.md); each release here says which PocketBase version it's built on.

## How this file is kept

- **Every commit to `main` with a user-visible change adds its line under `## Unreleased` in the same commit:** a
  feature, a fix, a changed behavior or default, a removal, or a new upstream PocketBase version. Docs-only and
  internal changes don't need one. **A change to one extension goes in that extension's `CHANGELOG.md` instead**, under
  its own `## Unreleased`, with the same headings ([Changelogs](docs/extensions.md#changelogs)).
- **Group lines under these headings, in this order, and leave out empty ones:**
  - **Highlights:** written when the release is cut, never per commit: the two to four changes a reader should hear
    about first (a new feature or extension, a big change), each a bold lead and a sentence or two with a link. Every
    release that adds something has one, so nothing new hides in the Extensions list.
  - **Upgrading:** what someone on the previous release must do, as steps. Required for anything breaking.
  - **Added**
  - **Changed**
  - **Fixed**
  - **Extensions:** written by the release step, never by hand: each extension's new version, linked to its changes.
  - **PocketBase:** the upstream version this is built on, when it changes.
- **Write for someone running the binary:** what changed and what to do, with the command, flag or config key.
- **One line per change.** GitHub shows every newline as a line break, so don't hard-wrap lines.
- **Cutting a release:**
  1. Run `go run ./.github/extensions release vX.Y.Z`. It raises the version of every extension with lines under its
     `## Unreleased` (minor when it has upgrade steps, else patch) and stamps that section with it, renames this
     file's `## Unreleased` to `## vX.Y.Z (YYYY-MM-DD)` with an Extensions list, and starts new empty `## Unreleased`
     sections. Review and commit.
  2. Write the section's **Highlights**. A release with a user-facing new feature also gets a blog post written from
     them: `site/pb_pages/(main)/blog/pocketone-vX-Y-Z.md` (front matter `title`, `description`, `date`, `author`,
     `tags: release`), what's new and why it matters, the upgrade steps if any, and links to the docs and the release.
     A maintenance release (fixes only) gets no post. Commit and land them (a post goes live on pocketone.dev with the
     next site deploy, so land it just before publishing, or right after).
  3. Run `examples/pocketone/release.sh --check`, then `examples/pocketone/release.sh vX.Y.Z` once it passed: it tags,
     writes the notes (`examples/pocketone/release-notes.sh vX.Y.Z`) and runs goreleaser, which leaves a draft release.
  4. Publishing the draft starts its announcement in GitHub Discussions (Announcements, with the release notes;
     goreleaser sets the category).

  The release header adds a link to the blog post (when there is one) and the install and upgrade steps on top. Release notes are this file's section for the version,
  with its links pointing at the release on GitHub.

## Unreleased

## v0.0.1-alpha.14 (2026-11-27)

### Highlights

- **WASM modules stay compiled on servers without `$HOME`.** Compiled modules are now cached in the app's `pb_cache/wasm`, so a server run as a systemd service no longer compiles a module on every call (about a second for a Go module). `pocketone cache clear` empties it ([WASM](ext/wasm/CHANGELOG.md)).
- **Two Pages fixes.** A filtered `ctx.realtime.send` with nobody subscribed sends nothing instead of failing with a 500, and an array read back with `ctx.store(name)` is a JS array again, so `push` works after the first request. Together they fix the Datastar starter's chat ([Pages](ext/pages/CHANGELOG.md)).
- **Multisite hosts know how each instance is doing.** The host provides [instance.status/v1](ext/contracts/instance/status/v1/README.md): each instance is `starting` until `/api/health` answers, then `running`, and `idle` or `failed` when it ends, with every change sent to subscribers ([Multisite](ext/multisite/CHANGELOG.md)).

### Extensions

- [Docker](ext/docker/CHANGELOG.md#005-2026-11-27) 0.0.4 → 0.0.5
- [Multisite](ext/multisite/CHANGELOG.md#012-2026-11-27) 0.1.1 → 0.1.2
- [Pages](ext/pages/CHANGELOG.md#021-2026-11-27) 0.2.0 → 0.2.1
- [Pocker](ext/pocker/CHANGELOG.md#014-2026-11-27) 0.1.3 → 0.1.4
- [WASM](ext/wasm/CHANGELOG.md#003-2026-11-27) 0.0.2 → 0.0.3

## v0.0.1-alpha.13 (2026-11-24)

### Highlights

- **LLM replaces Agent.** Your app calls cloud models on your own keys: text, pictures, speech, transcription, music, video and embeddings, from Vultr, Cloudflare Workers AI, OpenRouter or any OpenAI-compatible API. Add providers in Settings → LLM, then `await $llm.text.completeAsync({ model: "vultr/glm-5.3", prompt })` in page code. Every result says what it cost, every call is recorded, and limits in Settings → LLM (whole app, each user, each guest) stop a bill before it happens. Voice cloning and reference images are in too ([AI in your app](https://pocketone.dev/docs/extensions/llm)). Agent and its built-in model are gone (see Upgrading).
- **New: LLM Search.** Named buckets of your app's content (files, folders, zips, web pages, Git repositories, collections) searched by meaning and keywords, and `enrich: ["help"]` on a completion or chat sends the best passages with the question. The new `ask` starter kit builds on it: `pocketone pages create mydocs --starter ask` ([LLM Search](ext/llm-search/README.md)).
- **Pages routes go by file extension, like PHP.** `about.ts` beside `about.ets` is that page's code, and a `.ts` with no template is an endpoint; `+page.ts` and `+api.ts` are gone (see Upgrading). Async routes can wait up to 15 minutes, `setTimeout` and friends work, and `ctx.realtime.subscribe()` streams record changes to the visitor with the collection's rules applied ([Pages](ext/pages/CHANGELOG.md)).
- **New: Key-Value Store.** `$kv.get/set/delete/list` keeps named JSON values without a collection or a migration, on by default ([Key-Value Store](ext/kv/README.md)).

### Upgrading

- Pages, API Tokens, LLM Chat and Remotes have upgrade steps of their own: see each one's changelog under Extensions below. In Pages, rename each `+page.ts` to the name of the page it serves (`products/[id]/+page.ts` beside `index.ets` becomes `products/[id]/index.ts`) and each `+api.ts` to `<folder>.ts` beside its folder.
- Agent is replaced by [LLM](ext/llm/README.md): add your providers (Vultr, Cloudflare Workers AI, OpenRouter, or any OpenAI-compatible API) in Settings → LLM, then change `$agent.complete(opts)` to `await $llm.text.completeAsync({ model: "<provider>/<model>", ...opts })` in an async handler (every `$llm` call that reaches a provider is async). The built-in model, `pocketone.yaml`'s `agent:` section and a multisite host's shared model are gone. Agent Chat is now [LLM Chat](ext/llm-chat/CHANGELOG.md) (`$llm.chat`).

### Added

- [LLM](ext/llm/README.md), a standard extension: text, pictures, speech, transcription, music, video and embeddings from cloud providers on your own keys, as `$llm.<kind>` in page code and the `llm.*` contracts. Every call names its model as `provider/model`.

### Changed

- PocketPages is now just Pages, a feature of every PocketOne app: the docs, starter kits and `pocketone pages create` say "PocketOne app" (a new app's title and heading read PocketOne). The `pocketpages` Agent Skill is folded into the `pocketone` skill (`pages.md`); reinstall the plugin to drop the old skill.

### Fixed

- Realtime messages reach each subscriber in the order they were sent: two quick changes to the same record could arrive swapped, so a client could end on the stale one.
- A realtime client that disconnects no longer races the messages still being sent to it.

### Extensions

- [API Tokens](ext/api-tokens/CHANGELOG.md#030-2026-11-24) 0.2.1 → 0.3.0 (has upgrade steps)
- [Bulk Mail](ext/bulkmail/CHANGELOG.md#001-2026-11-24) 0.0.1
- [Config](ext/config/CHANGELOG.md#011-2026-11-24) 0.1.0 → 0.1.1
- [Deploy](ext/deploy/CHANGELOG.md#021-2026-11-24) 0.2.0 → 0.2.1
- [Firewall](ext/firewall/CHANGELOG.md#001-2026-11-24) 0.0.1
- [JSVM](ext/jsvm/CHANGELOG.md#013-2026-11-24) 0.1.2 → 0.1.3
- [Key-Value Store](ext/kv/CHANGELOG.md#001-2026-11-24) 0.0.1 (new)
- [LLM](ext/llm/CHANGELOG.md#001-2026-11-24) 0.0.1 (new)
- [LLM Chat](ext/llm-chat/CHANGELOG.md#010-2026-11-24) 0.0.2 → 0.1.0 (has upgrade steps)
- [LLM Search](ext/llm-search/CHANGELOG.md#010-2026-11-24) 0.1.0 (new)
- [Multisite](ext/multisite/CHANGELOG.md#011-2026-11-24) 0.1.0 → 0.1.1
- [Pages](ext/pages/CHANGELOG.md#020-2026-11-24) 0.1.2 → 0.2.0 (has upgrade steps)
- [Pocker](ext/pocker/CHANGELOG.md#013-2026-11-24) 0.1.2 → 0.1.3
- [Remotes](ext/remotes/CHANGELOG.md#010-2026-11-24) 0.0.2 → 0.1.0 (has upgrade steps)
- [Sandbox](ext/sandbox/CHANGELOG.md#004-2026-11-24) 0.0.3 → 0.0.4
- [Standard Set](ext/standard/CHANGELOG.md#013-2026-11-24) 0.1.2 → 0.1.3
- [Upgrade](ext/upgrade/CHANGELOG.md#004-2026-11-24) 0.0.3 → 0.0.4

## v0.0.1-alpha.12 (2026-11-21)

### Highlights

- **JS hooks no longer crash the app on shared maps.** A hook that got a record or other value another goroutine was writing could abort the whole process with `concurrent map iteration and map write`. Fixed in [JSVM](ext/jsvm/CHANGELOG.md#012-2026-11-21). Upgrade if you run JS hooks under load.
- **PocketOne branding in every dashboard.** Instances on a multisite host and other builds on the standard set now show PocketOne's logo and version, with Docs pointing at pocketone.dev.

### Extensions

- [Agent](ext/agent/CHANGELOG.md#003-2026-11-21) 0.0.2 → 0.0.3
- [JSVM](ext/jsvm/CHANGELOG.md#012-2026-11-21) 0.1.1 → 0.1.2
- [Standard Set](ext/standard/CHANGELOG.md#012-2026-11-21) 0.1.1 → 0.1.2

## v0.0.1-alpha.11 (2026-11-21)

### Highlights

- **New installation tools.** Windows installs with Scoop, macOS and Linux with `curl -fsSL https://pocketone.dev/install.sh | sh`, and every release has `.deb`, `.rpm` and `.apk` packages that set PocketOne up as a systemd service ([Installation](https://pocketone.dev/docs/installation)). The Homebrew formula moved to `pocketonehq/tap` (see Upgrading).
- **`pocketone clone`.** Start a local project from a running app: `pocketone clone <url>` signs you in as a superuser, pulls its code, and you're ready for `pocketone dev` ([Deploy](ext/deploy/CHANGELOG.md)).

### Upgrading

1. A multisite host runs one runtime: docker when `pocketone.yaml` has a `docker` section, else pocker. A host that had a `docker` section and served most instances on pocker now serves them all on docker; remove the `docker` section to stay on pocker.
2. Remove `runtime:` from instance records (instances file or mothership): it's no longer read, and every instance runs on the host's runtime.
3. `providers: { runtime/v1: ... }` takes one name (`runtime/v1: docker`), not a list.
4. Installed with Homebrew? The formula moved to the `pocketonehq/tap` tap: `brew uninstall pocketone`, then `brew install pocketonehq/tap/pocketone`. The `pockethost/tap` formula no longer gets new releases.

### Added

- Builds on top of PocketOne can be the whole `pocketone` binary plus their own commands: `pocketone.dev/examples/pocketone/binary.Main` takes the build's `--version` text, where `upgrade` gets releases, and extra commands (one with a PocketOne command's name replaces it). PocketHost's `pockethost` uses it.
- Install on Windows with [Scoop](https://scoop.sh): `scoop bucket add pocketonehq https://github.com/pocketonehq/scoop-bucket`, then `scoop install pocketone`. Every release updates the manifest; manual steps are in [Windows without Scoop](https://pocketone.dev/docs/guides/advanced-installation#windows-without-scoop).
- Install script for macOS and Linux: `curl -fsSL https://pocketone.dev/install.sh | sh` installs the newest release, checking its checksum; run it again to upgrade ([Installation](https://pocketone.dev/docs/installation)).
- Linux packages: every release has `.deb`, `.rpm` and `.apk` packages; the `.deb` and `.rpm` add a `pocketone` systemd service running as its own user, configured in `/etc/pocketone`, with data in `/var/lib/pocketone` ([Run PocketOne on a Linux server](https://pocketone.dev/docs/guides/linux-server)).

### Changed

- On a multisite host, an instance's `pb_cache` is hoisted out of its folder to the host's `pb_cache/instances/<instance id>/` (`extkit.CacheDir` and `extkit.CacheRoot` return it; hosts use `extkit.InstanceCacheRoot`). `instances` is reserved in a host's `pb_cache`.
- runtime/v1 has one provider per host: `pocketone serve` with a `multisite` section registers only the runtime it runs (docker with a `docker` section, else pocker), and the instance record's `runtime` field is gone.
- The `pocketone` commands that talk to a remote wait and try again when it answers 429 (rate limited), after its `Retry-After` (up to 4 times; a wait over a minute is reported instead).
- `pocketone login`, `link` and `clone` sign in as a superuser in the browser: the app's sign-in page opens on the superuser form, even when the browser is signed in there as an app user (whose token the deploy API refuses).

### Fixed

- `pocketone deploy` no longer overwrites files changed on the remote since your last deploy, pull or dev: it stops and lists them, and `--force` overwrites ([Deploy](ext/deploy/CHANGELOG.md)).
- Contract streams (`instance.watch/v1`'s stream, `remote/v1` logs and the rest) no longer end after 5 minutes: they clear the server's write timeout, as PocketBase's realtime does. Edges following the mothership reconnected (and refetched every hostname) every 5 minutes.

### Extensions

- [API Tokens](ext/api-tokens/CHANGELOG.md#021-2026-11-21) 0.2.0 → 0.2.1
- [Deploy](ext/deploy/CHANGELOG.md#020-2026-11-21) 0.1.0 → 0.2.0 (has upgrade steps)
- [Docker](ext/docker/CHANGELOG.md#004-2026-11-21) 0.0.3 → 0.0.4
- [Edge](ext/edge/CHANGELOG.md#002-2026-11-21) 0.0.1 → 0.0.2
- [MCP](ext/mcp/CHANGELOG.md#002-2026-11-21) 0.0.1 → 0.0.2
- [Multisite](ext/multisite/CHANGELOG.md#010-2026-11-21) 0.0.2 → 0.1.0 (has upgrade steps)
- [Pocker](ext/pocker/CHANGELOG.md#012-2026-11-21) 0.1.1 → 0.1.2
- [Remotes](ext/remotes/CHANGELOG.md#002-2026-11-21) 0.0.1 → 0.0.2
- [Standard Set](ext/standard/CHANGELOG.md#011-2026-11-21) 0.1.0 → 0.1.1
- [Upgrade](ext/upgrade/CHANGELOG.md#003-2026-11-21) 0.0.2 → 0.0.3

## v0.0.1-alpha.10 (2026-11-20)

### Highlights

- **`ctx.pb()` writes work in PocketPages.** `create`, `update` and every other call with a body threw `TypeError: operand 'prototype' property is not an object`, because `instanceof` failed on Go constructors such as `FormData`. They work now, and validation and rule failures come back as `ClientResponseError` ([JSVM](ext/jsvm/CHANGELOG.md), [Pages](ext/pages/CHANGELOG.md)).
- **`require()` works on every request.** A `require()` inside a handler or any function that runs at request time failed with `require("x"): not found` from the second request on. It now resolves and loads files on its own ([Pages](ext/pages/CHANGELOG.md)).

### Extensions

- [JSVM](ext/jsvm/CHANGELOG.md#011-2026-11-20) 0.1.0 → 0.1.1
- [Pages](ext/pages/CHANGELOG.md#012-2026-11-20) 0.1.1 → 0.1.2

## v0.0.1-alpha.9 (2026-11-19)

### Highlights

- **A fix for multisite hosts running Pocker.** On alpha.8, every Pocker instance that had API tokens failed to start: the API Tokens rename hit a bug in the sandbox's database driver, which also broke renaming any collection on a Pocker instance. Both work again. A single app (`pocketone serve`) never had the bug ([Sandbox](ext/sandbox/CHANGELOG.md)).

### Extensions

- [Sandbox](ext/sandbox/CHANGELOG.md#003-2026-11-19) 0.0.2 → 0.0.3

## v0.0.1-alpha.8 (2026-11-18)

### Highlights

- **A language model in every app.** `$agent.complete()` and `$agent.stream()` in `pb_hooks` answer from your own API or from a small model PocketOne runs itself, in the same binary: answers that match a JSON schema, threads, prompt caching, and stored conversations with [Agent Chat](ext/agent-chat/README.md). Pick the model in the dashboard under Settings → LLM ([Agent](ext/agent/README.md)).
- **WASM modules.** Put a `<name>.pb.wasm` built from Go, TinyGo, Rust, Zig or C in `pb_hooks` and call it from your hooks with `await $wasm.run.async(name, input)`; each call runs in its own box with memory and time limits. Thanks to @octavonu ([WASM](ext/wasm/README.md)).
- **Desktop apps.** `pocketone desktop build` turns the app folder you run with `pocketone serve` into a native desktop app for macOS, Windows and Linux, one executable each, from any machine ([Desktop](ext/desktop/README.md)).

### Upgrading

1. The tokens extension is now `api-tokens`: rename `tokens:` to `api-tokens:` in `pocketone.yaml`, use `$apiTokens` in JS hooks (was `$tokens`), and upgrade the `pocketone` CLI with your apps (the token API moved to `/api/_c/api-tokens/v1`). Its collections are renamed on the first start ([ext/api-tokens changelog](ext/api-tokens/CHANGELOG.md)).
2. Go import paths moved from `github.com/pockethost/pocketone/...` to `pocketone.dev/...` (the code is still on GitHub; pocketone.dev points Go at it). In your own Go code, replace `github.com/pockethost/pocketone/` with `pocketone.dev/` in imports and `go.mod` (for example `pocketone.dev/ext/multisite`), change `replace ganigeorgiev.com/pocketbase/pocketbase => github.com/pockethost/pocketone <version>` to `=> pocketone.dev <version>`, then run `go mod tidy`.

### Added

- `pocketone` runs WASM modules: put a `<name>.pb.wasm` in `pb_hooks` and call it from your hooks with `await $wasm.run.async(name, input)` ([ext/wasm](ext/wasm/README.md)).
- Contract streams can push: a stream operation (`extkit.Stream`) may be a POST with a JSON body, and its provider can send entries as they happen with `Run` instead of being polled every 500 ms; a `Run` error ends the stream with `event: error`. `pocketone remote <contract> <op>` and the generated clients follow ([Contracts](ext/contracts/README.md)).
- Extensions can own their schema: an extension that has `Migrations()` gets them applied at startup, after PocketBase's own migrations and before the app's `pb_migrations`, recorded in `_extension_migrations` apart from the app's history. `pocketone extensions migrations` lists them; `pocketone extensions migrate down <extension> [n]` reverts them ([Writing an extension](docs/extensions.md#your-schema-migrations-and-table-names)).
- `types.d.ts` types every contract `pb_hooks` can touch, generated from its Go interface: action hooks take their operation's input (`onAgentComplete((e) => e.input.maxTokens ...)`), and `e.provide("mail.fallback/v1", ...)` checks the object's methods and arguments; unknown ids stay untyped ([Contracts](ext/contracts/README.md#write-the-interface)).
- `pb_cache/`, next to `pb_data`, is where extensions keep files they can rebuild, one folder each (`pb_cache/<extension>/`). It's never backed up, deployed or synced; `pocketone cache` shows what's in it and `pocketone cache clear [extension...]` empties it ([Folders](https://pocketone.dev/docs/folders)). Add `pb_cache/` to your app's `.gitignore`.

### Changed

- Extension collections are named `_<extension>_<table>`, with hyphens in the extension's name as underscores (`_agent_chat_messages` for agent-chat); the conformance test no longer accepts a bare `_<extension>`, and CI refuses a table that falls under another extension's longer prefix ([Writing an extension](docs/extensions.md#your-schema-migrations-and-table-names)).
- Extensions keep their end-user docs in `ext/<name>/docs/` (was `site/`). `docs/index.md` is required and names the main docs nav section that links it (`section: Reference`); the README is the extension's page in the Extensions reference, at `/docs/extensions/<name>/extension`, and no longer appears in the docs' sidebar ([Writing an extension](docs/extensions.md)).
- PocketOne's Go modules are now `pocketone.dev/ext/<name>`, `pocketone.dev/examples/pocketone` and `pocketone.dev` (core, for the `replace`), so the import path no longer names where the repo lives.
- The repo moved to [github.com/pocketonehq/pocketone](https://github.com/pocketonehq/pocketone) (old `pockethost/pocketone` links redirect), and its PocketBase mirror to `github.com/pocketonehq/pocketbase`. In an existing clone: `git remote set-url origin https://github.com/pocketonehq/pocketone.git`.

### Extensions

- [Agent](ext/agent/CHANGELOG.md#002-2026-11-18) 0.0.2 (new)
- [Agent Chat](ext/agent-chat/CHANGELOG.md#002-2026-11-18) 0.0.2 (new)
- [API Tokens](ext/api-tokens/CHANGELOG.md#020-2026-11-18) 0.1.0 → 0.2.0 (has upgrade steps)
- [Cache](ext/cache/CHANGELOG.md#010-2026-11-18) 0.0.2 → 0.1.0 (has upgrade steps)
- [Deploy](ext/deploy/CHANGELOG.md#010-2026-11-18) 0.0.2 → 0.1.0 (has upgrade steps)
- [Desktop](ext/desktop/CHANGELOG.md#002-2026-11-18) 0.0.2 (new)
- [Docker](ext/docker/CHANGELOG.md#003-2026-11-18) 0.0.2 → 0.0.3
- [JSVM](ext/jsvm/CHANGELOG.md#010-2026-11-18) 0.0.2 → 0.1.0 (has upgrade steps)
- [Pages](ext/pages/CHANGELOG.md#011-2026-11-18) 0.1.0 → 0.1.1
- [Pocker](ext/pocker/CHANGELOG.md#011-2026-11-18) 0.1.0 → 0.1.1
- [S3 Fallback](ext/s3-fallback/CHANGELOG.md#002-2026-11-18) 0.0.1 → 0.0.2
- [S3 Redirect](ext/s3redirect/CHANGELOG.md#002-2026-11-18) 0.0.1 → 0.0.2
- [Sandbox](ext/sandbox/CHANGELOG.md#002-2026-11-18) 0.0.1 → 0.0.2
- [Standard Set](ext/standard/CHANGELOG.md#010-2026-11-18) 0.0.2 → 0.1.0 (has upgrade steps)
- [UI Cache](ext/uicache/CHANGELOG.md#002-2026-11-18) 0.0.2 (new)
- [Upgrade](ext/upgrade/CHANGELOG.md#002-2026-11-18) 0.0.1 → 0.0.2
- [WASM](ext/wasm/CHANGELOG.md#002-2026-11-18) 0.0.2 (new)

### PocketBase

- Built on PocketBase v0.42.3 plus one later upstream commit (c5003ce6; was v0.42.2). Realtime subscription changes no longer need to come from the connection's IP, OAuth2 redirects also accept the subscribing IP and log failures at WARN, and S3 multipart uploads that S3 reports as failed in a 200 response now fail. See its [changelog](CHANGELOG.pocketbase.md).

## v0.0.1-alpha.7 (2026-11-17)

### Upgrading

1. Put app and serve flags after the command name: `pocketone serve --hooksDir x`, not `pocketone --hooksDir x serve`. Only `--config` and `--env-file` (and core's `--dir`, `--dev`, `--encryptionEnv`, `--queryTimeout`, still accepted anywhere) can come before it.
2. In `pocketone.yaml`, move `public_dir`, `index_fallback`, `hooks_watch` and `automigrate` from `app:` to `server:` (see the config changelog).

### Added

- Agent Skills: a third skill, `pocketone-extensions`, for writing an extension and contributing it as a pull request; the `pocketone` skill now covers only using PocketOne in an app, and no longer carries its own copy of the PocketPages docs (the `pocketpages` skill has them). Update with `/plugin marketplace update pocketone` or `npx skills add pockethost/pocketone`.
- `app: js_limits:` in `pocketone.yaml` sets the JS engine's limits for the app: `memory` (default 64MiB), `task_time` (JS time per task, default 5s) and `task_wall` (wall time per task, default 30s).
- pocketone.dev has a [Remote administration](https://pocketone.dev/docs/remote) page and [CLI](https://pocketone.dev/docs/reference/cli), [REST](https://pocketone.dev/docs/reference/rest) and [MCP](https://pocketone.dev/docs/reference/mcp) references generated from the `pocketone` binary; every contract and extension page lists its commands, endpoints and MCP tools.
- pocketone.dev has a Tutorials section: [QuickJS](https://pocketone.dev/docs/quickjs) (limits, why there's no VM pool, a benchmark against goja) and [Configure with pocketone.yaml](https://pocketone.dev/docs/pocketone-yaml).
- pocketone.dev shows the current release in its header, linked to its release notes.

### Changed

- Each flag is now on the commands that read it instead of on every command: `--dir`, `--dev`, `--encryptionEnv`, `--queryTimeout`, `--jsvm`, `--hooksDir`, `--hooksPool` and `--migrationsDir` on the commands that open the app (`serve`, `migrate`, `superuser`, `pitr`, `token`, `oauth`, `template`); `--hooksWatch`, `--automigrate`, `--publicDir`, `--indexFallback`, `--pagesDir`, `--pitr*`, `--s3Redirect*` and `--autotls*` on `serve` only. Remote commands such as `deploy` and `link` list just their own flags plus `--config` and `--env-file`, and reject the others.

### Extensions

- [Cache](ext/cache/CHANGELOG.md#002-2026-11-17) 0.0.1 → 0.0.2
- [Config](ext/config/CHANGELOG.md#010-2026-11-17) 0.0.1 → 0.1.0 (has upgrade steps)
- [Deploy](ext/deploy/CHANGELOG.md#002-2026-11-17) 0.0.1 → 0.0.2
- [Docker](ext/docker/CHANGELOG.md#002-2026-11-17) 0.0.1 → 0.0.2
- [JSVM](ext/jsvm/CHANGELOG.md#002-2026-11-17) 0.0.1 → 0.0.2
- [Multisite](ext/multisite/CHANGELOG.md#002-2026-11-17) 0.0.1 → 0.0.2
- [Pages](ext/pages/CHANGELOG.md#010-2026-11-17) 0.0.1 → 0.1.0 (has upgrade steps)
- [Pocker](ext/pocker/CHANGELOG.md#010-2026-11-17) 0.0.1 → 0.1.0 (has upgrade steps)
- [Standard Set](ext/standard/CHANGELOG.md#002-2026-11-17) 0.0.1 → 0.0.2
- [Templates](ext/templates/CHANGELOG.md#002-2026-11-17) 0.0.1 → 0.0.2
- [Tokens](ext/tokens/CHANGELOG.md#010-2026-11-17) 0.0.1 → 0.1.0 (has upgrade steps)
- [Vacuum](ext/vacuum/CHANGELOG.md#002-2026-11-17) 0.0.1 → 0.0.2

## v0.0.1-alpha.6 (2026-11-16)

### Upgrading

- Environment variables: every PocketOne variable now starts with `P1_`, in every build (`PB_` stays PocketBase's own). Rename `POCKETONE_TOKEN` → `P1_TOKEN`, `POCKETONE_TOKEN_<REMOTE>` → `P1_TOKEN_<REMOTE>`, `POCKETONE_CONFIG_DIR` → `P1_CONFIG_DIR` and `PB_MAILTRAP_HTTP`/`PB_MAILTRAP_SMTP` → `P1_MAILTRAP_HTTP`/`P1_MAILTRAP_SMTP` in CI and scripts. The `pockethost` build reads `P1_TOKEN` too, no longer `POCKETHOST_TOKEN`.

### Added

### Changed

- A request with an unknown or expired `Authorization` token now gets a 401 instead of going on as a guest (PocketBase v0.42.0). Clients that send a stale token should drop it and sign in again. `/mcp` keeps answering with its own sign-in challenge, so MCP clients still re-authenticate.
- **contracts:** contract providers and `on<Contract><Op>` handlers written in pb_hooks can use the file's top-level variables.

### Fixed

- A crash or data race when an app stops its cron scheduler (on shutdown or restart), a PocketBase v0.42 regression; fixed in core until upstream has it.

### PocketBase

- Built on PocketBase v0.42.2 (was v0.41.1). See its [changelog](CHANGELOG.pocketbase.md) for v0.42.0 to v0.42.2.

## v0.0.1-alpha.4 (2026-11-16)

### Upgrading

### Changed

## v0.0.1-alpha.3 (2026-11-16)

### Upgrading

### Added

- `pocketone --version` prints the PocketBase version it's built on: `0.0.2 (PocketBase 0.41.1)`.

### Changed

- The startup lines listing the standard extensions and contracts print when the app serves, not before every command (`--version`, `help`, `migrate`).

### Fixed

## v0.0.1-alpha.2 (2026-11-16)

Built on PocketBase v0.41.1.

### Upgrading

- If you installed the old `v0.0.1`, reinstall (`brew reinstall pockethost/tap/pocketone`, or download the zip): `v0.0.1` became `v0.0.1-alpha.1`, and later alphas won't replace it on their own.

### Added

- **Extension contracts:** every extension API is a versioned contract, so extensions depend on what they need, not on each other by name.
- **Config:** `pocketone config --json` lists every section, key, type and default. The `pitr`, `autotls` and `s3redirect` sections set a single app's defaults; flags still win.
- **Dashboard:** the PocketOne logo, favicon and version in the superuser dashboard.
- Every extension's docs are on [pocketone.dev](https://pocketone.dev), now with search.

### Changed

- **`pocketone upgrade`** keeps every old executable as `pocketone-<version>` and follows prereleases: from one alpha to the next it just upgrades; from a stable release to a prerelease it asks first.
- **Config file is `pocketone.yaml`.**
- **Hooks:** in dev mode a broken `pb_hooks` file is skipped instead of stopping the app; a bad reload keeps the last good code.

### Fixed

- The hooks watcher's goroutine is stopped before bootstrap is cleared (a race in PocketBase's notify watcher).

## v0.0.1-alpha.1 (2026-11-16)

The first alpha, first published as `v0.0.1`. Built on PocketBase v0.41.1.

### Added

- Runs your existing `pb_data`, `pb_hooks` and `pb_migrations`.
- **pages** (formerly PocketPages): server-rendered pages from `pb_pages`; start with `pocketone pages create myapp`.
- **mcp** at `/mcp`, **Bulk Mail**, **Auto TLS**, **API tokens**, an **OAuth server**, **S3 redirects**, and `pocketone deploy | dev | pull | logs`.
- A dev-mode mail trap (`serve --dev`) at http://127.0.0.1:8025.
