// Old URL: /docs/writing-a-plugin moved to /docs/writing-an-extension.
module.exports = {
  get: (ctx) => ctx.res.redirect('/docs/writing-an-extension', { status: 301 }),
}
