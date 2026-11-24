// The wizard's chat helper (the dashboard's Story Time → Set up screen). Each step owns a few settings fields; a chat
// turn sends the step, every field's current value (as on the screen, saved or not) and the chat so far, and one
// structured call to the settings' text model answers with new values for just the fields the message is about, and a
// short reply saying what changed. The schema holds `field` to the step's own fields, so nothing else can change.

/** A settings field a step owns: its path in Tuning, what it's called on screen, and what it's for (the model reads this). */
export type StepField = {
  path: string
  label: string
  about: string
}

export type Step = { id: string; title: string; question: string; about: string; fields: StepField[] }

export const STEPS: Step[] = [
  {
    id: 'vibe',
    title: 'What is it about?',
    question: 'What do you want the general vibe of your story site to be?',
    about: 'The site\'s name, the tagline under it, and the general vibe every story is written in.',
    fields: [
      { path: 'site.title', label: 'Title',
        about: 'The site\'s name, shown large on the front page; its last word is highlighted. A few words. Also called the name or heading.' },
      { path: 'site.caption', label: 'Tagline',
        about: 'The one or two sentences under the title: what the stories are and why someone would listen. Also called the ' +
          'intro text, subtitle, description or the explanation under the title.' },
      { path: 'prompts.write', label: 'Writer preamble',
        about: 'What the stories are like, for the model that writes every one: who tells them, what they are about, how ' +
          'deep they go, their tone, pace and voice. Asking for the stories to be different (calmer, funnier, more ' +
          'technical, told by someone) means this field. Only what the person said or clearly implied: the app adds the ' +
          'length, reading aloud, the language and the answer\'s fields itself, so never write those.' },
    ],
  },
]

export type ChatTurn = { role: 'user' | 'assistant'; text: string }
export type Edit = { field: string; value: string }

const get = (o: any, path: string) => path.split('.').reduce((x, k) => (x == null ? x : x[k]), o)

const system = (s: Step) => [
  'You help someone set up a story site: a site where a model writes stories on request, which are read aloud with ' +
  `pictures and music. This step: ${s.about}`,
  `The fields:\n${s.fields.map((f) => `- "${f.path}" (${f.label}): ${f.about}`).join('\n')}`,
  'Each message comes with every field\'s current value. Work out which fields the latest message is about, and return ' +
  'a new value in "edits" only for those; leave the rest out. A message about one field ("I don\'t like the title") ' +
  'changes only that field, and keeps the rest of its text. A description of the whole site, such as a first answer ' +
  'to "' + s.question + '", starts every field fresh from what the person said (not from the current values), so they ' +
  'agree with each other. Each edit\'s "value" is the field\'s complete new text, not a fragment or a diff.',
  'Write only what the person said or clearly meant, in plain words. Never add rules, instructions, formats or details ' +
  'they didn\'t ask for: a short field that says exactly what they want is right.',
  '"reply" is one or two short, plain sentences: what you changed and how (name the fields by their labels), or, when ' +
  'the message is unclear or asks something, your question or answer with no edits.',
].join('\n\n')

/** One chat turn: the model's edits (only the step's fields, each checked) and its reply, plus the call (for its cost). */
export async function assist(t: any, stepId: string, values: any, chat: ChatTurn[]): Promise<{ edits: Edit[]; reply: string; result: any }> {
  const s = STEPS.find((x) => x.id === stepId)
  if (!s) throw new Error(`no step ${stepId}`)
  const current = Object.fromEntries(s.fields.map((f) => [f.path, String(get(values, f.path) ?? '')]))
  const last = chat[chat.length - 1]
  // the screen opens with the step's question as the helper's line; the system prompt has it, and a conversation starts with the user
  const earlier = chat.slice(0, -1)
  while (earlier.length && earlier[0].role === 'assistant') earlier.shift()
  const messages = [
    ...earlier.map((c) => ({ role: c.role, content: c.text })),
    { role: 'user', content: `The fields now:\n${JSON.stringify(current, null, 2)}\n\nMy message: ${last.text}` },
  ]
  const result = await $llm.text.completeAsync({ model: t.models.text, system: system(s), messages, reasoning: 'off',
    maxTokens: 8000, temperature: 0.7,
    schema: {
      type: 'object',
      properties: {
        edits: { type: 'array', items: { type: 'object', required: ['field', 'value'],
          properties: { field: { type: 'string', enum: s.fields.map((f) => f.path) }, value: { type: 'string' } } } },
        reply: { type: 'string' },
      },
      required: ['edits', 'reply'],
    } })
  // what the model wrote back: an empty or unchanged value isn't an edit
  const edits: Edit[] = []
  for (const e of result.data.edits) {
    const value = String(e.value).trim()
    if (value && value !== current[e.field]) edits.push({ field: e.field, value })
  }
  const reply = String(result.data.reply).trim()
  return { edits, reply, result }
}
