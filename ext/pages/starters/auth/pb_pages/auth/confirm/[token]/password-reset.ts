// The password reset email links here: {APP_URL}/auth/confirm/{TOKEN}/password-reset
const message = (e) => String((e && e.message) || e).replace(/^GoError: /, '')

module.exports = {
  load() {
    return { error: '' }
  },

  post(ctx) {
    const body = ctx.req.body()
    try {
      ctx.session.confirmPasswordReset(ctx.params.token, body.password, body.passwordConfirm)
    } catch (e) {
      return { error: message(e) }
    }
    return ctx.res.redirect('/auth/login', {
      flash: 'Your password has been changed. Sign in with the new one.',
    })
  },
}
