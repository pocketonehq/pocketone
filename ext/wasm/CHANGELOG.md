# WASM changelog

Changes to `ext/wasm`, newest first. A user-visible change adds its line under `## Unreleased` in the same commit.
Each PocketOne release raises this extension's `version:` when there's something under `## Unreleased` and renames the
section to that version ([Changelogs](../../docs/extensions.md#changelogs)).

## Unreleased

### Fixed

- Compiled modules are cached in the app's `pb_cache/wasm` by default instead of the user's cache folder, so the
  cache works when the server runs without `$HOME` (a systemd service, such as a PocketHost edge); before, every
  call there compiled its module. `pocketone cache clear` empties it.

## 0.0.2 (2026-11-18)

### Added

- `wasm_list_modules` on the app's MCP server (`mcp/v1`, superusers only): every module by name with its manifest and whether it runs, and instructions telling agents how to call a module with `$wasm.run.async` / `$wasm.call.async`.
- `ext/wasm`: WASI modules (Go, Rust, Zig, ...) as `pb_hooks/<name>.pb.wasm`, called from pb_hooks with `await $wasm.run.async(name, input)` and `await $wasm.call.async(name, fn, input)` (async only: a call runs off the app's JS VM); each describes itself (`<name> --describe`). Input in, output out, no access to the database, files, network, environment or processes.
- Docs on pocketone.dev under WASM: a page for each language (C and C++, Go, TinyGo, Rust, Zig, others) with what each costs per call, keeping state in JS between calls (with a TinyGo example), and recipes built on real libraries with tested hooks: Markdown to HTML, HTML sanitizing, thumbnails, QR codes, text diffs, CSV import.
- Example modules in C (`summary/c`, built with `zig cc`), AssemblyScript (`summary/assemblyscript`, with the WASI shim) and TinyGo (`summary/tinygo`, `stats-tinygo`), the C summary written by hand in WebAssembly text (`summary/wat`, built with WABT's `wat2wasm`) and in LLVM IR (`summary/llvm`, built with `zig cc`), and the recipes' Rust modules (`markdown`, `sanitize-html`, `diff`, `thumbnail`, `qrcode`, `csv`), kept built and run by the tests like the others.

### Changed

- The Zig example builds with Zig 0.17 (`std.Io`, `std.process.Init`).
- Compiled modules are kept on disk between calls and restarts (wazero's compilation cache, in `pocketone/wasm` under the user's cache folder; `CacheDir` moves it, `"off"` turns it off): a module's first call compiles it, later calls load its machine code 20 to 30 times faster (Go: 1 s to 38 ms; Rust and Zig: ~30 ms to ~1.3 ms). Memory is still held only while a call runs, and entries older than 30 days are pruned when the process opens the cache.
- Every example module is built in Docker and kept built, the Go ones (`summary/go`, `crypto`) included: `example/toolchains/Dockerfile` is the build environment for each language (Go, TinyGo, Rust, Zig and C), pinned (images by digest, Zig by checksum), so a rebuild on any machine gives the same bytes; `go test -run TestPrebuiltMatchSources -rebuild` rebuilds them there and checks the bytes.

