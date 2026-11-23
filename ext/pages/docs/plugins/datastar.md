---
title: Datastar Add-on
description: The Datastar add-on adds ctx.datastar helpers for Datastar script tags, SSE patches and signals.
---

# Datastar Add-on

The Datastar add-on adds helpers for [Datastar](https://data-star.dev). It's a custom plugin that ships with Pages
rather than a built-in: copy [`addons/datastar.js`](https://github.com/pocketonehq/pocketone/blob/main/ext/pages/addons/datastar.js)
into `pb_pages/_private/` (the `datastar` starter kit already has it) and list it in `+config.ts` by name:

```ts
// pb_pages/+config.ts
module.exports = {
  plugins: ['datastar'],
}
```

Add the script tags to your root layout's `<head>`. They load [Datastar](https://data-star.dev) v1.0.5. Datastar 1.0 uses
a colon between the attribute name and its key (`data-on:click`, `data-bind:name`, `data-signals:count`,
`data-class:active`, `data-attr:href`); the older hyphen form (`data-on-click`) is ignored. See the
[Datastar attribute reference](https://data-star.dev/reference/attributes).

```ets
<head>
  <%%- ctx.datastar.scripts() %>
</head>
```

## `ctx.datastar`

| Member | |
|---|---|
| `scripts({ spa, realtime }?)` | Returns the script tags. |
| `open({ keepAlive }?)` | Starts the SSE response now instead of with the first patch, for a route that waits for changes. The stream sends a keep-alive comment every `keepAlive` seconds (30 by default; `0` turns it off), whether it was opened here or by the first patch. |
| `patchElements(html, { selector, mode, useViewTransition, viewTransitionSelector, namespace }?)` | Sends a patch-elements SSE event on the current response. `mode` is `outer` (default), `inner`, `replace`, `prepend`, `append`, `before`, `after` or `remove`; `namespace` is `html` (default), `svg` or `mathml`. |
| `patchSignals(signals, { onlyIfMissing }?)` | Sends a patch-signals SSE event (JSON Merge Patch: `null` removes a signal). |
| `executeScript(js, { autoRemove, attributes }?)` | Sends a script to run in the browser. |
| `consoleLog(msg)` / `consoleError(err)` | Logs to the browser console. |
| `redirect(url)` | Sends the browser to `url` (a full page load). A Datastar fetch can't follow `ctx.res.redirect`, so use this from `@get`/`@post` handlers. |
| `dispatchCustomEvent(name, detail, { selector }?)` | Fires a `CustomEvent` on `document`, or on the elements matching `selector`. |
| `replaceURL(url)` | Changes the address bar without a navigation (`history.replaceState`). |
| `prefetch(urls)` | Adds speculation rules so the browser prefetches `urls`. |
| `signals()` | Reads the request's signals as an object: from the `datastar` query parameter for GET and DELETE, from the body otherwise. |
| `readSignals(target)` | Merges the request's signals into `target` and returns it (0.x's `readSignals(request, target)` also works). |
| `realtime.*` | Sends the same patches to realtime subscribers. Needs the [`realtime`](realtime.md) plugin. |

Every send also takes `eventId` and `retryDuration` (in ms; the default 1000 isn't sent).

## Pages as patches

When a request has the `Datastar-Request: true` header, the rendered page is sent without layouts, as a `patchElements` event. If the request has a `Datastar-Selector` header, the patch goes into that element.

This is how this site navigates between docs pages: the layout calls

```ets
<%%- ctx.datastar.scripts({ spa: { scope: '#toc', selector: '#content' } }) %>
```

so links in the table of contents fetch the page and patch it into `#content`. Plain left-clicks are intercepted;
modified clicks (Cmd/Ctrl/Shift/Alt, middle-click), links with a `target` attribute and links to other origins navigate
normally. Back/Forward work, including back to the page you landed on: each page opens at the top, and Back/Forward
return you to where you were scrolled.

## Example: a counter

```ets
<!-- pb_pages/counter/index.ets -->
<button data-on:click="@post('/counter')">
  Count: <span id="count"><%%= ctx.store('count') || 0 %></span>
</button>
```

```ts
// pb_pages/counter/index.ts
module.exports = {
  post(ctx) {
    const count = (ctx.store('count') || 0) + 1
    ctx.store('count', count)
    ctx.datastar.patchElements(`<span id="count">${count}</span>`)
  },
}
```

## Example: reading signals

```ts
module.exports = {
  post(ctx) {
    const { name, email } = ctx.datastar.signals()
    if (!name || !email) {
      ctx.datastar.patchSignals({ error: 'Please fill all fields' })
      return
    }
    ctx.datastar.patchElements('<div id="form">Thanks!</div>')
  },
}
```

## Live updates

To patch the page whenever records change, open a stream with `data-init="@get('/todos/live')"`, call
`ctx.datastar.open()`, and turn each change from [`ctx.realtime.subscribe()`](realtime.md#with-datastar) into a patch. Only the visitor's own stream
gets them, with the collection's rules applied for that visitor.

## Broadcasting

With the `realtime` plugin on, `ctx.datastar.realtime.*` sends patches to every realtime subscriber instead of the current response:

```ts
// pb_pages/+config.ts
module.exports = { plugins: ['realtime', 'datastar'] }
```

```ts
ctx.datastar.realtime.patchElements(`<span id="count">${count}</span>`)
```
