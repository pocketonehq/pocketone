---
title: File types
description: Which file extensions Pages runs, the two template styles (ETS and TSX), and the JavaScript versions that work the same way.
---

# File types

PocketOne apps are server-rendered pages with no build step. Every file in `pb_pages` is either code, a template or
a static file, and you write code and templates in TypeScript. PocketOne strips the types when it first uses a file,
with esbuild built into the binary; it never checks them.

## Code

A route's `.ts` (a page's code, or an endpoint), `+middleware.ts`, `+config.ts` and the modules in `_private/` are
TypeScript files. They export functions and objects, and never produce HTML themselves. To check their types with your editor or `tsc`, see
[TypeScript](typescript.md).

## Templates: pick one style

A template turns data into HTML. There are two styles, and an app uses one of them:

| | ETS | TSX |
|---|---|---|
| A file is | HTML with code in `<%% %>` tags | a function that returns markup |
| Pages | `about.ets` | `about.tsx` |
| Layouts, error pages | `+layout.ets`, `+error.ets`, `+404.ets` | `+layout.tsx`, `+error.tsx`, `+404.tsx` |
| Partials | `_private/card.ets`, `include('card', { item })` | `_private/Card.tsx`, `<Card item={item} />` |
| Good for | HTML-first apps, HTMX, Datastar, content sites | apps built from components, typed props |
| Read more | [ETS templates](ets.md) | [Components with TSX](jsx.md) |

These docs use ETS. It's the closest to what the browser gets: open the file and you see the page.

Both styles render on the server and send plain HTML. Neither has a client runtime, hydration or state; interactivity
comes from links, forms, HTMX, Datastar or a `<script>`.

The styles can include each other, which helps when you move an app from one to the other, but an app that mixes them
is harder to read. [Markdown](markdown.md) pages are the exception every app shares: a `.md` file runs EJS tags with
plain JavaScript inside, as a small escape hatch in content.

## JavaScript works too

Templates, `+middleware`, `+config` and `_private/` modules have JavaScript twins that behave the same, without
types. Rename a file and nothing else changes:

| TypeScript | JavaScript |
|---|---|
| `.ts` | `.js` |
| `.ets` | `.ejs` |
| `.tsx` | `.jsx` |

A route's code is the one exception: it is always `.ts`, because a `.js` file on a route path is a browser script,
served as-is. Apps written for [PocketPages 0.x](moving-from-0x.md) use `.js` and `.ejs`. A folder can't have two
framework files with the same name, such as `+layout.ets` beside `+layout.ejs`.

## Everything else

| File | What it is |
|---|---|
| `.ts` on a route path | A page's [code](loading-data.md#page-code), or an [endpoint](api-routes.md) |
| `.d.ts` | Type declarations: neither served nor run |
| `.md` | A [Markdown](markdown.md) page or partial |
| `.json` | Data you can `require` |
| `.js` and any other file | A [static file](static-content.md), served as-is |
