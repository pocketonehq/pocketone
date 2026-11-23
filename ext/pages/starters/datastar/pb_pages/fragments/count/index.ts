// POST /fragments/count (Datastar @post): bump the counter. index.ets renders the
// #count card, and the Datastar add-on sends it back as a patchElements event
// because the request carries `Datastar-Request: true`.
module.exports = {
  post(ctx) {
    ctx.app.runInTransaction(() => {
      ctx.store('count', (ctx.store('count') || 1) + 1)
    })
  },
}
