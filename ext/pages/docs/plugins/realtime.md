---
title: Realtime Plugin
description: The built-in realtime plugin sends messages to PocketBase realtime subscribers with ctx.realtime.send().
---

# Realtime Plugin

The `realtime` plugin sends messages to [PocketBase realtime](https://pocketbase.io/docs/realtime/) subscribers.

```ts
// pb_pages/+config.ts
module.exports = {
  plugins: ['realtime'],
}
```

## `ctx.realtime.send(topic, data, { filter }?)`

Sends `data` to the subscribers of `topic`. `filter(client)` may limit the recipients.

```ts
// pb_pages/ping.ts
module.exports = {
  post(ctx) {
    ctx.realtime.send('ping', { at: new Date().toISOString() })
    return ctx.res.json({ ok: true })
  },
}
```

On the client, subscribe with the PocketBase JS SDK:

```html
<script src="https://unpkg.com/pocketbase/dist/pocketbase.umd.js"></script>
<script>
  const pb = new PocketBase(location.origin)
  pb.realtime.subscribe('ping', (data) => console.log('Received:', data))
</script>
```

## Filtering recipients

```ts
ctx.realtime.send('alerts', 'Maintenance at 10:00', {
  filter: (client) => !!client.get('auth'),
})
```

## Sending HTML

For hypermedia libraries (HTMX, Datastar), send rendered HTML. `include()` is available in templates, so render the partial there:

```ts
// pb_pages/xapi/chat/index.ts
module.exports = {
  post() {}, // accept POST; the page renders and sends
}
```

```ets
<!-- pb_pages/xapi/chat/index.ets -->
<%% ctx.realtime.send('chat', include('chat-msg', { message: ctx.req.body().message })) %>
```

See [Realtime Events](../realtime.md) and the [Datastar add-on](datastar.md), which can send Datastar patches over realtime.
