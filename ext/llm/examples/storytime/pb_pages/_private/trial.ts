// Set up's "Try a story" (step 2): a story's text made with the screen's settings (saved or not) in the background,
// every step written to $kv as it finishes (what went in, what came out: story.ts's Trace), so the screen can poll it
// and show the whole log. Text only: no voice, pictures or music. One trial at a time: a new one replaces the last, and
// the old one stops writing when it notices.
import type { Tuning, TraceStep, Story } from './story'

const KEY = 'storytime/trial'

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
  story?: Story
  error?: string
  costUSD: number
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
    // the same steps a listener's story takes (story.make): "Your pick" with knowledge buckets picks a topic first
    let topic = asked || story.ANY_TOPIC
    if (asked) {
      trace({ step: 'Picking the topic', parts: [{ label: 'Given', text: asked }] })
    } else if (t.knowledge.buckets.length) {
      topic = (await story.inspiration(app, [], t, '', trace)).topic || topic
    } else {
      trace({ step: 'Picking the topic', parts: [{ label: 'No knowledge buckets',
        text: `The request is "${story.ANY_TOPIC}", so the writer picks the topic itself (see Rules the app adds).` }] })
    }
    const w = await story.write(t, topic, minutes, trace)
    // inspiration records its own calls
    story.record(app, 'sample-story', w.result)
    if (w.fixes) story.record(app, 'sample-story', w.fixes)
    if (w.said) story.record(app, 'sample-story', w.said)
    if (w.searched) story.record(app, 'search', w.searched)
    update((x) => { x.status = 'done'; x.story = w.story })
  } catch (err: any) {
    if (err instanceof Superseded) return
    try {
      update((x) => { x.status = 'failed'; x.error = err.message })
    } catch (_) {
      // superseded meanwhile
    }
  }
}

/** The last trial's story as the chat helper reads it, or "" when there's none. */
export function storyText(): string {
  const x = current()
  if (!x || !x.story) return ''
  const s = x.story
  return `Title: ${s.title}\nTopic: ${s.topic}\nWhat recurs in its pictures: ${s.look}\n\n${s.sentences.join('\n')}`
}
