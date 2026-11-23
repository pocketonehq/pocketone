// POST /ask {q, lang}: the header's ask box (assets/ask.js). Server-sent events: "sources" (the passages, with their page
// URLs), "text" pieces of the answer as it's written, then "done" {model, usage, cached} or "error" (a limit reached, no
// provider). The sources come first, so a refused answer still leaves the reader with the right pages.
export async function post(ctx: any) {
  const { q, lang } = ctx.req.body() || {}
  if (typeof q !== 'string' || !q.trim()) throw ctx.httpError(400, 'send {q}')
  const stream = ctx.res.sse()
  try {
    // awaited: the model works off the app's JS VM, so other requests are served while this one streams
    // lang: the reader's code language from the docs' TS | Go tabs ("go", else TypeScript)
    const r = await ctx.resolve('ask').ask(ctx, q, lang === 'go' ? 'go' : 'ts', {
      onSources: (s: unknown) => stream.send('sources', JSON.stringify(s)),
      onText: (t: string) => stream.send('text', JSON.stringify(t)),
    })
    stream.send('done', JSON.stringify(r))
  } catch (err: any) {
    // limit: a limit in Settings → LLM is used up (its message is written for the reader); not_configured: no provider
    // ($llm errors reach JS as "code: message")
    const msg = String(err.message || err).replace(/^Error: /, '')
    const m = msg.match(/^([a-z_]+): ([\s\S]*)$/)
    stream.send('error', JSON.stringify({ code: err.code || (m ? m[1] : ''), message: m ? m[2] : msg }))
  }
  stream.close()
}
