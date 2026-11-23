---
title: Managing Secrets in Pages
description: Read API keys and other secrets from environment variables with ctx.env().
---

# Managing Secrets in Pages

Keep API keys, passwords and other secrets in environment variables, and read them with `ctx.env(name)`. It returns `''` when the variable is unset.

```ts
// pb_pages/checkout.ts
module.exports = {
  post(ctx) {
    const key = ctx.env('STRIPE_SECRET_KEY')
    if (!key) throw new Error('STRIPE_SECRET_KEY is not set')
    // ...
  },
}
```

`ctx.env` reads the app's environment. Under a multitenant host's sandbox, it reads the instance's environment.

## Best practices

1. **Never put secrets in the page.** Anything a template outputs goes to the browser.

   ```ets
   <!-- DON'T -->
   <meta name="api-key" content="<%%= ctx.env('API_KEY') %>">

   <!-- OK: a public value -->
   <meta name="api-endpoint" content="<%%= ctx.env('PUBLIC_API_ENDPOINT') %>">
   ```

2. **Check for required variables** and fail clearly when one is missing.
3. **Use descriptive names**: `STRIPE_SECRET_KEY`, not `KEY`.
4. **Never commit secrets** to version control, and use different values in development and production.

## Setting environment variables

```bash
# Shell
export STRIPE_SECRET_KEY=sk_test_...
pocketone serve
```

```dockerfile
# Docker
ENV STRIPE_SECRET_KEY=sk_test_...
```
