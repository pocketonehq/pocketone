// POST /admin/estimate {values}: what a story at the default length costs with the given (unsaved) settings' models,
// step by step, each measured from the stories made with that model or worked out from its list price
// (story.estimateStory). Superusers only (admin/+middleware.ts).
export async function post(ctx: any) {
  const story = ctx.resolve('story')
  const { values } = ctx.req.body() || {}
  return ctx.res.json(200, await story.estimateStory(ctx.app, story.tuning(ctx.app, values || {})))
}
