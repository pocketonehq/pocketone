// Set up's "Try a story" (step 2): a story's text made with the screen's settings (saved or not) in the background,
// every step written to $kv as it finishes (what went in, what came out: story.ts's Trace), so the screen can poll it
// and show the whole log. One trial at a time: a new one replaces the last, and the old one stops writing when it
// notices.
//
// This is the wizard's own workflow, not story.make's: the writer gets only the preamble and the fine-tuning as its
// system prompt, the request (and what the knowledge buckets hold about it) as its prompt, and answers {title, body}.
// Nothing else: no rules about length or shape, no plain-words or pronunciation passes. Those come back later, each as
// its own step after an interesting story is written.
import type { Tuning, TraceStep } from './story'

const KEY = 'storytime/trial'
/** The trial step 2 handed on (Save and next): what step 3 starts from, until step 2 hands on another. */
const KEPT_KEY = 'storytime/wizard/story'

export type Trial = {
  id: string
  status: 'running' | 'done' | 'failed'
  started: string
  /** when a step last finished (or it started) */
  updated: string
  minutes: number
  /** what was asked: a topic, or "" for the app to pick one */
  topic: string
  steps: TraceStep[]
  story?: Draft
  error?: string
  costUSD: number
}

/** What the writer answers: a title and the story's text. */
export type Draft = { title: string; body: string }

const DRAFT_SCHEMA = {
  type: 'object',
  properties: { title: { type: 'string' }, body: { type: 'string' } },
  required: ['title', 'body'],
}

class Superseded extends Error {}

/** A trial with no step finished for this long has stalled (a model or search call that never answered). */
const STALLED_MS = 3 * 60_000

/** The latest trial, running or finished; null before the first. One that stalled shows as failed. */
export function current(): Trial | null {
  const x: Trial | null = $kv.get(KEY)
  if (x && x.status === 'running' && Date.now() - new Date(x.updated || x.started).getTime() > STALLED_MS) {
    x.status = 'failed'
    x.error = `Stalled: nothing answered for ${STALLED_MS / 60_000} minutes after "${x.steps.length ? x.steps[x.steps.length - 1].step : 'starting'}". Try again.`
  }
  return x
}

/** The stored trial as it is (current() reports a stalled one as failed without storing that). */
const stored = (): Trial | null => $kv.get(KEY)

/** Starts a trial (not awaited: it runs after the request answers) and returns it as it starts. */
export function start(story: any, app: any, t: Tuning, minutes: number, topic: string): Trial {
  const now = new Date().toISOString()
  const trial: Trial = { id: $security.randomString(12), status: 'running', started: now, updated: now, minutes, topic,
    steps: [], costUSD: 0 }
  $kv.set(KEY, trial)
  run(story, app, t, trial.id, minutes, topic)
  return trial
}

async function run(story: any, app: any, t: Tuning, id: string, minutes: number, asked: string) {
  /** Changes the stored trial, unless a newer one replaced it. */
  const update = (change: (x: Trial) => void) => {
    const now = stored()
    if (!now || now.id !== id) throw new Superseded()
    change(now)
    now.updated = new Date().toISOString()
    $kv.set(KEY, now)
  }
  const trace = (s: TraceStep) => update((x) => { x.steps.push(s); x.costUSD += s.costUSD || 0 })
  try {
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
    const result = await $llm.text.completeAsync({ model: t.models.text, reasoning: 'off', maxTokens: 12000, temperature: 0.9,
      system: system.map((p) => p.text).join('\n\n'), prompt: prompt.map((p) => p.text).join('\n\n'), schema: DRAFT_SCHEMA })
    const usd = story.record(app, 'sample-story', result)
    trace({ step: 'Writing', model: result.model, costUSD: usd, seconds: secs(t0), parts: [
      ...system.map((p) => ({ label: `System: ${p.label}`, text: p.text })),
      ...prompt.map((p) => ({ label: `Prompt: ${p.label}`, text: p.text })),
      { label: 'Answer', text: JSON.stringify(result.data, null, 2) }] })
    const draft: Draft = { title: String(result.data.title).trim(), body: String(result.data.body).trim() }
    update((x) => { x.status = 'done'; x.story = draft })
  } catch (err: any) {
    if (err instanceof Superseded) return
    try {
      update((x) => { x.status = 'failed'; x.error = err.message })
    } catch (_) {
      // superseded meanwhile
    }
  }
}

/** The trial step 2 handed on, or null. */
export function kept(): Trial | null {
  return $kv.get(KEPT_KEY)
}

/** Hands the latest trial on to the next step, if it's the one asked for and it finished; returns it, or null. */
export function keep(id: string): Trial | null {
  const x = current()
  if (!x || x.id !== id || x.status !== 'done') return null
  $kv.set(KEPT_KEY, x)
  return x
}

const secs = (t0: number) => Math.round((Date.now() - t0) / 100) / 10

/** The last trial's story as the chat helper reads it, or "" when there's none. */
export function storyText(): string {
  const x = current()
  if (!x || !x.story) return ''
  return `Title: ${x.story.title}\n\n${x.story.body}`
}
