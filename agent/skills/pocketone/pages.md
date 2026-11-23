# Building pages (`pb_pages`)

Read this when a project has a `pb_pages/` folder, a `+layout.ets`, `+config.ts` or `+middleware.ts`
file, `.ets` templates with `.ts` code beside them (or their `.ejs`/`.js` twins), or wants a server-rendered site on PocketOne. It covers routing,
handlers, middleware, layouts and slots, partials, the `ctx` API, plugins (auth, realtime, the Datastar add-on), JSVM
limits, and running and deploying.

Pages (formerly PocketPages) is the `pages` extension built into PocketOne. Files in a `pb_pages/` folder next to `pb_data/` become
routes, like PHP. Pages are ETS templates (EJS tags holding TypeScript), Markdown or TSX components rendered on the server, with PocketBase's JSVM API
(`$app`, `Record`, `$dbx`, `$security`, ...) available. No npm install, no boot file, no build step, no Node server.

It is **not** the npm `pocketpages` package (0.x). That one lived in `pb_hooks/pages/`, passed an `api` object and had
`+load.js`/`+get.js` loaders. Most 0.x code needs changes: see [moving-from-0x](reference/docs/extensions/pages/moving-from-0x.md).

The contract is [spec](reference/docs/extensions/pages/spec.md). Where a doc and the SPEC differ, trust the SPEC.

## Write TypeScript and ETS

- **Code is `.ts`, templates are `.ets`.** Code: a route's `.ts` (page code or endpoint), `+middleware`, `+config`, `_private/` modules,
  plugins. Templates: pages, `+layout`, `+error`, `+404`, partials. ETS is EJS syntax whose tags hold TypeScript.
  Types are stripped, never checked: no build step, no `tsconfig`.
- **One style per app.** Use TSX (`.tsx` components) only when the app already uses it or the user asks for it.
- **`.js`, `.ejs` and `.jsx` are the plain-JavaScript twins** of `.ts`, `.ets` and `.tsx` and behave the same.
  Existing apps (0.x ports included) keep them: don't rename a user's existing files unasked, and match the app's
  style when adding files.
- **`.md` pages run EJS tags with plain JavaScript**, not TypeScript.
- **Types:** annotate with the `pages` namespace from the app's `types.d.ts` (no import): `load(ctx: pages.Context)`,
  `({ children, slot }: pages.Props)`, `module.exports = { ... } satisfies pages.Handlers`, `pages.Middleware`,
  `pages.Config`. See [typescript](reference/docs/extensions/pages/typescript.md).

More: [file-types](reference/docs/extensions/pages/file-types.md), [ets](reference/docs/extensions/pages/ets.md).

## This is not Node

Page code (middleware, handlers, templates, plugins) runs in the app's JS VM (QuickJS, PocketOne's JS engine):

- **No Node or browser APIs**: no `fs`, `Buffer`, `window`, `document`, `fetch`. Use PocketBase's JSVM globals
  (`$http.send`, `$os`, `$security`, ...) and `ctx` helpers.
- **Sync by default, async when you want.** `ctx.app`, `ctx.pb()` and `$http.send()` return results directly; don't
  `await` them. To wait outside the VM, `await` a Go function's `.async` twin
  (`await ctx.app.countRecords.async('orders')`): middleware (`await next()`), `load`, handlers, ETS templates and
  TSX components may all be `async`. In a template, an async partial needs `<%- await include('name') %>`.
- **Records are Go objects.** Read with `record.get('x')` / `record.getString('x')` / `record.getInt('x')`, write
  with `record.set('x', v)`. `record.id` works.
- **`$app` finders take positional arguments**, and bind user input with `{:name}` placeholders, never string
  interpolation:
  ```ts
  ctx.app.findRecordsByFilter('posts', 'status = {:s}', '-created', 20, 0, { s: 'published' })
  ctx.app.findFirstRecordByData('products', 'slug', ctx.params.slug) // throws if not found
  ctx.app.findRecordById('posts', id)                                  // throws if not found
  ```
- **`require()`** works for local files and plain CommonJS packages. Lookup by bare name: `_private/` folders
  (nearest first), then `node_modules` up through `pb_pages`, then `pb_hooks/node_modules`, then the app folder's
  `node_modules`. `./` and `../` never leave `pb_pages`; to reach `pb_hooks`, use `require(__hooks + '/lib/x')`.
  Install npm packages in the app folder (beside `pb_pages`), not inside it: non-private files in `pb_pages` are served.

More: [jsvm](reference/docs/extensions/pages/jsvm.md), [ctx.app](reference/docs/extensions/pages/ctx/app.md).

## Project layout and running locally

```
myapp/
  pb_data/          # local DB; never deploy
  pb_hooks/         # PocketBase hooks (*.pb.js) stay here
  pb_migrations/
  pb_public/
  pb_pages/         # the app
    +config.ts      # optional: plugins, debug (top level only)
    +layout.ets     # optional: wraps every page
    +middleware.ts  # optional: runs for every route below
    +error.ets      # optional: error page (top level only)
    +404.ets        # optional: not-found page (top level only)
    index.ets       # /
    _private/       # partials and modules, never served
    assets/app.css  # static file at /assets/app.css
```

```bash
brew install pocketonehq/tap/pocketone        # Windows: scoop bucket add pocketonehq https://github.com/pocketonehq/scoop-bucket; scoop install pocketone
pocketone pages starters                     # list kits: minimal (default), mvp, htmx, daisyui, daisyui-docs, datastar, auth
pocketone pages create myapp --starter auth  # folder must not exist yet
cd myapp
pocketone superuser upsert you@example.com <password> --dir ./pb_data   # BEFORE the first serve
pocketone serve --dir ./pb_data --dev
```

- **Create the superuser before the first `serve`.** Without one, PocketBase opens its superuser installer in a browser.
- **Pass `--dir ./pb_data`.** `pocketone` keeps `pb_data` next to its executable by default; `pb_pages` and
  `pb_migrations` are found next to `pb_data`. `--pagesDir` points somewhere else.
- `--dev` turns on detailed error pages and `ctx.log.debug` output.
- No restart for page edits: the route table is rebuilt on any change in `pb_pages`, templates are cached by content
  hash. A rebuild never restarts the app or reloads `pb_hooks`. After changing `node_modules`, touch a file in
  `pb_pages` (or restart).

## File conventions

| File / folder | Meaning |
|---|---|
| `about.ets`, `about.md`, `about.tsx` | Page at `/about` |
| `docs/index.md` | Page at `/docs` (and `/docs/`) |
| `products/[id].ets`, `[id]/index.ets` | One segment → `ctx.params.id` (string). Must be the whole segment |
| `(group)/` | Groups files, scopes layouts/middleware/`_private`. **Not in the URL** |
| `robots.txt.ets`, `feed.xml.ets` | `/robots.txt` as `text/plain`, `/feed.xml` as XML; no layouts |
| `+layout.ets` / `.tsx` | Wraps pages in this folder and below. One per folder. Innermost applies first |
| `+middleware.ts` | `(ctx, next)`, runs root first for every route below |
| `about.ts` beside `about.ets` | That page's code: `load`, `get`, `post`, `put`, `patch`, `delete` (`index.ts` with `index.ets`, `[id].ts` with `[id].ets`) |
| `logout.ts`, `feeds/rss.ts` (no template of that name) | An endpoint at its own URL (`/logout`, `/feeds/rss`): handlers send the whole response (JSON, feeds, webhooks, redirects, SSE) |
| `+config.ts`, `+error.*`, `+404.*` | Top of `pb_pages` only |
| `_private/` | Partials for `include()`, modules for `require()` / `ctx.resolve()`. Never served |
| name starting `_`, `-`, `.` | Not routed (`-` for drafts and build inputs like `-app.tailwind.css`). Top-level `.well-known/` is served |
| any other file (`.css`, `.png`, `.html`, `.js`) | Static file at its path (groups removed), with ETag. Only `.ts` runs; `.d.ts` is never served |

Which route wins: `/api/...` and `/_/...` always belong to PocketBase. A literal segment beats `[param]`, compared
from the left. Segment counts must match: `products/[id].ets` doesn't match `/products/1/reviews`. A URL nothing in
`pb_pages` matches falls through to `pb_public`, then PocketBase. Matching is case-sensitive; a trailing slash matches
the same route.

Details: [routing](reference/docs/extensions/pages/routing.md), [directory-structure](reference/docs/extensions/pages/directory-structure.md).

## Request flow

1. Plugins: every `extendContext(ctx)`, then every `onRequest(ctx)`, in `+config.ts` order.
2. Every `+middleware.ts` from `pb_pages/` down to the route's folder (groups included).
3. The route's `.ts` (page code or endpoint): `load`, then the handler for the request method, if any.
4. The template, then layouts, innermost first.
5. The response: `200 text/html` unless code set a status or sent something else.

If any step sends a response (`ctx.res.redirect`, `json`, `html`, `text`, `file`, `sse`), later steps are skipped.

- **A page renders for any HTTP method.** Handlers are optional; a template can handle a form itself by checking
  `ctx.req.method`. `HEAD` runs `load` and `get` like `GET`, so keep side effects in `post`/`put`/`patch`/`delete`.
- Only endpoints (a `.ts` with no template) answer `405` for a method with no handler, and they must send a
  response or it's a 500. Their errors are JSON unless the request's `Accept` puts `text/html` first.
- A handler can set `ctx.data.x` or return a plain object, which is **shallow**-assigned into `ctx.data`.
- **JSON is only sent by `ctx.res.json()`** (and an endpoint's errors). Template output is never parsed as JSON.

More: [loading-data](reference/docs/extensions/pages/loading-data.md), [middleware](reference/docs/extensions/pages/middleware.md), [api-routes](reference/docs/extensions/pages/api-routes.md).

## Minimal patterns

**Page with a handler**

```ts
// pb_pages/posts/[slug]/index.ts
module.exports = {
  load(ctx) {
    try {
      ctx.data.post = ctx.app.findFirstRecordByData('posts', 'slug', ctx.params.slug)
    } catch (e) {
      throw ctx.httpError(404, 'No such post')
    }
  },
}
```

```ets
<%# pb_pages/posts/[slug]/index.ets %>
<% meta('title', data.post.getString('title')) %>
<article>
  <h1><%= data.post.getString('title') %></h1>
  <%- data.post.getString('body') %>
</article>
```

**Layout with slots and flash**

```ets
<%# pb_pages/+layout.ets %>
<!doctype html>
<html>
  <head>
    <title><%- slot('title') || meta('title') || 'My site' %></title>
    <link rel="stylesheet" href="<%= asset('/assets/app.css') %>">
  </head>
  <body>
    <%- include('nav') %>
    <% const flash = ctx.flash() %>
    <% if (flash) { %><mark><%= flash %></mark><% } %>
    <main><%- slot() %></main>
  </body>
</html>
```

A page sets a named slot with `<% slot('title', () => { %>About<% }) %>`; it outputs nothing in place.

**Form POST + redirect with a flash**

```ts
// pb_pages/contact.ts
module.exports = {
  post(ctx) {
    const { email, message } = ctx.req.body()
    const rec = new Record(ctx.app.findCollectionByNameOrId('messages'))
    rec.set('email', email)
    rec.set('message', message)
    try {
      ctx.app.save(rec)
    } catch (e) {
      return { error: 'Could not send', email, message } // page re-renders with these in data
    }
    return ctx.res.redirect('/contact', { flash: 'Thanks!' })
  },
}
```

**Auth guard middleware** (with `plugins: ['auth']` in `+config.ts`)

```ts
// pb_pages/account/+middleware.ts
module.exports = (ctx, next) => {
  if (!ctx.auth) return ctx.res.redirect('/auth/login', { flash: 'Please sign in' })
  return next()
}
```

**JSON route**

```ts
// pb_pages/feeds/latest.ts
module.exports = {
  get(ctx) {
    const posts = ctx.app.findRecordsByFilter('posts', 'published = true', '-created', 20, 0)
    return ctx.res.json(posts.map((p) => ({ id: p.id, title: p.getString('title') })))
  },
}
```

## Templates, partials, modules

- Template tags (ETS, and `.md`): `<% code %>`, `<%= escaped %>`, `<%- raw %>`, `<%# comment %>`, `-%>` and `<%_ _%>` trim whitespace,
  `<script server> ... </script>` is the same as `<% ... %>`.
- **In scope in templates:** `ctx`, `data`, `params`, `meta`, `asset`, `include`, `slot`, `require`, and `locals`.
  Everything else is on `ctx` (`ctx.auth`, `ctx.req`, `ctx.res`, `ctx.resolve`, `ctx.env`, ...).
- **There is no `echo()`.** A template returns its text; output goes through tags.
- `include(name, locals?)` renders a partial from `_private/`: the current template's folder, then each parent up to
  `pb_pages/`. Extensions are optional (`.ets`, `.ejs`, `.md`, `.jsx`, `.tsx` tried in that order). `/name` looks only in
  `pb_pages/_private/`; `/blog/_private/card` is a path from the top. Use `<%- include(...) %>`.
- **Partials get `locals`.** Passed values are top-level variables, and `locals` is the whole object, so test optional
  ones with `locals.subtitle` (a bare `subtitle` throws if it wasn't passed). Partials also see `ctx`, `data`, `params`.
- `ctx.include(name, locals?)` renders a partial from JS (handlers, middleware, plugins).
- `ctx.resolve(name, { mode })` loads a `_private/` module, looked up from the **route's** folder. Modes: `require`
  (default), `raw`, `script`, `style`.
- **Markdown (`.md`)**: YAML front matter (each key goes to `ctx.meta`), then EJS tags (plain JavaScript), then Markdown (CommonMark + GFM).
  Headings get ids; relative images go through `asset`. Layouts can't be Markdown.
- **TSX** (or `.jsx`): default export is a component; props `ctx`, `data`, `params`, `meta`, `asset`, `include`, `slot`,
  `children`. Server HTML only: no hydration, event-handler props are dropped (use `onclick="..."`, `hx-get`,
  `data-on:click`). Compiled by esbuild in the binary; types are stripped, not checked.

More: [ets](reference/docs/extensions/pages/ets.md), [partials](reference/docs/extensions/pages/partials.md), [layouts](reference/docs/extensions/pages/layouts.md),
[markdown](reference/docs/extensions/pages/markdown.md), [jsx](reference/docs/extensions/pages/jsx.md), [ctx overview](reference/docs/extensions/pages/ctx/index.md).

## `ctx` at a glance

| Member | |
|---|---|
| `ctx.req` | `method`, `url` (`{ href, origin, pathname, search }`), `query(name)` (string or `undefined`; `{ all: true }` for arrays), `header(name)`, `cookie(name, { json }?)`, `cookies()`, `body()` (JSON or form fields), `files(name)`, `ip`, `event` |
| `ctx.res` | `status(code)`, `header(name, value)`, `cookie(name, value, opts?)`, `clearCookie`, `redirect(url, { status, flash }?)` or `redirect(url, status)`, `json`, `html`, `text`, `file(path)`, `sse()`, `layout(false)`, `sent` |
| `ctx.data` / `ctx.params` / `ctx.route` | Shared data (`{}` to start) / route params (strings, no query) / `{ path, name, file }` |
| `ctx.auth` | Authenticated record or `null` (an `Authorization` header, or the `auth` plugin's cookie) |
| `ctx.app` / `ctx.pb()` | `$app` with full access / JS SDK client acting as the visitor, in-process |
| `ctx.flash()` | One-time message from the previous redirect, or `undefined` |
| `ctx.meta(k, v?)`, `ctx.asset(path)` | Page metadata / fingerprinted static URL (`/assets/app.3f9a1c2b7d4e.css`) |
| `ctx.env(name)`, `ctx.log.*`, `ctx.store(k, v?)` | Env var (`''` if unset) / `debug`, `info`, `warn`, `error` / app in-memory store |
| `ctx.httpError(status, msg)` | Throw it for a 404/403/...; JSON from an endpoint |
| `ctx.pagesDir`, `ctx.dev`, `ctx.error` | `pb_pages` path / dev mode flag / `{ status, message }` in `+error.ets` |
| `ctx.session`, `ctx.realtime`, `ctx.datastar` | Added by the `auth`, `realtime` and Datastar plugins |

Cookies set with `ctx.res.cookie` are `HttpOnly`, `SameSite=Lax`, `path=/` (and `Secure` on HTTPS) by default.
Pass `{ httpOnly: false }` for a cookie the browser's JS must read.

Reference: [req](reference/docs/extensions/pages/ctx/req.md), [res](reference/docs/extensions/pages/ctx/res.md), [data](reference/docs/extensions/pages/ctx/data.md).

## Plugins

```ts
// pb_pages/+config.ts — must export an object
module.exports = {
  plugins: ['auth', 'realtime', 'datastar'],
  debug: false,
}
```

- Only two keys: `plugins` and `debug`. ETS and Markdown are always on; they aren't plugins.
- A string is required as a module first (`_private/<name>.ts`, then a `node_modules` package, or a `./path`);
  only if none is found is it a built-in (`auth`, `realtime`). `require('./_private/x.ts')` loads one exact file;
  `{ factory, ...options }` passes options.
- **`auth`** (built in): `pb_auth` cookie sessions and `ctx.session`: `register`, `signInWithPassword`, `requestOTP`,
  `signInWithOTP`, `signInAnonymously`, `requestOAuth2`, `signInWithOAuth2`, `signIn(record)`, `signOut`,
  `authMethods`, and the email flows (`requestVerification`, `confirmVerification`, `requestPasswordReset`,
  `confirmPasswordReset`, `requestEmailChange`, `confirmEmailChange`). Accounts that need MFA are refused.
- **`realtime`** (built in): `ctx.realtime.send(topic, data, { filter }?)`. `data` is JSON-encoded, so an HTML
  string arrives quoted: `JSON.parse` it on the client. `for await (const { action, record } of
  ctx.realtime.subscribe('posts/*', { filter, expand }))` in an `async` handler streams record changes (with
  `ctx.res.sse()` or `ctx.datastar.patchElements`), with the collection's rules applied for the visitor; never poll
  with `sleep`.
- **Datastar is an add-on, not built in.** Copy `ext/pages/addons/datastar.js` into `pb_pages/_private/` (the
  `datastar` starter has it) and list `'datastar'`. It adds `ctx.datastar` (`scripts()`, `patchElements`,
  `patchSignals`, `signals()`, ...). Datastar 1.0 attributes use a colon: `data-on:click`.
- **Your own plugin**: a factory returning `{ name, onRequest(ctx), extendContext(ctx), transform({ html, ctx, file }) }`.

Details: [plugins](reference/docs/extensions/pages/plugins/index.md), [auth](reference/docs/extensions/pages/plugins/auth.md),
[authentication guide](reference/docs/extensions/pages/authentication.md), [realtime](reference/docs/extensions/pages/plugins/realtime.md),
[datastar](reference/docs/extensions/pages/plugins/datastar.md), [authoring](reference/docs/extensions/pages/plugins/authoring.md).

## Top gotchas

These are **startup errors** (logged as `pages: ...` with the file path; the previous route table keeps serving):

- 0.x loader files: `+load.js`, `+get.js`, `+post.js`, ... Any `+` name other than `+config`, `+layout`,
  `+middleware`, `+error`, `+404` is an error, `+page` and `+api` included. Put a page's code in `<page>.ts` beside it,
  exporting `load`, `get`, `post`, ...; an endpoint is `<name>.ts` with no template.
- `+layout.md` (a layout must be `.ets` or `.tsx`, or their `.ejs`/`.jsx` twins), or two layouts in one folder.
- `*.pb.js` / `*.pb.ts` hook files in `pb_pages`. They belong in `pb_hooks`.
- A `pb_pages/api/` folder (`/api/` is PocketBase's). Use `xapi/`, `feeds/`, `hooks/`, ...
- `post-[id].ets`: a param must be the whole segment. Folders can't start with `+`.
- `+config.ts`, `+error`, `+404` below the top level; two files claiming the same URL (`feed.ts` and `feed/index.ets`,
  or a static file and a page).

These **fail on every request** (500, reason in the log and on the dev error page):

- `+config.ts` exporting a function (`module.exports = (api) => ({...})`), unknown keys, a plugin listed twice, or a
  name that's neither a module nor a built-in.
- A plugin with 0.x hooks (`onExtendContextApi`, `onRender`, `onResponse`, `handles`). Use `extendContext`,
  `onRequest(ctx)`, `transform`.
- Middleware that returns without calling `next()` and without sending a response.

These **change quietly** from 0.x:

- `api`/`request`/`response`/`redirect()`/`env()`/`resolve()`/`dbg()` globals are gone: use `ctx.*` and `ctx.log.*`.
  `echo()`, `url()`, `stringify()` and micro-dash are gone.
- Query params are not in `ctx.params` and nothing is JSON-parsed: `ctx.req.query('page')` is a string.
- `formData()`/`body()` → `ctx.req.body()`. Flash: `redirect(path, { flash })` + `ctx.flash()`, not `?__flash=`.
  `<!-- slot:name -->` comments → `slot(name, fn)` / `slot(name)` / `slot()`.
- `pb_auth` is `HttpOnly`: `pb.authStore.loadFromCookie(document.cookie)` no longer works in the browser.
- `.md`, `.jsx`, `.tsx` files are always pages, never static files.
- `pocketpages-plugin-*` npm packages don't run; `pb()` is now `ctx.pb()`, built in.

## Debugging

- Run with `--dev`, or set `debug: true` in `+config.ts`: error pages show the message, file and line (mapped to the
  `.ets`/`.md` source) and a trimmed stack. Otherwise users see "Something went wrong".
- `ctx.log.debug()` prints only in dev mode or with `debug: true`; `info`/`warn`/`error` always go to the app's log.
- A change doesn't show up? Look for a `pages:` startup error in the log: a bad name or route conflict keeps the
  previous routes serving.

More: [debugging](reference/docs/extensions/pages/debugging.md), [errors](reference/docs/extensions/pages/errors.md).

## Deploying

`pb_pages` deploys with the other `pb_*` folders. Pages are read on each request, so they go live without a restart.

```bash
pocketone clone https://myapp.example.com  # no local copy yet: make ./myapp, link, log in, pull its code
pocketone link https://myapp.example.com   # add the remote, make it default, log in
pocketone deploy                           # upload what changed (pb_* except pb_data, package.json, node_modules)
pocketone logs                             # follow the remote's log
pocketone dev                              # two-way sync while you work, with logs
```

- On PocketHost: `pocketone link https://<name>.pockethost.io <token>`. In CI, set `P1_TOKEN` instead.
- Migrations and packages apply only on restart: `pocketone deploy --restart` or `pocketone restart`.
- Self-hosting: run `pocketone` (pages is built in) with `pb_pages` next to `pb_data`, without `--dev`, over HTTPS.
  Secrets are environment variables, read with `ctx.env('NAME')`.

More: [deploying](reference/docs/extensions/pages/deploying.md), [secrets](reference/docs/extensions/pages/secrets.md).

## Reference files

Getting started
- [spec](reference/docs/extensions/pages/spec.md): the full v1 contract, including every change from 0.x (section 12).
- [installation](reference/docs/extensions/pages/installation.md): installing PocketOne, quickstart, where `pb_pages` lives.
- [starter-kits](reference/docs/extensions/pages/starter-kits.md): `pocketone pages create` and the kits.
- [creating-a-page](reference/docs/extensions/pages/creating-a-page.md): first pages, index files, nested folders.
- [directory-structure](reference/docs/extensions/pages/directory-structure.md): folder layout and the `+ _ - .` prefixes.
- [moving-from-0x](reference/docs/extensions/pages/moving-from-0x.md): porting an app from the npm PocketPages package (0.x).

Routing and request flow
- [routing](reference/docs/extensions/pages/routing.md): files to URLs, params, groups, API routes, precedence.
- [parameters](reference/docs/extensions/pages/parameters.md): `ctx.params` and `ctx.req.query()`.
- [loading-data](reference/docs/extensions/pages/loading-data.md): inline loading, page code (`<page>.ts`), template-less pages.
- [middleware](reference/docs/extensions/pages/middleware.md): `(ctx, next)`, order, common uses.
- [api-routes](reference/docs/extensions/pages/api-routes.md): endpoints (`<name>.ts`) and JSON responses.
- [errors](reference/docs/extensions/pages/errors.md): thrown errors, `ctx.httpError`, `+error` and `+404` pages.

Templates
- [file-types](reference/docs/extensions/pages/file-types.md): `.ts`/`.ets`/`.tsx`, their JavaScript twins, `.md`.
- [ets](reference/docs/extensions/pages/ets.md): ETS templates: EJS tags holding TypeScript, escaping, what's in scope.
- [layouts](reference/docs/extensions/pages/layouts.md): `+layout` files and named slots.
- [partials](reference/docs/extensions/pages/partials.md): `include()`, lookup order, `locals`.
- [private-files](reference/docs/extensions/pages/private-files.md): `_private/` folders and unrouted names.
- [markdown](reference/docs/extensions/pages/markdown.md): `.md` pages, front matter, heading ids.
- [jsx](reference/docs/extensions/pages/jsx.md): `.tsx` (and `.jsx`) pages, layouts and partials.
- [static-content](reference/docs/extensions/pages/static-content.md), [asset-management](reference/docs/extensions/pages/asset-management.md), [caching](reference/docs/extensions/pages/caching.md): static files and ETags, `asset()` URLs, what's cached.

The `ctx` object
- [ctx/index](reference/docs/extensions/pages/ctx/index.md): every `ctx` member, with links.
- [ctx/req](reference/docs/extensions/pages/ctx/req.md): method, URL, query, headers, cookies, body, uploads.
- [ctx/res](reference/docs/extensions/pages/ctx/res.md): status, headers, cookies, redirects, JSON/HTML/text/file, SSE.
- [ctx/data](reference/docs/extensions/pages/ctx/data.md): `ctx.data`, `ctx.params`, `ctx.route`.
- [ctx/auth](reference/docs/extensions/pages/ctx/auth.md): the signed-in record and page guards.
- [ctx/app](reference/docs/extensions/pages/ctx/app.md): records, collections, settings via `$app`.
- [ctx/pb](reference/docs/extensions/pages/ctx/pb.md): the JS SDK as the visitor; `ctx.pb()` vs `ctx.app`.
- [ctx/flash](reference/docs/extensions/pages/ctx/flash.md): one-time messages across a redirect.
- [ctx/meta](reference/docs/extensions/pages/ctx/meta.md), [ctx/asset](reference/docs/extensions/pages/ctx/asset.md): page metadata and front matter; fingerprinted static URLs.
- [ctx/resolve](reference/docs/extensions/pages/ctx/resolve.md): loading `_private/` modules and files.
- [ctx/env](reference/docs/extensions/pages/ctx/env.md), [ctx/log](reference/docs/extensions/pages/ctx/log.md), [ctx/store](reference/docs/extensions/pages/ctx/store.md): environment variables, logging, the in-memory store.

Plugins
- [plugins/index](reference/docs/extensions/pages/plugins/index.md): built-ins, custom plugins, add-ons, lookup order.
- [plugins/auth](reference/docs/extensions/pages/plugins/auth.md), [authentication](reference/docs/extensions/pages/authentication.md): `ctx.session`; full sign-in flows (password, OTP, OAuth2, anonymous).
- [plugins/realtime](reference/docs/extensions/pages/plugins/realtime.md), [realtime](reference/docs/extensions/pages/realtime.md): `ctx.realtime.send()`, `ctx.realtime.subscribe()` (streaming record changes from a page); receiving in the browser.
- [plugins/datastar](reference/docs/extensions/pages/plugins/datastar.md): the Datastar add-on and `ctx.datastar`.
- [htmx](reference/docs/extensions/pages/htmx.md): using htmx: fragments without layouts, forms, realtime.
- [plugins/authoring](reference/docs/extensions/pages/plugins/authoring.md): writing a custom plugin.
- [config](reference/docs/extensions/pages/config.md): `+config.ts` options.

Runtime, debugging, deploying
- [jsvm](reference/docs/extensions/pages/jsvm.md): the JS runtime, `$app`, npm packages.
- [debugging](reference/docs/extensions/pages/debugging.md): dev mode, `debug: true`, logging.
- [deploying](reference/docs/extensions/pages/deploying.md): going live with `pb_pages` next to `pb_data`.
- [secrets](reference/docs/extensions/pages/secrets.md): secrets as environment variables.
- Speedruns: [google-oauth2](reference/docs/extensions/pages/speedruns/google-oauth2/index.md) (Google sign-in),
  [ses](reference/docs/extensions/pages/speedruns/ses/index.md) and [gs-gmail](reference/docs/extensions/pages/speedruns/gs-gmail/index.md) (email),
  [custom-domain-fly-cloudflare](reference/docs/extensions/pages/speedruns/custom-domain-fly-cloudflare.md) (domain on Fly.io).
