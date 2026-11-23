---
title: Text diffs
description: Keep a revision history of a text field as unified diffs, with the similar crate.
---

# Text diffs

## What it's for

A wiki page, a contract, a blog post: when people edit text, you want to see who changed what, and maybe roll back.
Storing a diff of each edit shows exactly the lines that changed, the way `git diff` does. The module compares two
texts line by line with [similar](https://crates.io/crates/similar) and returns a unified diff and how many lines were
added and removed.

## The route

A `pages` collection with a `body` text field, and a `revisions` collection with `page` (text or a relation), `diff`
(text), `added` and `removed` (numbers):

An API route saves an edit of a page's body (`PUT` with `{"body": "..."}`) and keeps it as a diff:

```ts
// pb_pages/wiki/[id].ts: every edit of a page's body is kept as a diff in revisions
interface Diff {
  changed: boolean
  unified: string
  added: number
  removed: number
}

export async function put(ctx: any) {
  if (!ctx.auth) throw ctx.httpError(401, "Sign in to edit")
  const page = ctx.app.findRecordById("pages", ctx.params.id)
  const { body } = ctx.req.body()
  const d = await $wasm.run.async<Diff>("diff", { old: page.getString("body"), new: body })
  page.set("body", body)
  ctx.app.save(page)
  if (d.changed) {
    const rev = new Record(ctx.app.findCollectionByNameOrId("revisions"))
    rev.load({ page: page.id, diff: d.unified, added: d.added, removed: d.removed })
    ctx.app.save(rev)
  }
  return ctx.res.json({ changed: d.changed, added: d.added, removed: d.removed })
}
```

The old body is read before the page changes. The revision is saved after the page, so only once the page itself is
saved.

What an edit gives:

```json
{"old": "Pocket\nbase\n", "new": "Pocket\nOne\nrocks\n"}
```

```json
{"unified": "--- old\n+++ new\n@@ -1,2 +1,3 @@\n Pocket\n-base\n+One\n+rocks\n", "added": 2, "removed": 1, "changed": true}
```

## The module

[`src/main.rs`](../../example/diff/src/main.rs):

```rust
let diff = TextDiff::from_lines(&input.old, &input.new);
// count the inserted and deleted lines...
let unified = diff
    .unified_diff()
    .context_radius(input.context.unwrap_or(3))
    .header("old", "new")
    .to_string();
```

`context` in the input sets how many unchanged lines show around each change (3 by default, as in `git diff`).

## Build

```sh
cd ext/wasm/example/diff
cargo build --release --target wasm32-wasip1   # rustup target add wasm32-wasip1 first
cp target/wasm32-wasip1/release/diff.wasm pb_hooks/diff.pb.wasm
```

## Cost

130 KB. The first call compiles it (about 35 ms); later calls load it from the cache in about 1.6 ms and diff a page in under 1 ms.

## Limits

- Both texts together are at most 4 MB (`MaxPayload`), as JSON.
- It's a line diff: a change inside a long line shows as the whole line removed and added.
