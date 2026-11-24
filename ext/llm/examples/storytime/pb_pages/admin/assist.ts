// POST /admin/assist {step, values, chat}: one chat turn of the Set up screen's helper (_private/assist.ts). values is
// the settings as on the screen (saved or not); chat is [{role: "user" | "assistant", text}], the latest message last.
// Answers {edits: [{field, value}], reply, costUSD, model}: new values for the step's fields the message was about. Nothing
// is saved. GET /admin/assist lists the steps. Superusers only (admin/+middleware.ts); the cost is recorded (kind assist).
export function get(ctx: any) {
  return ctx.res.json(200, { steps: ctx.resolve('assist').STEPS })
}

export async function post(ctx: any) {
  const story = ctx.resolve('story')
  const { step, values, chat } = ctx.req.body() || {}
  if (!Array.isArray(chat) || !chat.length || chat[chat.length - 1].role !== 'user' || !String(chat[chat.length - 1].text || '').trim()) {
    throw ctx.httpError(400, 'chat ends with the user\'s message')
  }
  const t = story.tuning(ctx.app, values || {})
  try {
    const out = await ctx.resolve('assist').assist(t, step, t, chat.map((c: any) => ({ role: c.role === 'assistant' ? 'assistant' : 'user', text: String(c.text || '') })))
    const usd = story.record(ctx.app, 'assist', out.result)
    return ctx.res.json(200, { edits: out.edits, reply: out.reply, model: out.result.model, costUSD: usd })
  } catch (err: any) {
    return ctx.res.json(502, { message: err.message })
  }
}
