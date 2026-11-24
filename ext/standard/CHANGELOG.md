# Standard Set changelog

Changes to `ext/standard`, newest first. A user-visible change adds its line under `## Unreleased` in the same
commit. Each PocketOne release raises this extension's `version:` when there's something under `## Unreleased` and
renames the section to that version ([Changelogs](../../docs/extensions.md#changelogs)).

## Unreleased

## 0.1.3 (2026-11-24)

### Added

- The [Key-Value Store](../kv/README.md) (`kv`) joins the set, on by default: `$kv` in page code and the kv/v1
  API. `kv: {enabled: false}` turns it off.
- [LLM Search](../llm-search/README.md) (`llm-search`) joins the set, on by default: search buckets, `$llm.search` in
  page code and the llm.search/v1 API. `llm-search: {enabled: false}` turns it off.

## 0.1.2 (2026-11-21)

- The superuser dashboard shows PocketOne's logo, favicon and version, and its Docs link opens pocketone.dev/docs, on
  every app the set runs: instances on a multisite host and other builds on the set now get it, not only the `pocketone`
  binary.

## 0.1.1 (2026-11-21)

- `Set.Switches`: the settings layer `Register` reads members' `enabled` from (default `ConfiguredSwitches`,
  pocketone.yaml), so a binary can keep a member off in code when its own code does the same job.

## 0.1.0 (2026-11-18)

### Upgrading

1. The member `Tokens` is now `APITokens` (ext/api-tokens, was ext/tokens): `set.APITokens = apitokens.New(apitokens.Config{...})`. Its pocketone.yaml section is `api-tokens:`.

### Added

- New member `AgentChat` (ext/agent-chat): stored conversations with the app's language model, `$agent.newChat()` and `$agent.chat(id).send(text)`. Its two collections (`_agent_chat_chats`, `_agent_chat_messages`) are created by its migration and stay empty until the app starts a chat. Turn it off on a single app with `agent-chat: {enabled: false}`.
- New member `Agent` (ext/agent): a language model for `pb_hooks`, called with `$agent.complete()`, from the app's own API or a model PocketOne runs itself; on a host, the host's shared model. Turn it off on a single app with `agent: {enabled: false}`.

- `pocketone extensions migrations`, `pocketone extensions migrate up [extension]` and `pocketone extensions migrate down <extension> [n]`: list, apply and revert the migrations of the extensions running on the app.
- `pocketone cache` shows each extension's folder in `pb_cache` and its size; `pocketone cache clear [extension...]` empties them.
- `standard.AppFromConfig(cfg)`: pocketone.yaml's `app` section (dir, dev, encryption_env, query_timeout, jsvm, hooks_dir, migrations_dir, hooks_pool, env_allow, js_limits) as the `pocketbase.Config` and `jsvm.Config` an app is built from, so a custom Go build gets every `app` key without copying the mapping. The `pocketone` binary uses it.

- New member `S3Fallback` (ext/s3-fallback, config section `storage`): a default bucket for apps without S3 of their own, so every PocketOne app and every isolate on a multisite host has it. It does nothing until the `storage` section mounts a backend. Reconfigure it with `set.S3Fallback = s3fallback.New(s3fallback.Config{...})`; a host that added it with `set.Add(s3fallback.New(...))` must drop that line (two extensions named `storage` stop startup).
- New member `UICache` (ext/uicache): dashboard files that keep their name (`/_/extensions.js`, `libs/`, fonts, images, extension files) are sent with `no-cache` or a five-minute lifetime instead of PocketBase's 14 days, so CDNs pick up updated extension screens. Turn it off on a single app with `uicache: {enabled: false}`.
- New member `WASM` (ext/wasm): WASI modules in `pb_hooks`, called with `$wasm`. Reconfigure it with `set.WASM = wasm.New(wasm.Config{...})`. Turn it off on a single app with `wasm: {enabled: false}`.

### Fixed

- A single app (the stock `pocketone` binary) now honors `enabled: false` in pocketone.yaml for every standard member: `settings`, `templates`, `tokens`, `remotes`, `mcp`, `pages`, `backups`, `bulkmail` and `deploy` each get a section with just `enabled` (before, those sections were refused as unknown and the switch was never read). Extensions that require the one you turn off go off too, and the startup lines say why.

## 0.0.2 (2026-11-17)

### Added

- New member `Cache` (ext/cache): cache buckets for extensions and apps (cache/v1), in memory with an optional disk tier. Turn it off per app with `cache: {enabled: false}`.
- New member `Templates` (ext/templates): extensions' page templates, ejected to `pb_templates` to edit (`pocketone template eject oauth` for the sign-in screens). Turn it off per app with `templates: {enabled: false}`.

