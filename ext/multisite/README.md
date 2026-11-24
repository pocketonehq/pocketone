---
owners: ["@benallfree"]
version: 0.1.1
title: "Multisite"
group: many
summary: "Many instances behind one listener, routed by hostname, run by one provider."
---

# Multisite

> Walkthrough: [PocketBase isolates](https://pocketone.dev/docs/features/isolates). This page is the technical reference.

Many PocketOne instances behind one listener, routed by hostname. Each request's hostname picks an instance, the
request passes the gates (such as the [firewall](../firewall/README.md)), and a runtime serves it:
[pocker](../pocker/README.md) runs instances as isolates in its own process, [docker](../docker/README.md) runs each in its own container.
A host runs one of them, and every instance runs on it. Everything a host plugs in is a host-scope contract: [runtime/v1](../contracts/runtime/v1/README.md),
[gate/v1](../contracts/gate/v1/README.md), [tls/v1](../contracts/tls/v1/README.md),
[http.wrap/v1](../contracts/http/wrap/v1/README.md) and [instance.source/v1](../contracts/instance/source/v1/README.md).

## Install

Multisite is built into `pocketone`. It takes over `serve` when `pocketone.yaml` has a `multisite` section; without
one, `pocketone serve` runs a single app as usual.

```yaml
multisite: {}       # pocker, unless there's a `docker` section (then docker)
```

In your own Go build, pass the host extensions you link (one runtime), build the host from the
config and run it. [`examples/pocketone/binary/multi.go`](../../examples/pocketone/binary/multi.go) is the stock binary's version:

```go
h, err := multisite.FromConfig(multisite.Options{Resolve: cfg.ResolvePath, Choices: providersSection,
	Extensions: []multisite.Extension{servertiming.New(), autotls.New(), &pocker.Provider{Resolve: cfg.ResolvePath}}})
if errors.Is(err, multisite.ErrNoProvider) {
	// no multisite section in pocketone.yaml: serve a single app instead
}
err = h.Run(ctx)
```

At startup the host logs who provides each host contract, e.g. `multisite: runtime/v1 ← pocker`. Registering two
runtimes is a conflict at startup unless `providers: { runtime/v1: docker }` picks one.

The module is `pocketone.dev/ext/multisite`.

## Configure

A host reads these `pocketone.yaml` sections:

```yaml
multisite: {}                        # host mode
server:
  https: ":443"                      # the main address
  http: ":80"                        # redirects to https (alone: serves plain HTTP)
  plain: [":8080"]                   # extra plain-HTTP addresses, no redirect (e.g. a private network)
  tls: { cert: cert.pem, key: key.pem }   # reloaded when the files change; or the autotls section
instances:
  file: ./instances.yaml             # where the instances come from
  dev: false                         # also serve unknown hostnames
pocker: { root: ./pb_root }          # a runtime's own section (pocker; docker only runs with its section)
firewall: { allow_cidrs: [10.0.0.0/8] }  # any gate's section turns that gate on
```

- `server` belongs to [Config](../config/README.md); `providers` is the contracts' section. With no `https` or `http`, the host listens on
  `127.0.0.1:8090`.
- The `instances` section is this extension's (keys below). In production the instances file is required; with
  `dev: true` it's optional.
- The `app` section is for a single app and is refused on a multisite host: each instance has its own folder under its
  runtime's root.

## Use

### The instances file

```yaml
instances:
  - id: blog                         # also the folder name: <root>/blog
    hosts: [blog.example.com, www.blog.example.com]
  - id: shop
    hosts: [shop.example.com]
    secrets: { STRIPE_KEY: sk_live_... }               # the app's environment, seen by its JS hooks
    encryption_key: 0123456789abcdef0123456789abcdef   # encrypts the app's stored settings
    access: { cors_origins: [https://shop.example.com] }
    limits: { max_js_execution: 60s }                  # over the runtime's defaults
    extensions: { firewall: { trusted_ips: [203.0.113.0/24] } }   # settings for extensions, by name
  - id: old
    hosts: [old.example.com]
    blocked: { status: 402, message: "Payment required" }
```

The file is checked for changes every 2 seconds; edits apply without restarting the host. Ids are lowercase
letters, digits, dots and dashes. Other fields: `version` and `runtime` (which release or runtime runs it), `dir`
(another folder), `dev`, `superusers` (pocker upserts them when the app starts), `private` (for host Go code, never seen by JS) and `remote`
(another server runs it).

### What a request gets

- A hostname no instance maps gets 404, and no folder is created. In dev mode it becomes a new instance named after
  the hostname, in dev mode.
- A `blocked` instance answers every request with its status and message.
- An instance with `remote` set is forwarded there after the gates (a plain proxy to the URL, or
  [Edge](../edge/README.md) for a fleet).
- Contracts the host provides for its instances (docker's remote/v1) answer after the gates and the blocked check,
  so the firewall, rate limits and a powered-off answer apply to them too.
- `GET /.well-known/health` on any hostname reports the host's status, how many instances it knows and how many are
  running.

### Switching runtime

Switching between pocker and docker is a config change (add or remove the `docker` section, or
`providers: { runtime/v1: docker }`): both keep instances in `<root>/<id>`. Stop the containers
first when leaving docker (they keep running across a host restart on purpose). Data only moves forward: an instance
started on a newer version than wrote its data is upgraded.

## Integrations

### Works with

| Extension | How they fit |
|---|---|
| [Pocker](../pocker/README.md) | runtime/v1: every instance runs in this process. |
| [Docker](../docker/README.md) | runtime/v1: each instance runs in its own container (with a `docker` section). |
| [Firewall](../firewall/README.md) | A gate: peer allow-list, client IPs and rate limits. On when the config has `firewall` or `rate_limits`. |
| [Edge](../edge/README.md) | Forwards requests for instances another edge runs (the `edges` section). |
| [Mothership](../mothership/README.md) | Its mirror is instance.source/v1 instead of the instances file, and lists the other edges. |
| [Auto TLS](../autotls/README.md) | Certificates for the main listener, issued for the hostnames instances map. |
| [Stats](../stats/README.md) | Measures disk use and last request for each instance. |
| [Server Timing](../servertiming/README.md) | Server-Timing headers for each step of a request. |

### Building on Host

- **Host extensions.** A `multisite.Extension` (`Name`, `RegisterHost(h)`) provides host-scope contracts on `h.Scope`:
  a runtime (`runtimev1.Contract.ProvideIn(h.Scope, rt)`), a gate, TLS, a wrapper, an instance source. It provides
  nothing when it isn't configured (no section). Pass it in `Options.Extensions`, or `multisite.Link` it from an
  `init` so linking the package is enough (the firewall does). By hand: `h.Use(exts...)`, then `h.Setup(choices)`.
- **Runtime.** `runtimev1.Runtime` (`Name`, `Serves`, `ServeInstance`, `Ready`), with optional `Prechecker`, `Syncer`,
  `Starter`, `Statser` and `Shutdowner`. Each instance runs on the runtime it names, else the first that serves it.
- **Instance source.** `sourcev1.Source` (`Snapshot`, `Watch`) feeds instances from anywhere, such as a control plane.
  The instances file (`YAMLSource`) is the default and steps aside for any other provider.
- **Gates.** A `gatev1.Gate` checks the peer (`AllowPeer`) and admits each request (`Admit`); `ClientIPResolver`
  passes the real client IP on to the app. Gates set on `h.Gates` directly run after the contract's.
- **Forwarding.** `multisite.Options.Forward` serves requests for instances with `remote` set (Edge fills it).
- **Instance routes.** `Host.InstanceRoutes` wrap the runtime for instances this host runs: they run after the
  peer check, the blocked answer and the gates, and before the runtime (never for forwarded instances).
  `multisite.InstanceOf(r)` is the resolved instance and `multisite.InstanceID` fits extkit's host handlers, so a
  contract the host provides for its instances goes here:

  ```go
  remotev1.Contract.ProvideForInstances(h.Scope, myRemote)
  h.InstanceRoutes = append(h.InstanceRoutes, func(next http.Handler) http.Handler {
  	return remotev1.W.HostHandler(h.Scope, multisite.InstanceID, myAuth, next)
  })
  ```
- **Listeners and wrappers.** tls/v1 (or `Host.TLS`) sets the listener's TLS, http.wrap/v1 providers wrap the whole
  handler (outside `Host.Wrappers`), and `Host.Background` runs jobs until shutdown.
- **Per-instance settings.** An extension reads its own `extensions.<name>` with `inst.ExtensionConfig(name)` and
  `multisite.DecodeExtensionConfig`.

## Example

A toy host: instances from a YAML file, served by a runtime that answers with the instance it resolved. Run it from
this folder with `go run ./example --instances instances.yaml`, then `curl -H 'Host: blog.localhost' localhost:8090/`.

```go
// A minimal host: instances from a YAML file, routed by hostname, served by a toy runtime that answers with the
// instance it resolved. Real hosts plug in a provider instead (ext/pocker as isolates, ext/docker in containers).
//
//	go run ./example --instances instances.yaml
//	curl -H 'Host: blog.localhost' localhost:8090/
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"pocketone.dev/ext/multisite"
)

type echoRuntime struct{}

func (echoRuntime) Name() string                        { return "echo" }
func (echoRuntime) Serves(inst multisite.Instance) bool { return true }
func (echoRuntime) Ready() bool                         { return true }
func (echoRuntime) ServeInstance(w http.ResponseWriter, r *http.Request, inst multisite.Instance) {
	fmt.Fprintf(w, "instance %s (%s%s)\n", inst.ID, r.Host, r.URL.Path)
}

func main() {
	instances := flag.String("instances", "instances.yaml", "instances file: instances: [{id: blog, hosts: [blog.localhost]}]")
	listen := flag.String("listen", "127.0.0.1:8090", "listen address")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	h := &multisite.Host{Source: &multisite.YAMLSource{Path: *instances}, Runtime: echoRuntime{}, Listeners: multisite.Listeners{Listen: *listen}}
	if err := h.Run(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

## How it works

| Piece | What it does |
|---|---|
| `Instance`, `InstanceSource`, `YAMLSource` | The instance record (instance.source/v1) and where instances come from. |
| `Directory` | Instances by hostname and id, kept current from the source. |
| `Handler` | Health path, peer check, hostname, blocked, then remote (the `Forward` hook) or local (the instance routes, then the runtime), with the gates. |
| `Runtime` | runtime/v1: what runs instances. One per host. |
| `Gate`, `ChainGates`, `HostGate` | Request gates (gate/v1). |
| `Extension`, `Link`, `Host.Use`, `Host.Setup` | Host extensions and the host's contract scope. |
| `Serve`, `TLSProvider` | Listeners: plain, TLS (a provider such as autotls, or cert files reloaded on change), redirect, graceful shutdown. |
| `Host`, `FromConfig` | All of the above assembled, by hand or from `pocketone.yaml`. |

## License

MIT
