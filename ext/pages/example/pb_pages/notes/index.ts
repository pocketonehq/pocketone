// The notes list. ctx.pb() is the PocketBase JS SDK, calling this app in-process as the visitor, so API rules apply.
export function load(ctx: pages.Context) {
  return { notes: ctx.pb().collection('notes').getFullList({ sort: '-created', expand: 'author' }) }
}

export function post(ctx: pages.Context) {
  if (!ctx.auth) return ctx.res.redirect('/auth/login', { flash: 'Sign in to post a note.' })
  const { title, body } = ctx.req.body()
  try {
    ctx.pb().collection('notes').create({ title, body, author: ctx.auth.id })
  } catch (e: any) {
    return ctx.res.redirect('/notes', { flash: 'Could not save: ' + (e.response?.message || e) })
  }
  return ctx.res.redirect('/notes', { flash: 'Note saved.' })
}
