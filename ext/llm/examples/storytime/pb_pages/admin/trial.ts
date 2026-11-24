// Set up's "Try a story" (_private/trial.ts). POST /admin/trial {values, minutes, topic?}: starts writing a story's
// text with the given (unsaved) settings, in the background, and answers at once with the trial. GET /admin/trial: the
// latest trial, with every step finished so far ({trial: null} before the first), and the one step 2 handed on (kept).
// POST /admin/trial {keep: id}: hands that trial on to the next step (Save and next on step 2). Superusers only
// (admin/+middleware.ts); each call's cost is recorded (kinds sample-story, inspire, search).
export function get(ctx: any) {
  const trial = ctx.resolve('trial')
  return ctx.res.json(200, { trial: trial.current(), kept: trial.kept() })
}

export function post(ctx: any) {
  const story = ctx.resolve('story')
  const { values, minutes, topic, keep } = ctx.req.body() || {}
  if (keep !== undefined) {
    const kept = ctx.resolve('trial').keep(String(keep))
    if (!kept) throw ctx.httpError(409, 'that story isn\'t the latest finished one: generate a story first')
    return ctx.res.json(200, { kept })
  }
  const t = story.tuning(ctx.app, values || {})
  const len = story.storyLength(t, minutes)
  const asked = typeof topic === 'string' ? topic.trim().slice(0, 500) : ''
  return ctx.res.json(200, { trial: ctx.resolve('trial').start(story, ctx.app, t, len.minutes, asked) })
}
