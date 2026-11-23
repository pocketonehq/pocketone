// Old URL: /docs/plugins/<name> moved to /docs/extensions/<name>.
module.exports = {
  get: (ctx) => ctx.res.redirect(`/docs/extensions/${encodeURIComponent(ctx.params.name)}`, { status: 301 }),
}
