---
title: ETS templates
description: An .ets file is HTML with TypeScript in EJS tags. The tags, escaping, what's in scope, and await.
---

# ETS templates

An `.ets` file is an HTML page with TypeScript in it. The tags are EJS's, and the code inside them is TypeScript:
PocketOne strips the types when it compiles the template, with esbuild built into the binary. Types are stripped, not
checked, so they document your code and help your editor without ever stopping a page. There is nothing to install or
configure, and no build step.

```ets
<!-- pb_pages/products/index.ets -->
<%% type Product = { name: string; cents: number }
const products: Product[] = [{ name: 'Widget', cents: 1999 }] %>
<ul>
<%% for (const p of products) { -%>
  <li><%%= p.name %>: $<%%= (p.cents / 100).toFixed(2) %></li>
<%% } -%>
</ul>
```

Nothing checks the types inside an `.ets` file, your editor included: editors don't know `.ets` yet, so associate it
with EJS for highlighting. For checked types, keep logic in the page's `.ts` or a `_private/` module (see
[TypeScript](typescript.md)).

What you write is what the browser gets, with the tags filled in. ETS is the template style these docs use
throughout. [Components with TSX](jsx.md) is the alternative; pick one style per app (see [File types](file-types.md)).

## Tags

| Tag | Meaning |
|---|---|
| `<%% code %>` | Run code, no output |
| `<%%= expr %>` | Output, HTML-escaped |
| `<%%- expr %>` | Output, raw |
| `<%%# comment %>` | Nothing |
| `<%%%` | A literal `<%%` |
| `-%>` | Also remove the newline right after the tag |
| `<%%_` / `_%>` | Also remove whitespace before / after the tag on its line |
| `<script server<%- '>' %> code </script>` | Same as `<%% code %>` |

## Escaping

`<%%= %>` replaces `&` `<` `>` `"` `'` with `&amp;` `&lt;` `&gt;` `&#34;` `&#39;`. `null` and `undefined` output as an empty string.

```ets
<p><%%= '<b>"x"</b>' %></p>   <!-- <p>&lt;b&gt;&#34;x&#34;&lt;/b&gt;</p> -->
<p><%%- '<b>raw</b>' %></p>    <!-- <p><b>raw</b></p> -->
<p><%%= null %></p>            <!-- <p></p> -->
```

Use `<%%-` only for HTML you trust, such as the output of `include()` or `slot()`.

## In scope

Inside a template you can use:

| Name | |
|---|---|
| `ctx` | The [request context](ctx/index.md) |
| `data` | `ctx.data` |
| `params` | `ctx.params` |
| `include` | Render a [partial](partials.md) |
| `slot` | Define or read a [slot](layouts.md) |
| `asset` | `ctx.asset` |
| `meta` | `ctx.meta` |
| `require` | Load a module: a path, or a name looked up in `_private/` then `node_modules` (see [JavaScript Runtime](jsvm.md)) |

## Output

A template returns its text. There is no `echo()`; anything you want in the output goes through a tag.

```ets
<ul>
<%% for (const n of [1, 2]) { -%>
  <li><%%= n %></li>
<%% } -%>
</ul>
```

## await

A template can `await`. Any tag that uses `await` turns the template into an async one, and Pages waits for it
before it sends the page:

```ets
<%% const orders = await ctx.app.countRecords.async('orders') %>
<p><%%= orders %> orders</p>
```

- Including a partial that uses `await` gives you a promise. Print it with `<%%- await include('stats') %>`.
- Printing a promise without `await` is an error, so you won't get `[object Promise]` in your page.
- A slot whose content awaits takes an async function: `<%% await slot('title', async () => { %>...<%% }) %>`.
- A template without `await` runs as a plain function, so sync templates cost nothing extra.

## Errors

A TypeScript syntax error names the file and the template line, like any other template error:
`index.ets: line 3: TypeScript: Unexpected "="`. A type that's wrong but well formed isn't an error at all, since
nothing checks it.

## Caching

A template is compiled once per content hash and reused, so an edited file is recompiled on the next request.
