---
title: Realtime Plugin
description: The built-in realtime plugin sends PocketBase realtime messages with ctx.realtime.send() and listens to record changes with ctx.realtime.subscribe().
---

# Realtime Plugin

The `realtime` plugin sends messages to [PocketBase realtime](https://pocketbase.io/docs/realtime/) subscribers, and
lets a page listen to record changes itself, to stream them to the browser.

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

## Listening to record changes

`ctx.realtime.subscribe(topic, { filter, expand, fields }?)` listens to `topic` the way a browser does on `/api/realtime`, as the visitor of this request, and gives each message
to a `for await` loop. Pair it with [`ctx.res.sse()`](../ctx/res.md#server-sent-events) to stream changes to the
browser with no polling:

```ts
// pb_pages/posts/live.ts
module.exports = {
  async get(ctx) {
    const stream = ctx.res.sse({ keepAlive: 30 }) // a comment every 30 s, so proxies keep a quiet stream open
    for await (const { action, record } of ctx.realtime.subscribe('posts/*', { filter: 'published = true' })) {
      stream.send(action, record)
    }
  },
}
```

- **Topics** are PocketBase's: `posts/*` for every record of a collection, `posts/RECORD_ID` for one. Each message is
  `{ action, record }`, with `action` `create`, `update` or `delete`. Any other topic gives the data sent to it
  with `ctx.realtime.send()`, so pages can talk to each other (a chat room needs no collection).
- **Rules apply as for the visitor.** The collection's list rule (view rule for one record) decides what reaches the
  page, with `ctx.auth` as `@request.auth`, kept current when that user's record changes. `filter`, `expand` and
  `fields` work as in the JS SDK's subscribe options. Hidden fields stay hidden unless the visitor is a superuser.
- **It ends with the request**: when the visitor leaves, the handler returns, or the stream reaches the app's await
  time (15 minutes by default; `EventSource` and Datastar reconnect). `close()` ends it early, and a `for await` that
  breaks out of the loop closes it too.
- **A page that falls behind is cut off.** Up to 64 messages wait for the loop. If more arrive before the page reads
  them, the subscription ends and the loop throws, so the client reconnects and loads fresh state instead of
  missing an update.
- Messages for different records may arrive in a different order than the changes were made, as on `/api/realtime`.

### With Datastar

With the [Datastar add-on](datastar.md), each change becomes a patch:

```ets
<!-- pb_pages/todos/index.ets -->
<ul id="todos" data-init="@get('/todos/live')">
  <%% for (const todo of ctx.app.findAllRecords('todos')) { %>
    <%%- include('todo', { todo }) %>
  <%% } %>
</ul>
```

```ets
<!-- pb_pages/todos/_private/todo.ets -->
<li id="todo-<%%= todo.id %>"><%%= todo.title %></li>
```

```ts
// pb_pages/todos/live.ts
module.exports = {
  async get(ctx) {
    ctx.datastar.open() // start the response now, not with the first change
    for await (const { action, record } of ctx.realtime.subscribe('todos/*')) {
      if (action === 'delete') {
        ctx.datastar.patchElements('', { selector: `#todo-${record.id}`, mode: 'remove' })
      } else if (action === 'create') {
        ctx.datastar.patchElements(ctx.include('todo', { todo: record }), { selector: '#todos', mode: 'append' })
      } else {
        ctx.datastar.patchElements(ctx.include('todo', { todo: record }))
      }
    }
  },
}
```

The add-on's stream sends a keep-alive comment every 30 seconds while the route waits.
