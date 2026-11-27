---
owners: ["@benallfree"]
version: 0.1.4
title: "Pocker"
group: many
summary: "Hundreds of PocketBase instances in one Go process, routed by hostname."
---

# Pocker

> Walkthrough: [PocketBase isolates](https://pocketone.dev/docs/features/isolates). This page is the technical reference.

Many PocketOne instances in one process (like Docker, but for PocketOne), routed by hostname. Each instance is a
normal app folder and starts on its first request, in tens of milliseconds. Pocker provides
[runtime/v1](../contracts/runtime/v1/README.md) for [Multisite](../multisite/README.md); its peer [Docker](../docker/README.md) runs each instance in a container instead.

## Install

Pocker is built into `pocketone`. Add a `multisite` section to `pocketone.yaml` and `pocketone serve` hosts many
instances instead of one app, with pocker as the runtime (docker too, when it has its `docker` section):

```yaml
multisite: {}
```

In your own Go build, add it to the host's extensions ([`examples/pocketone/binary/multi.go`](../../examples/pocketone/binary/multi.go)
is the stock binary's version), or run a `pocker.Server` on its own as [the example](#example) does:

```go
h, err := multisite.FromConfig(multisite.Options{Resolve: cfg.ResolvePath, Extensions: []multisite.Extension{
	&pocker.Provider{
		Resolve:  cfg.ResolvePath,
		Standard: standard.New(), // every instance gets the standard set (tokens, mcp, pitr, ...)
		Extensions: []pocker.Extension{
			myextension.New(), // your own PocketOne extensions, on every instance too
		},
	},
}})
```

The module is `pocketone.dev/ext/pocker`.

## Configure

The `pocker` section sets where instances live, the JS pool, outbound network and the default limits for every
instance (keys below). Listeners, instances and the firewall are the [Multisite](../multisite/README.md) sections (`server`,
`instances`, `firewall`).

```yaml
multisite: {}
instances: { file: ./instances.yaml }
pocker:
  root: ./pb_root           # one folder per instance: <root>/<id>
  hooks_pool_size: 2
  egress_allow: []          # private networks instances may still reach
  max_js_execution: 30s     # default limits; each instance can override them
  max_js_allocation: 64MiB
  isolate_config:           # every instance's config, in pocketone.yaml's shape
    api-tokens: { enabled: false }
```

`isolate_config` holds one section per extension the instances run (the standard set's members and your own
extensions), each taking what an instance's `extensions.<name>` takes. It is every instance's base: an instance's own
`extensions.<name>` overrides it key by key, so one instance can turn back on what `isolate_config` turns off. A
section for an extension the instances don't run stops startup. The top-level sections of `pocketone.yaml` configure
a single app; on a multisite host they don't reach the instances.

An instance overrides any limit in its own `limits` (missing fields keep the default):

```yaml
instances:
  - id: shop
    hosts: [shop.example.com]
    limits: { max_js_execution: 60s, max_concurrent_js: 32 }
```

Every instance runs the PocketOne code in the binary; an instance's `version` is ignored.

## Use

### Try it with any hostname

```yaml
# pocketone.yaml
server: { http: "127.0.0.1:8090" }
multisite: {}
instances: { dev: true }
pocker: { root: ./pb_root }
```

```sh
pocketone serve
curl -H 'Host: blog.localhost' http://127.0.0.1:8090/api/health   # creates and serves ./pb_root/blog.localhost
```

In dev mode a hostname no instance maps becomes an instance named after it, its folder is made on first use, and it
runs in dev mode. In production only the hostnames in the instances file are served (see
[Multisite](../multisite/README.md#the-instances-file)).

### Each instance

- A normal app folder, `<root>/<id>`: `pb_data`, `pb_hooks`, `pb_migrations`, `pb_public`. Its dashboard is at
  `https://<its hostname>/_/`.
- Its JS hooks see only its own `secrets`, through `$os.getenv` and `process.env`.
- A change to its `secrets`, `encryption_key`, `access`, `limits`, `dev`, `dir`, `superusers`, `private`, `extensions` or `remote`
  restarts it if it's running (the app stops, then a fresh one starts, as in a process restart). A hostname change doesn't restart it.
- A change in `pb_hooks` restarts just that instance. A broken hooks file affects only that instance (below).

### Broken hook files

In dev mode, a `pb_hooks` file that fails to load is logged and skipped, and the rest of the instance runs, so you can
fix it live. Otherwise an instance doesn't start with a broken file.

When a running instance's `pb_hooks` change, Pocker restarts it as PocketBase's watcher restarts a process: the running
app stops (requests in flight are cut off), then a fresh app starts with the new files. Only one app has an instance's
folder open at a time, so migrations and bootstrap code never run beside a live app. A broken file in production
leaves the instance down: requests get 502 with the error, which also goes to the instance log, and the first request
after a fix starts it.

### Logs

The app's own logs (the dashboard's Logs) are per instance, in its `pb_data`. `console.*` from JS hooks goes to
`<instance>/logs/exec.log` as JSON lines (`message`, `stream`, `time`), rotated to `exec.log.1` at 10 MiB. A start
failure is written there too.

## Integrations

### Works with

| Extension | How they fit |
|---|---|
| [Multisite](../multisite/README.md) | Routing, listeners, instances file and gates. Pocker is one of its runtimes (runtime/v1). |
| [Sandbox](../sandbox/README.md) | Every instance's JS hooks, SQL and outbound network run in it, so one instance can't reach another or the host. |
| [Firewall](../firewall/README.md) | Peer allow-list and rate limits in front of every instance; per-instance limits in `extensions.firewall`. |
| [Auto TLS](../autotls/README.md) | In the stock binary: certificates for the hostnames instances map (`autotls` section). |
| [Server Timing](../servertiming/README.md) | In the stock binary: Server-Timing headers on every response. |
| [Mailtrap](../mailtrap/README.md) | In the stock binary: dev instances with SMTP off send mail to a local inbox. |
| [Edge](../edge/README.md) | Instances another edge runs are forwarded there instead of started here. |
| [Stats](../stats/README.md) | Measures disk use and last request for each instance under `root`. |
| PocketOne extensions | In your own build, any extension such as [PITR](../pitr/README.md), [S3 Fallback](../s3-fallback/README.md) or [Mail Relay](../mailrelay/README.md) runs on every instance, and an instance's `extensions.<name>` settings override the extension's defaults. `enabled: false` turns it off for that instance. |

### Building on Pocker

- **Standard set (required).** `pocker.Provider.Standard` (or `pocker.Server.Standard`, `ManagerOptions.Standard`) is
  [ext/standard](../standard/README.md)'s set, and Pocker won't start an instance without it: a server without one
  refuses to run, so no host can make an instance that lacks the standard extensions. Every instance gets its members, each turned off per instance with `extensions.<name>.enabled: false`, and its
  startup lines (`standard: on: ...`) are in the instance's own log. Listing a member in `Extensions` too is an error.
- **Instance files.** The host provides [instance.fs/v1](../contracts/instance/fs/v1/README.md) on its scope
  (`Manager.Files()`): host-side code asks it for an instance's folder, takes its writers' lock, and restarts it around
  a change (`WithInstanceStopped`, then `StartInstance`). It isn't host-provided to the instances: inside an instance,
  its own app's provider answers.
- **Instance tokens.** The host provides [instance.tokens/v1](../contracts/instance/tokens/v1/README.md) on its scope
  (`Manager.Tokens()`): host-side code checks an API token one of its instances issued, with that instance's own
  api-tokens/v1 (starting the instance if it isn't running). A host uses it to accept an instance's own tokens on the
  routes it answers for that instance, so `pocketone login` and `clone`, which sign in to the instance, work there.
- **Contracts the host provides.** `Server.Host` (or `Provider.Host`) is the host's contract scope. A contract the
  host provides for its instances steps aside inside each instance's app, and the host answers it on the instance's
  hostname in `Server.InstanceRoutes` (or `Provider.InstanceRoutes`, `ManagerOptions.InstanceRoutes`): after the
  gates and the blocked answer, before the app. `pocker.Remote(m, host)` is remote/v1 for Pocker instances (restart
  through the Manager, logs from `logs/exec.log`); add your own auth:

  ```go
  host := extkit.NewScope("multisite")
  srv.Host = host
  srv.OnStart = func(m *pocker.Manager) { remotev1.Contract.ProvideForInstances(host, pocker.Remote(m, host)) }
  srv.InstanceRoutes = append(srv.InstanceRoutes, func(next http.Handler) http.Handler {
  	return remotev1.W.HostHandler(host, multisite.InstanceID, myAuth, next)
  })
  ```

  deploy/v1 works the same way with `deploy.New(...)` as the handler: `deployv1.Contract.ProvideForInstances(host,
  ...)` and `extkit.HostRoutes(host, deployv1.Contract, multisite.InstanceID, next)` as an instance route.
- **Extensions.** `pocker.Provider.Extensions` (or `pocker.Server.Extensions`) takes any `extkit.Extension`, registered on each
  instance's app before it starts with that instance's `extensions.<name>` settings. Host-level code can implement
  `AppIniter`, `JSIniter`, `Starter`, or be a host extension (`multisite.Extension`, registered on the host like any
  other: servertiming, autotls). See
  [POCKER.md](POCKER.md#extensions).
- **Setup per instance.** `SetupApp(inst, app)` runs after migrations and before the app serves, e.g. to add a route
  checked against `inst.Private`.
- **Manager.** `OnStart` hands host code the `Manager`: `WithInstanceStopped(ctx, id, fn)` changes an instance's files
  while it's stopped (deploys, restores), `StartInstance`, `Running`, `InstanceDir`, `LogInstance`.
- **Restarts.** `app.Restart()` (a backup restore, PITR) restarts only that instance.

## Example

A Pocker build with one extension: a Go route and an admin dashboard page on every instance, in dev mode. Run it from
this folder with `go run ./example`, then open `http://blog.localhost:8090/_/` (login `admin@example.com` /
`password`).

```go
// Command pocker-extension is a Pocker build with one extension: a Go route and an admin dashboard page,
// added to every instance. See docs/extensions.md.
//
// Logins (ext/devusers, local testing only): dashboard admin@example.com / password, app user user@example.com / password.
package main

import (
	"context"
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"

	"ganigeorgiev.com/pocketbase/pocketbase/apis"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"pocketone.dev/ext/devusers"
	"pocketone.dev/ext/pocker"
	"pocketone.dev/ext/standard"
)

//go:embed ui
var uiFiles embed.FS

func main() {
	cfg := pocker.DefaultConfig()
	cfg.Dev = true // any hostname; drop this for production (instances.yaml required)

	ui, _ := fs.Sub(uiFiles, "ui")

	srv := &pocker.Server{
		Config:   cfg,
		Standard: standard.New(), // every instance gets the standard extensions (tokens, mcp, pages, pitr, ...)
		// Runs for every instance's app after its migrations, before it serves.
		SetupApp: func(inst pocker.Instance, app core.App) error {
			// local testing: every instance gets superuser admin@example.com and user user@example.com, password "password"
			if err := devusers.Ensure(app); err != nil {
				return err
			}
			app.OnServe().BindFunc(func(se *core.ServeEvent) error {
				// a Go route, superuser only
				se.Router.GET("/api/hello", func(e *core.RequestEvent) error {
					return e.JSON(http.StatusOK, map[string]string{"instance": inst.ID})
				}).Bind(apis.RequireSuperuserAuth())

				// an admin dashboard extension: ui/main.js is loaded by the dashboard before its router starts
				se.UIExtensions = append(se.UIExtensions, core.UIExtension{Name: "hello", FS: ui})
				return se.Next()
			})
			return nil
		},
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := srv.Run(ctx); err != nil {
		log.Fatal(err)
	}
}
```

## Untrusted instances

Pocker runs every instance's JS hooks in the [sandbox](../sandbox/README.md). For why that's safe enough to host code
from people you don't know, and where it isn't, see [How is this safe?](../sandbox/README.md#how-is-this-safe).
Memory is the main limit: only one-shot allocations are capped, so a hook that grows memory slowly affects the whole
process. [POCKER.md](POCKER.md#sandbox) lists what the sandbox covers.

## Reference

[POCKER.md](POCKER.md) is the full guide: production `pocketone.yaml`, every instance field, extension interfaces, the
sandbox, PocketBase changes and versions.

## License

MIT
