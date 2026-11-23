// Every route under /admin is for superusers only. The dashboard's Story Time screen sends the signed-in superuser's
// token in the Authorization header (app.pb.send), which Pages reads into ctx.auth.
module.exports = (ctx: any, next: () => any) => {
  if (!ctx.auth?.isSuperuser()) {
    throw ctx.httpError(401, 'sign in as a superuser (the dashboard at /_/)')
  }
  return next()
}
