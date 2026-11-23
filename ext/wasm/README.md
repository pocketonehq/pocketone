---
owners: ["@octavonu"]
version: 0.0.2
title: "WASM"
group: binary
summary: "Bring your own WASM: Go, Rust or Zig modules called from page code, each a closed box."
---

# WASM

**Bring your own WASM (BYOW).** Call code written in Go, Rust, Zig or anything else that builds for WASI from
your page code, when you need a library from another language or more speed than JS gives you. A module is a small program
compiled to WebAssembly that your code calls like a function. Input in, output out, or an error. It can't reach the database, files, network, environment or other
processes; your code gives it what it needs and decides what to do with the result.

One call does it:

```ts
const out = await $wasm.run.async<{ text: string; words: number }>("summary", { title: "Hello", body: "one two three" })
// out is { text: "Hello", words: 3 }
```

`"summary"` names the module, `pb_hooks/summary.pb.wasm`. The object is its input, sent to it as JSON, and `out` is
the JSON it writes back, parsed. If the module fails, the call rejects with a `WasmError`. Calls are only `async`: the
module runs off the JS engine, so the app keeps serving other requests while it works.

In an API route, the input comes from the request and the output goes into the record it saves:

```ts
// pb_pages/posts.ts
export async function post(ctx: any) {
  const { title, body } = ctx.req.body()
  const out = await $wasm.run.async<{ text: string }>("summary", { title, body })
  const record = new Record(ctx.app.findCollectionByNameOrId("posts"))
  record.load({ title, body, summary: out.text })
  ctx.app.save(record)
  return ctx.res.json(201, { id: record.id, summary: out.text })
}
```

Use it for what JS does badly or slowly: parsing (EDIFACT, XML, binary formats), crypto, validation, number
crunching, or a library that only exists in Go, Rust or C.

WASM is an extension; the `.pb.wasm` files it runs are modules. Beyond this page: a page for [each language](docs/languages/index.md) (C, Go, TinyGo, Rust, Zig, AssemblyScript), [keeping state in JS](docs/guides/state.md) between calls, and [recipes](docs/recipes/index.md) built on real libraries (Markdown, HTML sanitizing, thumbnails, QR codes, diffs, CSV).

## Install

WASM is built into `pocketone`: it's a member of the [standard set](../standard/README.md), which every app gets,
standalone or an instance on a multisite host, and the set binds `$wasm` into the JS engine ([`ext/jsvm`](../jsvm/README.md)). Put a
`.pb.wasm` file in `pb_hooks` and call it from your page code.

In your own build, the set already has it. To change its settings, replace the member:

```go
set := standard.New()
set.WASM = wasm.New(wasm.Config{MaxConcurrent: 4})
```

Without the set, register it and bind it yourself:

```go
w := wasm.New(wasm.Config{})

app := pocketbase.New()
extkit.MustRegister(app, w)
jsvm.MustRegister(app, jsvm.Config{OnInit: func(rt js.Runtime) { extkit.BindJS(app, rt, w) }})
app.Start()
```

`$wasm` needs PocketOne's JS engine; with PocketBase's own (`--jsvm goja`) extensions bind nothing.

The module is `pocketone.dev/ext/wasm`. Modules are `pb_hooks/<name>.pb.wasm`; nothing else
needs setting up.

## Configure

The defaults suit most apps. To change them, pass them to `wasm.New` (`set.WASM = wasm.New(...)` in a standard set):

| Field | Default | What it does |
|---|---|---|
| `Dir` | `pb_hooks` beside the data dir | The module folder. |
| `MaxCallMemoryMB` | 32 | Memory each call may allocate (its heap, stack and input). Every call has its own, not shared: 8 calls at once may take 8 × 32 MB. A call that needs more crashes alone. |
| `Timeout` | 5s | Time limit for a call's run (compiling comes before it). |
| `MaxConcurrent` | 8 | Calls running at once in the app. More wait, up to `Timeout`, then fail as `busy`. |
| `MaxModulesMB` | 10 | MB of modules running at once: the `.wasm` sizes of the calls running, added up (file sizes, not memory). More wait, as above. It's also the largest module that runs. |
| `MaxPayload` | 4 MB | Largest input or output of a call. |
| `CacheDir` | `pocketone/wasm` in the user's cache folder (`~/.cache` on Linux, `~/Library/Caches` on macOS) | Where compiled modules are kept between calls and restarts (see How it works). `"off"` compiles every call. Keep it where only the server's process writes: it holds machine code the process runs. |

Its `pocketone.yaml` section has only `enabled`: `wasm: {enabled: false}` turns `$wasm` off on a single app. With Pocker, each instance can override all of these but `CacheDir`, which is the host's (see Integrations).

## Use

### Modules are files

A module is one file in `pb_hooks/`, `pb_hooks/<name>.pb.wasm`, called as `<name>`:

```
pb_hooks/summary.pb.wasm
pb_pages/posts.ts
```

It ships with the rest of your code: git, `pocketone deploy`, or SFTP. Like `node script.js`, every call reads the
module's file as it is then. A change in `pb_hooks/` also restarts the app, as for a `.pb.js` (under pocker the new
app takes over once it has started). To turn a module off, remove or rename it. Hidden files and files that don't end
in `.pb.wasm` are ignored, so upload a new version under a hidden name (`.summary.pb.wasm.part`) and rename it over the
old one: no call reads it half written.

A module that can't run (a broken module, a module over `MaxModulesMB`) fails its calls with `not_loaded` and says
why, and `$wasm.modules()` says it too; the other modules run as usual.

### The manifest: `--describe`

A module says what it is itself: run as `<name> --describe` (the function asked for is `--describe`, with nothing on
stdin), it prints its manifest. Answering is optional, and so is each field; a module that exits non-zero or prints
nothing gets the defaults:

```json
{
  "description": "Summarizes a post: its title and first words, and a word count",
  "version": "1.2.0",
  "functions": ["run"],
  "language": "Rust",
  "repository": "https://github.com/you/summary",
  "examples": [
    {
      "function": "run",
      "input": {"title": "Hello", "body": "one two three"},
      "output": {"text": "Hello: one two three", "words": 3}
    }
  ]
}
```

| Field | |
|---|---|
| `description` | what it does |
| `version` | the author's version, as you write it (`1.2.0`, `2026-11-15`): up to 32 letters, digits and `.+_-`; only shown |
| `functions` | the functions your code may call (default `["run"]`) |
| `language` | what it's written in, shown when the module doesn't say: a Go module always does, a Rust one unless it's stripped (`strip = true`), a Zig one never |
| `repository` | where its source is: an `http(s)` URL (no `javascript:`, no credentials), at most 256 characters |
| `examples` | up to 16 calls to try it with: `function` (default `run`), `input` (a JSON string is the module's stdin as it is, anything else goes as JSON, as with `$wasm.run.async`) and, optionally, the `output` it gives |

The manifest is only shown, by `$wasm.modules()`, for people and for tests (run each example and compare its
output): calls don't read it, nothing runs the examples, and nothing fetches the repository. A function that isn't
in `functions` still reaches the module, which answers it; by convention, with the code `no_such_function`. Function
names can't start with `-`, so `--describe` is never one of them. Try it locally:
`wazero run summary.pb.wasm --describe`, and an example:
`printf '%s' '{"title":"Hello","body":"one two three"}' | wazero run summary.pb.wasm run`.

The examples keep the manifest as `manifest.json` beside the source and build it in (Go's `//go:embed`, Rust's
`include_str!`, Zig's `@embedFile`), so the module is still one file. Any other field is refused: a module can't ask
for anything, so a manifest that expects more (dependencies, permissions) is shown as an error in
`$wasm.modules()`, and the module runs with the defaults. A manifest is at most 64 KB. `$wasm.modules()` checks it
with wazero's interpreter (below), within the module's limits.

### Calling from page code

```ts
await $wasm.run.async(name, input)            // the module's "run"
await $wasm.call.async(name, fn, input)       // one of its functions (its first argument)
await $wasm.inspect.async(name, fn, input)    // {output, stderr, error, compileMs, runMs}, for trying a module out
$wasm.modules()  // [{name, running, description, version, functions, language, repository, examples, size, error}]
$wasm.limits()   // {maxCallMemoryMB, timeoutMs, maxConcurrent, maxModulesMB, maxPayload}
```

Calls are async only, in an async handler. The JS engine runs one VM per app and can't interrupt a call into Go, so a
module called synchronously would hold the VM, and with it every other hook and request of the app, while it compiles
and runs (a Go module's first call compiles for over a second). A call runs off the VM instead, and the app serves other
requests meanwhile. `$wasm.run(...)` without `.async` throws, saying so.

The input is the module's stdin: a string or bytes (an `ArrayBuffer`, a `Uint8Array` or another typed array) as they are, nothing for
`undefined` or `null`, anything else as JSON. So `$wasm.run.async("edifact", raw)`, with `raw` a string, hands over the raw text,
byte for byte. The output is the module's stdout: parsed if it's JSON, a string if not, `null` if empty.

A failed call rejects with an `Error` named `WasmError`, with `code`, `message`, `retryable` and `module`:

```ts
try {
  record.set("summary", (await $wasm.run.async<{ text: string }>("summary", post)).text)
} catch (err) {
  const w = err as WasmError
  if (w.name !== "WasmError" || w.code !== "empty_body") throw err
}
```

| Code | |
|---|---|
| `not_found` | no `<name>.pb.wasm` in the folder |
| `not_loaded` | in the folder, but it can't run (the message says why: a broken module, too large), or the app isn't serving |
| `no_such_function` | the module has no such function (the code modules give, by convention) |
| `timeout` | over the time limit (retryable) |
| `crashed` | it trapped, panicked or ran out of memory |
| `too_large` | input or output over the limit |
| `busy` | the calls running left no room for this one (retryable); the message gives the numbers |
| `module_error` | it exited non-zero without a code of its own |
| anything else | the module's own code (below) |

`$wasm.inspect.async` makes the same call but never rejects for it: it returns the output (or `null`), the `error`
as `{code, message, retryable}` (or `null`), what the module wrote to stderr (up to 64 KB), and how long compiling
and running took. It's for pages that let a superuser try a module out; other code uses `run` and `call`.

The time limit covers the run; compiling comes before it (below).

`$wasm` is declared in each app's `types.d.ts` ([`wasm.d.ts`](wasm.d.ts)), for code completion and `tsc`.

### Writing a module

A module is a plain WASI program (`wasip1`), the way you'd write a command-line tool: read stdin, write stdout, exit.
The function asked for is its first argument (`os.Args[1]`, `"run"` for `$wasm.run.async`, `--describe` for its manifest,
above). To fail, exit non-zero; if
stderr is a JSON object `{"code", "message", "retryable"}`, your code gets it as is, any other text becomes the
`message` of a `module_error`. Anything it writes to stderr on success goes to the app's log (Logs in the
dashboard), so stderr is its log.

**Go** ([`example/summary/go`](example/summary/go/main.go)):

```go
//go:embed manifest.json
var manifest string // what --describe prints

func main() {
	if os.Args[1] == "--describe" {
		fmt.Print(manifest)
		return
	}
	var post struct{ Title, Body string }
	if err := json.NewDecoder(os.Stdin).Decode(&post); err != nil {
		fmt.Fprint(os.Stderr, `{"code":"bad_input","message":"not a post"}`)
		os.Exit(1)
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"text": post.Title, "words": len(strings.Fields(post.Body))})
}
```

```sh
GOOS=wasip1 GOARCH=wasm go build -o pb_hooks/summary.pb.wasm .
```

**Rust** ([`example/summary/rust`](example/summary/rust/src/main.rs)): a binary crate, serde for JSON.

```sh
rustup target add wasm32-wasip1   # once
cargo build --release --target wasm32-wasip1
cp target/wasm32-wasip1/release/summary.wasm pb_hooks/summary.pb.wasm
```

**Zig** ([`example/summary/zig`](example/summary/zig/src/main.zig)): an executable for `wasm32-wasi` (Zig 0.17). Set
`stack_size = 1 << 20` in `build.zig`: Zig's default stack is 16 MB, and every call would start with it.

Any language that builds a WASI program works the same way: [Languages](docs/languages/index.md) has a page for C
and C++, Go, TinyGo, Rust, Zig and AssemblyScript, each with a module to build, and what else runs. Try a module without an app: `echo '{"title":"Hi","body":"one two"}' | wazero run summary.pb.wasm run`.

Two things are refused when the module loads, with a message that says so:

- a library instead of a program: Go's `-buildmode=c-shared`, or any module without `_start` ("not a WASI program");
- a module that imports anything outside `wasi_snapshot_preview1`, such as an [Extism](https://extism.org) plugin
  built with its PDK, or host functions this host doesn't have.

### Which language

| Example | Module | First call (compiles) | Later calls (from the cache) | Machine code: in memory during a call, and in the cache |
|---|---|---|---|---|
| C | 25 KB | 17 ms | 0.5 ms | 86 KB |
| Rust | 98 KB | 33 ms | 1.3 ms | 389 KB |
| Zig | 95 KB | 30 ms | 1.2 ms | 357 KB |
| TinyGo | 284 KB | 74 ms | 3 ms | 988 KB |
| Go | 4.3 MB | 1.05 s | 38 ms | 18 MB |

A module's first call compiles it and writes its machine code to the cache (`CacheDir`); every later call, after a
restart too, loads it from there. Running the summary itself takes under a millisecond in every language. A call
holds its module's machine code in memory only while it runs, plus the program's own memory (its stack and heap, Go's
runtime and garbage collector included), which stays under `MaxCallMemoryMB`; between calls, nothing. Measured on a
laptop (2026-11-18), one call at a time; a Go module's size varies with what it imports (one that imports `net/http`
is ~11 MB). [Choosing a language](docs/languages/index.md) has the same numbers with how to pick.

Go calls compiling at once also slow each other down: one took +19 MB, two +38 MB, four +245 MB (2026-11-16, before
the cache; calls that load from the cache take far less). That's why the calls running at once are limited by their
modules' size, `MaxModulesMB`, and not only by their number. An admin picks the trade-off with that one number:

| `MaxModulesMB` | Go calls (4.4 MB) at once | Rust or Zig calls (~0.1 MB) | Most memory they take, measured |
|---|---|---|---|
| 5 | 1 | 8 (`MaxConcurrent`) | ~25 MB |
| 10 (default) | 2 | 8 | ~45 MB |
| 20 | 4 | 8 | ~250 MB |

Pick by what the module does. With the cache, every language is quick after its first call; what still differs is
memory and how many calls run at once. A Go call holds 18 MB of machine code and takes 38 ms to load it, and two run at
once with the defaults: fine for a cron job, an import or an admin action. A module on a hot path (every record save,
every request) is better in C, Rust, Zig or TinyGo (the same Go source, a module 15 times smaller), whose calls take a
few milliseconds and under 1 MB.

## Integrations

### Works with

| Extension | How they fit |
|---|---|
| [Multisite](../multisite/README.md) | Add it to the host's standard set and Pocker gives it to every instance it runs (Docker containers run stock images): one extension value serves them all. Each instance has its own modules, in its own `pb_hooks/`, its own call slots and limits. Each call reads the instance's module file as it is then; nothing runs between calls (below). A module change reloads just that instance, like a `.pb.js` change. |
| [Sandbox](../sandbox/README.md) | `$wasm` is safe to give a hostile tenant: a module gets nothing but its input. A call can't be interrupted by the JS engine's time limits, so `Timeout` (and compiling) bound it instead. |
| [Deploy](../deploy/README.md) | `pocketone deploy` sends the modules with the rest of `pb_hooks/`. |
| [MCP](../mcp/README.md) | The app's `/mcp` gets `wasm_list_modules` (superusers only): every module by name, with its manifest (functions, examples) and whether it runs. Agents connecting are told how to call a module with `$wasm`, so one working on the site knows its modules even without their sources. |

### Per-instance settings (Pocker)

Each instance can override `maxCallMemoryMB`, `maxConcurrent`, `maxModulesMB`, `timeout` (a duration, like `5s`) and
`maxPayload` in its `extensions.wasm` settings, over the Config given to `New`, key by key. `enabled: false` turns
`$wasm` off for that instance (`typeof $wasm === "undefined"` in its JS). A change restarts just that instance, on
its next request:

```yaml
instances:
  - id: abc
    extensions: { wasm: { timeout: 10s, maxCallMemoryMB: 64 } }
  - id: no-wasm
    extensions: { wasm: { enabled: false } }
```

Leave `dir` unset there: each instance then gets the `pb_hooks/` in its own folder. `MaxConcurrent` and
`MaxModulesMB` count per instance, so one instance's busy modules never make another's calls wait. A restart (new
settings, a pb_hooks change, a restore) lets calls in progress finish, and the instance's modules run as before
after it, from its folder: nothing to reload.

### Building on WASM

There's no Go API, endpoint or form for calling a module: the app's JS code (`$wasm`) is the only way in, so whatever
reaches a module went through the app's own code.

## Example

[`example/main.go`](example/main.go) runs one app with a `posts` collection and a hook that fills each new post's summary; its folder is laid
out like an instance (`pb_data/`, `pb_hooks/`). Build the module into `example/pb_hooks/`, then
`cd example && go run . serve --dev` and create a post.

```go
// Command example runs one app with ext/wasm and the summary module wired in from pb_hooks: a new post gets its
// summary before it's saved (pb_hooks/main.pb.js). The example folder is the app's folder, laid out like an
// instance on PocketHost: pb_data/ and pb_hooks/ side by side, the module beside the JS hooks.
//
//	cd example && go run . serve --dev
//
// Build the module into pb_hooks/ (before or after starting the app: each call reads it):
//
//	(cd summary/go && GOOS=wasip1 GOARCH=wasm go build -o ../../pb_hooks/summary.pb.wasm .)
//
// Then create a post (dashboard, or the API) and look at its summary field.
//
// Logins (ext/devusers, local testing only): dashboard admin@example.com / password, app user user@example.com / password.
package main

import (
	"log"

	"ganigeorgiev.com/pocketbase/pocketbase"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"pocketone.dev/ext/devusers"
	"pocketone.dev/ext/extkit"
	"pocketone.dev/ext/extkit/js"
	"pocketone.dev/ext/jsvm"
	"pocketone.dev/ext/wasm"
)

func main() {
	app := pocketbase.New()
	devusers.Register(app) // local testing: superuser admin@example.com and user user@example.com, password "password"
	app.OnBootstrap().BindFunc(func(e *core.BootstrapEvent) error {
		if err := e.Next(); err != nil {
			return err
		}
		return posts(e.App)
	})
	// the same extension value the standard set gives every app, standalone or an instance (set.WASM)
	w := wasm.New(wasm.Config{})
	extkit.MustRegister(app, w)
	jsvm.MustRegister(app, jsvm.Config{
		HooksDir: "pb_hooks",
		OnInit:   func(rt js.Runtime) { extkit.BindJS(app, rt, w) }, // $wasm in pb_hooks
	})
	if err := app.Start(); err != nil {
		log.Fatal(err)
	}
}

func posts(app core.App) error {
	if _, err := app.FindCollectionByNameOrId("posts"); err == nil {
		return nil
	}
	c := core.NewBaseCollection("posts")
	c.Fields.Add(&core.TextField{Name: "title"}, &core.TextField{Name: "body", Max: 100000},
		&core.TextField{Name: "summary", Max: 1000}, &core.NumberField{Name: "words"})
	return app.Save(c)
}
```

### More example modules

Each is a plain program with a `manifest.json` built in, which it prints for `--describe` (its functions and examples,
so `/wasm`-style pages and the wazero CLI can try it), built with the command in its header. They answer the usual questions about a feature like this:

| Module | Language | What it shows |
|---|---|---|
| [`summary/go`](example/summary/go), [`summary/tinygo`](example/summary/tinygo), [`summary/rust`](example/summary/rust), [`summary/zig`](example/summary/zig), [`summary/c`](example/summary/c), [`summary/assemblyscript`](example/summary/assemblyscript) | Go, TinyGo, Rust, Zig, C, AssemblyScript | The same module in six languages: the summary above, and how much the language matters (a Go module is ~4 MB and compiles for over a second; TinyGo ~280 KB and ~65 ms; Rust, Zig, C and AssemblyScript under 100 KB and 10–25 ms). |
| [`summary/wat`](example/summary/wat), [`summary/llvm`](example/summary/llvm) | WAT, LLVM IR | The C summary written by hand in two assembly languages, for fun: WebAssembly text calling WASI directly (1.8 KB), and LLVM IR built by clang with wasi-libc (3.8 KB). See [WAT](docs/languages/wat.md) and [LLVM IR](docs/languages/llvm.md). |
| [`stats-tinygo`](example/stats-tinygo) | TinyGo | State kept by the app: running statistics whose state the app stores in a record and passes in with each call ([Keeping state in JS](docs/guides/state.md)). |
| [`markdown`](example/markdown), [`sanitize-html`](example/sanitize-html), [`diff`](example/diff), [`thumbnail`](example/thumbnail), [`qrcode`](example/qrcode), [`csv`](example/csv) | Rust | Real libraries for real jobs (pulldown-cmark, ammonia, similar, image, qrcode, csv), each with page code in the [recipes](docs/recipes/index.md). |
| [`crypto`](example/crypto) | Go | AES-256-GCM encryption, with no `$os.exec`. The key comes in with each call; the module can't keep it or send it anywhere. |
| [`waflows`](example/waflows) | Rust | What JS can't do: a WhatsApp Flows endpoint needs RSA-OAEP and AES-GCM with WhatsApp's key and IV. Your code keeps the JSON and HTTP; the module does only those two. |
| [`edifact-orders-rust`](example/edifact-orders-rust) | Rust | A real format with a real library (edifact-rs): an EDIFACT ORDERS message, as raw text, read into an order. |
| [`order-xml`](example/order-xml) | Rust | The same order from XML (roxmltree), so one route takes either format and stores one shape. |
| [`devil`](example/devil) | Rust | Is it safe? It tries, in plain sight, to read the app's database and `/etc/passwd`, list folders, write a file, read the environment, run `sh -c id` and send to `attacker.example`. Every try says `refused`. |
| [`memhog`](example/memhog) | Rust | What does it cost? It asks for more memory than `MaxCallMemoryMB`: that call fails as `crashed`, saying which limit it hit, and the app goes on. `probe` shows the limit from inside. |

Every one of them is also kept built, beside its source (`example/<folder>/<name>.pb.wasm`: the Go ones about 4.5 MB,
the rest under 1 MB), so they double as the extension's tests: every run of `go test` loads each one, checks that its
`--describe` is its `manifest.json`, and runs its examples, with no compiler and no Docker needed. A change to
`ext/wasm` that breaks a real module shows there.

They're built in Docker, never with the compilers on your machine:
[`example/toolchains/Dockerfile`](example/toolchains/Dockerfile) is the build environment of every example language,
one stage each: Go 1.27.4, TinyGo 0.41.1, Rust 1.100.0 and Zig 0.17.0 (Zig, and C through `zig cc`), each image
pinned by digest and Zig's download by checksum, so a rebuild on any machine gives the same bytes. After changing a
module's source: `go test -run 'TestPrebuiltExamples/<folder>' -update`. To check every kept file is what its source
builds, byte for byte: `go test -run TestPrebuiltMatchSources -rebuild` (a few minutes). To move to a newer compiler,
change it in the Dockerfile and rebuild them all with `-update`. The Go builds use `-trimpath` and the Rust builds
remap the crates' paths to `/cargo`, so a module holds no path of the container that built it.

## What a module can't do

Modules run in [wazero](https://wazero.io) (pure Go, no cgo), in the app's process, with only the standard WASI
functions and nothing behind them:

- **No files:** nothing is mounted, not even `pb_hooks/`. `open` fails.
- **No network:** WASI preview 1 has no way to connect. (Go's `net` on wasip1 is an in-memory stand-in: a module can
  "listen", but nothing outside the program reaches it.)
- **No environment, no processes**, no arguments beyond the function name.
- **No state between calls:** every call gets a fresh instance, so nothing from one call (or one caller) reaches the
  next.
- **Bounded:** memory, time, input and output size, and how many calls run at once (see Configure).

It does get a real clock and real random numbers (crypto nonces need them). Data from the app, secrets and API
responses reach a module only as input from your code, and the result goes nowhere but back to it: the network
stays with `$http.send` in your JS.

## How it works

- Modules run only while the app serves (other commands, such as `migrate`, never read them).
- Like `node script.js`: each call reads its module's file (`<name>.pb.wasm`, once the call has room), compiles the
  module in a wazero runtime of its own (or loads its machine code from the cache), runs it, and closes it after.
  Nothing stays in memory between calls, so a folder of 100 modules costs nothing while they aren't called.
  Compiling validates the module: a broken or half-written one doesn't run. A call in progress finishes with what it
  read, whatever happens to the file meanwhile. Compiling counts toward `MaxConcurrent` and `MaxModulesMB`, so the
  memory it takes stays bounded too.
- Compiled modules are kept on disk, in wazero's compilation cache (`CacheDir`), keyed by each module's bytes: a
  changed module compiles again on its next call, and identical modules share one entry, across every app and
  instance in the process and across restarts. Loading from it is 20 to 30 times faster than compiling. Calls of the
  same module compile one at a time (wazero can't load one module twice at once safely); calls of other modules
  never wait for it, and a call whose caller gives up stops waiting (`busy`). When the process first opens the cache, it
  deletes entries older than 30 days, since nothing else does: a module still in use compiles once more then. The
  process runs the cache's contents as machine code, so it belongs where only the process writes, never in `pb_data`
  or `pb_hooks`.
- WASM watches nothing itself: no watches of its own, no timer, no work between calls, however many instances there
  are. (The JS engine's, or pocker's, watch on `pb_hooks/` restarts the app when a module changes, as for a hook.)
- `$wasm.modules()` lists the folder each time. It checks each module with wazero's interpreter, which validates it
  and runs `--describe` without making machine code (~0.25 s for a Go module), and keeps only what it found
  (manifest, language, error) until the module's file changes size or time; when the app starts serving, it checks
  them all once, so the log says which ones can't run.
- A call waits for room: fewer than `MaxConcurrent` calls running, and its module's size left in `MaxModulesMB`
  (`$wasm.modules()` gives each one's `size`). Compiling takes time and memory in step with a module's size, so with
  the defaults (8 calls, 10 MB) a Rust or Zig module (~0.1 MB) runs 8 calls at once and a Go one (~4.4 MB) 2, beside
  small ones. A call goes in as soon as there's room for it, so a small call doesn't queue behind a big one that's
  waiting; under a steady stream of small calls, a big one may wait until it gives up as `busy`.
- No collection and no database use: what's in the folder, and why a module can't run, comes from the files
  (`$wasm.modules()` and the log).

## License

MIT
