# Datastar starter

A realtime chat with rooms and a live counter, built with [Datastar](https://data-star.dev/) and the Datastar add-on (`_private/datastar.js`, listed as `'datastar'`) and the built-in `realtime` plugin. Datastar's `@post` calls (the button, or Enter in the message box) hit `/fragments/chat` and `/fragments/count`, sending the page's signals as the JSON body that `ctx.datastar.signals()` reads. `/fragments/count` has a template: because the request carries `Datastar-Request: true`, the rendered page comes back as a `patchElements` event. `/fragments/chat` is an endpoint (`fragments/chat.ts`, no template), so its `post()` builds the response itself. A chat message is also broadcast to every tab in the same room with `ctx.datastar.realtime.patchElements`, and `ctx.datastar.patchSignals` clears the input. Open two tabs to watch it update.

Templates are `.ets` (EJS whose tags hold TypeScript) and code files (`+config`, `+middleware`, and the `.ts` beside a page or serving an endpoint) are `.ts`; Pages strips the types when it loads them, so there is still no build step.

```bash
pocketone pages create myapp --starter datastar
cd myapp
pocketone superuser upsert you@example.com <password> --dir ./pb_data
pocketone serve --dir ./pb_data
```

Then open http://127.0.0.1:8090.

## Files

```
pb_pages/
  +config.ts                     the datastar add-on and the realtime plugin
  (site)/index.ets               the lobby
  (site)/[roomName]/index.ets    a chat room
  _private/                      the chat, message and counter partials, and datastar.js
  fragments/chat.ts              posts a message and patches every tab in the room
  fragments/count/               the counter, returned as a patchElements event
```

Live demo: [pocketpages-datastar.pockethost.io](https://pocketpages-datastar.pockethost.io).
