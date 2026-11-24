// Set up's runs: the steps that make something (step 2 writes a story, step 3 plans its pictures) do it in the
// background with the screen's settings (saved or not), every step written to $kv as it finishes (what went in, what
// came out: story.ts's Trace), so the screen can poll it and show the whole log. One run per wizard step at a time: a new
// one replaces the last, and the old one stops writing when it notices. Save and next hands a finished run on
// (keep): the next step starts from it until it's handed on again.
//
// This is the wizard's own workflow, not story.make's: the writer gets only the preamble and the fine-tuning as its
// system prompt, the request (and what the knowledge buckets hold about it) as its prompt, and answers {title, body}.
// Nothing else: no rules about length or shape, no plain-words or pronunciation passes. Those come back later, each as
// its own step.
import type { Tuning, TraceStep } from './story'

/** A wizard step's run. Each step adds its own fields: the story's minutes, topic and story; the pictures' count, the
 * story they're for, the spots, the vibe and the reference picture. */
export type Run = {
  id: string
  status: 'running' | 'done' | 'failed'
  started: string
  /** when a step last finished (or it started) */
  updated: string
  steps: TraceStep[]
  error?: string
  costUSD: number
  [field: string]: any
}

/** What the writer answers: a title and the story's text. */
export type Draft = { title: string; body: string }

const DRAFT_SCHEMA = {
  type: 'object',
  properties: { title: { type: 'string' }, body: { type: 'string' } },
  required: ['title', 'body'],
}

// where each step's latest run and handed-on run live (step 2's keys are the ones it had before step 3 existed)
const runKey = (step: string) => (step === 'story' ? 'storytime/trial' : `storytime/trial/${step}`)
const keptKey = (step: string) => `storytime/wizard/${step}`

export const STEPS_THAT_RUN = ['story', 'pictures']

class Superseded extends Error {}

/** A run with no step finished for this long has stalled (a model or search call that never answered). */
const STALLED_MS = 3 * 60_000

/**
 * A step's latest run, running or finished; null before the first. A run past the limit of the model call it's waiting
 * for (waiting), or with nothing finished for STALLED_MS, has stalled: it's marked failed here, when someone looks
 * (a run can't time itself out: timers end with the request that started it), and the call it left behind is ignored.
 */
export function current(step: string): Run | null {
  const x: Run | null = $kv.get(runKey(step))
  if (!x || x.status !== 'running') return x
  const w = x.waiting
  if (w && Date.now() - new Date(w.since).getTime() > w.seconds * 1000) {
    x.status = 'failed'
    x.error = `${w.what}: the model didn't answer in ${w.seconds} s (the provider sometimes drops a request). Generate again, or pick another model.`
  } else if (Date.now() - new Date(x.updated || x.started).getTime() > STALLED_MS) {
    x.status = 'failed'
    x.error = `Stalled: nothing answered for ${STALLED_MS / 60_000} minutes after "${x.steps.length ? x.steps[x.steps.length - 1].step : 'starting'}". Generate again.`
  } else {
    return x
  }
  delete x.waiting
  $kv.set(runKey(step), x)
  return x
}

/** The run a step handed on, or null. */
export function kept(step: string): Run | null {
  return $kv.get(keptKey(step))
}

/** Hands a step's latest run on to the next step, if it's the one asked for and it finished; returns it, or null. */
export function keep(step: string, id: string): Run | null {
  const x = current(step)
  if (!x || x.id !== id || x.status !== 'done') return null
  $kv.set(keptKey(step), x)
  return x
}

type Work = (trace: (s: TraceStep) => void, update: (change: (x: Run) => void) => void) => Promise<void>

/** Starts a step's run with its own fields (not awaited: work runs after the request answers); returns it as it starts. */
function begin(step: string, fields: { [k: string]: any }, work: Work): Run {
  const key = runKey(step)
  const now = new Date().toISOString()
  const run: Run = { ...fields, id: $security.randomString(12), status: 'running', started: now, updated: now, steps: [], costUSD: 0 }
  $kv.set(key, run)
  /** Changes the stored run, unless a newer one replaced it. */
  const update = (change: (x: Run) => void) => {
    const x: Run | null = $kv.get(key)
    // replaced by a newer run, or marked stalled (a late answer from a call it gave up on changes nothing)
    if (!x || x.id !== run.id || x.status !== 'running') throw new Superseded()
    change(x)
    x.updated = new Date().toISOString()
    $kv.set(key, x)
  }
  const trace = (s: TraceStep) => update((x) => { x.steps.push(s); x.costUSD += s.costUSD || 0 })
  work(trace, update).then(() => update((x) => { x.status = 'done' })).catch((err: any) => {
    if (err instanceof Superseded) return
    try {
      update((x) => { x.status = 'failed'; x.error = err.message })
    } catch (_) {
      // superseded meanwhile
    }
  })
  return run
}

const secs = (t0: number) => Math.round((Date.now() - t0) / 100) / 10

/**
 * A model call waited for at most seconds: the run notes what it's waiting for, so current() can mark it failed once
 * that's past (a provider sometimes never answers a request, and the HTTP client gives up only after 5 minutes).
 */
async function waitFor<T>(update: (change: (x: Run) => void) => void, what: string, seconds: number, call: () => Promise<T>): Promise<T> {
  update((x) => { x.waiting = { what, seconds, since: new Date().toISOString() } })
  const out = await call()
  update((x) => { delete x.waiting })
  return out
}

// ---- step 2: write a story

/** Writes a story's text (step 2). */
export function startStory(story: any, app: any, t: Tuning, minutes: number, asked: string): Run {
  return begin('story', { minutes, topic: asked }, async (trace, update) => {
    // the topic: given, or (with knowledge buckets) picked from them and checked against the stories told; with neither,
    // the writer gets no topic
    let topic = asked
    if (asked) {
      trace({ step: 'Picking the topic', parts: [{ label: 'Given', text: asked }] })
    } else if (t.knowledge.buckets.length) {
      topic = (await story.inspiration(app, [], t, '', trace)).topic
    } else {
      trace({ step: 'Picking the topic', parts: [{ label: 'No topic and no knowledge buckets', text: 'The writer gets no topic.' }] })
    }

    // what the knowledge buckets hold about it (story.research)
    let t0 = Date.now()
    const known = topic ? await story.research(t, topic) : { text: '' }
    trace({ step: 'Knowledge', seconds: secs(t0), parts: !t.knowledge.buckets.length || !topic
      ? [{ label: 'No search', text: !topic ? 'No topic to search for.' : 'No knowledge buckets: the writer uses the model\'s own knowledge.' }]
      : [{ label: 'Search', text: `buckets: ${t.knowledge.buckets.join(', ')}\nquery: ${topic}` },
        { label: 'How', text: `The ${known.found.results.length} best matches for the topic, by meaning and words. Each is used or ` +
          'not as marked: at most 6 from docs, 4 from commits and 4 from code, each up to 1,500 characters, with the opening ' +
          'of the README each belongs to. The writer gets exactly what\'s under Writing → Prompt: Knowledge.' },
        ...known.found.results.map((r: any, i: number) => ({
          label: `${known.used[i].startsWith('used') ? '✓' : '✗'} ${r.score.toFixed(3)}  ${r.document}${r.heading ? ` › ${r.heading}` : ''} (${known.used[i]})`,
          text: String(r.text) }))] })
    if (known.found) story.record(app, 'search', known.found)

    // the writer: the preamble and the fine-tuning; the request, its length and the knowledge
    const finetune = t.prompts.finetune.trim()
    const system = [{ label: 'Preamble', text: t.prompts.write.trim() },
      ...(finetune ? [{ label: 'Fine-tuning', text: finetune }] : [])]
    const words = Math.round(minutes * t.length.wordsPerMinute)
    const prompt = [{ label: 'Request', text: `${topic ? `Write a story about: ${topic}` : 'Write a story.'} About ${words} words.` },
      ...(known.text ? [{ label: 'Knowledge', text: `What the knowledge base holds about it:\n\n${known.text}` }] : [])]
    t0 = Date.now()
    const result: any = await waitFor(update, 'Writing', 90, () => $llm.text.completeAsync({ model: t.models.text, reasoning: 'off',
      maxTokens: 12000, temperature: 0.9, system: system.map((p) => p.text).join('\n\n'), prompt: prompt.map((p) => p.text).join('\n\n'),
      schema: DRAFT_SCHEMA }))
    const usd = story.record(app, 'sample-story', result)
    trace({ step: 'Writing', model: result.model, costUSD: usd, seconds: secs(t0), parts: [
      ...system.map((p) => ({ label: `System: ${p.label}`, text: p.text })),
      ...prompt.map((p) => ({ label: `Prompt: ${p.label}`, text: p.text })),
      { label: 'Answer', text: JSON.stringify(result.data, null, 2) }] })
    const draft: Draft = { title: String(result.data.title).trim(), body: String(result.data.body).trim() }
    update((x) => { x.story = draft })
  })
}

/** What a step's chat helper reads beside its fields: the story written last (step 2), or the story step 2 handed on
 * with the latest picture plan (step 3). */
export function context(view: string): string {
  if (view === 'story') {
    const x = current('story')
    return x && x.story ? `The story written last with these settings:\n\nTitle: ${x.story.title}\n\n${x.story.body}` : 'No story has been tried yet.'
  }
  if (view === 'pictures') {
    const s = kept('story'), p = current('pictures')
    const storyPart = s && s.story ? `The story:\n\nTitle: ${s.story.title}\n\n${s.story.body}` : 'No story yet.'
    const plan = p && p.spots ? `The pictures planned last:\n${p.spots.map((x: Spot, i: number) => `${i + 1}. at "${x.quote}": ${x.shows}`).join('\n')}` +
      (p.vibe ? `\n\nTheir theme (the reference picture's prompt): ${p.vibe}` : '') : 'No pictures planned yet.'
    return `${storyPart}\n\n${plan}`
  }
  return ''
}

// ---- step 3: where the pictures go, their shared vibe, and one reference picture

const SPOTS_SCHEMA = {
  type: 'object',
  properties: {
    pictures: {
      type: 'array',
      description: 'The pictures, in story order',
      items: {
        type: 'object',
        properties: {
          quote: { type: 'string', description: 'A few words copied exactly from the story, where this picture appears' },
          shows: { type: 'string', description: 'What the picture shows' },
        },
        required: ['quote', 'shows'],
      },
    },
  },
  required: ['pictures'],
}

const THEME_SCHEMA = {
  type: 'object',
  properties: { vibe: { type: 'string', description: 'The overall vibe, as the prompt for one reference picture' } },
  required: ['vibe'],
}

/** A picture's place: a quote from the story, whether it's really there, and what the picture shows. */
export type Spot = { quote: string; shows: string; found: boolean }

const squash = (x: string) => x.replace(/\s+/g, ' ').trim().toLowerCase()

/** Plans the pictures for the story step 2 handed on (step 3): the most important spots, their shared vibe, and a
 * reference picture painted from it. Stops before the story's pictures. */
export function startPictures(story: any, app: any, t: Tuning, count: number): Run {
  const from = kept('story')
  if (!from || !from.story) throw new Error('no story yet: generate one in step 2 and Save and next')
  const draft: Draft = from.story
  return begin('pictures', { count, storyId: from.id, storyTitle: draft.title }, async (trace, update) => {
    // 1. the spots
    let t0 = Date.now()
    const system = t.prompts.spots.replace(/\{count\}/g, String(count)).trim()
    const prompt = `${draft.title}\n\n${draft.body}`
    const planned: any = await waitFor(update, 'Finding the spots', 60, () => $llm.text.completeAsync({ model: t.models.plan,
      reasoning: 'off', maxTokens: 6000, temperature: 0.7, system, prompt, schema: SPOTS_SCHEMA }))
    let usd = story.record(app, 'sample-plan', planned)
    // what the model wrote back: each quote should be in the story, word for word
    const body = squash(draft.body)
    const spots: Spot[] = planned.data.pictures.map((p: any) => ({ quote: String(p.quote).trim(), shows: String(p.shows).trim(),
      found: body.includes(squash(String(p.quote))) }))
    trace({ step: 'Finding the spots', model: planned.model, costUSD: usd, seconds: secs(t0), parts: [
      { label: 'System: Finding the spots (settings)', text: system },
      { label: 'Prompt: the story from step 2', text: prompt },
      { label: 'Answer shape', text: JSON.stringify(SPOTS_SCHEMA, null, 2) },
      { label: 'Answer', text: JSON.stringify(planned.data, null, 2) },
      { label: `Checked: ${spots.filter((x) => x.found).length} of ${spots.length} quotes are in the story`,
        text: spots.map((x, i) => `${x.found ? '✓' : '✗'} ${i + 1}. "${x.quote}"`).join('\n') }] })
    update((x) => { x.spots = spots })

    // 2. the theme they share
    t0 = Date.now()
    const themeSystem = t.prompts.theme.trim()
    const themePrompt = spots.map((x, i) => `${i + 1}. ${x.shows}`).join('\n')
    const themed: any = await waitFor(update, 'The theme', 60, () => $llm.text.completeAsync({ model: t.models.plan, reasoning: 'off',
      maxTokens: 2000, temperature: 0.7, system: themeSystem, prompt: themePrompt, schema: THEME_SCHEMA }))
    usd = story.record(app, 'sample-plan', themed)
    const vibe = String(themed.data.vibe).trim()
    trace({ step: 'The theme', model: themed.model, costUSD: usd, seconds: secs(t0), parts: [
      { label: 'System: The theme (settings)', text: themeSystem },
      { label: 'Prompt: what each picture shows', text: themePrompt },
      { label: 'Answer shape', text: JSON.stringify(THEME_SCHEMA, null, 2) },
      { label: 'Answer', text: JSON.stringify(themed.data, null, 2) }] })
    update((x) => { x.vibe = vibe })

    // 3. the reference picture, painted from the vibe
    t0 = Date.now()
    const call = { model: t.models.image, prompt: vibe, aspectRatio: '16:9' }
    const painted: any = await waitFor(update, 'The reference picture', 120, () => $llm.image.generateAsync(call))
    usd = story.record(app, 'sample-picture', painted)
    const f = painted.files[0]
    const r = new Record(app.findCollectionByNameOrId('setup_files'))
    r.set('step', 'pictures')
    r.set('file', f.data ? $llm.file(f) : await $llm.fileAsync(f))
    app.save(r)
    const image = { id: r.id, file: r.getString('file') }
    trace({ step: 'The reference picture', model: painted.model, costUSD: usd, seconds: secs(t0), parts: [
      { label: 'Sent to the picture model', text: JSON.stringify(call, null, 2) }] })
    update((x) => { x.image = image })
  })
}
