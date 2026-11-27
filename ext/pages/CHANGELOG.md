# Pages changelog

Changes to `ext/pages`, newest first. A user-visible change adds its line under `## Unreleased` in the same
commit. Each PocketOne release raises this extension's `version:` when there's something under `## Unreleased` and
renames the section to that version ([Changelogs](../../docs/extensions.md#changelogs)).
Sections named after a PocketOne release are from before extensions had versions of their own.

## Unreleased

### Fixed

- `ctx.realtime.send(topic, data, { filter })` (and Datastar's realtime helpers) no longer fail with a 500 when
  nobody is subscribed to the topic; it sends nothing.

## 0.2.0 (2026-11-24)

### Added

- `ctx.realtime.subscribe(topic, { filter, expand, fields }?)` in the `realtime` plugin: an `async` route listens to
  record changes with `for await` and streams them over `ctx.res.sse()` or as Datastar patches, with no polling. It
  listens as the visitor, so the collection's rules apply as on `/api/realtime`, and ends with the request
  ([Realtime plugin](docs/plugins/realtime.md#listening-to-record-changes)).
- `ctx.res.sse({ keepAlive: 30 })` writes a keep-alive comment every 30 seconds while the handler runs, so proxies
  that close quiet connections keep a waiting stream open ([Server-sent events](docs/ctx/res.md#server-sent-events)).
- Datastar add-on: `ctx.datastar.open()` starts the stream before the first patch, for a route that waits for
  changes, and the add-on's stream sends a keep-alive comment every 30 seconds (`open({ keepAlive: 0 })` turns it
  off). Copy `addons/datastar.js` into `pb_pages/_private/` again to get them.

### Changed

- Routes go by file extension, like PHP: a `.ts` file on a route path runs. `about.ts` beside `about.ets` is that
  page's code (`load` and method handlers); a `.ts` with no template of the same name is an endpoint at its own URL
  (`admin/sample.ts` serves `/admin/sample`). `.js` files and every other file are static, so browser scripts can sit
  anywhere in `pb_pages`; `.d.ts` files are neither served nor run. `+page` and `+api` are gone: `+` now marks only
  framework files (`+middleware`, `+layout`, `+config`, `+error`, `+404`). Two files claiming one URL (`feed.ts` and
  `feed/index.ets`) is a startup error.
- An endpoint's errors are JSON unless the request's `Accept` puts `text/html` first (a browser opening the URL),
  when they're your `+error` page. A page's errors are always pages.

### Upgrading

- Rename each `+page.ts` (or `+page.js`) to the name of the page it serves, as `.ts`: `products/[id]/+page.ts` beside
  `index.ets` becomes `products/[id]/index.ts`. A `+page.ts` that served several pages in its folder is split into one
  `.ts` per page; move code they share into a `_private/` module (`ctx.route.name` checks go away).
- Rename each `+api.ts` (or `+api.js`) to `<folder>.ts` beside its folder (`feeds/rss/+api.ts` becomes
  `feeds/rss.ts`), or to `<folder>/index.ts` when the folder has a `+middleware` that must keep applying to it. Do the
  same for a `+page.ts` in a folder with no template.
- Route code must be `.ts`; plain JavaScript is valid in it. A `.js` file left on a route path is served as source.

### Added

- The `ask` starter kit (`pocketone pages create mydocs --starter ask`): ask your docs a question and get a short
  answer citing the pages it came from, from a folder of Markdown kept in an LLM Search bucket, with the visitor's
  calls under the LLM limits.

### Changed

- An `async` page or route keeps the app free while it waits: other requests are served meanwhile, it may wait up to the app's await time (15 minutes by default) instead of 30 seconds, and it stops when its client goes away. So a `ctx.res.sse()` stream can stay open, fed by a timer or an `.async` call ([Server-sent events](docs/ctx/res.md#server-sent-events)).

### Fixed

- `ctx.realtime.send()` messages reach each subscriber in the order the page sent them.

## 0.1.2 (2026-11-20)

### Fixed

- `ctx.pb()` writes work: `create`, `update` and the other calls with a body threw a `TypeError` from the SDK's
  `FormData` check (fixed in ext/jsvm). Validation and rule failures come back as `ClientResponseError`.
- `require()` inside a handler or any function that runs at request time works on every request, not just the
  first. A module's `require()` used the request that first loaded the module, so from the next request on it
  failed with `require("x"): not found`. It now resolves and loads files on its own.

## 0.1.1 (2026-11-18)

### Added

- TypeScript types for page code, in the app's `types.d.ts`: `pages.Context` (`ctx`), `pages.Handlers`
  (`+page`/`+api`), `pages.Middleware`, `pages.Config`, `pages.PluginFactory` and `pages.Props` for TSX, plus JSX
  element types. Use them with `satisfies` or as annotations and check with `tsc` or your editor; add your own
  `ctx.data` keys by extending `pages.Data`. See the new TypeScript docs page.
- Page code can be async. PocketPages awaits promises from `load`, the method handlers, `+api` handlers and
  `+middleware` (`await next()` included), and allows `await` in EJS templates (`<%- await include(...) %>` for an
  async partial) and in JSX/TSX components, async ones anywhere in the tree. Use the `.async` twin of a Go function
  (`await ctx.app.countRecords.async('orders')`) to wait outside the VM. Sync pages run exactly as before.
- `.ets` templates: EJS whose tags hold TypeScript. Types are stripped by the esbuild built into the binary, never
  checked, and a TypeScript syntax error names the template line. `.ets` works everywhere `.ejs` does (pages,
  layouts, error pages, partials), and an `include` name without an extension tries `.ets` first.
- A `tsx` starter kit, for apps written as TSX components.

### Changed

- The docs, starter kits and example app use TypeScript throughout: `.ts` code and `.ets` templates, with TSX
  as the component alternative (one style per app). A new File types page lists both styles and the plain-JavaScript
  twins (`.js`, `.ejs`, `.jsx`), which keep working unchanged.

### Fixed

- In a `.jsx`/`.tsx` template, an empty slot is now falsy, so `slot('title') || 'fallback'` falls back (it always
  printed the empty slot).
- `ctx.pb()`'s `getFullList` returns the records. It returned them nested in an array (`[[...]]`, or one list per
  batch), so lists rendered blank. Every SDK response is now plain JS data.

## 0.1.0 (2026-11-17)

### Fixed

- A long-running app no longer runs out of JS memory serving pages. Each page request stayed in the VM (its `rt` object and about 55 handles), so after a few thousand renders every page failed with `InternalError: out of memory` until the app restarted. Requests now free what they leave behind when they end.

### Changed

- The PocketPages docs and blog moved from pocketpages.dev into this folder (`site/`, `blog/`) and are now on
  pocketone.dev under [/docs/extensions/pages](https://pocketone.dev/docs/extensions/pages); the minimal starter links
  there.
- Compiled templates and modules are kept in the `pages.compiled` cache bucket (cache/v1) instead of a map that only grew: memory is capped (32 MiB per app) and old versions are evicted, and on a multisite host instances serving the same files share one compiled copy. Set its caps under `cache.buckets.pages.compiled` in `pocketone.yaml`.

### Upgrading

- Go API: `Pages.RenderTemplate` is gone. Its one user, the OAuth pages, moved to Go templates in ext/templates (templates/v1).

## PocketOne v0.0.1-alpha.6 (2026-11-16)

### Changed

- Runs on `ext/jsvm`. Templates can include each other at most 32 deep (was 64). A page that runs away now fails with the engine's "interrupted" or "out of memory" error.

## PocketOne v0.0.1-alpha.3 (2026-11-16)

### Upgrading

- A `+config.js` must export an object (`module.exports = { ... }`); the 0.x function form is now an error instead of being ignored.
- JS plugins that still use 0.x hooks (`onExtendContextApi`, `onRender`, `onResponse`, `handles`) are now an error that names the v1 hook to use (`extendContext`, `transform`).
- Move any `*.pb.js` / `*.pb.ts` file out of `pb_pages` into `pb_hooks`; in `pb_pages` it's now a startup error and isn't served.

### Changed

- A plugin name in `+config.js` is required first (a `_private/` file or package, such as the Datastar add-on as `'datastar'`), then a built-in.
- A bare `require('pkg')` looks in `_private/` folders first, then `node_modules` in `pb_pages`, then `pb_hooks/node_modules` and the app folder's `node_modules`; a miss lists the folders searched.
- A page renders for any HTTP method, as in 0.x.
- An `Authorization` header login wins over the `pb_auth` cookie, as in 0.x.
- Sign-in refuses accounts that need MFA.
- `pocketone pages create` and every starter README show the `superuser upsert` step before the first `serve`.
- The daisyui starters' Tailwind input is `-app.tailwind.css`, so it isn't served.
- The Datastar add-on follows the Datastar 1.0.5 SDK spec (`viewTransitionSelector`, `namespace`, signals from the query for DELETE) and has 0.x's helpers back.

### Fixed

- A circular `require` no longer crashes the process; it gets the exports built so far, as in Node.
- A new page is served after a build that still has errors, instead of 404 until restart.
- `__dirname` and `__filename` are absolute paths.
- `/.well-known/` is served again (ACME challenges, `security.txt`, app links).
- `include('nav.main')` finds `nav.main.ejs`, and 0.x's `include('/blog/_private/x')` form works.
- A layout that prints `<%- slot %>` shows the page instead of the slot function's source.
- A `+page.js` with no page beside it serves its folder's URL.
- `ctx.res.redirect(location, status)` takes a number and `ctx.res.cookie` takes `expires`.
- EJS templates get `locals`, as in EJS and 0.x.
