// POST /admin/check {clip}: transcribes a recording and compares it with the passage it was read from, so a stumble,
// a skipped line or a cut-off ending shows before the recording becomes the narrator. Superusers only; the cost is
// recorded like any other call (kind check) but the budget doesn't stop it.
const MODEL = process.env.STORY_TRANSCRIBE_MODEL || 'vultr/whisper-large-v3-turbo'

// words to compare, with a few homophones a transcriber can't tell apart folded together
const SAME: { [w: string]: string } = { too: 'to', two: 'to', there: 'their', theyre: 'their', four: 'for' }
const words = (s: string) => (s.toLowerCase().replace(/[’']/g, '').match(/[a-z0-9]+/g) || []).map((w) => SAME[w] || w)

/** How much of the passage was heard, in order: the longest common run of words, and the passage's words not in it. */
function compare(passage: string[], heard: string[]) {
  const n = passage.length, m = heard.length
  const L: number[][] = Array.from({ length: n + 1 }, () => new Array(m + 1).fill(0))
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) L[i][j] = passage[i] === heard[j] ? L[i + 1][j + 1] + 1 : Math.max(L[i + 1][j], L[i][j + 1])
  }
  const missing: number[] = []
  let i = 0, j = 0
  while (i < n && j < m) {
    if (passage[i] === heard[j]) {
      i++
      j++
    } else if (L[i + 1][j] >= L[i][j + 1]) {
      missing.push(i++)
    } else {
      j++
    }
  }
  while (i < n) missing.push(i++)
  // the last few words of the passage never heard: the recording stopped too soon
  const tail = Math.min(3, n)
  const cutOff = tail > 0 && missing.slice(-tail).length === tail && missing[missing.length - tail] === n - tail
  return { match: n ? L[0][0] / n : 0, missing: missing.map((k) => passage[k]), cutOff }
}

export async function post(ctx: any) {
  ctx.resolve('admin').superuserOnly(ctx)
  const story = ctx.resolve('story')
  const { clip } = ctx.req.body() || {}
  let r: any
  try {
    r = ctx.app.findRecordById('voices', String(clip || ''))
  } catch (_) {
    throw ctx.httpError(404, 'no such recording')
  }
  const started = Date.now()
  try {
    const t = await $llm.transcribe.transcribeAsync({ model: MODEL, audio: $llm.fromRecord(r, 'audio'), language: 'en' })
    const usd = story.record(ctx.app, 'check', t)
    const c = compare(words(r.getString('passage')), words(t.text))
    return ctx.res.json(200, { heard: t.text, match: Math.round(c.match * 100) / 100, missing: c.missing, cutOff: c.cutOff,
      model: t.model, costUSD: usd, seconds: Math.round((Date.now() - started) / 100) / 10 })
  } catch (err: any) {
    return ctx.res.json(502, { message: String(err && err.message ? err.message : err) })
  }
}
