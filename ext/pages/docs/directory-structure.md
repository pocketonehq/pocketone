---
title: Directory Structure
description: How a pb_pages folder is organized, and the special +, _, - and . prefixes.
---

# Directory Structure

All pages live in the `pb_pages/` folder, next to `pb_data/`:

```
myapp/
  pb_data/
  pb_hooks/
  pb_migrations/
  pb_public/
  pb_pages/
    +config.ts
    +layout.ets
    index.ets
    about.ets
    _private/
      nav.ets
    assets/
      app.css
    products/
      [id].ets
      [id].ts
    feeds/
      latest.ts
```

## Special names

A file or folder name starting with one of these characters is never a URL:

| Prefix | Meaning |
|---|---|
| `+` | Framework file: `+layout.ets` (or `+layout.tsx`) and `+middleware.ts`, one of each per folder. `+config.ts`, `+error.ets` and `+404.ets` are allowed only at the top of `pb_pages`. Any other `+` name, any other extension (such as `+layout.md`), and a top-only file in a subfolder are errors at startup. |
| `_` | Private. `_private/` folders hold partials and modules for `include()` and `ctx.resolve()`. Other `_` names are ignored. |
| `-` | Ignored (drafts, notes). |
| `.` | Ignored (dotfiles), except a top-level `.well-known/`, which is served as static files (ACME challenges, `security.txt`, app links). |

`node_modules/` is never routed either.

## Groups

A folder named `(name)` groups files without adding a URL segment: `(marketing)/pricing.ets` serves `/pricing`. A group can have its own `+layout.ets`, `+middleware.ts` and `_private/`.

## What else

- A `.ts` file beside a template of the same name is that page's code (`products/[id].ts`); any other `.ts` is an
  [endpoint](api-routes.md) at its own URL (`feeds/latest.ts` serves `/feeds/latest`).
- Every other file, `.js` included, is a [static file](static-content.md), served as-is.
- Page code can `require()` modules by name from `_private/` folders (nearest first), then `pb_pages/node_modules`, then
  `pb_hooks/node_modules` and the app folder's `node_modules`, then the app's usual module paths.
- Pages' own file access (routes, static files, includes, assets) never leaves `pb_pages`.
- A `pb_pages/api/` folder is an error at startup, because `/api/` belongs to PocketBase.
