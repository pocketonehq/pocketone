---
title: Recipes
description: "WASM modules built on real libraries, each with page code that calls it: Markdown, HTML sanitizing, QR codes, thumbnails, diffs, CSV, EDIFACT and XML orders, and crypto JS lacks."
---

# Recipes

A module can't reach the database, files or network, so what it's good for is computing: turning one thing into another. Each recipe here is a module built on a library from another language, with page code that uses it. Every module is kept built in the repository and checked by the extension's tests.

All of them are Rust: it has the most libraries that build for WASI as they are (see [Rust](../languages/rust.md)). Copy a module's `.pb.wasm` from its folder into your `pb_hooks/`, or build it from its source, and call it from your page code.

## Content

| Recipe | Library | Module | What it does |
|---|---|---|---|
| [Markdown to HTML](markdown.md) | pulldown-cmark | 258 KB | Renders a Markdown field to HTML when a record is saved. |
| [Sanitize HTML](sanitize-html.md) | ammonia | 803 KB | Cleans HTML users wrote, so it's safe to show. |
| [Text diffs](diff.md) | similar | 130 KB | Keeps a revision history of a text field as unified diffs. |

## Files and data

| Recipe | Library | Module | What it does |
|---|---|---|---|
| [Image thumbnails](thumbnail.md) | image | 448 KB | Makes a PNG thumbnail of each uploaded photo. |
| [QR codes](qrcode.md) | qrcode | 109 KB | Draws a QR code as SVG, for a ticket or a check-in link. |
| [CSV import](csv.md) | csv | 108 KB | Reads a CSV file, quoted fields and all, into records. |

## From the extension's examples

These came with the extension, to answer real questions. Each is kept built beside its source, in [`example/`](../../example/main.go)'s folders.

### Orders from EDIFACT and XML

[`edifact-orders-rust`](../../example/edifact-orders-rust/src/main.rs) reads an EDIFACT ORDERS message (raw text, as trading partners send it) into an order: parties, lines, prices and allowances, with the message's own counts checked. [`order-xml`](../../example/order-xml/src/main.rs) reads the same order from XML into the same JSON. So one route takes either format and stores one shape:

```ts
// pb_pages/orders.ts: the raw message is the request body, as text: a string goes in as it is
export async function post(ctx: any) {
  const raw = toString(ctx.req.event.request.body)
  const module = raw.trimStart().startsWith("<") ? "order-xml" : "edifact-orders-rust"
  const order = await $wasm.run.async<{ number: string; lines: object[] }>(module, raw)
  const record = new Record(ctx.app.findCollectionByNameOrId("orders"))
  record.load({ raw, number: order.number, lines: order.lines })
  ctx.app.save(record)
  return ctx.res.json(201, { id: record.id })
}
```

### Crypto JS doesn't have

[`waflows`](../../example/waflows/src/main.rs) does what a WhatsApp Flows endpoint needs and the JS runtime can't: RSA-OAEP to unwrap the request's key, and AES-GCM with WhatsApp's key and IV. Your page code keeps the JSON, the base64 and the HTTP; the module does only those three steps, with bytes as base64:

```ts
type Data = { data: string } // what each waflows function gives: bytes as base64
const body = ctx.req.body() // in the endpoint's post handler
const { data: key } = await $wasm.call.async<Data>("waflows", "rsa-decrypt", { privateKey, data: body.encrypted_aes_key })
const { data: request } = await $wasm.call.async<Data>("waflows", "gcm-open", { key, iv: body.initial_vector, data: body.encrypted_flow_data })
// ... answer the request, then seal the answer with the flipped IV
const flipped = Buffer.from(body.initial_vector, "base64").map((b) => ~b).toString("base64")
const { data } = await $wasm.call.async<Data>("waflows", "gcm-seal", { key, iv: flipped, data: Buffer.from(JSON.stringify(answer)).toString("base64") })
```

[`crypto`](../../example/crypto/main.go) (Go) is AES-256-GCM with a key your code passes in: `encrypt` takes `{key, text}`, `decrypt` takes `{key, data}`. The module can't keep the key or send it anywhere. Built with Go it's 4 MB: its first call compiles for about a second, and later calls load it in about 40 ms. The same source built with [TinyGo](../languages/tinygo.md) is 348 KB: about 75 ms the first time, a few ms after.

## Write your own

A recipe is a module like any other: a program that reads stdin and writes stdout. Pick the library first, then the language it's in, and check it builds for WASI: one that needs threads, sockets or files won't run (password-strength checking with zxcvbn, for one, reads the browser's clock and fails here). [Choosing a language](../languages/index.md) has the costs, and [Keeping state in JS](../guides/state.md) covers modules that work on a series.
