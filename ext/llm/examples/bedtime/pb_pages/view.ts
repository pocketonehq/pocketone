// POST /view {id}: counts a view of a finished story. The player sends it once a listener has heard 10 seconds of it.
// Counted in SQL (views + 1), so two views at once both count, and the record's updated time stays as it was.
export async function post(ctx: any) {
  const { id } = ctx.req.body()
  if (typeof id !== 'string' || !id) throw ctx.httpError(400, 'send {id}')
  const res = ctx.app.db()
    .newQuery("UPDATE stories SET views = views + 1 WHERE id = {:id} AND status = 'done'")
    .bind({ id })
    .execute()
  if (res.rowsAffected() === 0) throw ctx.httpError(404, 'No such story')
  return ctx.res.json(200, { ok: true })
}
