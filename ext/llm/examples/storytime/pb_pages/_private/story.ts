// Makes a story (by default a bedtime story for adults: one topic, told in depth) from a prompt, in the background, saving each step on its
// `stories` record so the page can show progress (it polls the record) and play the story once it's done.
//
//   1. write: the story, one sentence per line
//   2. read it aloud: one voiceover of the whole story
//   3. align: when each sentence and word is said in the voiceover ($llm.align)
//   4. place the pictures: the model reads the timed transcript and picks where each picture begins and what it shows,
//      plus a prompt for the song
//   5. paint the pictures (all at once), and 6. compose the song, at the same time
//
// The title and caption, the story lengths, the models, the narrator's voice, the prompts and the budget are the app's
// settings: _private/defaults.json (the same format as Export), with whatever a superuser saved on top (one JSON blob
// in $kv, see loadSaved). Every $llm call is awaited, so the work runs
// off the app's JS VM: the app keeps serving pages (and this story's own progress polls) while the models work. The
// request that starts a story doesn't wait for it; see story.ts.

/** About how many words a narrator reads in a minute at a bedtime pace. */
const WORDS_PER_MINUTE = 140

/** How many stories may be in the making at once, and how many may start in an hour. */
export const LIMITS = {
  making: Number(process.env.STORY_MAX_MAKING || 3),
  perHour: Number(process.env.STORY_MAX_PER_HOUR || 20),
}

/** The languages a speech model speaks, where it's known (GET /v1/audio/voices?model= on Vultr lists them). */
const SPEAKS: { [model: string]: string[] } = { 'vultr/auk': ['en', 'zh'] }

/** What the front page sends when the prompt is left empty: the model picks the topic. */
export const ANY_TOPIC = 'Your pick'

export type Tuning = {
  /** what the front page says: the title (its last word is highlighted), the caption under it, the prompt's placeholder
   * and the note under the buttons */
  site: { title: string; caption: string; placeholder: string; note: string }
  /** how long a story may be: the front page's slider runs from minMinutes (short) to maxMinutes (long), starting at
   * defaultMinutes; a story gets picturesPerMinute pictures a minute, at most maxPictures */
  length: { minMinutes: number; maxMinutes: number; defaultMinutes: number; picturesPerMinute: number; maxPictures: number }
  /** the player's final mix: the narration's loudness (dBFS, measured over its sounding parts) and how many dB the
   * music sits under it; every story's files are levelled to these when it plays */
  mix: { voiceDb: number; musicUnderDb: number }
  /** each as provider/model: a provider added in Settings → LLM, then its model */
  models: { text: string; idea: string; image: string; speech: string; speechOther: string; align: string; music: string }
  /** the narrator, for the speech model (not speechOther, which reads other languages with its own voice) */
  voice: {
    voice: string // the model's voice id: "default", a built-in speaker, "design" (a voice made from instructions) or "clone"
    instructions: string // how to read (a built-in voice), or the voice itself (design); not used by a clone
    clip: string // with "clone": the voices record whose recording the narrator imitates
    seed: number | null // the same seed gives the same generated voice every time
    speed: number | null // 1 is normal
    sample: string // what Sample reads
  }
  /**
   * The system prompts. write: {minutes} ("3 minutes"), {minWords}, {maxWords}, {anyTopic}; place: {n}. The JSON shape
   * each must answer in is fixed (WRITE_SCHEMA, PLACE_SCHEMA, the inspire schema), so keep the field names the prompts
   * mention.
   */
  prompts: { write: string; place: string; inspire: string }
  /** added after every picture's prompt */
  picture: string
  /** added after the music prompt the story gets; seconds of song (it loops) */
  music: { suffix: string; seconds: number }
  /** the most spent on models in a day (UTC), in US dollars */
  budget: number
}

/** The shipped defaults: an export file (settings, and recordings an Import would add). */
const SHIPPED = require('./defaults.json')

/** The defaults: defaults.json's settings, with the models and budget a deploy may set in its environment. */
export const DEFAULT_TUNING: Tuning = (() => {
  const t = JSON.parse(JSON.stringify(SHIPPED.settings))
  const env = process.env
  t.models.text = env.STORY_TEXT_MODEL || t.models.text
  t.models.idea = env.STORY_IDEA_MODEL || env.STORY_TEXT_MODEL || t.models.idea
  t.models.image = env.STORY_IMAGE_MODEL || t.models.image
  t.models.speech = env.STORY_SPEECH_MODEL || t.models.speech
  t.models.speechOther = env.STORY_SPEECH_MODEL_OTHER || t.models.speechOther
  t.models.align = env.STORY_ALIGN_MODEL || t.models.align
  t.models.music = env.STORY_MUSIC_MODEL || t.models.music
  if (env.STORY_DAILY_BUDGET) t.budget = Number(env.STORY_DAILY_BUDGET)
  return t
})()

/** The minutes a story may run (whole minutes, minMinutes to maxMinutes) and the pictures that many minutes get. */
export function storyLength(t: Tuning, minutes?: any): { minutes: number; pictures: number } {
  const L = t.length
  const m = Math.max(L.minMinutes, Math.min(L.maxMinutes, Math.round(Number(minutes) || L.defaultMinutes)))
  return { minutes: m, pictures: Math.max(1, Math.min(L.maxPictures, Math.round(m * L.picturesPerMinute))) }
}


const isObject = (v: any) => v && typeof v === 'object' && !Array.isArray(v)

/** base with the saved values on top, key by key; only keys base has, of the same kind, are taken. */
export function merge(base: any, over: any): any {
  if (!isObject(over)) return base
  const out: any = Array.isArray(base) ? [...base] : { ...base }
  for (const k of Object.keys(base)) {
    if (!(k in over)) continue
    const b = base[k], o = over[k]
    if (isObject(b)) out[k] = merge(b, o)
    else if (b === null || typeof b === 'number') out[k] = o === null || o === '' ? null : Number.isFinite(Number(o)) ? Number(o) : b
    else if (typeof o === typeof b) out[k] = o
  }
  // a number that defaults to null (seed, speed) keeps its null default above; budget and seconds stay numbers
  if (base === DEFAULT_TUNING) {
    out.budget = Number.isFinite(out.budget) && out.budget >= 0 ? out.budget : DEFAULT_TUNING.budget
    out.music.seconds = Math.max(10, Math.min(240, Number(out.music.seconds) || DEFAULT_TUNING.music.seconds))
    // lengths: whole minutes from 1 to 30, min at most max, the default between them; pictures from 1 to 20
    const L = out.length, D = DEFAULT_TUNING.length
    const whole = (v: any, def: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, Math.round(Number.isFinite(Number(v)) && v !== null ? Number(v) : def)))
    L.minMinutes = whole(L.minMinutes, D.minMinutes, 1, 30)
    L.maxMinutes = whole(L.maxMinutes, D.maxMinutes, L.minMinutes, 30)
    L.defaultMinutes = whole(L.defaultMinutes, D.defaultMinutes, L.minMinutes, L.maxMinutes)
    L.maxPictures = whole(L.maxPictures, D.maxPictures, 1, 20)
    L.picturesPerMinute = Math.max(0.1, Math.min(10, Number(L.picturesPerMinute) || D.picturesPerMinute))
    const M = out.mix, DM = DEFAULT_TUNING.mix
    M.voiceDb = Math.max(-40, Math.min(-6, Number.isFinite(Number(M.voiceDb)) && M.voiceDb !== null ? Number(M.voiceDb) : DM.voiceDb))
    M.musicUnderDb = Math.max(0, Math.min(40, Number.isFinite(Number(M.musicUnderDb)) && M.musicUnderDb !== null ? Number(M.musicUnderDb) : DM.musicUnderDb))
  }
  return out
}

// The saved settings are one JSON blob in the app's key-value store ($kv, kv/v1): no collection, no migration.
const SAVED_NAME = 'storytime/settings'
const loadSaved = (): any => $kv.get(SAVED_NAME)
const storeSaved = (value: any) => (value === null ? $kv.delete(SAVED_NAME) : $kv.set(SAVED_NAME, value))

/** The app's settings: the defaults with what a superuser saved on top. With extra, those (unsaved) values on top again. */
export function tuning(app: any, extra?: any): Tuning {
  const t = merge(DEFAULT_TUNING, loadSaved())
  return extra ? merge(DEFAULT_TUNING, merge(t, extra)) : t
}

/** Saves the settings (only the known keys, merged onto the defaults); null puts the defaults back. */
export function saveTuning(app: any, values: any): Tuning {
  healthCache = null
  if (values === null) {
    storeSaved(null)
    return DEFAULT_TUNING
  }
  const t = merge(DEFAULT_TUNING, values)
  storeSaved(t)
  return t
}

/** A prompt template with its {placeholders} filled in. */
const fill = (template: string, vars: { [k: string]: string | number }) =>
  template.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m))

const writePrompt = (t: Tuning, minutes: number) => fill(t.prompts.write, {
  minutes: `${minutes} minute${minutes === 1 ? '' : 's'}`,
  minWords: Math.round(minutes * WORDS_PER_MINUTE * 0.9),
  maxWords: Math.round(minutes * WORDS_PER_MINUTE * 1.1),
  anyTopic: ANY_TOPIC,
})

const WRITE_SCHEMA = {
  type: 'object',
  properties: {
    title: { type: 'string' },
    language: { type: 'string' },
    topic: { type: 'string' },
    look: { type: 'string' },
    sentences: { type: 'array', items: { type: 'string' } },
  },
  required: ['title', 'language', 'topic', 'look', 'sentences'],
}

const PLACE_SCHEMA = {
  type: 'object',
  properties: {
    style: { type: 'string' },
    music: { type: 'string' },
    pictures: {
      type: 'array',
      items: {
        type: 'object',
        properties: { line: { type: 'integer' }, prompt: { type: 'string' } },
        required: ['line', 'prompt'],
      },
    },
  },
  required: ['style', 'music', 'pictures'],
}

/** A recording the narrator imitates: the voices record, as a speech call's reference (the clip and what it says). */
export function cloneReference(app: any, clip: string): { reference: any; name: string } {
  if (!clip) throw new Error('the clone voice needs a recording: record one on Settings → Story Time → Narrator')
  let r: any
  try {
    r = app.findRecordById('voices', clip)
  } catch (_) {
    throw new Error('the narrator\'s recording is gone: pick or record another on Settings → Story Time → Narrator')
  }
  return { reference: { audio: $llm.fromRecord(r, 'audio'), text: r.getString('passage') }, name: r.getString('name') }
}

/** A recording's name, for the story's splash (without reading the clip). */
export function clipName(app: any, clip: string): string {
  try {
    return app.findRecordById('voices', clip).getString('name')
  } catch (_) {
    return 'a recording'
  }
}

/**
 * The speech call's options for text in language: the tuned voice on the main speech model; the other model (for
 * languages the main one doesn't speak) gets only the language and seed, since it reads with its own voice. A clone
 * goes to both: the other model (chatterbox) clones too, so a story in another language is still read in that voice.
 */
export function speechCall(t: Tuning, text: string, language = 'en', app?: any): any {
  const other = SPEAKS[t.models.speech]?.includes(language) === false
  const options: any = { language }
  if (t.voice.seed !== null) options.seed = t.voice.seed
  const clone = t.voice.voice === 'clone' ? cloneReference(app, t.voice.clip) : null
  if (other) return clone ? { model: t.models.speechOther, text, options, reference: clone.reference } : { model: t.models.speechOther, text, options }
  const call: any = { model: t.models.speech, text, options }
  if (clone) {
    // a cloned voice takes its delivery from the recording: no voice id or instructions
    call.reference = clone.reference
    if (t.voice.speed !== null) call.speed = t.voice.speed
    return call
  }
  // design without a description has nothing to design from: the default voice reads instead
  const designWithout = t.voice.voice === 'design' && !t.voice.instructions.trim()
  if (t.voice.voice && t.voice.voice !== 'default' && !designWithout) call.voice = t.voice.voice
  if (t.voice.instructions.trim()) call.instructions = t.voice.instructions.trim()
  if (t.voice.speed !== null) call.speed = t.voice.speed
  return call
}

type Line = { index: number; text: string; start: number; end: number }
type Word = { line: number; text: string; start: number; end: number }
export type Picture = { start: number; prompt: string; image?: string }
export type Story = {
  title: string
  language: string // ISO 639-1
  topic: string
  look: string
  sentences: string[]
  // from here on, filled in as the story is made
  duration?: number // seconds of narration
  lines?: Line[] // when each sentence is said
  words?: Word[] // when each word is said
  style?: string
  music?: string
  pictures?: Picture[] // in order; each shows from its start to the next one's
  /** how it was made, kept since the tuning can change: seconds it took, and the models and voice that made it */
  made?: { seconds: number; models: { [step: string]: string }; voice: string }
}

export const OUT_OF_TOKENS = 'Out of tokens for today; try again tomorrow. Please enjoy something from our archives.'
export const RESTING = 'The storyteller can\'t reach its models right now. Please enjoy something from our archives.'

/** What a call cost, in millionths of a dollar (usage.costMicroUSD, 0 when not known). */
const micro = (u: any) => (u && typeof u.costMicroUSD === 'number' ? u.costMicroUSD : 0)

/** Saves what a model call cost (a costs row) and returns it in dollars. */
export function record(app: any, kind: string, result: any, story = ''): number {
  const m = micro(result && result.usage)
  try {
    const r = new Record(app.findCollectionByNameOrId('costs'))
    r.set('kind', kind)
    r.set('model', String((result && result.model) || ''))
    r.set('micro_usd', m)
    r.set('story', story)
    app.save(r)
  } catch (err: any) {
    console.log(`storytime: couldn't record a cost: ${err && err.message ? err.message : err}`)
  }
  return m / 1e6
}

/** What the app has spent on models today, since 00:00 UTC. */
export function spentToday(app: any): number {
  const since = new Date().toISOString().slice(0, 10) + ' 00:00:00.000Z'
  const rows = app.findRecordsByFilter('costs', 'created >= {:since}', '', 100000, 0, { since })
  return rows.reduce((t: number, r: any) => t + r.getInt('micro_usd'), 0) / 1e6
}

let healthCache: { at: number; models: string; problem: string | null } | null = null

/**
 * Whether every model the app uses is there: $llm exists, each model's provider is in Settings → LLM and answers, and
 * offers the model (Vultr lists its aligner only for a working key, so a wrong key shows up here too). null when all
 * is well, else what's wrong. Checked at most once a minute, and again when the models change.
 */
export async function health(app: any): Promise<string | null> {
  const t = tuning(app)
  const key = JSON.stringify(t.models)
  if (healthCache && healthCache.models === key && Date.now() - healthCache.at < 60_000) return healthCache.problem
  let problem: string | null = null
  if (typeof $llm === 'undefined') {
    problem = 'this PocketOne has no $llm (the llm extension)'
  } else {
    const needs: [string, string][] = [['text', t.models.text], ['speech', t.models.speech], ['align', t.models.align],
      ['image', t.models.image], ['music', t.models.music]]
    for (const [kind, model] of needs) {
      try {
        const provider = model.split('/')[0]
        const list = await ($llm as any)[kind].modelsAsync({ provider })
        if (!(list.models || []).some((m: any) => m.id === model)) {
          problem = `${model} isn't offered: is a provider named "${provider}" in Settings → LLM, with a working key?`
          break
        }
      } catch (err: any) {
        problem = `${model}: ${err && err.message ? err.message : err}`
        break
      }
    }
  }
  healthCache = { at: Date.now(), models: key, problem }
  if (problem) console.log(`storytime: not healthy: ${problem}`)
  return problem
}

/**
 * Why nothing new may be made right now, or null. message is for the listener; reason says more, for the app's admins.
 * Checks the models (health), the day's budget, then the limits on stories in the making and per hour.
 */
export async function refusal(app: any, what: 'story' | 'idea'): Promise<{ status: number; message: string; reason?: string } | null> {
  const problem = await health(app)
  if (problem) return { status: 503, message: RESTING, reason: problem }
  const budget = tuning(app).budget
  const spent = spentToday(app)
  if (spent >= budget) return { status: 429, message: OUT_OF_TOKENS, reason: `spent $${spent.toFixed(2)} of $${budget} today` }
  if (what === 'idea') return null
  const busy = canStart(app)
  return busy ? { status: 429, message: busy } : null
}

/** Whether another story may start now, by the limits on stories in the making and per hour; a reason when not. */
export function canStart(app: any): string | null {
  const making = app.countRecords('stories', $dbx.exp("status IN ('queued', 'writing', 'making')"))
  if (making >= LIMITS.making) return `${making} stories are being made right now; try again in a few minutes.`
  const since = new Date(Date.now() - 3600_000).toISOString().replace('T', ' ')
  const recent = app.countRecords('stories', $dbx.exp('created > {:since}', { since }))
  if (recent >= LIMITS.perHour) return 'That\'s a lot of stories for one hour; try again later.'
  return null
}

/** A JSON answer from the text model; a model sometimes writes broken JSON (bad_answer), and a second try usually works. */
async function ask(model: string, opts: any): Promise<any> {
  const once = () => $llm.text.completeAsync({ model, reasoning: 'off', maxTokens: 12000, ...opts })
  return once().catch((err: any) => {
    if (!String(err && err.message).startsWith('bad_answer')) throw err
    return once()
  })
}

/** Step 1: the story's text for prompt, as long as minutes. Returns the story and the call (for its cost). */
export async function write(t: Tuning, prompt: string, minutes: number): Promise<{ story: Story; result: any }> {
  const result = await ask(t.models.text, { system: writePrompt(t, minutes), prompt: `Tonight's story: ${prompt}`, schema: WRITE_SCHEMA, temperature: 0.9 })
  const written = result.data || {}
  const story: Story = {
    title: String(written.title || 'A story'),
    language: String(written.language || 'en').toLowerCase().slice(0, 2),
    topic: String(written.topic || ''),
    look: String(written.look || ''),
    sentences: (written.sentences || []).map((s: any) => String(s).replace(/\s+/g, ' ').trim()).filter(Boolean),
  }
  if (story.sentences.length < 5) throw new Error(`the story came back too short: ${story.sentences.length} sentences, ${JSON.stringify(written).slice(0, 300)}`)
  return { story, result }
}

/** The music call for a story's music prompt, as tuned. */
export const musicCall = (t: Tuning, prompt: string, seconds = t.music.seconds) => ({
  model: t.models.music,
  prompt: `${prompt}. ${t.music.suffix}`.trim(),
  lyrics: '[instrumental]',
  seconds,
  wait: '6m',
})

/** The picture call for a picture prompt in a story's style, as tuned. */
export const pictureCall = (t: Tuning, style: string, prompt: string) => ({
  model: t.models.image,
  prompt: `${style}. ${prompt}. ${t.picture}`.trim(),
  aspectRatio: '16:9',
})

/** Makes the story for record id. Never throws: a failure is saved on the record. */
export async function make(app: any, id: string): Promise<void> {
  const save = (fields: { [k: string]: any }) => {
    const r = app.findRecordById('stories', id)
    for (const k in fields) r.set(k, fields[k])
    app.save(r)
  }
  let spent = 0
  const t = tuning(app)
  const began = Date.now()
  try {
    const rec = app.findRecordById('stories', id)
    const prompt = rec.getString('prompt')
    const fallback = storyLength(t)
    const minutes = rec.getInt('minutes') || fallback.minutes
    const wanted = rec.getInt('picture_count') || fallback.pictures

    // 1. write
    save({ status: 'writing', progress: 'Writing the story' })
    const w = await write(t, prompt, minutes)
    spent += record(app, 'write', w.result, id)
    const story = w.story
    save({ status: 'making', title: story.title, story, progress: 'Reading it aloud', cost: spent })

    // 2. one voiceover of the whole story, in the tuned voice (or the other model, told the language, for a language
    // the main one doesn't speak)
    const call = speechCall(t, story.sentences.join(' '), story.language, app)
    const voice = await $llm.speech.speakAsync(call)
    spent += record(app, 'speech', voice, id)
    save({ progress: 'Listening for when each word is said', cost: spent })

    // 3. align: one line per sentence
    const a = await $llm.align.alignAsync({ model: t.models.align, audio: voice.file, text: story.sentences.join('\n'), mode: 'speech',
      language: story.language })
    spent += record(app, 'align', a, id)
    story.duration = a.duration
    story.lines = a.lines.map((l: any) => ({ index: l.index, text: l.text, start: l.start, end: l.end }))
    story.words = a.words.map((x: any) => ({ line: x.line, text: x.text, start: x.start, end: x.end }))
    if (!story.lines.length) throw new Error('the voiceover could not be lined up with the story')
    save({ story, progress: 'Choosing the pictures', cost: spent })

    // 4. place the pictures from the timed transcript
    const n = wanted
    const minGap = Math.min(10, a.duration / n / 2) // seconds a picture shows at least
    const transcript = story.lines.map((l) => `[${l.index}] ${l.start.toFixed(1)}s ${l.text}`).join('\n')
    const placedCall = await ask(t.models.text, {
      system: fill(t.prompts.place, { n }),
      prompt: `Topic: ${story.topic}\nWhat recurs in the pictures: ${story.look}\nLength: ${a.duration.toFixed(0)} seconds\n\n${transcript}`,
      schema: PLACE_SCHEMA,
    })
    spent += record(app, 'place', placedCall, id)
    const placed = placedCall.data || {}
    const startOf = new Map(story.lines.map((l) => [l.index, l.start]))
    const chosen = (placed.pictures || []).filter((p: any) => p && p.prompt).slice(0, n)
    if (!chosen.length) throw new Error('no pictures were chosen')
    // where the model put them, when every choice is a real line, in order, and not too close to the one before;
    // otherwise spread them evenly, each at the line nearest its share of the story
    let starts = chosen.map((p: any, i: number) => (i === 0 ? 0 : startOf.get(Number(p.line))))
    const placedWell = starts.every((s: any, i: number) => s !== undefined && (i === 0 || s >= starts[i - 1] + minGap))
    if (!placedWell) {
      starts = chosen.map((_: any, i: number) => {
        if (i === 0) return 0
        const target = (a.duration * i) / chosen.length
        return story.lines!.reduce((best, l) => (Math.abs(l.start - target) < Math.abs(best - target) ? l.start : best), 0)
      })
    }
    const pictures: Picture[] = chosen.map((p: any, i: number) => ({ start: starts[i] as number, prompt: String(p.prompt) }))
    story.style = String(placed.style || 'soft watercolor, warm night light')
    story.music = String(placed.music || 'quiet ambient music on soft piano and strings')
    story.pictures = pictures
    save({ story, cost: spent })

    // 5. paint the pictures and 6. compose the song, at the same time; progress saved as each piece arrives
    const done = { pictures: 0, music: false }
    const report = () =>
      save({ progress: `Painting pictures ${done.pictures}/${pictures.length} · ${done.music ? 'Song ready' : 'Composing the song'}` })
    report()
    const painted = Promise.all(pictures.map(async (p) => {
      const r = await $llm.image.generateAsync(pictureCall(t, story.style!, p.prompt))
      spent += record(app, 'image', r, id)
      done.pictures++
      report()
      return r.files[0]
    }))
    const composed = (async () => {
      const r = await $llm.music.createAsync(musicCall(t, story.music!))
      spent += record(app, 'music', r, id)
      done.music = true
      report()
      return r.files && r.files[0]
    })()
    const [images, song] = await Promise.all([painted, composed])

    // the files, then each picture's file name (the record keeps them in the order they were set)
    const r = app.findRecordById('stories', id)
    r.set('narration', $llm.file(voice.file))
    r.set('pictures', images.map((f: any) => $llm.file(f)))
    if (song) r.set('music', await $llm.fileAsync(song))
    app.save(r)
    const names = app.findRecordById('stories', id).get('pictures')
    pictures.forEach((p, i) => { p.image = names[i] })
    const voiceName = call.reference ? [`clone of "${clipName(app, t.voice.clip)}"`,
          call.model !== t.models.speech ? `(${story.language})` : '', call.speed ? `speed ${call.speed}` : ''].filter(Boolean).join(' ')
      : call.model !== t.models.speech ? `${call.model}'s own voice (${story.language})`
      : [call.voice || 'default', call.instructions ? `"${call.instructions}"` : '', t.voice.seed !== null ? `seed ${t.voice.seed}` : '',
          call.speed ? `speed ${call.speed}` : ''].filter(Boolean).join(', ')
    story.made = {
      seconds: Math.round((Date.now() - began) / 1000),
      models: { writing: t.models.text, voice: call.model, timing: t.models.align, pictures: t.models.image, music: t.models.music },
      voice: voiceName,
    }
    save({ story, status: 'done', progress: '', cost: spent })
  } catch (err: any) {
    const message = String(err && err.message ? err.message : err)
    console.log(`storytime: story ${id} failed: ${message}`)
    try {
      save({ status: 'failed', error: message, cost: spent })
    } catch (_) {
      // the record is gone
    }
  }
}

/** A topic for a new story, unlike the ones already told and the suggestions in avoid. */
export async function inspire(app: any, avoid: string[] = [], t: Tuning = tuning(app)): Promise<string> {
  const told = app.findRecordsByFilter('stories', "status = 'done'", '-views,-created', 200, 0)
  const list = told.map((r: any) => {
    const topic = (JSON.parse(r.getString('story') || '{}') || {}).topic
    return `- ${r.getString('title')}${topic ? ` (${topic})` : ''}`
  }).join('\n')
  const r = await $llm.text.completeAsync({
    model: t.models.idea,
    system: t.prompts.inspire,
    prompt: (list ? `Stories already told:\n${list}` : 'No stories have been told yet.') +
      (avoid.length ? `\n\nAlready suggested, so suggest something different:\n${avoid.map((a) => `- ${a}`).join('\n')}` : ''),
    schema: { type: 'object', properties: { topic: { type: 'string' } }, required: ['topic'] },
    reasoning: 'off',
    maxTokens: 4000,
    temperature: 1,
  })
  record(app, 'inspire', r)
  return String((r.data && r.data.topic) || '').trim()
}

/** A story in the making whose record hasn't moved for this long was cut off (the app restarted). */
export const STALE_MS = 15 * 60_000
