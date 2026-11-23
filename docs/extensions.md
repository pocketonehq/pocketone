# Create your own extension

A PocketOne extension is Go code compiled into your build. There are two kinds, by where they run:

- **App extensions** add to an app: Go routes, PocketBase hooks, collections, admin dashboard screens, a JS API for
  `pb_hooks`, contracts other extensions use. They run on every app: a standalone app, or each instance of a
  multisite host. Almost every extension is one.
- **Host extensions** add to a multisite host, the process that serves many instances: request gates (firewall, rate
  limits), TLS, middleware around every instance, instance sources, background jobs.

How a multisite host runs its instances (as isolates in its own process with [pocker](../ext/pocker/README.md), in containers with
[docker](../ext/docker/README.md), or both) is the host's `runtime/v1` contract. App extensions don't depend on it.

## App extensions: `extkit`

Write an extension once, against [`extkit`](../ext/extkit/extkit.go) (its own module,
`pocketone.dev/ext/extkit`; core has no part in it), and the same value runs on every app:

```go
type Extension struct{ cfg Config }

func New(cfg Config) *Extension { return &Extension{cfg: cfg} }

func (p *Extension) Name() string { return "hello" } // config key, `_hello_<table>` collections, pb_cache/hello, logs

func (p *Extension) Register(app core.App, cfg extkit.Config) error {
    c := p.cfg
    if err := cfg.Decode(&c); err != nil { // this app's settings over the defaults (json tags)
        return err
    }
    app.OnServe().BindFunc(func(se *core.ServeEvent) error { /* routes, UI extensions */ return se.Next() })
    return nil
}

// optional: a $hello global in the app's JS hooks
func (p *Extension) BindJS(app core.App, rt js.Runtime) { /* rt.Set("$hello", ...) */ }

// optional: its TypeScript declarations, added to each app's types.d.ts
func (p *Extension) TypeScript() string { return `declare var $hello: { greet(name: string): string };` }
```

- **Every app has the [standard set](../ext/standard/README.md)** (tokens, mcp, pitr, deploy and the rest). Add yours
  to it: `set.Add(myextension.New(...))`. On a standalone app, build the app with `set.NewApp(...)`, or build it
  yourself and call `set.Register(app, jsvm.Config{...})`. The stock binary does the second: `pocketbase.NewWithConfig`,
  then `set.Register` ([`examples/pocketone`](../examples/pocketone/binary/binary.go)). Without the set:
  `extkit.MustRegister(app, extensions...)`, and `extkit.BindJS(app, vm, extensions...)` in jsvm's `OnInit`.
- **On a multisite host** every instance gets the set, with that instance's `extensions.<name>` settings over your
  constructor's defaults. `enabled: false` there means `Register` and `BindJS` are never called for that instance.
  What runs inside an instance depends on its runtime: pocker registers the set in each isolate; a docker instance runs
  the `pocketone` binary in its container, so it has the stock set (your own extras need your own image); an instance
  on a stock PocketBase image runs none, and gets only the contracts the host provides for it
  ([Working together](#working-together-contracts)).
- **Routes:** an extension has none of its own. Its API is contracts, mounted for you under `/api/_c/<id>/`; design
  the contract in `ext/contracts` first ([Working together](#working-together-contracts)). Admin settings go through
  `settings/v1` (`settingsv1.Add`). Only paths a standard fixes stay at the root (`/.well-known/*`, `/mcp`). The
  conformance test fails any other route unless `conformance.Options.AllowRoutes` lists it with a reason.
- **Process-wide settings** come from a `pocketone.yaml` section your extension owns (`extkit.NewSection`).
- **State per app:** keep it in `app.Store()`. Something shared across apps (a pool, a client) closes through
  `extkit.Closer`. Data that must survive a restart goes in your own collections
  ([Your schema](#your-schema-migrations-and-table-names)); files you can rebuild go in `pb_cache`
  ([Disposable files](#disposable-files-pb_cache)). Never write into `pb_data` yourself, and never into the app's
  `pb_migrations`.
- **Name the command with `extkit.Command`:** when a message tells people what to run ("run 'pocketone login
  prod'"), build it from `extkit.Command` (default `pocketone`), never a literal. A build shipped under another name
  (PocketHost's, say) sets it once in `main`, or with `-ldflags "-X pocketone.dev/ext/extkit.Command=pockethost"`.
- [bulkmail](../ext/bulkmail/extension.go) is the reference implementation, and
  [`ext/standard/example`](../ext/standard/example/main.go) a complete program.

## Your schema: migrations and table names

An extension that stores records owns its collections and creates them with its own migrations, in Go, shipped in
its module. Never copy its schema into an app's `pb_migrations`.

```go
func (p *Extension) Migrations() []extkit.Migration {
    return []extkit.Migration{
        {Name: "0001_chats", Up: createChats, Down: dropChats},
        {Name: "0002_chats_title_index", Up: addTitleIndex, Down: dropTitleIndex},
    }
}

func createChats(txApp core.App) error {
    c := core.NewBaseCollection("_hello_chats")
    c.Fields.Add(&core.TextField{Name: "title"})
    return txApp.Save(c)
}
```

- **An extension with a `Migrations()` method is an `extkit.Migrator`.** extkit applies its pending migrations on every
  app it runs on (each isolate has its own database): at bootstrap, right after PocketBase's own system migrations,
  so its collections exist for every command, before the app's `pb_migrations` run at serve, and an app migration may
  refer to them.
- **Order:** within an extension, by `Name` (zero-pad the numbers: `0001_chats`); across extensions, in the order they
  register, which puts a contract's provider before the extensions that require it.
- **Each migration runs in a transaction**, like PocketBase's: one that fails leaves nothing behind and stops the ones
  after it, and the app doesn't start.
- **History** is in `_extension_migrations` (extension, name, applied), apart from the app's `_migrations`, so the
  app's `migrate down` and `migrate history-sync` never touch your tables, and turning your extension off keeps its
  history. `pocketone extensions migrations` lists every extension's migrations; `pocketone extensions migrate down
  <extension> [n]` reverts the last n (`0`: all). A migration with no `Down` can't be reverted.
- **Never edit a migration that has shipped.** Change the schema with a new one. To rename a collection, use
  `extkit.RenameCollection(name, id, from, to)`: it renames by id (keep the id, so code that finds it by id keeps
  working), works on system collections, and reverts.
- **They build into the binary**, so there are no files to copy into the app. A migration that needs a file (a SQL
  script, a JSON schema) embeds it with `//go:embed` and reads it in `Up`.

**Table names:** every collection your extension creates is named **`_<name>_<table>`**: your extension's name with
hyphens as underscores, then the table: `_bulkmail_campaigns`, `_api_tokens_oauth_clients`, `_llm_chat_messages` (for
llm-chat). The leading underscore keeps them apart from the app's own collections, and the name says which extension
owns a table. The conformance test fails any other name unless `conformance.Options.AllowCollections` lists it with a
reason (a collection the app's users are meant to see and edit, say). When one extension's prefix starts with
another's (`llm`'s `_llm_` and `llm-chat`'s `_llm_chat_`), the shorter one never names a table under the longer
one's prefix; `go run ./.github/extensions check` refuses it.

The conformance test checks a Migrator's migrations too: they apply at bootstrap, applying again does nothing,
reverting all of them changes the schema, and applying them again brings back exactly what they made.

## Disposable files: `pb_cache`

Files your extension can rebuild (a download, a computed index, a model's saved state) go in **`pb_cache/<name>/`**,
next to `pb_data`. Get the folder from extkit, never build the path:

```go
dir, err := extkit.CacheDir(app, p.Name())       // this app's pb_cache/hello, created
shared, err := extkit.HostCacheDir(app, p.Name()) // one folder for every app of the process (a shared store)
```

- **Anything in it may be deleted at any time** (`pocketone cache clear`, an admin, a cleanup), and your extension
  carries on: it rebuilds, re-downloads or re-reads what it needs. If losing a file loses data, it doesn't belong
  here: put it in a collection.
- **Never backed up, deployed or synced:** it's outside `pb_data`, so backups skip it, and `deploy` and `dev` leave it
  out like `pb_data`. Apps add `pb_cache/` to their `.gitignore`.
- **Per app:** each app has its own, and one instance never sees another's. On a multisite host an instance's
  `pb_cache` is outside its folder, in the host's `pb_cache/instances/<instance id>/` (`CacheDir` returns it), so
  copies, archives and moves of the instance folder never carry it. `HostCacheDir` is for caches that serve every
  instance at once; it is the host's own `pb_cache/<name>/` (a single app is its own host). Never put one instance's
  data there.
- **Keep it bounded.** Give your cache a size budget (a setting with a sensible default) and evict past it; on a host,
  every instance's cache shares the host's disk.
- `pocketone cache` shows each folder and its size; say in your README what yours holds and whether it may grow large.

## Working together: contracts

> Start with [How PocketOne fits together](contracts.md) for the idea: every API an extension offers is a named,
> versioned contract, any extension can provide one, and everything else (the dashboard, the CLI, agents, `pb_hooks`,
> other extensions) calls the contract, never the extension. This section is how to do it in code.

In short, when you design an extension:

1. **Find the contracts you need** in the [catalogue](../ext/contracts/README.md). Use them; don't reach into another
   extension.
2. **Decide what you offer.** Anything a client calls is a contract. If one already promises it, provide that one
   (your extension then stands in for the current provider). If not, write the contract first, named for what it
   does, not for your extension.
3. **Declare it** in `Contracts()` and **provide it** in `Register`. The startup lines, whoami and the catalogue show
   it from then on.

Extensions never depend on each other by name. They depend on **contracts**: versioned Go interfaces in
[`ext/contracts`](../ext/contracts/README.md), such as `deploy/v1` or `mail.fallback/v1`. Every API an extension
offers is a contract too, served at `/api/_c/<id>/`: extensions have no routes of their own.

Every contract, standard or community, lives in `ext/contracts/<id path>/vN/` with a README like an extension's
(owners and a one-line summary in its front matter; `## Use`, `## Provide`, `## Extensions`, `## License`). Its
Extensions section is generated from every extension's `Contracts()`: who provides it, requires it and works with
it (`go run ./.github/extensions contracts`; CI fails when it's stale or when an extension names a contract that
doesn't exist). Whoever first creates a contract owns it, so an extension author who adds one owns both. Design the
contract first, then the extension that provides it.

An extension declares what it implements, requires and works with:

```go
func (e *Extension) Contracts() extkit.Contracts {
    return extkit.Contracts{
        Implements: extkit.IDs("deploy/v1"),
        Defaults:   extkit.IDs("deploy/v1"), // steps aside if another extension provides it
        WorksWith:  extkit.IDs("api-tokens/v1"), // used when present
    }
}

func (e *Extension) Register(app core.App, cfg extkit.Config) error {
    deployv1.Contract.ProvideDefault(app, myDeployer{})
    return nil
}
```

At startup PocketOne resolves every contract and prints who provides it:

```
standard: deploy/v1 ← fastdeploy (default deploy stepped aside)
standard: bulkmail off: needs mail.fallback/v1, which nothing provides
```

- **Look contracts up after registration** (in hooks, at serve time): `deployv1.Contract.Must(app)`. A lookup inside
  `Register` panics.
- **Defaults, conflicts, choices.** A default steps aside for any other provider. Two other providers of a contract that
  allows one fail the start until `pocketone.yaml` picks one (`providers: { mail.fallback/v1: mailrelay }`).
- **Wrappers** decorate whatever provides a contract (`Contract.Wrap`), in registration order.
- **Wire contracts** have an HTTP API at `/api/_c/<id>/<op>`, action hooks in Go (`deployv1.OnUpload`) and JS
  (`onDeployUpload`), an MCP tool per operation and a client. Every caller passes the same scope check and hooks.
- **From pb_hooks:** an app's JS declares and provides its contracts in `onContracts`, which runs once after
  `pb_hooks` load and before the seal (so before bootstrap):

  ```js
  onContracts((e) => {
    e.provide("mail.fallback/v1", { send(m) { /* ... */ } }) // a contract open to JS
    e.require("restore/v1")   // the start fails (dev mode: warns) when nothing provides it
    e.worksWith("api-tokens/v1")  // shown in the startup lines
    e.next()
  })
  ```

  The startup lines show it (`pb_hooks: provides mail.fallback/v1; requires restore/v1`). Methods run on pooled
  runtimes like any hook handler, so they can't use the file's local variables. A Go extension that *requires* a
  contract only JS provides is resolved before the JS runs: name `pb_hooks` under `providers:` for it in
  `pocketone.yaml`. `types.d.ts` types both sides from the contract's Go interface: `e.provide` checks the object
  against the contract (`contracts.Providers`), and each action hook's `e.input` has its operation's input type.
- **CLI:** every operation of a wire contract is a `pocketone remote ...` command with no code (`remote restore to
  --at -2h`), flags from its input. Provide [cli.commands/v1](../ext/contracts/cli/commands/v1/README.md) to replace
  one with your own, or to add one (`remote rehash`).
- **No imports between extensions.** An extension imports `extkit` and contract packages, never another extension's
  module (tests and examples may). What it needs from another goes in the contract package: api-tokens/v1 has
  `RegisterScope`, `FromRequest` and `WithScopes`; remote/v1 has its CLI client (`remoteclient`), which deploy's
  commands use.

## Compiled in, not loaded at run time

Extensions are Go packages linked into the binary, the way Caddy modules work. Go's `plugin` package (`.so` files)
isn't used:
- it needs identical toolchain and dependency versions;
- it can't unload;
- it rules out static `CGO_ENABLED=0` builds.

A build is a small `main.go`: the standard set with your extensions added, as a standalone app or a multisite host.

## Your module

PocketOne keeps PocketBase's module path, so upstream merges stay clean. Point it at PocketOne in your `go.mod`:

```
require ganigeorgiev.com/pocketbase/pocketbase v0.41.1

replace ganigeorgiev.com/pocketbase/pocketbase => pocketone.dev <commit or version>
```

Go only honors `replace` in the main module, so the final binary's `go.mod` needs this line, not only your
extension's.

## Admin screens: `UIExtensions`

The dashboard loads extensions before it starts its router:
- Each extension is a named file system served at `/_/extensions/<name>/`.
- Every extension's `main.js` is bundled into `/_/extensions.js`.

Add yours in the same `OnServe` hook:

```go
//go:embed ui
var uiFiles embed.FS

ui, _ := fs.Sub(uiFiles, "ui")
se.UIExtensions = append(se.UIExtensions, core.UIExtension{Name: "hello", FS: ui})
```

`ui/main.js` runs in the dashboard with its UI kit (`t.*`) and globals (`app.routes`, `app.store`, `app.pb`):

```js
// a new page, with a link in the header
app.routes.superuserOnly("#/hello", async () => {
    const res = await app.pb.send("/api/hello");
    return t.div({ className: "page" },
        t.div({ className: "page-content" }, t.h1({}, "Hello"), t.p({}, `Instance ${res.instance}`)),
    );
});
app.store.headerLinks.push({ href: "#/hello", icon: "ri-emotion-happy-line", label: "Hello" });

// add to an existing screen: screens marked with pbEvent fire mount:<name> with their element
document.addEventListener("mount:pageBackupsSettings", (e) => {
    e.detail.prepend(t.p({ className: "txt-hint" }, "Hello from an extension."));
});
```

- **More hooks:** `app.store.settingsNavGroups` adds entries to the Settings sidebar. Search `pbEvent:` in
  [`ui/src`](../ui/src) for every screen you can add to (Backups exposes `pageBackupsSettings`, `backupsList`,
  `backupsSettingsForm`, `backupCreateModal` and `backupRestoreModal`).
- **No build step:** plain JS works. Bundle with Vite if you prefer.
- **Experimental upstream:** this UI extension API is undocumented and can change between PocketBase releases
  (`ui/README.md`). Keep `main.js` small, and test it after each upstream update.

## Host extensions

A multisite host is one process serving many instances. A host extension works on that process, for every instance.
Like app extensions, host extensions work together through contracts, on the host's own scope:

| Concern | Contract | Providers |
|---|---|---|
| What runs the instances (one or several, in preference order) | [runtime/v1](../ext/contracts/runtime/v1/README.md) | [pocker](../ext/pocker), [docker](../ext/docker) |
| Which instances exist (the instance record) | [instance.source/v1](../ext/contracts/instance/source/v1/README.md) | `instances.yaml` (default), [mothership](../ext/mothership) |
| Admit requests (firewall, rate limits) | [gate/v1](../ext/contracts/gate/v1/README.md) | [firewall](../ext/firewall) |
| TLS for the host's listener | [tls/v1](../ext/contracts/tls/v1/README.md) | [autotls](../ext/autotls) |
| Middleware around everything | [http.wrap/v1](../ext/contracts/http/wrap/v1/README.md) | [servertiming](../ext/servertiming) |

A host extension is a `multisite.Extension`: `Name()` and `RegisterHost(h)`, which provides its contracts on
`h.Scope` and provides nothing when it isn't configured (no section). Pass it in `multisite.Options.Extensions` (in
preference order), or `multisite.Link` it from an `init` so linking its package is enough. The host seals its scope
once, with `providers:` choices for host contracts, and logs who provides what:

```go
h, err := multisite.FromConfig(multisite.Options{Choices: providers, Extensions: []multisite.Extension{
	servertiming.New(),         // http.wrap/v1, outermost
	autotls.New(),              // tls/v1 from the `autotls` section
	&pocker.Provider{},         // runtime/v1
	&docker.Provider{},         // runtime/v1 too, when there's a `docker` section
}})
```

```
multisite: runtime/v1 ← pocker, docker
multisite: tls/v1 ← autotls
```

Background work goes on `h.Background`, and an extension with `Close(ctx) error` is closed once every app has
stopped.

Some contracts are provided by the host for its instances (host-provided contracts), because the instance can't
answer them itself: docker runs stock PocketBase images, so the host answers remote/v1 for them. The host serves them
in `h.InstanceRoutes`, which run after the peer check, the blocked answer and the gates (firewall, rate limits) and
before the runtime, with the resolved instance in `multisite.InstanceOf(r)`:

```go
remotev1.Contract.ProvideForInstances(h.Scope, myRemote)
h.InstanceRoutes = append(h.InstanceRoutes, func(next http.Handler) http.Handler {
	return remotev1.W.HostHandler(h.Scope, multisite.InstanceID, myAuth, next)
})
``` Other servers in the fleet are [edge](../ext/edge) (`edge.EdgeSource`, the host's `Forward`), and
runtime-only hooks are pocker's `Starter`, `SetupApp`, `OnStart`, `OnEvent` and `Manager`
([POCKER.md](../ext/pocker/POCKER.md#extensions)).

App extensions that only need settings shared by every instance (mailrelay, s3-fallback, pitr) read their own
`pocketone.yaml` section on first use and stay app extensions: they work on any host and on a single app.

## Rules on a multisite host

On a multisite host, instances can belong to people who don't trust each other, and your Go code runs with the host's
full power, outside the JS sandbox:
- **Act only for the app you were called for.** Keep per-app state in `app.Store()`, and use `extkit.AppID(app)` for
  anything shared that must differ per instance (a bucket prefix, a rate-limit key); never build a path from request
  input.
- **Instance data needs instance auth.** Protect routes that read or change it with PocketBase's auth middlewares
  (`apis.RequireSuperuserAuth()`, record auth).
- **Exposing something to JS is a sandbox decision.** Expose a function to an instance's JS only if it's safe for a
  hostile tenant to call. See [How is this safe?](../ext/sandbox/README.md#how-is-this-safe).
- **Outbound calls follow the egress policy.** Go's default HTTP client already refuses private addresses. If your
  extension must reach a private service, give it its own `http.Client` and transport, and never let instance input pick
  the address.

## Contributing an extension

Extensions are welcome, from us and from you. Open a pull request that adds `ext/<your-extension>/`.

### Change PocketBase's core as little as possible

PocketOne stays close to upstream so every PocketBase release merges cleanly. If your extension needs something the
core doesn't offer yet:
- **Add the smallest hook that makes it possible:** an extension point or an override, not your extension's logic. A
  nil-checked function field, a list callers can append to, or an event, with upstream behavior unchanged when nothing
  is registered.
- **Make it generic.** Name it for what it allows ("storage backend for an app"), not for your extension. Assume someone
  else's extension will use it too, and that we'll offer it to upstream PocketBase.
- **Mark every changed line** in PocketBase's own files with `// PocketOne:` and a few words on why, so the next upstream
  merge knows what to keep.
- **Keep it separate:** the core change goes in its own commit, with a test, ahead of the extension.
- **Prefer what exists:** jsvm `OnInit`, `OnServe`, the record and request hooks, `BaseAppConfig.DBConnect`,
  `UIExtensions`. Many extensions need no core change at all.

### What an extension folder contains

```
ext/<name>/
  go.mod            module pocketone.dev/ext/<name>
  README.md         the extension's docs, and its page on pocketone.dev (below)
  CHANGELOG.md      its changes, newest first (see Changelogs)
  TODO.md           optional: work that waits for something else to exist first (see Blocked work)
  <name>.go         the extension
  <name>_test.go
  conformance_test.go  the shared extension test (below)
  modes_test.go     the feature on the real binary, in every mode it runs in (below)
  example/main.go   a minimal program that builds cleanly and shows the feature
  ui/               optional: admin screens (embedded main.js)
  docs/             optional: end-user docs (tutorials, guides), as pages (below)
  blog/             optional: posts for the pocketone.dev blog
```

- **`example/main.go` is required.** Keep it as small as possible: just enough to build a binary that shows the
  feature working, with no extra harness. `go build ./example` and `go vet ./...` must pass with no warnings.
  [`ext/standard/example`](../ext/standard/example) and [`ext/sandbox/example`](../ext/sandbox/example) are the size to aim for.
- **Examples come with two logins:** superuser `admin@example.com` and user `user@example.com` (verified, in
  `users`), both with password `password`. Every `example/main.go` calls
  `devusers.Register(app)` right after the app is created (on a multisite host, `devusers.Ensure(app)` in the
  host's per-instance setup), from [`ext/devusers`](../ext/devusers/devusers.go). A fresh data dir is then ready to log
  in to, with no installer page and no CLI step. Never register it in a production build.
- **`conformance_test.go` is required.** It runs the shared extension test from
  [`ext/extkit/conformance`](../ext/extkit/conformance/conformance.go) once:

  ```go
  func TestConformance(t *testing.T) {
  	conformance.Run(t, func() extkit.Extension { return myextension.New(myextension.Config{}) })
  }
  ```

  It checks that the extension registers, migrates, serves and shuts down on a fresh app with no goroutines left
  behind; that its only routes are contracts' (`/api/_c/<id>/`); that PocketBase's own collections are unchanged and every collection it adds is named `_<name>` or `_<name>_…`; and
  that one extension value serves several apps at once (as on a multisite host) without registering on one app
  touching another's hooks. An extension that requires another (`Contracts().Requires`) lists it in
  `conformance.Options{Deps: ...}`. An extension that adds MCP tools ([`ext/mcp`](../ext/mcp/README.md#tools-from-an-extension)) also passes
  `conformance.Options{Setup: ...}` that registers MCP on the test apps; the test then checks each tool is named
  `<name>_<verb>_<noun>`, is described, and has an object input schema (`pb_` is reserved). Run it with `go test -race`. It's the minimum every extension passes; your own tests cover the
  feature.
- **`modes_test.go` tests the feature in every mode it runs in**, on the real `pocketone` binary, with
  [`ext/extkit/modetest`](../ext/extkit/modetest/modetest.go). Declare the instances the test needs, each configured
  in full (files, extension settings, secrets), and the harness lays them out once per mode: `Single` (each its own
  `pocketone serve`), `Host` (one multisite host serving them all) and `Fleet` (several hosts that forward to each
  other). An app extension runs `modetest.AllApp` (Single and Host); a host extension picks Host and/or Fleet.

  ```go
  func TestModes(t *testing.T) {
  	top := modetest.Topology{Instances: []modetest.Inst{{ID: "app", Extensions: map[string]any{"myextension": map[string]any{"limit": 5}}}}}
  	modetest.Run(t, top, modetest.AllApp, func(t *testing.T, w *modetest.World) {
  		s := w.Instance("app")
  		s.MustJSON(http.MethodGet, "/api/_c/myextension/v1", s.Token(), nil, 200, nil)
  	})
  }
  ```

  The same test body runs in every mode: each instance has the URL `http://<id>.localhost:<port>` in all of them, and
  `World.Update`, `World.Add`, `World.Restart` and `Site.WaitRestarted` do what each mode does (a host reloads its
  instances file; a single app restarts its process). Where a mode really behaves differently, check `w.Mode` and say
  why in a comment. `-short` skips mode tests.
- **The README starts with front matter**: who owns the extension and its version (see [Ownership](#ownership)), and
  its entry in the [catalogue on pocketone.dev](https://pocketone.dev/docs/extensions): its `title`, its `group` (one
  of the ids in [`site/extensions.json`](../site/extensions.json): `binary`, `many`, `services`, `saas`, `authors`) and
  a one-line `summary`. Nothing outside your folder lists your extension: the site finds it there.

  ```markdown
  ---
  owners: ["@your-github-handle"]
  version: 0.1.0
  title: "My Extension"
  group: services
  summary: "One line: what it does for someone running an app."
  ---
  ```

- **The README has five required sections**, in this order. It is also your extension's page on pocketone.dev.
  - `## Install`: how to get it (built into `pocketone`, or the lines to add to a Go build).
  - `## Configure`: the `pocketone.yaml` section, flags and settings screens. The site adds a table of your section's
    keys, generated from your `extkit.NewSection` defaults; describe each key with a `doc:"…"` struct tag.
  - `## Use`: day to day: the admin screens and what to click, the commands.
  - `## Integrations`: the extensions yours works with and how (multisite hosting goes here, not in the sections
    above), and how other extensions can build on yours (hooks, filesystem kinds, per-instance settings, Go
    and REST APIs). Hosting is a [Multisite](../ext/multisite/README.md) row: what an instance gets and its
    `extensions.<name>` settings. Pocker and Docker are two `runtime/v1` providers, not the host: name one only for
    what differs by runtime (isolates, which share the host's process; sleeping instances; and today per-instance settings and the
    host's extras, which only Pocker applies), never as the stand-in for "a multisite host".
  - `## Example`: the code that uses it. A sentence on what your `example/main.go` runs, then the whole file in a
    ```` ```go ```` block, exactly as it is; `go run ./.github/extensions examples` copies it in, and CI fails when the two
    differ. Without an example (an extension built into `pocketone`), say where to see it used.

  Write the first three for someone running one PocketOne app, not for a Go developer. Anything else (how it works,
  limits) goes in sections after these, and extra guides can sit next to the README as other `.md` files.
- **Your README is your extension's reference; end-user docs go in `docs/`.** The README is your extension's page in
  the Extensions reference. Tutorials, guides and recipes, written for someone learning to use it, go in an optional
  `docs/` folder of [Pages](/docs/extensions/pages) pages beside it, which pocketone.dev serves under your
  extension's URL: `docs/ctx/req.md` is `/docs/extensions/<name>/ctx/req`. [`ext/pages/docs`](../ext/pages/docs)
  (Pages' docs) and [`ext/wasm/docs`](../ext/wasm/docs) are the examples.
  - `docs/index.md` is required: the docs' landing page, at `/docs/extensions/<name>`. Its front matter names the main
    nav section that links it, one of the `# Section` headings of the main docs nav: the subject your extension is about
    (`Database`, `Users and sign-in`, `Hooks and routes`, `Websites`, `Email`, `AI`, `Go`, `Deploy and run`), and the
    link's text is its `title`:

    ```yaml
    ---
    title: WASM
    section: Hooks and routes
    description: Bring your own WASM ...
    ---
    ```

  - Any other page can be listed in the main nav too, the same way. A guide that gets something done across features
    (start to finish, not just with your extension) goes under `How-to guides`, with `nav:` as the link's text when the
    page's title doesn't say the outcome; guides about your extension alone stay in your own nav:

    ```yaml
    ---
    title: 'Speedrun: Google OAuth2'
    section: How-to guides
    nav: "Let users sign in with Google"
    ---
    ```

    Your README moves to `/docs/extensions/<name>/extension`, where the Extensions reference links it.
  - `docs/_private/toc.md` is its nav: `# Section` headings and `- [Title](page.md)` links. Your docs pages show it as
    their sidebar, in place of the main docs nav, under a link back and a link to `index.md`. The README and the guides
    beside it aren't in it: they're in the Extensions reference.
  - Pages are templates: EJS runs in `.md` files as in any PocketOne app, so a code sample writes `<%%` for a
    literal `<%`. Partials go in `docs/_private/`; the site's own (`browser`, for one) are found too.
  - Link the way GitHub does, relative to the file (`ctx/req.md`, `../routing.md`, `../README.md`, `shot.png` beside
    the page); the site turns them into site URLs, and a link to a file that doesn't exist fails the build.
  - Content only: no `+` files (layouts, `+config.js`, middleware, API routes).
  - `blog/*.md` files are posts on the pocketone.dev blog at `/blog/<file name>`, with the same front matter as the
    blog's own (title, description, date, author, tags) and links relative to the file.
- **Examples are TypeScript page code and Go.** Show server code as [Pages](/docs/extensions/pages) page code in
  TypeScript (endpoints and page code in `.ts` files, modules in `_private/`, and an `.ets` template when the example needs a page), and the
  same thing in Go when an app's Go code can do it. Not `pb_hooks`, and not JavaScript: pages give routes a file
  structure people can read, and TypeScript carries the types.
  - **In `docs/`**, put the versions of one example in consecutive fenced blocks (```` ```ts ````, ```` ```go ````, and
    ```` ```ets ```` when it needs a template), nothing between. pocketone.dev shows them as tabs (TS | Go | ETS), one at
    a time. TS and ETS are the same choice, TypeScript, against Go: a group has one or the other, never both (a
    page's `.ts` next to the `.ets` template it feeds shows both blocks), and picking any tab switches every group on every
    page to that side and remembers it. On GitHub they're just consecutive blocks.
  - **In the README**, keep samples inline: when you show the Go version too, put a line of text before it ("In Go:")
    so the two don't become tabs and the README reads as plain Markdown.
- **Extensions here are MIT.** The README ends with `## License` and the single line `MIT`. An extension under another
  license lives in its author's own repository, not here.
  [`ext/pitr/README.md`](../ext/pitr/README.md) is the example to follow.
- **Your pull request touches only your folder** (and, for a new contract, its folder in `ext/contracts`). Don't commit
  generated files: the site pages, nav, search index, `llms.txt` and contract indexes are rebuilt on `main` after your
  change merges ([`.github/regenerate.sh`](../.github/regenerate.sh)). To preview your page, run
  `cd site/sync && go run .` and don't commit what it writes outside your folder.
- **CI enforces all of this.** [`.github/workflows/extensions.yml`](../.github/workflows/extensions.yml) fails when an extension
  README is missing its owners, version, title, group or summary, one of the five sections or its MIT `## License`, or its Example doesn't match `example/main.go`, when it has no
  `CHANGELOG.md`, or when a module that implements `extkit.Extension` has no conformance
  test, then builds, vets and runs `go test -race` on every module the
  change affects: the modules it touches, every module that reaches one of them through a `replace`, or all of them
  for a core change. [`.github/workflows/site.yml`](../.github/workflows/site.yml) checks that the site builds from
  your README. Run everything CI runs on your pull request, locally, from the repository root:

  ```bash
  go run ./.github/extensions test            # what you changed since origin/main, committed or not
  go run ./.github/extensions test ext/<name> # or name the modules
  ```

  When it ends with `ok`, CI passes too.

  Tests that need an external tool (MinIO, Docker) skip themselves when it isn't installed, so they don't block CI.
- **Dependencies stay in your module.** Heavy libraries go in your extension's `go.mod`, never in core's.
- **Standalone first.** Where it makes sense, an extension works on one standalone app, and adds more on a multisite
  host.
- **MIT**, like PocketBase and the rest of this repo.

### Changelogs

Each extension keeps its changelog in its own folder, `ext/<name>/CHANGELOG.md`, so a change to it touches nothing
else. The root [CHANGELOG.md](../CHANGELOG.md) is for the rest: core, the `pocketone` binary, the site.

- **A user-visible change adds its line under `## Unreleased`** in the same commit, grouped under `### Upgrading`
  (steps for anything breaking), `### Added`, `### Changed` or `### Fixed`. One line per change, written for someone
  running an app, with the command, flag or config key.
- **Don't raise `version:` yourself.** Each PocketOne release raises the version of every extension with lines under
  its `## Unreleased`: minor when it has `### Upgrading` lines, otherwise patch. It renames that section to the new
  version and lists the extension in the release's notes, linked to its changes
  (`go run ./.github/extensions release vX.Y.Z`).

```markdown
# My Extension changelog

## Unreleased

### Added

- `myext.retries` in `pocketone.yaml`: how many times to retry a failed delivery (default 3).
```

### Blocked work

An extension may keep a `TODO.md` in its folder: the work on it that waits for something else to exist first (a
contract, another extension's change, a host feature). Each item says what it waits on, under a `**Waits on:**` line,
and what to do once that lands; delete the item when it's done. Work that isn't blocked doesn't go here: do it, or
leave it out. So a look across `ext/*/TODO.md` shows what each missing piece would unblock. The file isn't published:
pocketone.dev skips it.

### Ownership

Every extension and every contract has owners, listed in its README's front matter (GitHub handles); an extension
also has a version there. Whoever submits an extension or first creates a contract owns it, so an author who adds a
contract with their extension owns both.

Who approves an outside pull request:

| What it changes | Who approves |
|---|---|
| PocketBase's own code, the standard extensions, the site, docs, examples, CI | a maintainer |
| A community extension | one of its owners, or a maintainer |
| A contract | one of its owners, or a maintainer |
| A new extension or a new contract | a maintainer |

Owners are read from the base branch, never from the pull request, so a pull request can't make its author an owner
and approve itself. Nothing merges unless the checks pass. An extension's version goes up when PocketOne releases
with changes to it ([Changelogs](#changelogs)), and its Go module tag (`ext/<name>/vX.Y.Z`) follows that version. A
planned extension (a README and no code yet) stays at `0.0.0`.

```bash
go run ./.github/extensions approvers origin/main HEAD   # who may approve each area a change touches
```

## Planned extensions

Our own extensions live in this repo under [`ext/`](../ext), each as its own Go module (`pocketone.dev/ext/<name>`). See [ext/README.md](../ext/README.md).
