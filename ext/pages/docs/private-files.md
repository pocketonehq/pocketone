---
title: Working with Private Files and Directories in Pages
description: How _private folders hold partials and TypeScript modules for include() and ctx.resolve(), and which names are never routed.
---

# Working with Private Files and Directories

## Names that are never routed

| Prefix | Meaning |
|---|---|
| `+` | Framework file (`+layout.ets`, `+middleware.ts`, ...) |
| `_` | Private. `_private/` folders hold partials and modules. Other `_` names are ignored. |
| `-` | Ignored (drafts, notes) |
| `.` | Ignored (dotfiles) |

```
pb_pages/
  _private/        # private (not routable)
  -drafts/         # ignored
  helpers/         # normal folder (routable)
  +middleware.ts   # framework file (not routable)
  +layout.ets      # framework file (not routable)
  index.ets        # page
```

## The `_private` folder

`_private/` folders serve two purposes:

1. Template partials, rendered with [`include()`](partials.md)
2. TypeScript modules and other files, loaded with [`ctx.resolve()`](ctx/resolve.md)

Both use the same lookup: `_private/` in the current template's folder, then each parent folder up to `pb_pages/`. A name starting with `/` looks only in `pb_pages/_private/`.

```
pb_pages/
  _private/
    header.ets
    format.ts
  products/
    _private/
      product-card.ets
      queries.ts
    index.ets
```

```ets
<!-- In products/index.ets -->
<%%
  const queries = ctx.resolve('queries')   // products/_private/queries.ts
  const format = ctx.resolve('format')     // _private/format.ts
%>
<%%- include('header') %>                  <!-- _private/header.ets -->
<%% for (const p of queries.featured(ctx.app)) { %>
  <%%- include('product-card', { p, format }) %>
<%% } %>
```

## Modules

A module in `_private/` is a CommonJS file:

```ts
// pb_pages/products/_private/queries.ts
module.exports = {
  featured: (app) => app.findRecordsByFilter('products', 'featured = true', '-created', 10),
}
```

## Notes

1. Files in `_private/` are never served.
2. Keep secrets in environment variables (see [Managing Secrets](secrets.md)), not in files.
3. Keep private files close to where they're used, and hoist them up when several sections share them.
