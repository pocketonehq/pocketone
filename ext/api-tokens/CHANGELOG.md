# API Tokens changelog

Changes to `ext/api-tokens` (named `ext/tokens` until 2026-11-18), newest first. A user-visible change adds its line under `## Unreleased` in the same
commit. Each PocketOne release raises this extension's `version:` when there's something under `## Unreleased` and
renames the section to that version ([Changelogs](../../docs/extensions.md#changelogs)).
Sections named after a PocketOne release are from before extensions had versions of their own.

## Unreleased

### Upgrading

1. Go: `Tokens.Collections()` returns `([]string, error)`; handle the error.

### Changed

- `Collections()` and `GET /api/_c/api-tokens/v1/scopes` report a failure to read the auth collections instead of
  answering with an empty list.

- The OAuth server's signing key is kept in the app's key-value store (kv/v1) under the private name
  `_api-tokens/oauth-server`; the old `_params` row `oauth_server` moves there by itself on first use, so signed-in
  clients stay signed in.

## 0.2.1 (2026-11-21)

- OAuth Server: an `account` parameter on authorize and device requests names the account type the client needs
  (`account=_superusers`). The sign-in page shows only that form, a browser signed in as another type gets that form
  instead of consent (saying who's signed in), and no other type is issued a token.
- Tokens from another system keep working: `RegisterPrefix` lets secrets start with another prefix (e.g. `ph_pat_`)
  besides `pbt_`, and `Import` stores a secret with a registered prefix (its hash) with an owner, scopes and expiry.
  New tokens still start with `pbt_`.
- New contract api-tokens.verify/v1: another server checks one of the app's tokens
  (`POST /api/_c/api-tokens.verify/v1`, superusers, scope `api_tokens_verify`) and gets its owner and scopes, or why
  it isn't valid. A host in front of an app's accounts uses it instead of a route of its own.
- Scopes can take values: a token can hold a scope for some values only, written `name=value` (`notes=abc123`), for
  example to limit it to some records. Register the scope with `values` (JS) or `Values` (Go, with an optional
  `Check` when a token is created); routes check with `$apiTokens.hasScope(e, name, value)` / `HasScopeFor`. The
  dashboard's token form, `pocketone token create --scope name=value` and OAuth scope requests accept them.

## 0.2.0 (2026-11-18)

### Upgrading

The extension is renamed from `tokens` to `api-tokens`, so "tokens" no longer reads as model tokens next to ext/agent. Upgrade the `pocketone` CLI and your apps together: an older CLI's `login` calls the old API path.

1. In `pocketone.yaml`, rename the `tokens:` section to `api-tokens:` (and `extensions: {tokens: ...}` to `extensions: {api-tokens: ...}` on a multisite host, `isolate_config: {tokens: ...}` likewise).
2. In JS hooks, `$tokens` is now `$apiTokens`.
3. The token API moved from `/api/_c/tokens/v1` to `/api/_c/api-tokens/v1` (contract `api-tokens/v1`, was `tokens/v1`).
4. The collections are renamed on the first start by the extension's migrations (ids unchanged): `_tokens` → `_api_tokens_tokens`, `_tokens_oauth_clients` → `_api_tokens_oauth_clients`, `_tokens_oauth_codes` → `_api_tokens_oauth_codes`. Code that reads them by name switches to the new names, or to the ids.
5. Go: the module is `github.com/pockethost/pocketone/ext/api-tokens` (package `apitokens`), the contract package `ext/contracts/api-tokens/v1` (`apitokensv1`), the test helpers `apitokens/apitokenstest`, and the standard set's field `set.APITokens` (was `set.Tokens`).

### Changed

- Named `api-tokens` (was `tokens`); collections follow the `_<extension>_<table>` rule.

## 0.1.0 (2026-11-17)

### Upgrading

- `pocketone oauth eject` is gone: run `pocketone template eject oauth`. The OAuth pages are now Go templates in `pb_templates/oauth/` (`signin.tpl`, `head.tpl`, ...); copies in `pb_pages/_private/oauth/` are no longer read, so carry your edits over to the new files.
- Go API: `tokens.Eject`, `tokens.ExistsError`, `tokens.EjectSubdir` and `OAuthServer.PagesDir()` are gone; the pages are the `tokens.TemplateSet` ("oauth") template set of templates/v1.

### Changed

- The OAuth pages are one set of Go templates, embedded in the binary and rendered through templates/v1 (ext/templates), so the built-in pages and the ones you eject are the same files.

## PocketOne v0.0.1-alpha.6 (2026-11-16)

### Upgrading

- The CLI's built-in OAuth client is now `pocketone-cli` (was `pocketbase-cli`). Browser logins made before this release can't renew once the remote is upgraded: run `pocketone login <remote>` again when one expires.

### Changed

- The CLI's OAuth client id, the CLI's User-Agent and the default deploy source are now `pocketone-cli`, matching the "pocketone CLI" name on the sign-in pages.

## PocketOne v0.0.1-alpha.2 (2026-11-16)

### Added

- Custom scopes for apps and extensions (Go guards, `$tokens` in JS, scope headers for collection rules), and any auth record can own tokens. OAuth sign-in goes through PocketBase's own auth endpoints.
