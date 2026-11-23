// GET /admin/tuning: the tuning in effect, the defaults, the speech model's voices, and the recordings a clone can imitate.
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
  const clips = ctx.app.findRecordsByFilter('voices', '', '-created', 50, 0).map((r: any) => ({
    id: r.id, name: r.getString('name'), passage: r.getString('passage'), created: r.getString('created'),
    // superusers only, played on this screen with the signed-in superuser's file token
    file: r.getString('audio'),
  }))
  // LLM Search's buckets, for Knowledge; the stories bucket is Story Time's own
  let buckets: any[] = [], stories: any = null
  try {
    buckets = $llm.search.buckets().map((b: any) => ({ name: b.name, title: b.title, documents: b.documents, chunks: b.chunks }))
    stories = buckets.find((b: any) => b.name === story.STORIES_BUCKET) || { name: story.STORIES_BUCKET, documents: 0 }
    buckets = buckets.filter((b: any) => b.name !== story.STORIES_BUCKET)
  } catch (_) {
    // LLM Search is off
  }
  return ctx.res.json(200, { values, defaults: story.DEFAULT_TUNING, voices, clips, buckets, stories, spentToday: story.spentToday(ctx.app) })
}

export async function post(ctx: any) {
  ctx.resolve('admin').superuserOnly(ctx)
  const story = ctx.resolve('story')
  const body = ctx.req.body() || {}
  const values = story.saveTuning(ctx.app, body.reset ? null : body.values || {})
  return ctx.res.json(200, { values })
}
