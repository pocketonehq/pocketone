# Multisite changelog

Changes to `ext/multisite`, newest first. A user-visible change adds its line under `## Unreleased` in the same
commit. Each PocketOne release raises this extension's `version:` when there's something under `## Unreleased` and
renames the section to that version ([Changelogs](../../docs/extensions.md#changelogs)).

## Unreleased

### Added

- A source that doesn't hold every instance can answer hostnames itself: with an instance.lookup/v1 provider on the host, every request's hostname is looked up there (404 not found, 503 when the answer isn't known). `multisite.Provides` is a host extension that only provides contracts.

### Changed

- `DecodeExtensionConfig` refuses keys the target type has no field for, so an instance's extension settings under the wrong names are an error instead of decoding to nothing.

## 0.1.0 (2026-11-21)

### Upgrading

1. Go: `Host.Runtimes` and `Handler.Runtimes` are now `Runtime` (one runtime/v1 provider); set `Runtime: rt` instead of `Runtimes: []multisite.Runtime{rt}`.

### Changed

- A host runs one runtime: runtime/v1 has one provider (two registered conflict at startup unless `providers: { runtime/v1: <name> }` picks), and an instance the runtime can't serve gets 503. Instances no longer name a runtime.

## 0.0.2 (2026-11-17)

### Changed

- A multisite host refuses `server.public_dir`, `index_fallback`, `pages_dir`, `hooks_watch` and `automigrate`, the single-app settings now in that section.

