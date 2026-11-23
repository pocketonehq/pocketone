# TSX starter

Server-rendered components: a `+layout.tsx`, an `index.tsx` page built from a `_private/Feature.tsx` component, and `index.ts` beside it, which loads the page's data. TSX renders straight to HTML on the server (no React, no client runtime, no hydration), and Pages strips the types when it loads each file, so there is no build step. Pick one template style per app: TSX here, or `.ets` in the other kits.

```bash
pocketone pages create myapp --starter tsx
cd myapp
pocketone superuser upsert you@example.com <password> --dir ./pb_data
pocketone serve --dir ./pb_data
```

Then open http://127.0.0.1:8090.

## Files

```
pb_pages/
  +layout.tsx
  index.ts                       loads the page's data
  index.tsx
  _private/Feature.tsx           a component
```
