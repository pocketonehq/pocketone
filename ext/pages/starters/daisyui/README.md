# DaisyUI starter

A layout with a responsive navbar and drawer, built with [Tailwind CSS](https://tailwindcss.com/) and [DaisyUI](https://daisyui.com/) (cupcake theme). The compiled `pb_pages/app.css` is included and linked through `asset()`, so the app runs as-is; npm is only for rebuilding the CSS.

Templates are `.ets` (EJS whose tags hold TypeScript) and code files (`+config`, `+middleware`, and the `.ts` beside a page or serving an endpoint) are `.ts`; Pages strips the types when it loads them, so there is still no build step.

```bash
pocketone pages create myapp --starter daisyui
cd myapp
pocketone superuser upsert you@example.com <password> --dir ./pb_data
pocketone serve --dir ./pb_data
```

Then open http://127.0.0.1:8090.

To change styles, rebuild the CSS while you edit (Tailwind scans `pb_pages/**/*.{ets,md}`):

```bash
npm i
npm run dev:css
```

## Files

```
pb_pages/
  +layout.ets                    the navbar and drawer around every page
  index.ets
  app.css                        compiled Tailwind and DaisyUI
```

Live demo: [pocketpages-daisyui.pockethost.io](https://pocketpages-daisyui.pockethost.io).
