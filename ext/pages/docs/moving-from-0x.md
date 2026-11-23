---
title: Moving from PocketPages 0.x
description: What changed between the PocketPages npm package (0.x) and Pages in PocketOne.
---

# Moving from PocketPages 0.x

Pages is part of PocketOne (the `pages` extension). It keeps the ideas of the npm package (a folder of files that become routes, EJS, layouts, middleware, private partials) and changes the shapes. Apps written for 0.x don't run unchanged.

There is no migration tool. Start a new app with `pocketone pages create <name>`, then move your pages over using the list below. The npm package is being retired.

## Folder and boot

| 0.x | v1 |
|---|---|
| `pb_hooks/pages/`, booted by `require('pocketpages')` in a `.pb.js` file | `pb_pages/` next to `pb_data`. No boot file, no npm install. |
| `create-pocketpages` | `pocketone pages create <name> [--starter <kit>]` |

Delete the `.pb.js` boot file and the `pocketpages` dependencies, and move the contents of `pb_hooks/pages/` to `pb_pages/`.

## JavaScript or TypeScript

Your `.ejs` templates, `+middleware.js`, `+config.js` and `_private/` modules stay as they are. v1 also runs
TypeScript (`.ts` code and `.ets` templates, types stripped, never checked), and the rest of these docs use it.
Renaming a file from `.js` to `.ts` or `.ejs` to `.ets` changes nothing else, so you can switch a file at a time or not
at all. A route's own code (a page's loaders and handlers, an endpoint) is always `.ts`, since a `.js` file on a route
path is served to the browser as-is; plain JavaScript is valid there. This guide shows the rest of v1's code as `.js`
to match yours. See [File types](file-types.md).

## The context object

0.x passed an `api` object and spread its members into each template. v1 passes one `ctx` object with `req`, `res`, `data`, `params`, `route` and helpers. See the [ctx reference](ctx/index.md).

| 0.x | v1 |
|---|---|
| `request`, `response` | `ctx.req`, `ctx.res` |
| `body()`, `formData` | `ctx.req.body()` |
| `redirect(path)` | `ctx.res.redirect(path)` |
| `response.redirect(path, status)` | `ctx.res.redirect(path, status)` (unchanged) |
| `env()`, `store()`, `dbg()`, `info()`, `warn()`, `error()` | `ctx.env()`, `ctx.store()`, `ctx.log.debug()` / `info()` / `warn()` / `error()` |
| `auth`, `request.auth` | `ctx.auth` |
| `resolve(name)` | `ctx.resolve(name)` |

Inside templates, `data`, `params`, `asset` and `meta` are still available without the `ctx.` prefix. The other
0.x template globals are gone, including `resolve`, `redirect` and `env`: use `ctx.resolve()`, `ctx.res.redirect()` and
`ctx.env()`.

## Parameters, query strings and cookies

- Query parameters are no longer mixed into `params`, and nothing is JSON-parsed for you. `ctx.params` holds route parameters only; read the query string with `ctx.req.query(name)`, which returns strings.
- Cookies are strings. Pass `{ json: true }` to parse one: `ctx.req.cookie('prefs', { json: true })`.

## Middleware

0.x middleware either returned a value or called `next(extra)`, depending on how many arguments it declared. v1 has one signature:

```js
module.exports = (ctx, next) => {
  ctx.data.user = ctx.auth
  return next()
}
```

Put shared data on `ctx.data`. See [Middleware](middleware.md).

## Loaders

`+load.js`, `+get.js`, `+post.js` and friends are replaced by one `.ts` file beside each page (`products.ts` beside `products.ejs`) that exports `load`, `get`, `post`, `put`, `patch` and `delete`. Returned objects are shallow-assigned into `ctx.data` (no deep merge). As in 0.x, every handler is optional and a page renders for any method, so templates that check the request method keep working (`request.method` becomes `ctx.req.method`).

```js
// 0.x: +load.js
module.exports = (api) => ({ products: $app.findAllRecords('products') })

// v1: products.ts, beside products.ejs
module.exports = {
  load(ctx) {
    return { products: ctx.app.findAllRecords('products') }
  },
}
```

See [Loading Data](loading-data.md).

## JSON

0.x sent template output as JSON if it parsed as JSON. v1 never does. Send JSON with `ctx.res.json()`, usually from an endpoint: a `.ts` file with no template, such as `feeds/latest.ts`. See [Endpoints and JSON](api-routes.md).

## Layouts and slots

| 0.x | v1 |
|---|---|
| `<!-- slot:name -->` comments in the page | `slot('name', () => { %>...<%% })` |
| `slot` and `slots.name` in layouts | `slot()` and `slot('name')` |

See [Layouts and Slots](layouts.md).

## Output

`echo()` is gone. A template returns its text; anything you want in the output goes through a tag. For streaming, use `ctx.res.sse()`.

## Flash messages

0.x put the message in a `?__flash=` query parameter with `redirect(path, { message })`. v1 uses a one-time cookie:

```js
return ctx.res.redirect('/account', { flash: 'Saved' })
```

Read it on the next request with `ctx.flash()`. See [flash](ctx/flash.md).

## Assets

`asset()` URLs now carry the first 12 hex characters of the file's SHA-256, not the full hash.

## Configuration and plugins

- `+config.js` still takes plugin package names, which are loaded with `require` as before, but the packages must be
  v1 plugins: the 0.x `pocketpages-plugin-*` packages don't run on v1. `auth` and `realtime` are built in, EJS and
  Markdown are always on, and Datastar is an [add-on](plugins/datastar.md) you copy into `pb_pages/_private/` and list as `'datastar'`. Its API is 0.x's, with `signals()` added alongside `readSignals`. npm plugin package names and EJS options are gone.
- `+config.js` must export an object (`module.exports = { ... }`). 0.x's function form, `module.exports = (api) => ({ ... })`, is an error.
- A `package.json` at the app's root still works: bare `require('pkg')` looks in the app folder's `node_modules` after `pb_pages`.
- EJS and Markdown are always on. `pocketpages-plugin-ejs` and `pocketpages-plugin-marked` have no v1 equivalent because they aren't needed.
- The auth plugin no longer goes through the JS SDK over HTTP (`api.pb()`). Use `ctx.session.*`, which calls the app directly. See [Authentication](plugins/auth.md).

See [+config.js](config.md) and [Plugins](plugins/index.md).

## Behavior that changed quietly

These don't fail with an error, so check for them when porting:

- **Cookies are `HttpOnly` by default.** Pass `{ httpOnly: false }` to `ctx.res.cookie` for a cookie your page's JS reads. The `pb_auth` cookie stays `HttpOnly`, so `pb.authStore.loadFromCookie(document.cookie)` in the browser no longer works.
- **Realtime messages are JSON-encoded.** `ctx.realtime.send(topic, data)` sends `JSON.stringify(data)`, so a string (such as an HTML fragment) arrives in quotes: `JSON.parse` it on the client. A `filter` now gets one argument, the client, and only narrows clients already subscribed to the topic.
- **`.md`, `.jsx` and `.tsx` files are always pages**, never served as static files.
- **Front matter is plain YAML.** EJS tags inside it aren't run.
- **`require` never leaves `pb_pages`.** To load code from `pb_hooks`, use `require(__hooks + '/lib/x')`.
- **Hook files (`*.pb.js`) go in `pb_hooks`.** In `pb_pages` they're an error and aren't served.

## Removed globals

| 0.x | v1 |
|---|---|
| `url()` | `ctx.req.url` |
| `stringify()` | `JSON.stringify` |
| micro-dash | plain JavaScript |
| `pb()` (JS SDK plugin) | `ctx.pb()`, built in: same SDK, no host setting, runs in-process as the signed-in user |

## Routing

A route with fewer segments no longer matches a longer URL. Segment counts must match: `products/[id].ejs` serves `/products/123` but not `/products/123/reviews`.
