// Old URL: /docs/plugins/<name>/<guide> moved to /docs/extensions/<name>/<guide>.
module.exports = {
  get: (ctx) =>
    ctx.res.redirect(
      `/docs/extensions/${encodeURIComponent(ctx.params.name)}/${encodeURIComponent(ctx.params.guide)}`,
      { status: 301 },
    ),
}
