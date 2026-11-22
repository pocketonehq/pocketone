// Old URL: /docs/examples/bedtime-story moved to /docs/examples/story-time when the example became Story Time.
module.exports = {
  get: (ctx) => ctx.res.redirect('/docs/examples/story-time', { status: 301 }),
}
