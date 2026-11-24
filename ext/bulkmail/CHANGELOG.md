# Bulk Mail changelog

Changes to `ext/bulkmail`, newest first. A user-visible change adds its line under `## Unreleased` in the same
commit. Each PocketOne release raises this extension's `version:` when there's something under `## Unreleased` and
renames the section to that version ([Changelogs](../../docs/extensions.md#changelogs)).

## Unreleased

## 0.0.1 (2026-11-24)

### Changed

- The settings are kept in the app's key-value store (kv/v1) under the private name `_bulkmail/settings`; the old
  `_params` row `bulkmail` moves there by itself the first time they're read, so unsubscribe links keep working.

### Fixed

- An address whose suppression check fails (a database error) is skipped and the error logged, instead of being
  mailed as if it weren't suppressed.

