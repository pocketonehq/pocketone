// GET /admin/tuning: the tuning in effect, the defaults, and the speech model's voices.
// POST /admin/tuning {values}: saves it (known keys only, on top of the defaults); {reset: true} puts the defaults back.
// Superusers only.
export async function get(ctx: any) {
  ctx.resolve('admin').superuserOnly(ctx)
  const story = ctx.resolve('story')
  const values = story.tuning(ctx.app)
  let voices: any[] = []
  try {
    voices = await $llm.speech.voicesAsync(values.models.speech)
  } catch (_) {
    // the provider doesn't say, or isn't set up
  }
  return ctx.res.json(200, { values, defaults: story.DEFAULT_TUNING, voices, spentToday: story.spentToday(ctx.app) })
}

export async function post(ctx: any) {
  ctx.resolve('admin').superuserOnly(ctx)
  const story = ctx.resolve('story')
  const body = ctx.req.body() || {}
  const values = story.saveTuning(ctx.app, body.reset ? null : body.values || {})
  return ctx.res.json(200, { values })
}
