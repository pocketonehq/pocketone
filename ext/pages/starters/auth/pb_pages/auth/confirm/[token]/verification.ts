// The verification email links here: {APP_URL}/auth/confirm/{TOKEN}/verification
module.exports = {
  load(ctx) {
    try {
      ctx.session.confirmVerification(ctx.params.token)
    } catch (e) {
      throw ctx.httpError(400, 'This verification link is invalid or has expired.')
    }
    return ctx.res.redirect('/', { flash: 'Your account has been verified.' })
  },
}
