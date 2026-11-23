---
title: Using Middleware in Pages
description: +middleware.ts files run root first for every route below them, with one (ctx, next) signature.
---

# Middleware

Middleware files (`+middleware.ts`) run before your handlers and templates. Use them to:

- Check authentication
- Load data a whole section shares
- Set headers
- Validate requests

## Basic example

```ts
// pb_pages/admin/+middleware.ts
module.exports = (ctx, next) => {
  if (!ctx.auth) return ctx.res.redirect('/login')
  ctx.data.section = 'admin'
  return next()
}
```

- One signature: `(ctx, next)`. Call `next()` to continue, and return its result.
- Returning without calling `next()` ends the request. If no response was sent, that's an error (a `500`; in dev mode the error page names the middleware file).
- Share data through `ctx.data`.
- Middleware can be `async`. `await next()` returns once the rest of the request has run, and throws what it threw.

```ts
// pb_pages/+middleware.ts
module.exports = async (ctx, next) => {
  ctx.data.unread = await ctx.app.countRecords.async('notifications', $dbx.hashExp({ read: false }))
  const started = Date.now()
  await next()
  ctx.log.info('rendered', ctx.route.path, Date.now() - started, 'ms')
}
```

## Order

Every `+middleware.ts` from `pb_pages/` down to the route's folder runs, root first. Groups count too.

```
pb_pages/
  +middleware.ts         # runs first (all routes)
  products/
    +middleware.ts       # runs second (/products/...)
    [id]/
      +middleware.ts     # runs third (/products/[id])
```

The full order for a request is:

1. Plugins' `onRequest`
2. Middleware, root first
3. The route's `.ts`, the page's code or the endpoint (`load`, then the method's handler)
4. The template, then layouts, innermost first
5. The response

If any step sends a response, the later steps are skipped.

## Common uses

### Auth guard

```ts
module.exports = (ctx, next) => {
  if (!ctx.auth) return ctx.res.redirect('/login')
  return next()
}
```

### Request validation

```ts
// pb_pages/products/[id]/+middleware.ts
module.exports = (ctx, next) => {
  if (!/^[a-z0-9]+$/.test(ctx.params.id)) throw ctx.httpError(400, 'Invalid ID')
  return next()
}
```

### Shared data

```ts
// pb_pages/+middleware.ts
module.exports = (ctx, next) => {
  ctx.data.categories = ctx.app.findRecordsByFilter('categories', 'active = true', 'name', 100)
  return next()
}
```

```ets
<nav>
  <%% for (const c of data.categories) { %>
    <a href="/categories/<%%= c.id %>"><%%= c.getString('name') %></a>
  <%% } %>
</nav>
```

### Headers

```ts
module.exports = (ctx, next) => {
  ctx.res.header('X-Frame-Options', 'DENY')
  return next()
}
```
