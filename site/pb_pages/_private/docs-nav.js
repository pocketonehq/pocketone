// Which docs nav a page shows:
//   - the pages of an extension with docs of its own (ext/<name>/docs) show its nav, (main)/_private/toc-<name>.md,
//     except its extension docs (/docs/extensions/<name>/extension);
//   - every other extension page shows the Extensions nav (toc-extensions), and contract pages the Contracts nav
//     (toc-contracts), which the main nav's Extend PocketOne section slides into;
//   - the example apps (/docs/examples) show their own, hand-written nav (toc-examples);
//   - the JavaScript and Go API references show theirs (toc-javascript, toc-go, generated), and the REST API its own
//     (toc-rest, hand-written);
//   - everything else shows the main nav (toc).
// site/sync writes those navs and lists the extensions with docs in site-navs.js. The docs layouts load this with
// ctx.resolve, not as a +config.js plugin: a host loads plugins once, at start, so a deploy that changed one wouldn't
// take effect until a restart. assets/docs.js decides the same way which links change the nav.
//
//   const docsNav = ctx.resolve('docs-nav')(ctx)
//   docsNav.name  // '' for the main nav, 'contracts', 'extensions', 'examples', 'javascript', 'go', 'rest', 'starters', or an extension's name (its docs)
//   docsNav.toc   // the partial to include: 'toc' or 'toc-<name>'
//   docsNav.names // every extension with docs of its own
module.exports = (ctx) => {
  const names = ctx.resolve('site-navs')
  const path = ctx.req.url.pathname
  const m = path.match(/^\/docs\/extensions\/([^/]+)/)
  let name = ''
  // an extension's docs show their nav; its README (/docs/extensions/<name>/extension, site/sync's readmeSlug)
  // are an extension page like any other
  if (m && names.includes(m[1]) && !new RegExp(`^/docs/extensions/${m[1]}/extension(/|$)`).test(path)) name = m[1]
  else if (/^\/docs\/extensions(\/|$)/.test(path)) name = 'extensions'
  else if (/^\/docs\/contracts(\/|$)/.test(path)) name = 'contracts'
  else if (/^\/docs\/examples(\/|$)/.test(path)) name = 'examples'
  else if (/^\/docs\/reference\/javascript(\/|$)/.test(path)) name = 'javascript'
  else if (/^\/docs\/reference\/go(\/|$)/.test(path)) name = 'go'
  else if (/^\/docs\/rest(\/|$)/.test(path)) name = 'rest'
  else if (/^\/docs\/starters(\/|$)/.test(path)) name = 'starters'
  return { name, names, toc: name ? `toc-${name}` : 'toc' }
}
