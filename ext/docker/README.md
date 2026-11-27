---
owners: ["@benallfree"]
version: 0.0.5
title: "Docker"
group: many
summary: "A host provider that runs each instance in its own container."
---

# Docker

Run every instance on a host in its own container, with the official PocketBase or PocketOne binary of the version
the instance asks for. One host can serve PocketBase 0.23 and 0.36 and PocketOne side by side.

Docker is one of the two runtime providers of the multi-instance [multisite](../multisite/README.md). The other is
[Pocker](../pocker/README.md), which runs every instance inside one process. A host runs one of them.

## Install

Docker is built into `pocketone`. It needs a Docker daemon on the same machine; nothing else to install.

It's off until `pocketone.yaml` has a `multisite` and a `docker` section (below). Then `pocketone serve` runs the host
instead of a single app.

In your own Go build, add it to the host's extensions; it provides the [runtime/v1](../contracts/runtime/v1/README.md)
contract when `pocketone.yaml` has a `docker` section:

```go
h, err := multisite.FromConfig(multisite.Options{Resolve: cfg.ResolvePath,
	Extensions: []multisite.Extension{&docker.Provider{Resolve: cfg.ResolvePath}}})
```

The module is `pocketone.dev/ext/docker`. Add `docker.NewCommand(...)` to your root command for the
`docker seed` and `docker update` commands.

## Configure

Add a `multisite` section (host mode) and a `docker` section to `pocketone.yaml`. Listeners (`server`), instances
(`instances`) and the firewall are the host's sections. The `docker` section makes docker the host's runtime instead of
pocker; every instance runs in a container.

```yaml
multisite: {}
docker:
  image: alpine:3.20                 # base image the binaries run in
  binaries: /var/lib/pocketbase      # release binary cache
  root: /var/lib/instances           # one folder per instance: <root>/<id>
  versions: ["pb:0.23", "pb:0.36", "p1:0.0"]
  default_version: "pb:0.36.*"
  idle_ttl: 10m
  max_concurrent_starts: 4
instances:
  file: ./instances.yaml
```

`image`, `binaries` and `root` are required.

**Versions.** An instance picks its binary with the prefix of its `version`: `pb:` is upstream PocketBase, `p1:` is
PocketOne, no prefix means `pb:`. `versions` lists the minors the host serves; the newest cached patch that matches
wins. A version that isn't listed gets a clear 503. Upstream PocketBase 0.22 and older are refused unless you lower
`min_upstream` and keep those releases in the cache yourself.

**Per instance** (`instances.yaml` or a mothership): `version`, `secrets` (passed as environment), `encryption_key`,
`dev`, and `dir` to put its folder somewhere other than `<root>/<id>`. An instance with `runtime` set to anything but
`docker` isn't served here.

**Limits and hardening** are optional: `nano_cpus`, `pids_limit`, `nofile`, `cap_drop` and `no_new_privileges`.

## Use

### Fetch the binaries

```bash
pocketone docker update   # find the latest patch of each docker.versions minor, record and download it
pocketone docker seed     # download whatever is recorded but not in the cache yet
```

`update` checks upstream PocketBase releases (ganigeorgiev.com, then the archived GitHub) and PocketOne releases on
GitHub, verifies each download against the release's checksums.txt, and records the list in the cache's
`releases.json`. Run it when you want new patches. Nothing is downloaded at request time: when the host starts, any
listed release missing from the cache is downloaded in the background.

The cache layout is `<binaries>/<version>/<platform>/pocketbase` for PocketBase (PocketHost's existing layout) and
`<binaries>/p1/<version>/<platform>/pocketone` for PocketOne.

### Run the host

```bash
pocketone serve
```

- A container starts on an instance's first request and is ready when `/api/health` answers. At most
  `max_concurrent_starts` start at once.
- It stops after `idle_ttl` without requests.
- It restarts on its next request when what it runs changed (secrets, version, mounts). A change to routing only
  doesn't restart it.
- It stops when the instance is deleted or moves to another edge.
- Containers keep running when the host restarts; the next process adopts them by their labels.

### Hooks and migrations

A change in an instance's `pb_hooks` restarts its container. PocketBase's own hooks watcher stays on too, so a hook
file that fails to load is logged and skipped instead of stopping the instance. A request that arrives while the
instance restarts waits for it instead of failing; one that lost its connection mid-restart is sent once more if
it's safe to repeat (GET, HEAD or OPTIONS without a body). Migrations run when a container boots.

### Maintenance

Instance folders are ordinary folders under `root`. To work on one while nothing has it open, a host's Go code calls
`WithInstanceStopped` (below); requests for that instance wait up to 30 s, then get a 503 with `Retry-After`.
The host provides [instance.fs/v1](../contracts/instance/fs/v1/README.md) on its scope from the same methods
(`Runtime.Files(dir)`), so host-side code (a host's deploy or file drive) gets an instance's folder, writers' lock
and restart from the contract.

### Remote control

Containers run stock images, and a PocketBase image has no PocketOne extensions, so the host answers
[remote/v1](../contracts/remote/v1/remote.go) for them at `/api/_c/remote/v1/` on each instance's hostname: the
`pocketone remote` commands work the same as against a Pocker instance or a single app. It answers after the host's
gates (firewall, rate limits) and blocked check, so a blocked instance's remote/v1 gets the blocked answer and the
container stays stopped.

- `GET /api/_c/remote/v1` (whoami) is public and lists `remote/v1`.
- `status`, `restart` and `logs` need a superuser token of that instance (`pocketone remote login` with a superuser
  email and password). The host asks the instance itself to check it.
- A host with its own accounts sets `docker.Provider.RemoteAuth` to decide who may call them instead (PocketHost: the
  instance owner's tokens). `remote/v1` is provided on the host's scope, so whoami also lists the other contracts the
  host provides for its instances (a host's `deploy/v1`).
- `restart` replaces the container; `logs` replays and follows the container's output, the last 1000 lines per
  instance kept by this host since it started following the container.

### Switching provider

Docker and Pocker both use `<root>/<id>`, so switching is a config change. Stop the containers first when leaving
docker, since they keep running across a host restart on purpose. Data only moves forward: an instance started on a
newer PocketBase than wrote its data is upgraded.

## Integrations

### Works with

| Extension | How they fit |
|---|---|
| [Multisite](../multisite/README.md) | Docker is a runtime provider of the host: the host routes by hostname, runs the listeners and gates, and hands each request to docker. |
| [Pocker](../pocker/README.md) | The other provider. A host runs one of them, over the same instance folders. |
| [Edge](../edge/README.md) | For a fleet: requests for instances on another edge are forwarded there. |
| [Mothership](../mothership/README.md) | Instances (and their versions) come from a mothership instead of a file. |
| [Firewall](../firewall/README.md) | Rate limits and allow lists in front of every instance. |
| [Remotes](../remotes/README.md) | The host provides remote/v1 for every container, so the CLI's whoami, status, restart and logs work. |

### Building on Docker

- **`docker.Provider`** fields extend the provider from Go: `Container` adds mounts, environment and labels to each
  container; `Version` rewrites an instance's version before the binary is picked; `OnLog` receives container output
  (default: the debug log); `Versions` replaces the `versions` list (for example a fleet's list from a mothership);
  `MinUpstream` replaces `min_upstream`.
- **`Spec`** (what `Container` fills in): `Binds`, `Env`, `Labels`, plus `Cmd` to adjust the command (for example
  drop a flag an old release lacks) and `HealthPath` / `HealthAnyStatus` for the readiness check.
- **`VersionsSource`**: any type with `Releases(ctx)` decides which releases the host serves, each pinned to its
  download and sha256. `YAMLVersions` (the `versions` key) is the built-in one.
- **Runtime API**: `docker.New(docker.Config{...})` builds the runtime without the config file.
  `WithInstanceStopped(ctx, id, fn)` stops an instance, holds its requests and runs `fn` on its folder (for deploys
  and restores); `StartInstance` starts one now; `InstanceDir` returns its folder. `Remote(host, dir)` and `SuperuserAuth()` are the
  remote/v1 provider and the host-side auth that `Provider` wires in, for a host built by hand
  (`remotev1.W.HostHandler(host, multisite.InstanceID, rt.SuperuserAuth(), next)` in `Host.InstanceRoutes`).
- **Release helpers**: `ParseVersion`, `Pick` / `PickMin`, `LatestPatches`, `Update` and `Seed` are what the commands
  use.

## Example

A minimal docker host: instances from a YAML file, each in its own container, routed by hostname on one listener. With
a Docker daemon running, start it from this folder with
`go run ./example --instances instances.yaml --data ./instances --binaries ./bin --versions pb:0.23,p1:0.0`.

```go
// A minimal docker host: instances from a YAML file, each in its own container with the PocketBase or PocketOne
// version it asks for, routed by hostname on one listener.
//
//	go run ./example --instances instances.yaml --data ./instances --binaries ./bin --versions pb:0.23,p1:0.0
//
// instances.yaml:
//
//	instances:
//	  - { id: blog, hosts: [blog.localhost], version: "pb:0.23.*" }
//	  - { id: shop, hosts: [shop.localhost], version: "p1:0.0.*" }
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"pocketone.dev/ext/docker"
	"pocketone.dev/ext/multisite"
)

func main() {
	instances := flag.String("instances", "instances.yaml", "instances file")
	data := flag.String("data", "./instances", "folder with one folder per instance")
	binaries := flag.String("binaries", "./bin", "PocketBase and PocketOne binary cache")
	versions := flag.String("versions", "pb:0.23,p1:0.0", "minors to serve (latest patch each; fetched at start)")
	image := flag.String("image", "alpine:3.20", "base image")
	listen := flag.String("listen", "127.0.0.1:8090", "listen address")
	flag.Parse()

	b := &docker.Binaries{Root: *binaries}
	tracked := strings.Split(*versions, ",")
	if err := docker.RunUpdate(context.Background(), os.Stdout, docker.DefaultDiscovery(), b, tracked); err != nil {
		slog.Error("versions", "error", err)
		os.Exit(1)
	}
	rt, err := docker.New(docker.Config{Image: *image, DataRoot: *data, Binaries: b, Versions: docker.YAMLVersions{Tracked: tracked, Root: b.Root}})
	if err != nil {
		slog.Error("docker", "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	h := &multisite.Host{Source: &multisite.YAMLSource{Path: *instances}, Runtime: rt, Listeners: multisite.Listeners{Listen: *listen}}
	if err := h.Run(ctx); err != nil {
		slog.Error("host", "error", err)
		os.Exit(1)
	}
}
```

## How it works

- Each instance folder is mounted at `/home/pockethost` and the binary read-only at `/home/pockethost/pocketbase`.
  The container runs `./pocketbase serve --http 0.0.0.0:8090`, plus `--dev` for dev instances and
  `--encryptionEnv=PB_ENCRYPTION_KEY` when the instance has an encryption key. Its secrets are its environment.
- The port is published on 127.0.0.1 and the host proxies to it. This is the layout PocketHost's Node edge uses, so
  its instance folders and binary cache move as they are.
- Every container carries labels: `p1.runtime`, `p1.instance`, `p1.version` (the resolved release, such as
  `pb:0.36.9`) and `p1.spec` (a hash of its configuration, used to tell when it must restart). `adopt_image` also
  adopts unlabeled containers of an image, for taking over from another manager.
- Docker is reached over its socket API directly; there's no Docker SDK dependency.

### Status

Tested against a real Docker daemon, with upstream PocketBase 0.23 and PocketOne 0.0.1 side by side, alone and as a
host's runtime. Not yet in production.

`go test ./...` runs the Docker tests when a daemon is reachable (they're skipped with `-short`). The first run
downloads an upstream release and a PocketOne release into the user cache.

## License

MIT
