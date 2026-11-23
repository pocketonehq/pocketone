// GET /progress?id=<id>: a story's status, title and progress, for its page's poll while it's being made. Asking also
// checks the story hasn't stalled (story.watch): a step past its time limit is retried once, from the last saved step.
export async function get(ctx: any) {
  const story = ctx.resolve('story')
  const id = ctx.req.query('id') ?? ''
  if (!id) throw ctx.httpError(400, 'send ?id=')
  let r: any
  try {
    r = ctx.app.findRecordById('stories', id)
  } catch (_) {
    throw ctx.httpError(404, 'No such story')
  }
  story.watch(ctx.app, id)
  r = ctx.app.findRecordById('stories', id)
  return ctx.res.json(200, { status: r.getString('status'), title: r.getString('title'), progress: r.getString('progress') })
}
