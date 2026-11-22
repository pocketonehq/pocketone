// GET /admin/export: the saved settings and every recording (its audio included, base64), as one JSON file to import on
// another copy of the app (dev to prod, or back). The same format as _private/defaults.json. Superusers only.
export function get(ctx: any) {
  ctx.resolve('admin').superuserOnly(ctx)
  const story = ctx.resolve('story')
  const voices = ctx.app.findRecordsByFilter('voices', '', 'created', 0, 0).map((r: any) => ({
    id: r.id,
    name: r.getString('name'),
    passage: r.getString('passage'),
    created: r.getString('created'),
    audio: $llm.fromRecord(r, 'audio'), // { name, type, data (base64) }
  }))
  const out = { app: 'storytime', version: 1, exported: new Date().toISOString(), settings: story.tuning(ctx.app), voices }
  const day = out.exported.slice(0, 10)
  ctx.res.header('Content-Disposition', `attachment; filename="storytime-${day}.json"`)
  return ctx.res.json(200, out)
}
