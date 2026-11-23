// POST /fragments/chat (Datastar @post, with the page's signals as the JSON body). There's no template in this
// folder, so this handler sends the whole response, and any other method answers 405.
module.exports = {
  post(ctx) {
    const { clientId, roomName, from, message } = ctx.datastar.signals()

    // Save the message in the room's ephemeral store. No HTML sanitizing needed: chat-messages.ets prints it with
    // <%= %>, which escapes it.
    let messages = []
    ctx.app.runInTransaction(() => {
      messages = ctx.store(`room_${roomName}`) || []
      const text = String(message || '').trim().slice(0, 500)
      if (text.length > 0) {
        messages.push({ from, message: text })
        ctx.store(`room_${roomName}`, messages)
      }
    })
    ctx.log.debug(`${clientId} sent a message to ${roomName}`)

    const html = ctx.include('chat-messages.ets', { messages })

    // Broadcast the chat box to every realtime client in this room. A client joins a room by sending a message
    // there: the sender is tagged as it passes through the filter.
    ctx.datastar.realtime.patchElements(html, {}, {
      filter: (client) => {
        if (client.id() === clientId) client.set('roomName', roomName)
        return client.get('roomName') === roomName
      },
    })

    // Answer the sender: the new chat box, and an empty message input.
    ctx.datastar.patchElements(html)
    ctx.datastar.patchSignals({ message: '' })
  },
}
