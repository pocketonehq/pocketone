// Set up's runs (_private/trial.ts): a step that makes something does it in the background. Superusers only
// (admin/+middleware.ts); each call's cost is recorded.
//
// GET /admin/trial?step=story|pictures: that step's latest run, with every step finished so far, and the run it handed
// on (kept); {run: null} before the first.
// POST /admin/trial {step: "story", values, minutes, topic?}: writes a story with the given (unsaved) settings.
// POST /admin/trial {step: "pictures", values, count}: finds the spots for count pictures in the story step 2 handed on,
// their theme, and paints one reference picture.
// POST /admin/trial {step, keep: id}: hands that run on to the next step (Save and next).
const stepOf = (x: any) => (x === 'pictures' ? 'pictures' : 'story')

export function get(ctx: any) {
  const trial = ctx.resolve('trial')
  const step = stepOf(ctx.req.query('step'))
  return ctx.res.json(200, { run: trial.current(step), kept: trial.kept(step) })
}

export function post(ctx: any) {
  const story = ctx.resolve('story')
  const trial = ctx.resolve('trial')
  const body = ctx.req.body() || {}
  const step = stepOf(body.step)
  if (body.keep !== undefined) {
    const kept = trial.keep(step, String(body.keep))
    if (!kept) throw ctx.httpError(409, 'that isn\'t the latest finished run: generate first')
    return ctx.res.json(200, { kept })
  }
  const t = story.tuning(ctx.app, body.values || {})
  if (step === 'pictures') {
    const count = Math.max(1, Math.min(20, Math.round(Number(body.count) || 5)))
    try {
      return ctx.res.json(200, { run: trial.startPictures(story, ctx.app, t, count) })
    } catch (err: any) {
      throw ctx.httpError(409, err.message)
    }
  }
  const len = story.storyLength(t, body.minutes)
  const asked = typeof body.topic === 'string' ? body.topic.trim().slice(0, 500) : ''
  return ctx.res.json(200, { run: trial.startStory(story, ctx.app, t, len.minutes, asked) })
}
