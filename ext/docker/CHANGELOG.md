# Docker changelog

Changes to `ext/docker`, newest first. A user-visible change adds its line under `## Unreleased` in the same
commit. Each PocketOne release raises this extension's `version:` when there's something under `## Unreleased` and
renames the section to that version ([Changelogs](../../docs/extensions.md#changelogs)).
Sections named after a PocketOne release are from before extensions had versions of their own.

## Unreleased

### Added

- The runtime reports every container that stops, ends on its own or fails to start (runtime/v1's exit signal), lists its running instances and tells the host how to health check each one, so the host's instance status is right.

## 0.0.4 (2026-11-21)

### Added

- The Docker host provides [instance.fs/v1](../contracts/instance/fs/v1/README.md) on its scope (`Runtime.Files(dir)`): each instance's folder on the host, a writers' lock and a restart that stops its container (the next request starts a fresh one).

### Changed

- The host keeps its own `pb_cache` next to the instances' folders, and each instance's host-side cache (instance.fs/v1's `CacheDir`, deploy's upload store) in its `instances/<instance id>/`, outside the folder the container mounts. `Runtime.Files` takes the host scope.
- Serves every instance by default (`Config.Serves` can still narrow it); instances no longer name a runtime, and a host runs docker or pocker, never both.

## 0.0.3 (2026-11-18)

### Fixed

- A `GET`, `HEAD` or `OPTIONS` request that arrives while a `pb_hooks` change restarts the instance waits for it instead of sometimes failing with 502 "instance unavailable". The change restarts it twice in a row (PocketBase in place, then a new container), and the request was sent again only once; it now goes again until the instance answers, for up to `StartTimeout`. A connection the instance (or Docker's port proxy) resets, or a kept-alive connection it closes as the request goes out, also counts as dropped.

## 0.0.2 (2026-11-17)

### Fixed

- A container that exits right after it starts no longer loses what it printed: the runtime attaches to its output before starting it, so the reason it exited reaches `OnLog` (and `remote/v1`'s logs) even though Docker removes it at once.

## PocketOne v0.0.1-alpha.3 (2026-11-16)

### Added

- A host with its own accounts decides who may call `remote/v1` (status, restart, logs) for its containers with `docker.Provider.RemoteAuth`; without it, the instance's own superuser login is checked as before. `remote/v1` now lives on the host's contract scope, so `pocketone remote whoami` also lists the host's other contracts for the instance (a host's `deploy/v1`).
