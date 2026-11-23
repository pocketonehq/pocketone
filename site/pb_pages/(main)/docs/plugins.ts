// Old URL: the extension docs moved from /docs/plugins to /docs/extensions.
module.exports = {
  get: (ctx) => ctx.res.redirect('/docs/extensions', { status: 301 }),
}
