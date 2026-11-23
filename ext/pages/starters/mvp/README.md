# MVP.css starter

One page styled with [MVP.css](https://andybrewer.github.io/mvp/): classless CSS that makes plain semantic HTML look good, for a minimal, responsive, mobile-friendly site with no build step.

`.ets` is EJS whose tags hold TypeScript; Pages strips the types when it loads the page. Code files you add (a page's `.ts`, endpoints, `+middleware`, `+config`) are `.ts`.

```bash
pocketone pages create myapp --starter mvp
cd myapp
pocketone superuser upsert you@example.com <password> --dir ./pb_data
pocketone serve --dir ./pb_data
```

Then open http://127.0.0.1:8090.

## Files

```
pb_pages/
  index.ets
```

Live demo: [pocketpages-mvp.pockethost.io](https://pocketpages-mvp.pockethost.io).
