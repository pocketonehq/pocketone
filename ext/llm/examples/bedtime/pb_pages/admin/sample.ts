// POST /admin/sample {what, values, prompt?}: tries the given (unsaved) tuning on one piece, so it can be heard or seen
// before saving. what: "voice" (reads the sample text), "music" (20 seconds), "picture", or "story" (writes a 1-minute
// story's text). Superusers only; the cost is recorded like any other call (kind sample-<what>) but the budget doesn't
// stop it.
const dataURL = (f: any) => (f && f.data ? `data:${f.type || 'application/octet-stream'};base64,${f.data}` : f && f.url)

// audio is saved as a samples record and played from /api/files/...: the dashboard plays media only from its own
// origin (its Content-Security-Policy blocks data: audio). The last 20 samples are kept.
async function audioURL(app: any, what: string, f: any): Promise<string> {
  if (!f) return ''
  const r = new Record(app.findCollectionByNameOrId('samples'))
  r.set('what', what)
  r.set('audio', f.data ? $llm.file(f) : await $llm.fileAsync(f))
  app.save(r)
  for (const old of app.findRecordsByFilter('samples', '', '-created', 100, 20)) app.delete(old)
  return `/api/files/samples/${r.id}/${r.getString('audio')}`
}

export async function post(ctx: any) {
  ctx.resolve('admin').superuserOnly(ctx)
  const story = ctx.resolve('story')
  const { what, values, prompt } = ctx.req.body() || {}
  const t = story.tuning(ctx.app, values || {})
  const started = Date.now()
  const done = (r: any, out: any) => {
    const usd = story.record(ctx.app, `sample-${what}`, r)
    return ctx.res.json(200, { ...out, model: r.model, costUSD: usd, seconds: Math.round((Date.now() - started) / 100) / 10 })
  }
  try {
    if (what === 'voice') {
      if (t.voice.voice === 'design' && !t.voice.instructions.trim()) {
        return ctx.res.json(400, { message: 'design makes a voice from Instructions: describe the voice there first' })
      }
      const call = story.speechCall(t, t.voice.sample || story.DEFAULT_TUNING.voice.sample, 'en')
      const r = await $llm.speech.speakAsync(call)
      return done(r, { audio: await audioURL(ctx.app, what, r.file), call: { ...call, text: undefined } })
    }
    if (what === 'music') {
      const r = await $llm.music.createAsync(story.musicCall(t, prompt || 'Quiet ambient music on soft piano and strings', 20))
      return done(r, { audio: await audioURL(ctx.app, what, r.files && r.files[0]) })
    }
    if (what === 'picture') {
      const r = await $llm.image.generateAsync(story.pictureCall(t, prompt || 'Muted oil painting, soft light, deep blues and ambers',
        'An old observatory on a hill under a starry sky'))
      return done(r, { image: dataURL(r.files[0]) })
    }
    if (what === 'story') {
      const w = await story.write(t, prompt || story.ANY_TOPIC, 1)
      return done(w.result, { story: { title: w.story.title, topic: w.story.topic, look: w.story.look, sentences: w.story.sentences } })
    }
    if (what === 'idea') {
      const idea = await story.inspire(ctx.app, [], t)
      return ctx.res.json(200, { idea, seconds: Math.round((Date.now() - started) / 100) / 10 })
    }
    throw ctx.httpError(400, 'what is voice, music, picture, story or idea')
  } catch (err: any) {
    if (err && err.status) throw err
    return ctx.res.json(502, { message: String(err && err.message ? err.message : err) })
  }
}
