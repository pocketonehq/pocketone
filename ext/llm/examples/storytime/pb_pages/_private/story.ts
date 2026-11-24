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
  length: { minMinutes: number; maxMinutes: number; defaultMinutes: number; picturesPerMinute: number; maxPictures: number
    /** how many words the narrator reads in a minute: sets how many words a story of N minutes gets */
    wordsPerMinute: number }
  /** the player's final mix: the narration's loudness (dBFS, measured over its sounding parts) and how many dB the
   * music sits under it; every story's files are levelled to these when it plays */
  mix: { voiceDb: number; musicUnderDb: number }
  /** what the stories know: LLM Search buckets ($llm.search) the writer is grounded in and Inspire me draws from; none
   * means the models' own knowledge. skip: globs for documents not worth a story (go.mod, *.lock, toc*.md); a pattern
   * without / matches the base name */
  knowledge: { buckets: string[]; skip: string[] }
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
   * mention. ideas: how Inspire me turns knowledge passages into story requests ({count}). pronounce: how words in this
   * field are said, with examples, for the writer's respellings and the pronunciation call.
   */
  prompts: { write: string; place: string; inspire: string; ideas: string; pronounce: string }
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
    L.wordsPerMinute = whole(L.wordsPerMinute, D.wordsPerMinute, 60, 250)
    out.knowledge.buckets = (Array.isArray(out.knowledge.buckets) ? out.knowledge.buckets : [])
      .filter((b: any) => typeof b === 'string' && b.trim()).map((b: string) => b.trim()).slice(0, 10)
    out.knowledge.skip = (Array.isArray(out.knowledge.skip) ? out.knowledge.skip : [])
      .filter((b: any) => typeof b === 'string' && b.trim()).map((b: string) => b.trim()).slice(0, 50)
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
  minWords: Math.round(minutes * t.length.wordsPerMinute * 0.9),
  maxWords: Math.round(minutes * t.length.wordsPerMinute * 1.1),
  anyTopic: ANY_TOPIC,
}) + sayNote(t)

const WRITE_SCHEMA = {
  type: 'object',
  properties: {
    title: { type: 'string' },
    language: { type: 'string' },
    topic: { type: 'string' },
    look: { type: 'string' },
    sentences: { type: 'array', items: { type: 'string' } },
    say: {
      type: 'array',
      items: { type: 'object', properties: { term: { type: 'string' }, spoken: { type: 'string' } }, required: ['term', 'spoken'] },
    },
  },
  required: ['title', 'language', 'topic', 'look', 'sentences', 'say'],
}

// ---- a cloned voice takes no instructions (its delivery comes from the recording), so how a word is said has to be in
// the text it reads: the writer lists the words a narrator could get wrong with a respelling, the voice and the aligner
// read the respelled sentences, and the captions keep the written words

/** Told to the writer after the profile's prompt: list the words to respell, as the settings say words are said. */
const sayNote = (t: Tuning) => '\n\nIn "say", list each word in the story a narrator could say wrong, one word each, ' +
  'exactly as written in the sentences, with how to spell it so a text-to-speech voice says it right. Leave out ordinary ' +
  `words; an empty list is fine. How words are said: ${t.prompts.pronounce}`

export type Say = { term: string; spoken: string }

/** The writer's respellings worth using: one written word each, said differently from how it is written. */
export function sayList(list: any): Say[] {
  const out: Say[] = []
  for (const x of Array.isArray(list) ? list : []) {
    const term = String((x && x.term) || '').trim(), spoken = String((x && x.spoken) || '').replace(/\s+/g, ' ').trim()
    if (!term || !spoken || /\s/.test(term) || term === spoken || out.some((o) => o.term === term)) continue
    out.push({ term, spoken })
  }
  return out.slice(0, 40)
}

/** Words a narrator may say wrong, found by their shape: acronyms (SES, HTTPS) and names with capitals inside (SQLite). */
export function oddWords(sentences: string[]): string[] {
  const out = new Set<string>()
  for (const s of sentences) for (const w of s.match(/[\p{L}\p{N}][\p{L}\p{N}'’-]*[\p{L}\p{N}]/gu) || []) {
    const word = w.replace(/['’]s$/, '')
    if (/^\p{Lu}{2,}\d*s?$/u.test(word) || /^\p{L}+\p{Lu}/u.test(word) && /\p{Ll}/u.test(word)) out.add(word)
  }
  return [...out].slice(0, 40)
}

const PRONOUNCE_SCHEMA = {
  type: 'object',
  properties: { words: { type: 'array', items: { type: 'object', properties: { term: { type: 'string' }, spoken: { type: 'string' } }, required: ['term', 'spoken'] } } },
  required: ['words'],
}

/**
 * The respellings for a story: the writer's list, plus how the odd-looking words it left out are said (one small call;
 * a word read as written comes back unchanged and is dropped).
 */
async function pronounce(t: Tuning, sentences: string[], say: Say[]): Promise<{ say: Say[]; result?: any }> {
  const ask_ = oddWords(sentences).filter((w) => !say.some((x) => x.term === w))
  if (!ask_.length) return { say }
  const result = await ask(t.models.text, {
    system: 'You tell a narrator how to say words. For each word, answer how to spell it so a text-to-speech voice says ' +
      `it right; a word read as written comes back unchanged. ${t.prompts.pronounce}`,
    prompt: ask_.join('\n'), schema: PRONOUNCE_SCHEMA, temperature: 0,
  })
  return { say: sayList([...say, ...(result.data.words || [])]), result }
}

const escapeRe = (x: string) => x.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')

/** A sentence as the narrator reads it: each listed word (whole words only, as written) replaced by its respelling. */
export function respell(sentence: string, say: Say[] = []): string {
  let out = sentence
  for (const { term, spoken } of [...say].sort((a, b) => b.term.length - a.term.length)) {
    out = out.replace(new RegExp(`(^|[^\\p{L}\\p{N}])${escapeRe(term)}(?![\\p{L}\\p{N}])`, 'gu'), `$1${spoken}`)
  }
  return out
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
  return app.findRecordById('voices', clip).getString('name')
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
  say?: Say[] // words the narrator reads respelled (the captions keep the written word)
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

/** Saves what a model call cost (a costs row) and returns it in dollars. */
export function record(app: any, kind: string, result: any, story = ''): number {
  const m = result.usage.costMicroUSD // in millionths of a dollar; always there
  const r = new Record(app.findCollectionByNameOrId('costs'))
  r.set('kind', kind)
  r.set('model', result.model)
  r.set('micro_usd', m)
  r.set('story', story)
  app.save(r)
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
  const needs: [string, string][] = [['text', t.models.text], ['speech', t.models.speech], ['align', t.models.align],
    ['image', t.models.image], ['music', t.models.music]]
  for (const [kind, model] of needs) {
    // the provider is asked over the network, and the model names are settings: either can be wrong
    try {
      const provider = model.split('/')[0]
      const list = await ($llm as any)[kind].modelsAsync({ provider })
      if (!list.models.some((m: any) => m.id === model)) {
        problem = `${model} isn't offered: is a provider named "${provider}" in Settings → LLM, with a working key?`
        break
      }
    } catch (err: any) {
      problem = `${model}: ${err.message}`
      break
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
    if (!err.message.startsWith('bad_answer')) throw err
    return once()
  })
}

/** Step 1: the story's text for prompt, as long as minutes. Returns the story and the call (for its cost). */
// ---- every sentence is read aloud, and the captions are the same words (lined up with the voice word by word), so
// the sentences themselves must be speakable: no code, paths or symbols a narrator can't say

/** A sentence with Markdown and symbols turned into words: backticks and emphasis gone, a link's text kept, arrows said. */
export function speakable(sentence: string): string {
  return sentence
    .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1') // [text](url): the text
    .replace(/`+/g, '') // backticks
    .replace(/\*\*|__/g, '').replace(/(^|\s)[*_](\S[^*_]*\S)[*_](?=\s|[.,;:!?]|$)/g, '$1$2') // **bold**, *em*
    .replace(/\s*(?:→|->|=>|⟶)\s*/g, ' to ').replace(/\s*(?:←|<-)\s*/g, ' from ').replace(/\s*›\s*/g, ', ')
    .replace(/\s+&\s+/g, ' and ').replace(/(\d)\s*%/g, '$1 percent')
    .replace(/[#~^|\\{}<>]/g, ' ')
    .replace(/\s+([.,;:!?])/g, '$1').replace(/\s{2,}/g, ' ').trim()
}

/** Whether a sentence still has something a narrator can't read: a path, a flag, a file name, a URL, snake_case, code. */
export function looksLikeCode(sentence: string): boolean {
  return /https?:\/\/|www\./.test(sentence) || // a URL
    /(^|\s)--?[a-z][\w-]*/i.test(sentence) || // a flag
    /\b[\w.-]+\/[\w./-]+/.test(sentence) || // a path (a/b, ext/llm/README.md)
    /\b[a-z][\w-]+\.[a-z]{1,4}\b/.test(sentence) || // a file or host name (story.ts, example.com)
    /\b[a-z]+_[a-z_]+\b/.test(sentence) || // snake_case
    /\b\w+\(\)|\s==?\s|\w=\w/.test(sentence) || // a call, an assignment
    /\b[a-z]+[A-Z]\w*\b/.test(sentence) // camelCase
}

const SPEAKABLE_SCHEMA = { type: 'object', properties: { sentences: { type: 'array', items: { type: 'string' } } }, required: ['sentences'] }

/** Speakable sentences: cleaned, then any that still look like code rewritten in plain words (one call for all of them). */
async function speakableSentences(t: Tuning, sentences: string[]): Promise<{ sentences: string[]; result?: any }> {
  const clean = sentences.map(speakable)
  // rewritten: what still looks like code, and what the writer marked as code or wrote with arrows (a command or a
  // menu path reads badly even with the marks gone: "Settings → LLM")
  const marked = (s: string) => /`|→|->|=>|⟶|›/.test(s)
  const bad = clean.map((s, i) => (looksLikeCode(s) || marked(sentences[i]) ? i : -1)).filter((i) => i >= 0)
  if (!bad.length) return { sentences: clean }
  const result = await ask(t.models.text, {
    system: 'You rewrite sentences of a story so a narrator can read them aloud. Replace file paths, commands, flags, code, ' +
      'config keys, URLs and symbols with plain words that say what they are ("a setting in the key-value store", "the ' +
      'command to switch models"). Keep the meaning, the tone and the language; keep each to one sentence; never add facts.',
    prompt: bad.map((i, n) => `${n + 1}. ${clean[i]}`).join('\n') + `\n\nAnswer with the ${bad.length} rewritten sentences, in order.`,
    schema: SPEAKABLE_SCHEMA, temperature: 0.3,
  })
  const fixed: string[] = (result.data.sentences || []).map((x: any) => speakable(String(x)))
  bad.forEach((i, n) => { if (fixed[n]) clean[i] = fixed[n] })
  return { sentences: clean, result }
}

/**
 * What the knowledge buckets hold about a topic, as a researcher would hand it over: one search, the matches sorted into
 * what the docs say (they explain what it is and why), how it came to be (commits) and how it works (code), so the
 * writer can orient a listener before the details. "" without knowledge buckets or matches.
 */
export async function research(t: Tuning, topic: string): Promise<{ text: string; found?: any }> {
  if (!t.knowledge.buckets.length) return { text: '' }
  const found = await $llm.search.searchAsync({ buckets: t.knowledge.buckets, query: topic, limit: 20 })
  const piles: { [k: string]: string[] } = { docs: [], history: [], code: [] }
  const owners = new Map<string, Owner>()
  const look = lookups()
  for (const r of found.results) {
    if (skipped(t, r.document)) continue
    // commits are named "commit <sha>" (llm.search/v1); otherwise the document's kind, as the bucket split it
    const pile = /^commit /.test(r.document) ? 'history' : look.kind(r.bucket, r.document) === 'code' ? 'code' : 'docs'
    const cap = pile === 'docs' ? 6 : 4
    if (piles[pile].length >= cap) continue
    const owner = pile === 'history' ? null : look.owner(r.bucket, r.document)
    if (owner) owners.set(owner.folder, owner)
    const label = `${r.document}${r.heading ? ` › ${r.heading}` : ''}${owner ? `, part of ${owner.title}` : ''}`
    piles[pile].push(`(${label})\n${String(r.text).slice(0, 1500)}`)
  }
  const part = (title: string, list: string[]) => (list.length ? `## ${title}\n\n${list.join('\n\n')}` : '')
  const text = [
    part('What these belong to (each one\'s README, opening)', [...owners.values()].map((o) => `(${o.title}, ${o.folder})\n${o.opening}`)),
    part('What the docs say', piles.docs), part('How it came to be (commits)', piles.history), part('How it works (code)', piles.code),
  ].filter(Boolean).join('\n\n')
  return { text, found }
}

/** Whether a document is one the settings' knowledge.skip patterns leave out (dependency lists, tables of contents). */
export function skipped(t: Tuning, document: string): boolean {
  return t.knowledge.skip.some((p) => globRe(p).test(document))
}

/** A glob as a regexp on a document name: * is anything but /, ** anything; a pattern without / matches the base name. */
export function globRe(pattern: string): RegExp {
  const body = pattern.split('**').map((x) => x.split('*').map((y) => y.replace(/[.+?^${}()|[\]\\]/g, '\\$&')).join('[^/]*')).join('.*')
  return new RegExp(pattern.includes('/') ? `^${body}$` : `(^|/)${body}$`, 'i')
}

/** What a passage is part of: the nearest folder above it with a README in the same bucket, that README's title and opening. */
export type Owner = { folder: string; title: string; opening: string }

/**
 * Lookups in the knowledge buckets for one story or one Inspire me (fresh each time, since buckets refresh): each
 * document's kind (markdown, code or text) and what it is part of. Both are local reads, no embedding.
 */
function lookups() {
  const docs = new Map<string, any[]>(), readmes = new Map<string, Owner | null>()
  const list = (bucket: string, prefix: string) => {
    const key = bucket + ':' + prefix
    if (!docs.has(key)) {
      docs.set(key, $llm.search.documents({ bucket, prefix }))
    }
    return docs.get(key) as any[]
  }
  const readme = (bucket: string, folder: string): Owner | null => {
    const key = bucket + ':' + folder
    if (!readmes.has(key)) {
      const doc = list(bucket, `${folder}/README`).find((d: any) => /^readme\.md$/i.test(d.name.slice(folder.length + 1)))
      readmes.set(key, doc ? ownerFrom(bucket, folder, doc.name) : null)
    }
    return readmes.get(key) as Owner | null
  }
  return {
    kind: (bucket: string, document: string): string => {
      const d = list(bucket, document).find((x: any) => x.name === document)
      return d ? d.kind : ''
    },
    owner: (bucket: string, document: string): Owner | null => {
      const parts = document.split('/')
      for (let n = parts.length - 1; n >= 1; n--) {
        const o = readme(bucket, parts.slice(0, n).join('/'))
        if (o) return o
      }
      return null
    },
  }
}

/** A README's title (front matter title:, else its first # heading, else the folder's name) and its first 800 characters. */
function ownerFrom(bucket: string, folder: string, name: string): Owner {
  const opening = $llm.search.sample({ bucket, prefix: name, count: 50 }).filter((c: any) => c.document === name)
    .sort((a: any, b: any) => a.startLine - b.startLine).map((c: any) => c.text).join('\n').slice(0, 800)
  const fm = opening.match(/^title:\s*["']?(.+?)["']?\s*$/m), h1 = opening.match(/^#\s+(.+?)\s*$/m)
  const title = ((fm && fm[1]) || (h1 && h1[1]) || folder.split('/').pop() || folder).trim()
  return { folder, title, opening }
}

export async function write(t: Tuning, prompt: string, minutes: number): Promise<{ story: Story; result: any; fixes?: any; said?: any; searched?: any }> {
  // grounded in the knowledge buckets: what they hold about the topic follows the request, docs first
  const known = await research(t, prompt)
  const request = `Tonight's story: ${prompt}` + (known.text ? `\n\nWhat the knowledge base holds about it:\n\n${known.text}` +
    '\n\nBefore any detail, name what the story is about by its own name (from what the passages belong to) and say in a ' +
    'sentence what it is and why someone would use it. Call it by its title (the README\'s title or first heading), not a tagline. An example, recipe or test is an example of that thing, never the ' +
    'thing itself: say what it is an example of.' : '')
  const result = await ask(t.models.text, { system: writePrompt(t, minutes), prompt: request, schema: WRITE_SCHEMA, temperature: 0.9 })
  const written = result.data
  const story: Story = {
    title: String(written.title || 'A story'),
    language: String(written.language || 'en').toLowerCase().slice(0, 2),
    topic: String(written.topic || ''),
    look: String(written.look || ''),
    sentences: (written.sentences || []).map((s: any) => String(s).replace(/\s+/g, ' ').trim()).filter(Boolean),
    say: sayList(written.say),
  }
  if (story.sentences.length < 5) throw new Error(`the story came back too short: ${story.sentences.length} sentences, ${JSON.stringify(written).slice(0, 300)}`)
  const spoken = await speakableSentences(t, story.sentences)
  story.sentences = spoken.sentences
  const said = await pronounce(t, story.sentences, story.say || [])
  story.say = said.say.filter((x) => story.sentences.some((s) => respell(s, [x]) !== s))
  return { story, result, fixes: spoken.result, said: said.result, searched: known.found }
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

/** The longest each step may take before the story counts as stalled (seconds). */
export const STEP_TIMEOUT: { [step: string]: number } = {
  topic: 240,
  write: 300, // the story, the speakable rewrite and the pronunciations, together
  speech: 300,
  align: 180,
  place: 180,
  'paint and compose': 540, // the song call itself waits up to 6 minutes
}

/** How many times a story is attempted: the first try and one retry, which resumes where the first one stopped. */
export const ATTEMPTS = 2

/** Thrown in an attempt that a newer attempt replaced: it stops quietly, leaving the record to the newer one. */
class Superseded extends Error {}

/** One line of a story's log: when, which attempt, which step, what happened, how long it took, and any detail. */
export type LogEntry = { at: string; attempt: number; step: string; event: string; ms?: number; detail?: string }

const UNFINISHED = ['queued', 'writing', 'making']

/**
 * Checks a story still being made: if the step it's on has run past STEP_TIMEOUT (a model call that never answered,
 * or a server restart mid-story), the story is tried again from its last saved step, once; after the last attempt it's
 * failed. Called whenever someone watches a story (its page's progress poll) and when a story is started, so a stall
 * is noticed without a timer (a timer can't outlive the request that set it).
 */
export function watch(app: any, id: string): void {
  const r = app.findRecordById('stories', id)
  if (!UNFINISHED.includes(r.getString('status'))) return
  const entries: LogEntry[] = JSON.parse(r.getString('log') || '[]') || []
  const attempt = r.getInt('attempts')
  // the step this attempt is on: its last start with no end after it
  const mine = entries.filter((e) => e.attempt === attempt)
  const last = mine[mine.length - 1]
  if (!last || last.event !== 'start') return
  const limit = STEP_TIMEOUT[last.step] || 300
  const ms = Date.now() - new Date(last.at).getTime()
  if (ms < limit * 1000) return
  const detail = `${last.step} took longer than ${limit} s`
  appendLog(app, id, { at: new Date().toISOString(), attempt, step: last.step, event: 'timeout', ms, detail })
  if (attempt < ATTEMPTS) {
    appendLog(app, id, { at: new Date().toISOString(), attempt, step: 'story', event: 'retry', detail })
    make(app, id) // not awaited; it takes the next attempt, so the stalled one is ignored if it ever answers
  } else {
    const f = app.findRecordById('stories', id)
    f.set('status', 'failed')
    f.set('error', detail)
    f.set('attempts', attempt + 1) // nothing from the stalled attempt touches the record now
    app.save(f)
    appendLog(app, id, { at: new Date().toISOString(), attempt, step: 'story', event: 'failed', detail })
  }
}

/** Every story still being made, checked as watch does. */
export function watchAll(app: any): void {
  for (const r of app.findRecordsByFilter('stories', "status = 'queued' || status = 'writing' || status = 'making'", 'created', 20, 0)) {
    watch(app, r.id)
  }
}

function appendLog(app: any, id: string, e: LogEntry) {
  const r = app.findRecordById('stories', id)
  const entries: LogEntry[] = JSON.parse(r.getString('log') || '[]') || []
  entries.push(e)
  r.set('log', entries.slice(-200))
  app.save(r)
}

/**
 * Makes the story for record id. Never throws: a failure is saved on the record.
 *
 * Each step's result is saved on the record as it's reached (the story after writing, the narration file after reading
 * it aloud, the timings after aligning, the pictures' places after placing), and every step is written to the record's
 * log (hidden: superusers see it in the dashboard). A step that fails, or runs past STEP_TIMEOUT (see watch), ends the
 * attempt; the story is then tried once more (ATTEMPTS), and the retry skips every step already saved. The attempt
 * count lives on the record: each attempt takes the next number, an attempt that's no longer the latest stops at its
 * next step, and nothing runs a story more than ATTEMPTS times.
 */
export async function make(app: any, id: string): Promise<void> {
  const save = (fields: { [k: string]: any }) => {
    const r = app.findRecordById('stories', id)
    for (const k in fields) r.set(k, fields[k])
    app.save(r)
  }
  // the attempt this is, counted on the record so a retry happens once at most
  const first = app.findRecordById('stories', id)
  const attempt = first.getInt('attempts') + 1
  if (attempt > ATTEMPTS) return
  first.set('attempts', attempt)
  app.save(first)

  const log = (step: string, event: string, extra: { ms?: number; detail?: string } = {}) =>
    appendLog(app, id, { at: new Date().toISOString(), attempt, step, event, ...extra })
  /** Stops this attempt if a newer one took over (a retry after it stalled). */
  const current = () => {
    if (app.findRecordById('stories', id).getInt('attempts') !== attempt) throw new Superseded()
  }
  /** Runs one step: logged when it starts and ends, with how long it took. */
  const step = async <T>(name: string, run: () => Promise<T>): Promise<T> => {
    current()
    const t0 = Date.now()
    log(name, 'start')
    let out: T
    try {
      out = await run()
    } catch (err: any) {
      current()
      log(name, 'error', { ms: Date.now() - t0, detail: String(err.message).slice(0, 500) })
      throw err
    }
    current()
    log(name, 'done', { ms: Date.now() - t0 })
    return out
  }

  let spent = first.getFloat('cost')
  const t = tuning(app)
  const began = Date.now()
  try {
    if (attempt > 1) log('story', 'resume')
    const rec = app.findRecordById('stories', id)
    const prompt = rec.getString('prompt')
    const fallback = storyLength(t)
    const minutes = rec.getInt('minutes') || fallback.minutes
    const wanted = rec.getInt('picture_count') || fallback.pictures
    const saved: Story | null = JSON.parse(rec.getString('story') || 'null')

    // 1. write. "Your pick" with knowledge buckets: Inspire me picks a topic from them first, one not told yet
    let story: Story
    if (saved && saved.sentences && saved.sentences.length) {
      story = saved
    } else {
      let topic = prompt
      if (prompt === ANY_TOPIC && t.knowledge.buckets.length) {
        save({ status: 'writing', progress: 'Picking the story' })
        topic = (await step('topic', () => inspire(app, [], t, id))) || prompt
      }
      save({ status: 'writing', progress: 'Writing the story' })
      const w = await step('write', () => write(t, topic, minutes))
      spent += record(app, 'write', w.result, id)
      if (w.fixes) spent += record(app, 'write', w.fixes, id)
      if (w.said) spent += record(app, 'write', w.said, id)
      if (w.searched) spent += record(app, 'search', w.searched, id)
      story = w.story
      save({ title: story.title, story, cost: spent })
    }
    save({ status: 'making', title: story.title, progress: 'Reading it aloud' })

    // 2. one voiceover of the whole story, in the tuned voice (or the other model, told the language, for a language
    // the main one doesn't speak)
    // read respelled; the captions (story.sentences, and each line's text below) keep the written words
    const spoken = story.sentences.map((x) => respell(x, story.say))
    const call = speechCall(t, spoken.join(' '), story.language, app)
    if (!app.findRecordById('stories', id).getString('narration')) {
      const voice = await step('speech', () => $llm.speech.speakAsync(call))
      spent += record(app, 'speech', voice, id)
      const r = app.findRecordById('stories', id)
      r.set('narration', $llm.file(voice.file))
      r.set('cost', spent)
      app.save(r)
    }
    const narration = $llm.fromRecord(app.findRecordById('stories', id), 'narration')

    // 3. align: one line per sentence
    if (!story.lines || !story.lines.length) {
      save({ progress: 'Listening for when each word is said' })
      const a = await step('align', () => $llm.align.alignAsync({ model: t.models.align, audio: narration,
        text: spoken.join('\n'), mode: 'speech', language: story.language }))
      spent += record(app, 'align', a, id)
      story.duration = a.duration
      story.lines = a.lines.map((l: any) => ({ index: l.index, text: story.sentences[l.index] || l.text, start: l.start, end: l.end }))
      story.words = a.words.map((x: any) => ({ line: x.line, text: x.text, start: x.start, end: x.end }))
      if (!story.lines.length) throw new Error('the voiceover could not be lined up with the story')
      save({ story, cost: spent })
    }
    const duration = story.duration!

    // 4. place the pictures from the timed transcript
    if (!story.pictures || !story.pictures.length) {
      save({ progress: 'Choosing the pictures' })
      const n = wanted
      const minGap = Math.min(10, duration / n / 2) // seconds a picture shows at least
      const transcript = story.lines!.map((l) => `[${l.index}] ${l.start.toFixed(1)}s ${l.text}`).join('\n')
      const placedCall = await step('place', () => ask(t.models.text, {
        system: fill(t.prompts.place, { n }),
        prompt: `Topic: ${story.topic}\nWhat recurs in the pictures: ${story.look}\nLength: ${duration.toFixed(0)} seconds\n\n${transcript}`,
        schema: PLACE_SCHEMA,
      }))
      spent += record(app, 'place', placedCall, id)
      const placed = placedCall.data
      const startOf = new Map(story.lines!.map((l) => [l.index, l.start]))
      const chosen = (placed.pictures || []).filter((p: any) => p && p.prompt).slice(0, n)
      if (!chosen.length) throw new Error('no pictures were chosen')
      // where the model put them, when every choice is a real line, in order, and not too close to the one before;
      // otherwise spread them evenly, each at the line nearest its share of the story
      let starts = chosen.map((p: any, i: number) => (i === 0 ? 0 : startOf.get(Number(p.line))))
      const placedWell = starts.every((s: any, i: number) => s !== undefined && (i === 0 || s >= starts[i - 1] + minGap))
      if (!placedWell) {
        starts = chosen.map((_: any, i: number) => {
          if (i === 0) return 0
          const target = (duration * i) / chosen.length
          return story.lines!.reduce((best, l) => (Math.abs(l.start - target) < Math.abs(best - target) ? l.start : best), 0)
        })
      }
      story.pictures = chosen.map((p: any, i: number) => ({ start: starts[i] as number, prompt: String(p.prompt) }))
      story.style = String(placed.style || 'soft watercolor, warm night light')
      story.music = String(placed.music || 'quiet ambient music on soft piano and strings')
      save({ story, cost: spent })
    }
    const pictures = story.pictures!

    // 5. paint the pictures and 6. compose the song, at the same time; progress saved as each piece arrives
    const done = { pictures: 0, music: false }
    // a picture or song that answers after this attempt gave up stays quiet: the retry owns the progress line now
    const report = () => {
      if (app.findRecordById('stories', id).getInt('attempts') !== attempt) return
      save({ progress: `Painting pictures ${done.pictures}/${pictures.length} · ${done.music ? 'Song ready' : 'Composing the song'}` })
    }
    report()
    const [images, song] = await step('paint and compose', () => Promise.all([
      Promise.all(pictures.map(async (p) => {
        const r = await $llm.image.generateAsync(pictureCall(t, story.style!, p.prompt))
        spent += record(app, 'image', r, id)
        done.pictures++
        report()
        return r.files[0]
      })),
      (async () => {
        const r = await $llm.music.createAsync(musicCall(t, story.music!))
        spent += record(app, 'music', r, id)
        done.music = true
        report()
        return r.files && r.files[0]
      })(),
    ]))

    // the files, then each picture's file name (the record keeps them in the order they were set)
    const r = app.findRecordById('stories', id)
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
      seconds: Math.round((Date.now() - new Date(rec.getString('created').replace(' ', 'T')).getTime()) / 1000),
      models: { writing: t.models.text, voice: call.model, timing: t.models.align, pictures: t.models.image, music: t.models.music },
      voice: voiceName,
    }
    rememberStory(id, story)
    save({ story, status: 'done', progress: '', cost: spent })
    log('story', 'done', { ms: Date.now() - began })
  } catch (err: any) {
    if (err instanceof Superseded) return
    const message = err.message
    console.log(`storytime: story ${id} attempt ${attempt} failed: ${message}`)
    try {
      if (app.findRecordById('stories', id).getInt('attempts') !== attempt) return // a newer attempt took over
      if (attempt < ATTEMPTS) {
        log('story', 'retry', { detail: String(message).slice(0, 500) })
        save({ progress: 'Trying again', cost: spent })
        make(app, id) // not awaited, like the first attempt; it resumes from what this one saved
        return
      }
      save({ status: 'failed', error: message, cost: spent })
      log('story', 'failed', { detail: String(message).slice(0, 500) })
    } catch (_) {
      // the record is gone
    }
  }
}

/** A topic for a new story, unlike the ones already told and the suggestions in avoid. */
// ---- the stories told, as a bucket: each finished story is a document (named by its id), so Inspire me can ask
// "has anything like this been told?" by meaning, not just by title
export const STORIES_BUCKET = 'storytime-stories'

export function storiesBucket() {
  return $llm.search.ensure({ name: STORIES_BUCKET, title: 'Story Time: stories told',
    description: 'Every story Story Time has told (title, topic, transcript), so Inspire me can find topics not told yet. Kept by Story Time.' })
}

/** Adds (or replaces) a finished story in the stories bucket. */
export function rememberStory(id: string, story: Story) {
  storiesBucket()
  $llm.search.add({ bucket: STORIES_BUCKET, name: id, kind: 'text',
    text: `${story.title}\nTopic: ${story.topic}\n\n${story.sentences.join(' ')}` })
}

/** Adds every finished story the bucket doesn't have yet (stories told before it existed); returns how many. */
export function rememberAll(app: any): number {
  storiesBucket()
  const have = new Set($llm.search.documents(STORIES_BUCKET).map((d: any) => d.name))
  let added = 0
  for (const r of app.findRecordsByFilter('stories', "status = 'done'", 'created', 0, 0)) {
    if (have.has(r.id)) continue
    const story = JSON.parse(r.getString('story') || 'null')
    if (story && story.sentences) {
      rememberStory(r.id, story)
      added++
    }
  }
  return added
}

const CANDIDATES_SCHEMA = {
  type: 'object',
  properties: {
    candidates: {
      type: 'array', minItems: 3, maxItems: 6,
      items: { type: 'object', properties: { subject: { type: 'string' }, prompt: { type: 'string' } }, required: ['subject', 'prompt'] },
    },
  },
  required: ['candidates'],
}

/** Passages that make poor story seeds: dependency lists, lock files, navigation tables. */

/** The pick: which of n candidates (its number) and why. */
const pickSchema = (n: number) => ({
  type: 'object',
  properties: { choice: { type: 'integer', minimum: 1, maximum: n }, why: { type: 'string' } },
  required: ['choice', 'why'],
})

export type Inspiration = {
  topic: string
  why?: string
  /** what it drew from: the knowledge passages, and each candidate with the told stories nearest it */
  material?: string[]
  candidates?: { topic: string; nearest: string[] }[]
}

/**
 * A topic no story has told. With knowledge buckets: draw random passages from them, have the model propose candidate
 * topics grounded in those passages, look up the told stories nearest each candidate, and have the model pick the
 * candidate the told stories cover least. Without: the model sees the told stories' titles and suggests something else.
 */
export async function inspiration(app: any, avoid: string[] = [], t: Tuning = tuning(app), story = ''): Promise<Inspiration> {
  const avoiding = avoid.length ? `\n\nAlready suggested, so suggest something different:\n${avoid.map((a) => `- ${a}`).join('\n')}` : ''
  if (!t.knowledge.buckets.length) {
    const told = app.findRecordsByFilter('stories', "status = 'done'", '-views,-created', 200, 0)
    const list = told.map((r: any) => {
      const topic = (JSON.parse(r.getString('story') || '{}') || {}).topic
      return `- ${r.getString('title')}${topic ? ` (${topic})` : ''}`
    }).join('\n')
    const r = await $llm.text.completeAsync({ model: t.models.idea, system: t.prompts.inspire,
      prompt: (list ? `Stories already told:\n${list}` : 'No stories have been told yet.') + avoiding,
      schema: { type: 'object', properties: { topic: { type: 'string' } }, required: ['topic'] }, reasoning: 'off', maxTokens: 4000, temperature: 1 })
    record(app, 'inspire', r, story)
    return { topic: String(r.data.topic || '').trim() }
  }

  // 1. seeds: passages picked at random from what the stories know, at most one per thing they belong to, each with that
  // thing's README opening, so an idea starts from a subject and not from a stray fragment
  const drawn = $llm.search.sample({ buckets: t.knowledge.buckets, count: 24 })
  if (!drawn.length) throw new Error(`the knowledge buckets (${t.knowledge.buckets.join(', ')}) are empty: fill them on the dashboard's Search screen`)
  const look = lookups()
  const seen = new Set<string>()
  const passages: any[] = []
  for (const p of drawn) {
    if (skipped(t, p.document) || String(p.text).trim().length < 200) continue
    const owner = /^commit /.test(p.document) ? null : look.owner(p.bucket, p.document)
    const key = owner ? owner.folder : p.document
    if (seen.has(key)) continue
    seen.add(key)
    passages.push({ ...p, owner })
    if (passages.length === 5) break
  }
  if (!passages.length) passages.push(...drawn.slice(0, 5))
  const material = passages.map((p: any, i: number) => [
    `[${i + 1}] ${p.document}${p.heading ? ` › ${p.heading}` : ''}${p.owner ? `, part of ${p.owner.title}` : ''}`,
    p.owner ? `What ${p.owner.title} is (${p.owner.folder}/README):\n${p.owner.opening.slice(0, 500)}` : '',
    `The passage:\n${String(p.text).slice(0, 1200)}`,
  ].filter(Boolean).join('\n'))

  // 2. one idea per seed, each a story request that stands on its own
  const c = await $llm.text.completeAsync({ model: t.models.idea, system: t.prompts.inspire, reasoning: 'off', maxTokens: 3000, temperature: 0.9,
    prompt: `Material to draw from (random passages, each with what it belongs to):\n\n${material.join('\n\n')}\n\n` +
      fill(t.prompts.ideas, { count: passages.length }) + avoiding,
    schema: CANDIDATES_SCHEMA })
  record(app, 'inspire', c, story)
  // the model may number what it lists ("3. Why..."): a prompt never starts with its place in a list
  const unnumbered = (x: any) => String(x || '').replace(/^\s*(?:\d{1,2}[.)]|[a-z]\))\s+/i, '').replace(/\s+/g, ' ').trim()
  const ideas = (c.data.candidates || [])
    .map((x: any) => ({ subject: unnumbered(x && x.subject), prompt: unnumbered(x && x.prompt) })).filter((x: any) => x.prompt)
  const candidates: string[] = ideas.map((x: any) => x.prompt)
  if (!candidates.length) throw new Error('no candidate topics came back')

  // 3. the told stories nearest each candidate, by meaning
  storiesBucket()
  const near: { topic: string; nearest: string[] }[] = []
  for (const topic of candidates) { // by the whole request: it names the subject and the angle
    const found = await $llm.search.searchAsync({ bucket: STORIES_BUCKET, query: topic, limit: 3 })
    const told = app.findRecordsByIds('stories', found.results.map((r: any) => r.document))
    const titles = [...new Set(told.map((r: any) => r.getString('title')))] as string[]
    near.push({ topic, nearest: titles })
  }

  // 4. the candidate the told stories cover least. The model answers with the candidate's number (a schema holds it to
  // 1..n), never the topic retyped, so the topic is exactly the candidate's words
  const pick = await $llm.text.completeAsync({ model: t.models.idea, reasoning: 'off', maxTokens: 1000, temperature: 0.3,
    system: 'You choose the next story. You get numbered candidate story requests, each with the stories already told that are ' +
      'closest to it. Pick the one those stories cover least, so the next story is about something new; between ones ' +
      'equally new, pick the one a listener would find most interesting. "choice" is its number; "why" says in one short ' +
      'sentence what makes it new.',
    prompt: near.map((n, i) => `${i + 1}. ${n.topic}\n   closest told: ${n.nearest.length ? n.nearest.join('; ') : 'none'}`).join('\n'),
    schema: pickSchema(near.length) })
  record(app, 'inspire', pick, story)
  const choice = Math.round(Number(pick.data.choice))
  const topic = candidates[choice >= 1 && choice <= candidates.length ? choice - 1 : 0]
  return { topic, why: String(pick.data.why || '').trim(), material: passages.map((p: any) => p.document), candidates: near }
}

/** Just the topic (Inspire me's button). */
export async function inspire(app: any, avoid: string[] = [], t: Tuning = tuning(app), story = ''): Promise<string> {
  return (await inspiration(app, avoid, t, story)).topic
}

/** A story in the making whose record hasn't moved for this long was cut off (the app restarted). */
export const STALE_MS = 15 * 60_000

// ---- what a story costs: the settings screen shows it beside the budget, for the models chosen there

/** One step of a story's cost: what it came to, and how that was worked out. */
export type EstimatePart = {
  step: string // what the step does, in words
  model: string // the provider/model it uses ('' for knowledge search, which uses LLM Search's own model)
  usd: number | null // null: not known (no stories made with the model yet, and it lists no price)
  method: 'stories' | 'listed' | 'unknown' // stories: the average of the finished stories made with the model
  stories?: number // how many stories it's the average of
  note?: string // listed: what the list price was multiplied by
}
export type StoryEstimate = { minutes: number; pictures: number; usd: number; complete: boolean; parts: EstimatePart[] }

/** The steps a story costs money in: the costs collection's kind, the model kind, and what the cost grows with. */
const COST_STEPS: { kind: string; label: string; modelKind: string; model: (t: Tuning) => string; per: 'minute' | 'picture' | 'story' }[] = [
  { kind: 'inspire', label: 'Picking the story', modelKind: 'text', model: (t) => t.models.idea, per: 'story' },
  { kind: 'search', label: 'Knowledge search', modelKind: '', model: () => '', per: 'minute' },
  { kind: 'write', label: 'Writing', modelKind: 'text', model: (t) => t.models.text, per: 'minute' },
  { kind: 'speech', label: 'Reading it aloud', modelKind: 'speech', model: (t) => t.models.speech, per: 'minute' },
  { kind: 'align', label: 'Timing each word', modelKind: 'align', model: (t) => t.models.align, per: 'minute' },
  { kind: 'place', label: 'Choosing the pictures', modelKind: 'text', model: (t) => t.models.text, per: 'minute' },
  { kind: 'image', label: 'Painting the pictures', modelKind: 'image', model: (t) => t.models.image, per: 'picture' },
  { kind: 'music', label: 'Composing the music', modelKind: 'music', model: (t) => t.models.music, per: 'story' },
]

/** Tokens in a word of English, for the list-price estimate of the text steps. */
const TOKENS_PER_WORD = 1.4

/**
 * What a story at the default length costs with t's models, step by step. Each step is the average of what it cost in
 * the finished stories made with the same model (measured: per minute, per picture or per story, scaled to this
 * length), or when there are none, the model's list price times the story's shape (the words written, the minutes
 * read aloud, the pictures, the song's length), or unknown when it lists no price either.
 */
export async function estimateStory(app: any, t: Tuning): Promise<StoryEstimate> {
  const { minutes, pictures } = storyLength(t)
  const picking = t.knowledge.buckets.length > 0 // the front page gives no prompt: with knowledge, a topic is picked first
  const steps = COST_STEPS.filter((s) => (s.kind !== 'inspire' && s.kind !== 'search') || picking)

  // measured: each finished story's cost per step and model, per minute, picture or story
  const made = new Map<string, { minutes: number; pictures: number }>()
  for (const r of app.findRecordsByFilter('stories', "status = 'done'", '-created', 500, 0)) {
    made.set(r.id, { minutes: r.getInt('minutes') || 1, pictures: r.getInt('picture_count') || 1 })
  }
  const perStory = new Map<string, Map<string, number>>() // kind|model → story → micro-dollars
  for (const c of app.findRecordsByFilter('costs', "story != ''", '', 0, 0)) {
    if (!made.has(c.getString('story'))) continue
    const key = c.getString('kind') + '|' + (c.getString('kind') === 'search' ? '' : c.getString('model'))
    const m = perStory.get(key) || new Map<string, number>()
    m.set(c.getString('story'), (m.get(c.getString('story')) || 0) + c.getInt('micro_usd'))
    perStory.set(key, m)
  }

  const listings: { [kind: string]: any[] } = {}
  const modelInfo = async (kind: string, id: string) => {
    if (!listings[kind]) listings[kind] = (await $llm[kind].modelsAsync()).models
    return listings[kind].find((m: any) => m.id === id)
  }
  const listed = (m: any, unit: string) => (m?.prices || []).find((p: any) => p.unit === unit && p.source !== 'measured')

  const words = minutes * t.length.wordsPerMinute
  const storyTokens = Math.round(words * TOKENS_PER_WORD)
  const parts: EstimatePart[] = []
  for (const s of steps) {
    const model = s.model(t)
    const seen = perStory.get(s.kind + '|' + model)
    if (seen && seen.size) {
      const scale = s.per === 'minute' ? minutes : s.per === 'picture' ? pictures : 1
      let sum = 0
      for (const [id, micro] of seen) {
        const st = made.get(id)!
        sum += micro / (s.per === 'minute' ? st.minutes : s.per === 'picture' ? st.pictures : 1)
      }
      parts.push({ step: s.label, model, usd: (sum / seen.size) * scale / 1e6, method: 'stories', stories: seen.size })
      continue
    }
    if (!s.modelKind) {
      parts.push({ step: s.label, model, usd: null, method: 'unknown' })
      continue
    }
    const m = await modelInfo(s.modelKind, model)
    let usd: number | null = null
    let note = ''
    const tokens = (input: number, output: number) => {
      const i = listed(m, '1M input tokens'), o = listed(m, '1M output tokens')
      if (!i && !o) return null
      note = `about ${input.toLocaleString()} tokens in and ${output.toLocaleString()} out`
      return ((i ? i.usd * input : 0) + (o ? o.usd * output : 0)) / 1e6
    }
    if (s.kind === 'write') usd = tokens(3000 + storyTokens * 3, storyTokens * 3) // the story, its speakable rewrite, the pronunciations
    else if (s.kind === 'place') usd = tokens(1000 + Math.round(storyTokens * 1.5), 800) // the timed transcript in, the pictures out
    else if (s.kind === 'inspire') usd = tokens(6000, 1500) // passages in, candidates and a pick out
    else if (s.kind === 'speech' || s.kind === 'align') {
      const sec = listed(m, 'second'), chars = listed(m, '1K characters')
      if (sec) { usd = sec.usd * minutes * 60; note = `${minutes} min of audio` }
      else if (chars) { usd = chars.usd * words * 6 / 1000; note = `about ${(words * 6).toLocaleString()} characters` }
    } else if (s.kind === 'image') {
      const each = listed(m, 'image'), mp = listed(m, 'megapixel')
      if (each) { usd = each.usd * pictures; note = `${pictures} pictures` }
      else if (mp) { usd = mp.usd * pictures; note = `${pictures} pictures at 1 megapixel each` }
    } else if (s.kind === 'music') {
      const sec = listed(m, 'second')
      if (sec) { usd = sec.usd * t.music.seconds; note = `${t.music.seconds} s of music` }
    }
    parts.push(usd === null ? { step: s.label, model, usd: null, method: 'unknown' } : { step: s.label, model, usd, method: 'listed', note })
  }
  const known = parts.filter((p) => p.usd !== null)
  return { minutes, pictures, usd: known.reduce((a, p) => a + (p.usd as number), 0), complete: known.length === parts.length, parts }
}
