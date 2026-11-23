// POST /admin/import {app: "storytime", version: 1, settings, voices}: what /admin/export wrote. Each recording is added
// unless the same one (same name and audio) is already here; the narrator's recording is pointed at the copy here.
// Then the file's settings are laid over the current ones and saved: a partial file (a profile with only the front
// page and prompts, say) changes just what it has. Superusers only.
export function post(ctx: any) {
  const story = ctx.resolve('story')
  const body = ctx.req.body() || {}
  if (body.app !== 'storytime' || body.version !== 1) throw ctx.httpError(400, 'not a Story Time export (app "storytime", version 1)')

  // the recordings already here, by name and a hash of their audio
  const hash = (f: any) => $security.sha256(String(f && f.data || ''))
  const here: { [key: string]: string } = {}
  for (const r of ctx.app.findRecordsByFilter('voices', '', '', 0, 0)) {
    here[`${r.getString('name')}|${hash($llm.fromRecord(r, 'audio'))}`] = r.id
  }

  const ids: { [oldId: string]: string } = {}
  let added = 0, kept = 0
  const col = ctx.app.findCollectionByNameOrId('voices')
  for (const v of Array.isArray(body.voices) ? body.voices : []) {
    if (!v || !v.audio || !v.audio.data) continue
    const key = `${v.name || ''}|${hash(v.audio)}`
    if (here[key]) {
      ids[v.id] = here[key]
      kept++
      continue
    }
    const r = new Record(col)
    r.set('name', String(v.name || 'Imported voice').slice(0, 80))
    r.set('passage', String(v.passage || ''))
    r.set('audio', $llm.file(v.audio))
    ctx.app.save(r)
    ids[v.id] = here[key] = r.id
    added++
  }

  const t = body.settings && typeof body.settings === 'object' ? JSON.parse(JSON.stringify(body.settings)) : {}
  if (t.voice && t.voice.clip) {
    // a recording in the file maps to its copy here; one that isn't in the file is kept if it's already here
    let here = ''
    try {
      here = ctx.app.findRecordById('voices', t.voice.clip).id
    } catch (_) {}
    t.voice.clip = ids[t.voice.clip] || here
  }
  const values = story.saveTuning(ctx.app, story.merge(story.tuning(ctx.app), t))
  return ctx.res.json(200, { values, voices: { added, kept } })
}
