// Old URL: /docs/remote moved to /docs/features/remote.
module.exports = {
  get: (ctx) => ctx.res.redirect('/docs/features/remote', { status: 301 }),
}
