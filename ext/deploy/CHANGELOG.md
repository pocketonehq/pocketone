# Deploy changelog

Changes to `ext/deploy`, newest first. A user-visible change adds its line under `## Unreleased` in the same
commit. Each PocketOne release raises this extension's `version:` when there's something under `## Unreleased` and
renames the section to that version ([Changelogs](../../docs/extensions.md#changelogs)).
Sections named after a PocketOne release are from before extensions had versions of their own.

## Unreleased

### Fixed

- A deploy whose runtime can't say whether the instance is running fails with that error, instead of applying the files without the restart it asked for.

## 0.2.0 (2026-11-21)

### Upgrading

1. The deploy API's file routes and `pocketone pull` no longer reach `pb_data`, powered off or not. Move data with the PocketBase API, backups (restore/v1), or by copying the folder on the server.
2. Deploys are no longer recorded. The `_deploy_deploys` collection and its rows are deleted on the first start, by the extension's migration; export them first if you want to keep them.
3. `pocketone deploy` no longer takes `--commit` or `-m`/`--message` (they only fed the deploy record): remove them from scripts and CI.

### Added

- `pocketone clone <url> [dir]` starts a local project from a running app: makes the folder, adds the app as the default remote, signs in like `link`, and pulls its code. Then `pocketone dev`.
- `pocketone pull` with no paths also downloads the lockfile (`bun.lock`, `bun.lockb`) and `patches/`.
- `pocketone deploy` stops when files changed on the remote since your last deploy, pull or dev (an edit made on the server, a migration the dashboard wrote), lists them, and deploys nothing; `--force` overwrites them. Deploy and pull now keep the same record `dev` does (`.pocketone/sync.json`). A project with no record of the remote yet (a fresh checkout, CI) deploys as before.

### Changed

- `pull`, `clone` and `dev` download in bundles: the tree once (`GET /tree?deep=1`, every folder in one answer) and the files that differ in one `POST /files/bundle` per 32 MB, instead of one request per folder and per file, so a pull no longer trips a proxy's rate limit (Cloudflare's on PocketHost). Files already the same aren't downloaded, and `pull` saves the deploy base from the same tree. Older servers are pulled file by file as before.
- The deploy API gets the instance's folder, writers' lock and restart from the new instance.fs/v1 contract, and provides it on a single app (the app's own folder). Its code-path rules moved there too.
- File routes (`/files`, `/files/raw`) and `pull` never reach `pb_data`.
- The upload store moves from `.pb_deploy/` to `pb_cache/deploy/` (PocketOne's folder for disposable files). Deploys still upload only what changed (files already in the app's folder count); the old `.pb_deploy/` folder can be deleted.

### Fixed

- A deploy no longer deletes the app's `pb_cache` folder (extensions' caches, such as ext/cache's disk tier), and the file routes no longer reach it: `pb_cache` was counted as code on the server.

### Removed

- Deploy history: the `_deploy_deploys` collection, the commit response's `deployId`, and `Backend.RecordDeploy` (a host's backend now only finds the instance and authenticates).

## 0.1.0 (2026-11-18)

### Upgrading

1. Deploy history is renamed from `_deploys` to `_deploy_deploys` (its id, `pbc_deploys`, stays) on the first start, by the extension's migration. Code that reads it by name switches to `_deploy_deploys`, or to the id.

### Changed

- Deploy history is the `_deploy_deploys` collection, following the `_<extension>_<table>` rule; an extension migration renames it.
- `deploy`, `dev` and the deploy set never include `pb_cache` (extensions' disposable files), like `pb_data`.

## 0.0.2 (2026-11-17)

### Changed

- `pocketone pull` also fetches `pb_templates` by default (ejected page templates, ext/templates). `deploy` and `dev` already sync it with the other `pb_*` folders.

## PocketOne v0.0.1-alpha.2 (2026-11-16)

### Changed

- Two-way `pocketone dev` sync, and a deploy never restarts the app on its own (`deploy --restart`, `POST /restart`, or `r` in dev).
