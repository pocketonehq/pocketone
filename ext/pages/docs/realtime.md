---
title: Realtime Events
description: Send realtime messages from pages with the realtime plugin, and receive them in the browser with the PocketBase JS SDK.
---

# Realtime Events

PocketBase pushes realtime messages to browsers over server-sent events. In Pages, page code sends, and browsers receive.

## Sending from a page

Turn on the [`realtime` plugin](plugins/realtime.md) and call `ctx.realtime.send()`:

```ts
// pb_pages/+config.ts
module.exports = { plugins: ['realtime'] }
```

```ts
// pb_pages/rooms/[id].ts
module.exports = {
  post(ctx) {
    ctx.realtime.send(`rooms/${ctx.params.id}/delta`, ctx.req.body())
    return ctx.res.json({ ok: true })
  },
}
```

Limit recipients with a filter:

```ts
ctx.realtime.send('alerts', 'Hi', { filter: (client) => !!client.get('auth') })
```

Page code can't subscribe to realtime topics itself; it only sends.

## Receiving in the browser

Use the [PocketBase JS SDK](https://github.com/pocketbase/js-sdk#realtimeservice):

```js
const pb = new PocketBase(location.origin)

const unsubscribe = await pb.realtime.subscribe(`rooms/${ROOM_ID}/delta`, (e) => {
  console.log('Received delta:', e)
})

// Later
unsubscribe()
```

The SDK reconnects on its own and resubscribes to its topics after a reconnect.

## Hypermedia

To push HTML instead of data, see [HTMX Integration](htmx.md#realtime-updates) and the [Datastar add-on](plugins/datastar.md#broadcasting).
