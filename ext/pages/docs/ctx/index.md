---
title: The ctx Object
description: Reference for ctx, the one object per request passed to middleware, handlers, templates and plugins.
---

# The `ctx` Object

Pages creates one `ctx` object per request and passes it to middleware, handlers, templates and plugins.

```ts
// +middleware.ts
module.exports = (ctx, next) => next()

// about.ts (beside about.ets), or an endpoint such as feeds/rss.ts
module.exports = { load(ctx) {} }
```

```ets
<!-- any template -->
<p><%%= ctx.req.url.pathname %></p>
```

In templates, `data`, `params`, `asset` and `meta` are shortcuts for `ctx.data`, `ctx.params`, `ctx.asset` and `ctx.meta`.

## Members

| Member | |
|---|---|
| [`ctx.req`](req.md) | The request: method, URL, query, headers, cookies, body, files |
| [`ctx.res`](res.md) | The response: status, headers, cookies, redirects, JSON, HTML, text, files, SSE |
| [`ctx.data`](data.md) | Plain object shared by middleware, handlers and templates |
| [`ctx.params`](data.md#ctxparams) | Route parameters |
| [`ctx.route`](data.md#ctxroute) | The matched route: `{ path, name, file }` |
| [`ctx.flash()`](flash.md) | The one-time message from the previous redirect |
| [`ctx.auth`](auth.md) | The authenticated record, or `null` |
| [`ctx.app`](app.md) | The app (`$app`) |
| [`ctx.pb()`](pb.md) | A JS SDK client acting as the visitor (API rules apply) |
| [`ctx.env(name)`](env.md) | Environment variable |
| [`ctx.log`](log.md) | `debug`, `info`, `warn`, `error` |
| [`ctx.meta(key, value?)`](meta.md) | Per-request page metadata |
| [`ctx.asset(path)`](asset.md) | Fingerprinted URL for a static file |
| [`ctx.include(name, locals?)`](../partials.md) | Render a `_private/` partial from JS and get its text (same lookup as `include`, from the route's folder) |
| [`ctx.resolve(name, { mode }?)`](resolve.md) | Load a module from `_private/` |
| [`ctx.store(name, value?)`](store.md) | The app's in-memory store |
| `ctx.pagesDir` | Absolute path of the app's `pb_pages` folder (read-only), e.g. for `$os.readDir` |
| `ctx.dev` | `true` in dev mode |
| [`ctx.httpError(status, message)`](../errors.md) | An error to throw for a specific status |
| [`ctx.error`](../errors.md) | `{ status, message }`, in `+error.ets` |

## Added by plugins

| Member | Plugin |
|---|---|
| `ctx.session` | [auth](../plugins/auth.md) |
| `ctx.realtime` | [realtime](../plugins/realtime.md) |
| `ctx.datastar` | [datastar](../plugins/datastar.md) |

Custom plugins can add their own members with [`extendContext`](../plugins/authoring.md).
