# JSVM changelog

Changes to `ext/jsvm`, newest first. A user-visible change adds its line under `## Unreleased` in the same
commit. Each PocketOne release raises this extension's `version:` when there's something under `## Unreleased` and
renames the section to that version ([Changelogs](../../docs/extensions.md#changelogs)).
Sections named after a PocketOne release are from before extensions had versions of their own.

## Unreleased

### Added

- Timers: `setTimeout`, `setInterval`, `clearTimeout`, `clearInterval` and `queueMicrotask`, as in browsers and Node.js. Each callback runs as its own task with its own time limits; a thrown error is logged. A timer set while serving a request is cleared once the request is answered or its client goes away; one set at the top of a hook file runs until cleared. At most 1024 are pending at once (`VMConfig.MaxTimers`).

## 0.1.3 (2026-11-22)

### Changed

- An async handler (a route, a hook, a cron job) may now await for up to 15 minutes in all (`VMConfig.AwaitLimit`, `await_time` in `pocketone.yaml`'s `app: js_limits:`). Before, a request whose handler awaited `.async` calls (`sleep.async`, provider calls) for more than 30 seconds failed with a timeout while the VM sat idle. `TaskBudget` (JS time) and `TaskWall` (wall time) now bound each stretch of JS between awaits, as they already did; `WaitTimeout` bounds only the synchronous part.

### Fixed

- A superuser UI extension registered from `pb_hooks` the documented way (`$app.onServe().bindFunc((e) => { e.uiExtensions.push({ name, fs: $os.dirFS(dir) }); e.next() })`) works: `$os.dirFS` now stays a Go file system in JS instead of turning into a string, and a JS object holding Go values becomes a Go struct field by field, so `fs` keeps its value. Before, the app stopped at startup with `cannot unmarshal string into Go struct field UIExtension.fs`.

- A handler stops when its call is given up: past `AwaitLimit`, or when the request it serves ends because the client went away. Before, a request that timed out failed but its handler kept running with no one waiting. Now async calls it started are dropped when they finish (the code after its `await` never runs) and new ones throw. A detached promise still runs on after its handler returns.

## 0.1.2 (2026-11-21)

### Fixed

- An app no longer crashes with `fatal error: concurrent map iteration and map write` when a JS hook gets a value (a record, a collection list, ...) that another goroutine is writing. The JS memory limit measured each Go call's result by walking every map it could reach, including maps other goroutines own; now types with their own sizer are measured under their own locks, structs holding a mutex are charged their size, and maps behind a pointer are charged from their length. This crashed the PocketHost mothership on 2026-11-21.

## 0.1.1 (2026-11-20)

### Fixed

- Go constructors (`FormData`, `Record`, `Collection`, `DateTime`, the field types, ...) work with `instanceof`, as in
  goja: each has a `prototype` (with `constructor` pointing back), and `new X()` is an instance of `X`. Before,
  `value instanceof FormData` threw `TypeError: operand 'prototype' property is not an object`, which broke every
  `ctx.pb()` create and update in PocketPages (the JS SDK makes that check on every request with a body).

## 0.1.0 (2026-11-18)

### Upgrading

- `types.d.ts` moved from `pb_data/` to the project root, the folder holding `pb_hooks` (next to `pb_data` and `pocketone.yaml`), so a fresh checkout is typed before the app first runs. Delete the old `pb_data/types.d.ts`. Hook and migration files with `/// <reference path="../pb_data/types.d.ts" />` still point at the old place: change them to `../types.d.ts`, or drop the line if a `tsconfig.json` includes `types.d.ts`. Migrations that `migrate` and automigrate write still carry the old line (PocketBase's templates). If you kept your own `tsconfig.json`, change its `pb_data/types.d.ts` to `types.d.ts`. Commit `types.d.ts` and `tsconfig.json`. `Config.TypesDir` still overrides the folder.

### Added

- The app writes a `tsconfig.json` beside `types.d.ts` when there's none (it never overwrites one), including `types.d.ts`, `pb_hooks`, `pb_migrations` and `pb_pages` (JSX checked, `"jsx": "preserve"`), so editors and `tsc --noEmit` work without setup. With hooks embedded in the binary (ext/bundle) and no `TypesDir`, neither file is written.
- Extensions can change the app's finished `types.d.ts` with `FilterTypes(decl string) string` (`js.TypesFilter` in
  `ext/extkit/js`), run after every extension's `TypeScript()`. `ext/sandbox` uses it to declare what it hides `never`.

### Fixed

- A busy app no longer runs its JS VM out of memory. The VM's loop ran about one queued free (a JS value Go had dropped) per request, so under steady concurrent load the frees backed up, Go's finalizers stalled, and after about 25,000 pages at 50 concurrent requests every page and hook failed with `InternalError: out of memory` until the app restarted. The loop now runs the frees a request leaves before it takes the next one; a pages site served 150,000 requests in 30 seconds at a steady 130 MB.

## 0.0.2 (2026-11-17)

### Added

- Go API: `js.Release(v)` (`ext/extkit/js`) frees a JS value now instead of when Go collects its wrapper, for values a Go closure held by the VM can reach (otherwise a cycle neither GC frees). `(*jsvm.VM).GC()` runs a Go collection, the frees it posts and QuickJS's cycle collector, for tests and diagnostics.

### Fixed

- A `$app` database call at a hook file's top level, which fails because hook files load before the database opens (as in PocketBase), now says so and points to `onBootstrap`, instead of a bare nil pointer error.
- The README no longer says a call that waits lets other requests run as well without `.async`; under load a plain `sleep(100)` served 19 requests a second against 481 with `await sleep.async(100)`.

## PocketOne v0.0.1-alpha.6 (2026-11-16)

### Upgrading

- PocketBase's `plugins/jsvm` is upstream's again: `Config.Extensions`, `jsvm.Run` and `jsvm.ScriptPath` are gone from it. Use `ext/jsvm` (its extension methods and `js.Run`) instead. `ext/jserrors` is removed; `ext/jsvm` reports real file positions.
- Go code that binds JS takes `js.Runtime` (`github.com/pockethost/pocketone/ext/extkit/js`) instead of `*goja.Runtime`: `extkit.JSBinder.BindJS(app, rt)`, `extkit.BindJS(app, rt, ...)`, `extkit.AppSet.BindJS`, Pocker's `InitJS(inst, app, rt)`, `mcp.BindJS(app, rt)`, `tokens.BindJS(app, rt)`, `bulkmail.BindJS(app, rt)`. `extkit.JSContracts(app)` no longer takes a runner. `standard.Set.Register` takes an `ext/jsvm` config.

### Added

- `pb_data/types.d.ts` now covers PocketOne: generated from PocketOne's core (its extra hooks, e.g. `onFilesystemCreate`), with typed `.async` on every Go function, `require`, `process`, `Buffer`, `import.meta`, and the API of each extension on in the app (`$mcp`, `$tokens`, `$bulkmail`, `onContracts`). Extensions add theirs with a `TypeScript()` method.
- TypeScript: `.ts`, `.mts` and `.cts` hooks (`*.pb.ts`, `*.pb.mts`, `*.pb.cts`), migrations and modules run directly, with types stripped by esbuild and error positions in the `.ts` source. `.ts` and `.mts` are ES modules, `.cts` is CommonJS; `import "./x.js"` finds `x.ts`. No runtime type checking: use `tsc --noEmit` with `pb_data/types.d.ts`.
- ES modules: `pb_hooks/*.pb.mjs` hook files, `pb_migrations/*.mjs` migrations and `.mjs` modules use `import`/`export` with top-level `await` and `import.meta`. `*.pb.js` and the new `*.pb.cjs` stay CommonJS scripts with `require()`. `import` and `require()` share one resolver (relative paths, `node_modules`, `package.json` `exports` conditions) and one instance per module; `require()` of an ES module without top-level `await` returns its namespace, as in Node 22.
- `jsvm: goja` under `app:` in `pocketone.yaml`, or `--jsvm goja`, runs a single app on PocketBase's own JS engine for pure PocketBase compatibility (pages, `$mcp`, `$tokens`, `$bulkmail` and JS contracts are off; the startup lines say so). The default is `quickjs`.
- New `ext/jsvm` extension, PocketOne's JS engine: one QuickJS VM per app with real globals, async handlers and `.async` Go calls, and hard per-app memory and time limits. It runs PocketBase's JS API unchanged, and is pure Go (no cgo), so `CGO_ENABLED=0` builds and cross-compiles work as with stock PocketBase.

### Changed

- The `pocketone` binary and every Pocker instance run JS on `ext/jsvm` (QuickJS, one VM per app) instead of goja. Hooks, routes and migrations run as before; hook files now share one global scope, handlers can be `async`, and errors name `pb_hooks/<file>:<line>:<col>` on their own (`ext/jserrors` is no longer used). `--hooksPool` is accepted and ignored.

### Fixed

- A hook can no longer pin a CPU with an Array method on a huge sparse array or array-like (`new Array(1e9).join("x")`, `[].indexOf.call({ length: 2 ** 53 - 1 }, 1)`), which QuickJS ran without checking the time limit. Array methods that walk every index, and passing an array to a Go function, now throw a `RangeError` past `MemoryLimit / 16` elements (4,194,304 at the default 64 MiB), a length no dense array fits in that memory.
