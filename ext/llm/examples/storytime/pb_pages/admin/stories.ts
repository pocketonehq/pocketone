// POST /admin/stories: adds every finished story the stories bucket (storytime-stories) doesn't have yet, so Inspire me
// knows them as told. Superusers only.
export function post(ctx: any) {
  const story = ctx.resolve('story')
  return ctx.res.json(200, { added: story.rememberAll(ctx.app) })
}
