# Config changelog

Changes to `ext/config`, newest first. A user-visible change adds its line under `## Unreleased` in the same
commit. Each PocketOne release raises this extension's `version:` when there's something under `## Unreleased` and
renames the section to that version ([Changelogs](../../docs/extensions.md#changelogs)).

## Unreleased

## 0.1.1 (2026-11-24)

### Added

- `await_time` in `app: js_limits:`: how long an async JS handler may take in all, awaits included (default 15m). `task_time` and `task_wall` count each stretch of JS between awaits.

## 0.1.0 (2026-11-17)

### Upgrading

1. Move `public_dir`, `index_fallback`, `hooks_watch` and `automigrate` from the `app` section to the `server` section of `pocketone.yaml`. An old key under `app` now stops startup as unknown.

### Changed

- `app` holds what every command that opens the app reads (`dir`, `dev`, `encryption_env`, `query_timeout`, `jsvm`, `hooks_dir`, `hooks_pool`, `migrations_dir`, `env_allow`); `server` holds what `serve` reads, now including `public_dir`, `index_fallback`, `hooks_watch` and `automigrate`.

### Added

- `server.pages_dir`: the PocketPages folder, the `--pagesDir` default.

