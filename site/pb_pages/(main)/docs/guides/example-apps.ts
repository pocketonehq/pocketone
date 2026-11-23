// Old URL: the example apps moved to their own section, /docs/examples.
module.exports = {
  get: (ctx) => ctx.res.redirect('/docs/examples', { status: 301 }),
}
