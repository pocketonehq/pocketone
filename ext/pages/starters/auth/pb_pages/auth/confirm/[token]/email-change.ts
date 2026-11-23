// The email change email links here: {APP_URL}/auth/confirm/{TOKEN}/email-change
const message = (e) => String((e && e.message) || e).replace(/^GoError: /, '')

module.exports = {
  load() {
    return { error: '' }
  },

  post(ctx) {
    const body = ctx.req.body()
    let record
    try {
      record = ctx.session.confirmEmailChange(ctx.params.token, body.password)
    } catch (e) {
      return { error: message(e) }
    }
    const flash = `Your email address is now ${record.email()}.`
    // signed in as this account: the session was renewed with the new address
    if (ctx.auth) return ctx.res.redirect('/account', { flash })
    return ctx.res.redirect('/auth/login', { flash: flash + ' Sign in with it.' })
  },
}
