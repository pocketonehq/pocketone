---
owners: ["@benallfree"]
version: 0.1.0
title: "Remotes"
group: binary
summary: "Named remotes and saved logins for the pocketone command."
---

# Remotes

> Walkthrough for app owners: [Remote CLI](https://pocketone.dev/docs/features/remote). This page is the technical reference.

Named remotes for the `pocketone` command: the apps a project deploys to, each signed in to once. Give an app a name
like `prod`, log in, and commands such as `pocketone deploy prod` know where to go and who you are.

```bash
pocketone remote add prod https://myapp.example.com
pocketone login prod
pocketone deploy prod          # Deploy
```

## Install

Remotes is built into `pocketone` and **on**: the `remote`, `login`, `logout` and `link` commands are always there, and
every app provides [remote/v1](../contracts/README.md): whoami at `GET /api/_c/remote/v1` (so other machines can sign
in to it), status, restart and logs. There's nothing to install: it's in the [standard set](../standard/README.md).

In your own Go build, use the standard set, or register it through its contracts yourself:

```go
app := pocketbase.New()
extkit.Setup(app, []extkit.Extension{remotes.New(remotes.Config{})}, nil, nil) // remote/v1; on *pocketbase.PocketBase also the commands
app.Start()
```

The module is `pocketone.dev/ext/remotes`. Add [Tokens](../api-tokens/README.md) for API tokens and
[the OAuth server](../api-tokens/OAUTH.md) for browser login.

## Configure

There is no `pocketone.yaml` section, flag or settings screen. Remotes keeps two files and reads a few environment
variables.

### The project's remotes

`.pocketbase/remotes.json` in the project holds names and URLs only, so commit it:

```json
{"default": "prod", "remotes": {"prod": {"url": "https://myapp.example.com"}}}
```

Commands find it by walking up from the current folder. It isn't a `pb_*` name, since `pb_*` folders are deployed.

### Saved logins

Logins are saved per user, keyed by URL, in `~/.config/pocketone/credentials.json` (mode 0600). Set
`P1_CONFIG_DIR` to use another folder; with `XDG_CONFIG_HOME` set, it's `$XDG_CONFIG_HOME/pocketone/`.

### Environment variables

| Variable | What it does |
|---|---|
| `P1_TOKEN` | A token that overrides the saved login, for CI or for one command (`P1_TOKEN=... pocketone deploy staging`). |
| `P1_CONFIG_DIR` | Where saved logins are kept. |

A token in the environment wins over a saved login. Every PocketOne variable starts with `P1_`, in every build (the
`pockethost` binary reads `P1_TOKEN` too). The `pocketone` folder comes from the command's name, so a build shipped
under another name keeps its logins apart (`~/.config/pockethost`).

## Use

### Manage remotes

```bash
pocketone remote add prod https://myapp.example.com --default
pocketone remote list            # names, URLs and who you're logged in as; * marks the default
pocketone remote default prod
pocketone remote rm staging      # the saved login stays; see logout
pocketone remote whoami prod     # who you are there, your token's scopes, and what the app offers
```

A URL without a scheme gets `https://`. The first remote you add becomes the default. A command that names no remote
uses the default, else the only remote, else the one called `default`. Any command also takes a URL in place of a
name: `pocketone deploy https://other.example.com`.

### Log in

```bash
pocketone login prod
```

`link` signs in the same way when it adds a remote. Either way you sign in as one of the app's superusers (or its
users, on the browser pages). `login` tries, in order:

1. **The browser**, when the remote runs [the OAuth server](../api-tokens/OAUTH.md). Your browser opens, you sign in
   and approve, and the command finishes. Over SSH or without a display it shows a code to enter on another device
   instead; `--device` asks for that, and `--no-browser` prints the URL without opening it.
2. **A superuser's email and password**, when the remote has no OAuth server (stock PocketBase, such as an app a
   multisite host runs in Docker). The password isn't shown as you type. The login lasts 30 days; changing that
   superuser's password ends it. A superuser with multi-factor authentication on signs in with a token instead.
3. **A pasted token** (`pbt_...`, a PocketHost token, or a PocketBase auth token): paste it at the email prompt, or
   pass `--token <token>` or `--token-stdin`.

A browser login keeps a refresh token and renews itself when it's within a day of expiring.

You'll see `Logged in to https://myapp.example.com as admin@example.com (oauth).`

### Link in one step

```bash
pocketone link https://myapp.example.com
pocketone link https://myapp.pockethost.io ph_pat_...
```

`link` adds the remote (named `default`, or `--name`), makes it the default if there's none yet, and logs in. It takes
the same login flags.

### Log out

```bash
pocketone logout prod
```

Forgets the saved login. A browser login is also revoked on the remote (its refresh token); a pasted token stays
valid until you revoke it in the dashboard. If the remote can't be reached, it tells you to revoke it in the dashboard.

## Integrations

### Works with

| Extension | How they fit |
|---|---|
| [Deploy](../deploy/README.md) | `deploy`, `dev` and `pull` resolve their remote and login through Remotes and find deploy/v1 in whoami's contracts; `restart` and `logs` use remote/v1. |
| [OAuth](../api-tokens/OAUTH.md) (in Tokens) | Provides oauth/v1, so whoami lists it; `login` then signs in through the browser (loopback redirect on 127.0.0.1 with PKCE) or a device code. |
| [Tokens](../api-tokens/README.md) | Provides the API tokens `login` takes when pasted. Whoami reports a token's id and scopes. |

### remote/v1

Remote control of an app, at `/api/_c/remote/v1`:

| Operation | Endpoint | Token scope |
|---|---|---|
| whoami | `GET /api/_c/remote/v1` | none needed (any token may ask who it is) |
| status | `GET /api/_c/remote/v1/status` | `remote:read` |
| restart | `POST /api/_c/remote/v1/restart` | `remote` |
| logs | `GET /api/_c/remote/v1/logs?n=100` (server-sent events) | `remote:read` |

Whoami says who the request's credential is and what the app offers, so clients can discover it:

```json
{"auth": {"collection": "_superusers", "id": "…", "email": "admin@example.com"}, "via": "token",
 "tokenId": "…", "scopes": ["deploy", "remote"], "contracts": ["deploy/v1", "oauth/v1", "remote/v1", "api-tokens/v1"],
 "extensions": ["tokens", "remotes", "deploy"],
 "app": {"name": "Acme", "url": "https://myapp.example.com"}}
```

`via` is `token` for an API token, `jwt` for a PocketBase auth token, and empty with no credential (`auth` is then
`null`, and `extensions` is left out). `contracts` are the contract APIs the app offers, each at `/api/_c/<id>/`:
everything a client can use (oauth/v1's metadata is at `/.well-known/oauth-authorization-server`). Restart answers at once and restarts just after (on a multisite host,
only that instance).

### Building on Remotes

- **remote/v1 client.** `remotev1.NewClient(url, token, httpClient)` (ext/contracts/remote/v1) calls whoami, status,
  restart and logs.
- **Go client.** The CLI client lives with the contract, in
  [`remoteclient`](../contracts/remote/v1/remoteclient) (ext/contracts/remote/v1/remoteclient), so other extensions'
  commands use it without importing this one. `remoteclient.Open(dir, name)` resolves a remote and its credential
  (environment first, then the saved login, renewing a browser login when due) and returns a `*remoteclient.Client`:
  base URL, bearer token, an HTTP client with short connect timeouts, and `JSON`, `NewRequest`, `Do` and `Whoami`.
  It also holds the linked project (`LoadProject`), saved logins (`LoadCredentials`) and the sign-ins
  (`LoginWithToken`, `OAuthLogin`, `PasswordLogin`). Deploy's commands build on it.
- **Commands.** `remotes.Commands()` returns the `remote`, `login`, `logout` and `link` commands, for a binary that
  isn't a `*pocketbase.PocketBase`. The rest of `remote` is generated: every operation of every contract the binary
  links is a `remote ...` command ([cli.commands/v1](../contracts/cli/commands/v1/README.md)), such as `remote status`,
  `remote restart`, `remote restore to --at -2h`.
- **Login.** `remotes.BrowserLogin` is the sign-in `login` tries first (`remoteclient.OAuthLogin`; tests replace it).

## Example

[`example/main.go`](example/main.go) is an app with remotes and API tokens. Run two copies and add one as a remote of the other; its comment has the commands.

```go
// Command example is a PocketOne app with remotes and API tokens: run two copies, add one as a remote of the other's
// project and sign in.
//
//	go run ./example serve --http=127.0.0.1:8090                # the "remote"
//	go run ./example remote add local http://127.0.0.1:8090    # in a project folder
//	go run ./example login local                               # browser sign-in: admin@example.com / password
//	go run ./example remote whoami
//
// Logins (ext/devusers, local testing only): dashboard admin@example.com / password, app user user@example.com / password.
package main

import (
	"log"

	"ganigeorgiev.com/pocketbase/pocketbase"
	"pocketone.dev/ext/api-tokens"
	"pocketone.dev/ext/devusers"
	"pocketone.dev/ext/extkit"
	"pocketone.dev/ext/remotes"
)

func main() {
	app := pocketbase.New()
	devusers.Register(app) // local testing: superuser admin@example.com and user user@example.com, password "password"

	// registered through their contracts (the standard set does this for you): remotes provides remote/v1 (whoami at
	// /api/_c/remote/v1, status, restart, logs) and the remote/login/logout/link commands; tokens adds API tokens
	// and the OAuth server login signs in through
	if _, err := extkit.Setup(app, []extkit.Extension{apitokens.New(apitokens.Config{}), remotes.New(remotes.Config{})}, nil, nil); err != nil {
		log.Fatal(err)
	}

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
```

## License

MIT
