---
owners: ["@benallfree"]
version: 0.0.2
title: "MCP"
group: binary
summary: "An MCP endpoint at /mcp so agents can manage your app and call its tools."
---

# MCP

> Walkthrough for app owners: [MCP for AI agents](https://pocketone.dev/docs/features/mcp). This page is the technical reference.

A [Model Context Protocol](https://modelcontextprotocol.io) endpoint at `/mcp`, so an agent (Claude Code, a Claude
connector, any Streamable HTTP client) can work with your app. No sidecar process.

- **Every tool is one REST call, run as the caller.** A tool call becomes a request to your app's own API, made
  in-process as whoever signed in. API rules, request hooks, middleware and rate limits apply because it *is* the REST
  request. MCP makes no permission decisions of its own.
- **The whole stock API is built in** as `pb_*` tools (records, files, auth, collections, settings, logs, crons, SQL,
  backups). An app that only uses the stock API needs nothing else.
- **Your own endpoints get tools the same way:** write the REST endpoint, then the tool wrapper (`$mcp.addEndpoint`
  in `pb_hooks`, or `AddEndpoint` from Go).
- **Every contract the app provides is there too.** Its operations are tools (`restore_points`,
  `bulkmail_start`, `remote_status`, ...), with input schemas from the contract's types, called through the contract
  so its superuser check, scopes and hooks apply. MCP provides the [mcp/v1](../contracts/mcp/v1/README.md) contract:
  another MCP server can replace this one, and serves the same contract tools.
- **Sign-in with OAuth** through [the OAuth server](../api-tokens/OAUTH.md): add `https://<host>/mcp` to Claude and sign
  in in a browser, as a superuser or an app user. The connection stays signed in until you revoke it.

## Install

MCP is built into `pocketone` and always on, with [Tokens](../api-tokens/README.md) and
[the OAuth server](../api-tokens/OAUTH.md) for sign-in and `$mcp` in `pb_hooks`. There's nothing to install or turn on.

MCP is a member of the [standard set](../standard/README.md), so every PocketOne app has it: a standalone app or an
instance on a [multisite host](../multisite/README.md), from the stock binary or your own Go build made with
`standard.New()`. The set also registers Tokens (with its OAuth server) for sign-in and binds `$mcp` in `pb_hooks`:

```go
set := standard.New()
app := set.NewApp(pocketbase.Config{}, jsvm.Config{HooksDir: "pb_hooks"})
app.Start()
```

Tools that `pb_hooks` declare before MCP registers wait for it. The module is `pocketone.dev/ext/mcp`.

## Configure

There is no `pocketone.yaml` section, flag, environment variable or settings screen for MCP. In `pocketone` it serves
`/mcp` with every built-in tool, and:
- anyone who can sign in through [the OAuth server](../api-tokens/OAUTH.md) can connect: superusers and app users of
  any auth collection, each with their own rights;
- an agent's connection doesn't expire; it lasts until you revoke it;
- the server's name for clients comes from **Settings → Application → Application name**.

A Go build can change the path, the tools, the caps and the sign-in (see [Go config](#go-config)).

### Scopes

MCP registers no scopes of its own. When an agent connects, the consent page offers **Full access** (the account's
rights, like a PocketBase auth token) or **Only these**, with a box for each of your
[token scopes](../api-tokens/README.md#your-own-scopes) (scopes that only reach contract APIs under `/api/_c/`, like `deploy`, aren't
offered unless the client asks for them). The protected resource metadata lists the same scopes in
`scopes_supported`, so a client can ask for some up front; the user can still change the ticks.

A connection limited to scopes is held to them exactly like an API token with those scopes on the REST API:

- Each tool call is checked against the scopes' paths and methods (or `Match` guards). A request they don't cover is
  refused with `403`, and the tool returns that as an error.
- Collection rules see only those scopes in `@request.headers.x_token_scope_<name>`.
- `tools/list` hides tools none of the scopes reaches, and the instructions name the scopes.

The token itself still works only on `/mcp`. To limit an agent further, sign it in as an account with fewer rights.

### Behind a proxy

The OAuth resource URL must match the URL clients use. MCP builds it from the request's host, taking the scheme from
**Settings → Application URL** when its host is the same, else from TLS or `X-Forwarded-Proto`. If your proxy rewrites
the host, set `Config.PublicURL` in a Go build.

### Browsers

Requests with an `Origin` header from another site get `403`. Clients that send no `Origin`, such as Claude's servers
and Claude Code, pass. A Go build can allow more origins with `Config.AllowedOrigins`.

## Use

### Connect Claude Code

```bash
claude mcp add --transport http my-app https://<host>/mcp
claude mcp login my-app        # or /mcp in a session
```

A browser opens on your app's sign-in page. Sign in (as an app user, or with **Sign in as a superuser**), check what
the consent page says and click **Allow**. Claude and Claude Code can connect without registering, through their
published client metadata documents.

For a Claude connector, add `https://<host>/mcp` as a custom connector and sign in the same way. Other MCP clients
register themselves with your app first.

### Paste a token instead

A PocketBase auth token, or an API token not bound to any resource, works on `/mcp` as it does on the REST API, and
tool calls forward it unchanged. A scoped API token works only if one of its scopes covers `/mcp`; to limit an agent
to scopes, connect it through the consent page instead (see [Scopes](#scopes)).

### Who a tool call runs as

Whoever signed in. A superuser gets what the API gives superusers; an app user gets what your collection rules give
them. Superuser-only tools are left out of other callers' tool list (that's presentation; the endpoint still
decides).

### See and revoke connected agents

Each sign-in creates an API token bound to `/mcp`, named "<client> (OAuth)". They're listed under
**Settings → API tokens** with an `oauth: <client id>` label; click **Revoke** to disconnect an agent.

### Built-in tools

One per stock endpoint:

| Group | Tools | Who |
|---|---|---|
| `health` | `pb_check_health` | everyone |
| `records` | `pb_list_records` (`perPage` 20 by default), `pb_get_record`, `pb_create_record`, `pb_update_record`, `pb_delete_record`, `pb_run_batch` | per collection rules |
| `auth` | `pb_list_auth_methods`, `pb_request_otp`, `pb_request_verification`, `pb_confirm_verification`, `pb_request_password_reset`, `pb_confirm_password_reset`, `pb_request_email_change`, `pb_confirm_email_change` | per collection |
| `files` | `pb_download_file` (protected files through a file token made as the caller; images as images, text as text, others as embedded data; up to 10 MB) | per view rules |
| `collections` | `pb_list_collections`, `pb_get_collection`, `pb_create_collection`, `pb_update_collection`, `pb_delete_collection`, `pb_truncate_collection`, `pb_import_collections`, `pb_get_collection_scaffolds`, `pb_list_oauth2_providers`, `pb_dry_run_view` | superusers |
| `settings` | `pb_get_settings`, `pb_update_settings`, `pb_test_s3`, `pb_test_email`, `pb_generate_apple_client_secret` | superusers |
| `logs` | `pb_list_logs`, `pb_get_log`, `pb_get_log_stats`, `pb_truncate_logs` | superusers |
| `crons` | `pb_list_crons`, `pb_run_cron` | superusers |
| `sql` | `pb_run_sql` | superusers |
| `backups` | `pb_list_backups`, `pb_create_backup`, `pb_upload_backup`, `pb_download_backup`, `pb_delete_backup`, `pb_restore_backup` | superusers |

- Record create and update take field values in `data`, with files as `{"name", "base64", "type"}` objects
  (`"field+"` appends, `"field-"` removes).
- `pb_delete_collection`, `pb_truncate_collection`, `pb_delete_backup` and `pb_restore_backup` need `confirm` set to
  the name.
- Endpoints that hand out a new token (auth-with-*, auth-refresh, impersonate) and realtime (a stream) aren't tools.
- Each tool call counts against the API's rate limits, like any request. Batch sub-requests skip route middleware
  (PocketBase behaviour).

### Add your own tools from pb_hooks

Write the endpoint as you would anyway, then declare its tool at the top level of a hooks file:

```js
// pb_hooks/blog.pb.js
routerAdd('POST', '/api/blog/posts/{id}/publish', (e) => { /* … */ }, $apis.requireSuperuserAuth())

$mcp.addEndpoint({
  name: 'blog_publish_post',
  description: 'Publish a draft post and email subscribers.',
  method: 'POST',
  path: '/api/blog/posts/{id}/publish', // {id} comes from args.id
  inputSchema: { type: 'object', required: ['id'], properties: { id: { type: 'string' } } },
  // optional; request is the default mapping, return a changed copy
  request: ({ args, auth, request }) => ({ ...request, body: { at: args.when } }),
  // optional; throw for an error the model can read
  response: ({ status, json }) => (status === 200 ? { published: json.id } : json),
})
$mcp.instructions('blog_publish_post also emails subscribers.')
```

- The tool runs the request as the caller, so the route's own auth check is what protects it. One tool serves every
  caller.
- `title`, `outputSchema` and `annotations` are optional. Without annotations, `GET` tools are marked read-only and
  `DELETE` tools destructive.
- `request` gets `{args, auth, request}` and returns `{method, path, query, body, headers, files}` (missing keys keep
  the default; `$mcp.pathValue(v)` escapes a path segment).
- `response` gets `{args, auth, status, headers, body, json, truncated}`.
- Both run one at a time on the VM that loaded the hooks and may use the file's top-level variables; the REST call
  itself runs outside it. Keep them small: the work belongs in the endpoint.
- `$mcp.tools()` lists the registered tool names.

Agents are told to prefer the app's own tools: when an app has any, the instructions sent on connect say to use them
when one fits and fall back to `pb_*`, and the tool list shows them first. Put work that spans collections in a
custom endpoint (one transaction, the app's checks), not in a chain of `pb_*` calls. A wrapper can sit over a stock
endpoint too, to give it the app's meaning (for example `notes_add_note` over `POST /api/collections/notes/records`).

### In the logs

Without the OAuth server on the app, MCP logs a warning on serve:
`mcp: the OAuth server (ext/api-tokens) is off; /mcp accepts pasted PocketBase auth tokens only, and clients can't sign in by themselves`.
In `pocketone` it always is.

## Integrations

### Works with

| Extension | How they fit |
|---|---|
| [OAuth](../api-tokens/OAUTH.md) (in Tokens) | Signs MCP clients in. MCP points clients at it from the protected resource metadata, turns on its client self-registration, and makes tokens bound to `/mcp` never expire. Who may sign in comes from its `Collections` (default: superusers and every auth collection). |
| [Tokens](../api-tokens/README.md) | The access tokens are API tokens bound to `https://<host>/mcp` (RFC 8707): they work nowhere else, and are listed and revoked under Settings → API tokens. |
| Your token scopes | On a full-access connection a tool call holds every scope you registered (except ones its collection can't hold): `$apiTokens.requireScope` guards pass, and `@request.headers.x_token_scope_<name>` is `"1"` in collection rules. On a connection limited to scopes it holds only those, and requests they don't cover are refused (see [Scopes](#scopes)). |
| [Pages](../pages/README.md) | A `pb_pages` endpoint (a `.ts` file with no template) is a REST endpoint like any other; declare its tool with `$mcp.addEndpoint`. |
| [S3 Redirect](../s3redirect/README.md) | `pb_download_file` follows its one redirect to storage. |
| JS hooks | `$mcp` in `pb_hooks` (above). |

On a [multisite host](../multisite/README.md), every instance that runs PocketOne has MCP from the standard set, with
its own tokens, OAuth sign-in and `/mcp`; with Pocker, an instance turns it off with `extensions.mcp.enabled: false`.

### Tools from Go

An extension registered on the same app reaches the server with `mcp.For(app)`:

```go
server := mcp.For(app) // or the *mcp.Server that mcp.MustRegister returned

// the endpoint, as you'd write it anyway
app.OnServe().BindFunc(func(se *core.ServeEvent) error {
    se.Router.POST("/api/blog/posts/{id}/publish", publish).Bind(apis.RequireSuperuserAuth())
    return se.Next()
})

// the tool wrapper
server.MustAddEndpoint(mcp.Endpoint{
    Name:        "blog_publish_post",            // lowercase <extension>_<verb>_<noun>; pb_ is the built-ins'
    Title:       "Publish post",
    Description: "Publish a draft post and email subscribers.",
    Method:      http.MethodPost,
    Path:        "/api/blog/posts/{id}/publish", // {id} is filled from args.id
    InputSchema: map[string]any{"type": "object", "required": []any{"id"},
        "properties": map[string]any{"id": map[string]any{"type": "string"}}},
    Annotations: mcp.Write,                      // default from the method: GET read-only, DELETE destructive
    Request:     nil,                            // optional: func(c *mcp.Call) (*mcp.Request, error)
    Response:    nil,                            // optional: func(c *mcp.Call, res *mcp.Response) (any, error)
})
server.AddInstructions("blog_publish_post also emails subscribers.")
```

The default mapping:

- `{name}` path segments come from the argument of the same name, escaped. Empty, `.` and `..` are refused (the
  router would clean them into another route).
- Other arguments go to the query string for `GET`, `HEAD` and `DELETE`, and to the JSON body for other methods.
- An argument holding a file, `{"name": "a.png", "base64": "…", "type": "image/png"}` or a list of them, makes the
  body multipart (other arguments ride along as `@jsonPayload`), the way PocketBase takes uploads. Up to 10 MB per
  file.
- Arguments are checked against the schema's `required`, top-level `type`s, `enum`s and `additionalProperties: false`
  before the request is made; the endpoint validates the rest.
- The result: 2xx JSON comes back as the result (also as `structuredContent` when it's an object); other 2xx bodies
  as text; 4xx/5xx as a tool error with PocketBase's `{status, message, data}`, so the model can fix its call. JSON
  results are capped at 1 MB and say when they were cut.

`Request` sees the caller (`c.Auth()`) but can't change who the request runs as: an `Authorization` header it sets is
dropped. Build paths with `c.Path(template)` or `mcp.PathValue`, never by joining raw strings. `c.Do` makes another
request as the same caller (e.g. a file token before a download). `Response` may return any JSON-able value, a
string, or an `mcp.Content` list (images, text, embedded files). `SuperuserOnly: true` hides a tool from other
callers' tool list.

### Tools from an extension

An extension's API is contracts, and a contract's operations are already tools: nothing to add. For a tool that
isn't a contract operation, an extension calls `mcpv1.AddTool(app, mcpv1.Tool{...})` (and `mcpv1.AddInstructions`)
in its `Register`, which works with whichever extension provides mcp/v1.

A PocketOne extension that adds tools names them `<extension>_<verb>_<noun>`: its name (with `-` as `_`), then at least two
lowercase words, such as `notes_list_notes` or `oauth_server_revoke_client`. Each has a description, and an input
schema, when it has one, is a JSON Schema object. The [conformance test](../../docs/extensions.md) checks all of this
when the extension's test puts an MCP server on the test apps:

```go
conformance.Run(t, newExtension, conformance.Options{Setup: func(app core.App) error {
    _, err := mcp.Register(app, mcp.Config{})
    return err
}})
```

### How sign-in works

With Tokens (with its OAuth server) on the app, `/mcp` uses OAuth the way Claude connectors and Claude Code expect:

1. A request without a token gets `401` with
   `WWW-Authenticate: Bearer resource_metadata="https://<host>/.well-known/oauth-protected-resource/mcp"`.
2. The protected resource metadata names the app as its authorization server (the OAuth server's issuer). The OAuth server
   publishes the authorization server metadata, with PKCE S256, client metadata documents and dynamic registration.
3. The client registers itself (Claude's published identity through a client metadata document, or dynamic
   registration), and the user signs in in a browser.
4. The access token is an API token bound to `https://<host>/mcp`. It carries the account's rights but works only on
   `/mcp`. Each MCP request makes a PocketBase auth token for the same account, and tool calls reach the REST API
   with it (as Pages' `ctx.pb()` does). For a token limited to scopes, those requests carry the scopes
   (`tokensv1.WithScopes`, through the api-tokens/v1 contract), and Tokens checks them as it would a scoped token's request.

Without the OAuth server, `/mcp` falls back to `PocketBaseTokenAuth`: pasted PocketBase auth tokens (`Collections` limits
which auth collections, default any), and the warning above. `Config.Auth` overrides the choice; `mcp.OAuth{Issuer}`
overrides the authorization server listed in the metadata.

## Example

[`example/main.go`](example/main.go) runs one app with an MCP endpoint at `/mcp`: the built-in `pb_*` tools, a tool over a Go route and one over a JS route.

```bash
cd example
go run . serve
```

The example registers [devusers](../devusers/devusers.go), so the logins are `admin@example.com` / `password`
(superuser) and `user@example.com` / `password` (user). It also registers Tokens (with its OAuth server) for OAuth sign-in,
and adds a tool over a Go route (`example_get_whoami`) and one over a JS route (`example_list_recent_users`, in
[`pb_hooks/mcp.pb.js`](example/pb_hooks/mcp.pb.js)). Then:

```bash
claude mcp add --transport http pb-example http://127.0.0.1:8090/mcp
claude mcp login pb-example
```

```go
// Command example runs one PocketOne app with an MCP endpoint at /mcp: the built-in pb_* tools (the whole stock REST
// API), a tool over a Go route, and a tool over a JS route from pb_hooks/mcp.pb.js.
//
//	cd ext/mcp/example && go run . serve
//
// Logins (ext/devusers, local testing only): dashboard admin@example.com / password, app user user@example.com / password.
//
// Sign-in is OAuth (the OAuth server in ext/api-tokens): add the endpoint to Claude Code and sign in in the browser when it
// asks. Tool calls run as whoever signed in.
//
//	claude mcp add --transport http pb-example http://127.0.0.1:8090/mcp
//	claude mcp login pb-example   (or /mcp in a session)
package main

import (
	"log"
	"net/http"

	"ganigeorgiev.com/pocketbase/pocketbase"
	"ganigeorgiev.com/pocketbase/pocketbase/apis"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"pocketone.dev/ext/api-tokens"
	"pocketone.dev/ext/devusers"
	"pocketone.dev/ext/extkit/js"
	"pocketone.dev/ext/jsvm"
	"pocketone.dev/ext/mcp"
)

func main() {
	app := pocketbase.New()
	devusers.Register(app) // local testing: superuser admin@example.com and user user@example.com, password "password"

	// OAuth sign-in: API tokens + the authorization server. ext/mcp picks it up on serve and turns on dynamic client
	// registration.
	apitokens.MustRegister(app, apitokens.Config{}) // its OAuth server comes with it

	server := mcp.MustRegister(app, mcp.Config{
		Name:        "pb-example",
		Description: "Example PocketOne app with ext/mcp.",
	})

	// A tool from Go: write the REST endpoint, then the tool wrapper. The tool runs the request as the caller, so
	// the route's own auth check is what protects it.
	app.OnServe().BindFunc(func(se *core.ServeEvent) error {
		se.Router.GET("/api/example/whoami", func(e *core.RequestEvent) error {
			return e.JSON(http.StatusOK, map[string]any{"collection": e.Auth.Collection().Name, "email": e.Auth.Email()})
		}).Bind(apis.RequireAuth())
		return se.Next()
	})
	server.MustAddEndpoint(mcp.Endpoint{
		Name:        "example_get_whoami",
		Title:       "Who am I",
		Description: "The signed-in record's collection and email.",
		Method:      http.MethodGet,
		Path:        "/api/example/whoami",
	})

	jsvm.MustRegister(app, jsvm.Config{
		OnInit: func(rt js.Runtime) { mcp.BindJS(app, rt) },
	})

	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}
```

## Go config

| Field | Default | |
|---|---|---|
| `Name`, `Version`, `Description`, `Icons` | app name slug, `dev` | `serverInfo` |
| `Instructions` | generated | first paragraph of the instructions sent on connect |
| `PublicURL` | the request's host | origin used for the OAuth resource URL; set it when a proxy rewrites the host |
| `Auth` | `OAuth{}` with Tokens' OAuth server on, else `PocketBaseTokenAuth{}` | the `Authenticator` |
| `BuiltinGroups` | all | which `pb_*` groups (`mcp.BuiltinGroups`) |
| `ReadOnlyBuiltins` | `false` | keep only the read-only `pb_*` tools |
| `NoBuiltins` | `false` | drop every `pb_*` tool |
| `MaxResponse` | 1 MB | cap on a JSON result |
| `MaxFile` | 10 MB | cap on one file uploaded or downloaded through a tool |
| `AllowedOrigins` | none | extra browser origins allowed besides the app's own |

The endpoint speaks MCP Streamable HTTP, stateless: each POST carries one JSON-RPC message and gets JSON back.
Protocol versions `2025-11-25`, `2025-06-18` and `2025-03-26`. Tools only for now.

## License

MIT
