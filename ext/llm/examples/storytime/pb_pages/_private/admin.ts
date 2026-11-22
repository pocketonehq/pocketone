// For /admin's APIs: only a superuser may use them. The admin page sends the dashboard's superuser token in the
// Authorization header, which Pages reads into ctx.auth.
export function superuserOnly(ctx: any) {
  if (!ctx.auth || typeof ctx.auth.isSuperuser !== 'function' || !ctx.auth.isSuperuser()) {
    throw ctx.httpError(401, 'sign in as a superuser (the dashboard at /_/)')
  }
}
