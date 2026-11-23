# Firewall changelog

Changes to `ext/firewall`, newest first. A user-visible change adds its line under `## Unreleased` in the same
commit. Each PocketOne release raises this extension's `version:` when there's something under `## Unreleased` and
renames the section to that version ([Changelogs](../../docs/extensions.md#changelogs)).

## Unreleased

### Fixed

- An instance's own `extensions.firewall` settings (rate limits, trusted IPs) now apply when its host gets instances from a remote source such as a mothership: they crossed the wire under Go field names and were dropped, so every instance got the default limits. Settings under unknown names are now logged once per instance instead of silently ignored.
