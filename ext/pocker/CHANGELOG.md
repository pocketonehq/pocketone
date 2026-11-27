# Pocker changelog

Changes to `ext/pocker`, newest first. A user-visible change adds its line under `## Unreleased` in the same
commit. Each PocketOne release raises this extension's `version:` when there's something under `## Unreleased` and
renames the section to that version ([Changelogs](../../docs/extensions.md#changelogs)).
Sections named after a PocketOne release are from before extensions had versions of their own.

## Unreleased

## 0.1.4 (2026-11-27)

### Added

- The runtime reports every app that stops, restarts or fails to start (runtime/v1's exit signal) and lists its running instances, so the host's instance status is right.

## 0.1.3 (2026-11-24)

### Added

- `Provider.Env` / `Server.Env`: a host adds values to every instance's environment (`$os.getenv`, `process.env`), over the instance's own secrets, so a secret can't override them. PocketHost uses it for `PH_POCKETBASE_VERSION`.

### Fixed

- A dev-mode instance no longer logs `pb_hooks/<file> failed to load; skipped it (dev mode): <nil>` for every hook file that loaded fine; only a file that really failed is logged.

## 0.1.2 (2026-11-21)

### Added

- The Pocker host provides [instance.fs/v1](../contracts/instance/fs/v1/README.md) on its scope (`Manager.Files()`): each instance's code folder, a writers' lock and a restart that stops its app and starts it again. Host-side code (a host's deploy or file drive) gets an instance's files from it; code inside an instance keeps its own.
- The Pocker host provides [instance.tokens/v1](../contracts/instance/tokens/v1/README.md) on its scope (`Manager.Tokens()`): host-side code checks an API token an instance issued with that instance's own api-tokens/v1, so a host can accept the instance's tokens (`pocketone login` and `clone` sign in to the instance) on the routes it answers for it.

### Changed

- The Manager's own handler (`Manager.ServeHTTP`) uses the host's instance.lookup/v1 when there is one.
- Each instance's `pb_cache` is outside its folder, in the host's `pb_cache/instances/<instance id>/`: the instance's extensions (`extkit.CacheDir`) and host-side caches about it (instance.fs/v1's `CacheDir`, deploy's upload store) go there. Copies, archives and moves of an instance folder no longer carry it; an old `pb_cache` left in an instance folder can be deleted.
- Serves every instance; instances no longer name a runtime, and a host runs pocker or docker, never both.

### Fixed

- Standard extensions keep their per-instance state apart: every instance's app has its instance id (`extkit.AppID`) before the standard set registers. It used to be set only for the host's own extensions, so every instance shared one cache bucket of each name (one budget, and one instance's clear emptied everyone's).

## 0.1.1 (2026-11-18)

### Fixed

- Instances attach to the multisite host's own contract scope when `Host` isn't set, so contracts the host provides for its instances (`ProvideForInstances`, such as agent/v1) reach them and the app's own provider steps aside.

### Added

- `pocker: isolate_config:` is every instance's config in pocketone.yaml's shape, one section per extension: `isolate_config: {tokens: {enabled: false}}` turns tokens off on every instance, and an instance's own `extensions.<name>` overrides it. A section for an extension the instances don't run stops startup.

### Changed

- Instances attach to the multisite host's own scope when `Server.Host` is unset, so contracts a host extension provides for its instances reach them.
- Caches shared by every instance (ext/cache's disk tier) go in the host's own `pb_cache`, in the data root next to the instances' folders.

## 0.1.0 (2026-11-17)

### Upgrading

- `pocker.NewManager(scope, opts)` takes the host's contract scope instead of an instance source: the Manager runs the instances instance.source/v1 supplies there. Provide your source with `sourcev1.Contract.ProvideIn(scope, src)` (in tests, `sourcetest.Scope(instances...)` from `ext/contracts/instance/source/v1/sourcetest`). `pocker.Server` and `pocker.Provider` are unchanged.

### Added

- Automigrate on every instance, as on a PocketHost docker instance and a single `pocketone` app: creating, changing or deleting a collection over the API (or the dashboard) writes its JS migration into the instance's `pb_migrations`. Writing it doesn't restart the instance.

### Changed

- A settings change to a running instance (`secrets`, `limits`, `dev`, ...) restarts it right away, as a `pb_hooks` change does: the app stops once the requests it is serving finish (up to 10s), then a fresh one starts; requests that arrive meanwhile get 503 with `Retry-After`. It used to start a new app on the next request while the old one ran on for 10 seconds beside it. An instance that isn't running is left alone.
- A `pb_hooks` change restarts the instance like PocketBase's watcher restarts a process: the running app stops, then a fresh one starts, so two apps never have the instance's folder open (migrations and bootstrap code no longer run beside a live app). In production a broken hook file now leaves the instance down (502 naming the file) until it's fixed, instead of the previous code serving on; dev mode still skips the broken file.

## PocketOne v0.0.1-alpha.6 (2026-11-16)

### Changed

- Each instance's JS has a hard memory cap (64 MiB: JS heap plus Go objects it holds); gradual growth is now limited per instance, not only one-shot allocations. Process-reaching Go methods are also hidden when an app type embeds `*core.BaseApp`.

## PocketOne v0.0.1-alpha.4 (2026-11-16)

### Upgrading

- If you build your own host on Pocker, set `Standard: standard.New()` on `pocker.Server`, `pocker.Provider` or `pocker.ManagerOptions`. Pocker now refuses to start instances without the standard set, so every instance has tokens, OAuth sign-in, MCP, pages and the rest. Drop standard members (pages, pitr, settings, ...) from `Extensions`; configure them on the set instead. The stock `pocketone` binary already does this.

### Changed

- Every instance gets the standard extensions; there's no way to start one without them.
