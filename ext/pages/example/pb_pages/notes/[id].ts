// One note. ctx.pb() is the PocketBase JS SDK, calling this app in-process as the visitor, so API rules apply.
export function load(ctx: pages.Context) {
  try {
    return { note: ctx.pb().collection('notes').getOne(ctx.params.id, { expand: 'author' }) }
  } catch (e: any) {
    throw ctx.httpError(404, 'No such note')
  }
}
