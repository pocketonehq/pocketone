// POST /story {prompt?, minutes?}: saves (no prompt: the model picks the topic) a new story record and starts making it,
// without waiting: making one takes a few
// minutes, and a request may wait 30 seconds at most. The page then polls GET /progress?id= until its status is done
// or failed.
export async function post(ctx: any) {
  const story = ctx.resolve('story')
  const { prompt, minutes } = ctx.req.body()
  if (prompt !== undefined && typeof prompt !== 'string') throw ctx.httpError(400, 'send {prompt?, minutes?}')
  story.watchAll(ctx.app) // stories that stalled with nobody watching are retried (or failed) before counting the limits
  // the models must be there and answering, and today's spending under the budget
  const no = await story.refusal(ctx.app, 'story')
  if (no) return ctx.res.json(no.status, { message: no.message, reason: no.reason })

  const r = new Record(ctx.app.findCollectionByNameOrId('stories'))
  r.set('prompt', (prompt || '').trim().slice(0, 500) || story.ANY_TOPIC) // empty: the model picks the topic
  // the length the slider asked for, held to the settings' range; the pictures follow from it (picturesPerMinute)
  const len = story.storyLength(story.tuning(ctx.app), minutes)
  r.set('minutes', len.minutes)
  r.set('picture_count', len.pictures)
  r.set('status', 'queued')
  r.set('progress', 'Getting ready')
  ctx.app.save(r)

  // not awaited: the story is made after this request has answered. make() saves its own failures on the record.
  story.make(ctx.app, r.id)
  return ctx.res.json(200, { id: r.id })
}
