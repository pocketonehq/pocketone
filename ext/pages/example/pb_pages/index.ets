<% slot('title', () => { %>Pages demo<% }) %>
<h1>Pages, native in PocketBase</h1>
<p>Everything here is served from <code>pb_pages/</code> by the <code>ext/pages</code> extension: routing, static files,
EJS and esbuild run in Go, page code in PocketBase's JS VM. No Node, no build step. Edit a file and reload.</p>

<div class="grid">
  <% for (const d of [
    ['/jsx', 'JSX and TSX', 'Components and typed props, the alternative template style.'],
    ['/docs', 'Markdown', 'Front matter, headings with ids, fingerprinted images.'],
    ['/notes', 'Notes', 'A collection read with ctx.pb() as the visitor; sign in to post.'],
    ['/clock', 'Server-sent events', 'ctx.res.sse() streams ticks from an endpoint (clock/stream.ts).'],
    ['/speed', 'Speed', 'Animated results vs npm 0.x, and a live speed check against this server.'],
    ['/stats', 'JSON API', 'An endpoint (stats.ts) returning JSON.'],
    ['/robots.txt', 'robots.txt', 'robots.txt.ets, served as text/plain.'],
    ['/nope', '404 page', 'Anything unmatched renders +404.ets.'],
  ]) { %>
    <%- include('badge-card', { href: d[0], title: d[1], text: d[2] }) %>
  <% } %>
</div>

<p class="muted">You are <%= ctx.auth ? ctx.auth.email() : 'not signed in' %>. Demo account: user@example.com / password.</p>
