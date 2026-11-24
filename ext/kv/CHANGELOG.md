# Key-Value Store changelog

Changes to `ext/kv`, newest first. A user-visible change adds its line under `## Unreleased` in the same
commit. Each PocketOne release raises this extension's `version:` when there's something under `## Unreleased` and
renames the section to that version ([Changelogs](../../docs/extensions.md#changelogs)).

## Unreleased

## 0.0.1 (2026-11-24)

### Added

- New: the app's key-value store. Named JSON values kept without a collection or a migration (rows `kv:<name>` in
  PocketBase's `_params`, so they ride along in backups): `$kv.get/set/delete/list` in page code and pb_hooks, the
  [kv/v1](../contracts/kv/v1/README.md) helpers (`kvv1.Get`, `kvv1.Set`, ...) for Go, and superuser operations at
  `/api/_c/kv/v1` (also `pocketone remote kv ...` and MCP). On by default. Names starting with `_` are private to Go
  code (the API and `$kv` refuse them); LLM, Bulk Mail and API Tokens keep their settings and keys there.

