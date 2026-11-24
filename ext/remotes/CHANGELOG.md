# Remotes changelog

Changes to `ext/remotes`, newest first. A user-visible change adds its line under `## Unreleased` in the same
commit. Each PocketOne release raises this extension's `version:` when there's something under `## Unreleased` and
renames the section to that version ([Changelogs](../../docs/extensions.md#changelogs)).
Sections named after a PocketOne release are from before extensions had versions of their own.

## Unreleased

### Upgrading

- Per-remote token variables (`P1_TOKEN_<NAME>`, such as `P1_TOKEN_STAGING`) are no longer read: `P1_TOKEN` is the only variable, and it overrides the saved login for whatever remote the command uses. Rename `P1_TOKEN_<NAME>` to `P1_TOKEN`, set per job or per command (`P1_TOKEN=... pocketone deploy staging`). `remoteclient.EnvToken` takes no remote name.

## 0.0.2 (2026-11-21)

### Added

- `link` and `login` sign in with a superuser's email and password when the remote has no OAuth server (stock PocketBase, such as an app a multisite host runs in Docker): the password isn't echoed, and the session is swapped for a 30-day token (PocketBase's impersonate). A pasted token still works at the same prompt.

### Fixed

- `--token-stdin` at a terminal says "Paste the token:" instead of waiting silently.

## PocketOne v0.0.1-alpha.6 (2026-11-16)

### Upgrading

- `login` and `link` no longer take a password: `--email`, `--password`, `--collection` and `POCKETONE_PASSWORD` are gone. Sign in through the browser (`--device` on a machine without one), or pass an API token with `--token`, `--token-stdin` or `P1_TOKEN`. A saved login made with a password keeps working; `logout` no longer revokes its token, so delete it in the dashboard.
