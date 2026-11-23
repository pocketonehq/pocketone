<%
// The front page: ask for a story (it picks the topic, or the visitor names one), or pick one already told. Its words and the story lengths come from the settings.
const { refusal, tuning, storyLength } = ctx.resolve('story')
const t = tuning(ctx.app)
const L = t.length
const start = storyLength(t)
// the title's last word is highlighted
const titleWords = t.site.title.trim().split(/\s+/)
const titleLast = titleWords.length > 1 ? titleWords.pop() : ''
// when no story can be made now (the models aren't there, or today's budget is spent), the form says why and is off
const closed = await refusal(ctx.app, 'idea')
// most listened to first (a view is 10 seconds of listening), then the newest
const told = ctx.app.findRecordsByFilter('stories', "status = 'done'", '-views,-created', 60, 0)
// stories being made right now (not ones cut off by a restart), shown with their progress
const fresh = new Date(Date.now() - ctx.resolve('story').STALE_MS).toISOString().replace('T', ' ')
const making = ctx.app.findRecordsByFilter('stories', "status != 'done' && status != 'failed' && updated > {:fresh}", '-created', 10, 0, { fresh })
%>
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title><%= t.site.title %></title>
  <style>
    :root { color-scheme: dark; --bg: #0b1020; --ink: #e9e4d8; --dim: #a39d8f; --line: #232a44; --glow: #f2c46d; }
    * { box-sizing: border-box; }
    body { margin: 0; min-height: 100vh; background: radial-gradient(ellipse at top, #1a2140 0%, var(--bg) 60%); color: var(--ink);
      font: 17px/1.55 Georgia, 'Iowan Old Style', serif; }
    main { max-width: 680px; margin: 0 auto; padding: 56px 16px 80px; }
    h1 { font-weight: normal; font-size: 40px; margin: 0 0 6px; letter-spacing: .01em; }
    h1 span { color: var(--glow); }
    .sub { color: var(--dim); margin: 0 0 32px; }
    textarea { width: 100%; min-height: 96px; padding: 14px 16px; border-radius: 12px; border: 1px solid var(--line);
      background: #0f1530; color: var(--ink); font: inherit; resize: vertical; }
    textarea:focus { outline: 2px solid #3b4675; }
    label { color: var(--dim); font-size: 15px; }
    .length { display: flex; align-items: center; gap: 10px; flex: 1; min-width: 260px; }
    .length input[type=range] { flex: 1; accent-color: var(--glow); }
    .length b { color: var(--ink); font-weight: normal; min-width: 130px; }
    label input { width: 64px; margin-left: 6px; padding: 6px 8px; border-radius: 8px; border: 1px solid var(--line); background: #0f1530;
      color: var(--ink); font: inherit; }
    .row { display: flex; gap: 12px; align-items: center; margin-top: 12px; flex-wrap: wrap; }
    button { padding: 12px 22px; border: 0; border-radius: 999px; background: var(--glow); color: #1b1606; font: inherit;
      cursor: pointer; }
    button:disabled { opacity: .5; cursor: default; }
    .note { color: var(--dim); font-size: 14px; }
    .closed { padding: 14px 16px; border-radius: 12px; border: 1px solid #3b4675; background: #121a3a; margin: 0 0 16px; }
    form.off { opacity: .5; }
    ul.making { list-style: none; padding: 0; margin: 0; display: grid; gap: 8px; }
    ul.making a { display: flex; align-items: center; gap: 12px; padding: 10px 14px; border-radius: 10px; background: #0f1530;
      border: 1px solid var(--line); color: var(--ink); text-decoration: none; flex-wrap: wrap; }
    ul.making small { color: var(--dim); font-size: 13px; flex-basis: 100%; padding-left: 30px; }
    .moonlet { width: 18px; height: 18px; border-radius: 50%; box-shadow: inset -5px -2px 0 0 var(--glow); animation: bob 3s ease-in-out infinite; flex: none; }
    @keyframes bob { 50% { transform: rotate(-20deg); } }
    #custom { margin-bottom: 4px; }
    .mode { margin: 14px 0 0; font-size: 15px; }
    .mode a { color: var(--dim); text-decoration: underline; text-underline-offset: 3px; }
    .mode a:hover { color: var(--ink); }
    ul.told small { display: block; color: var(--dim); font-size: 13px; margin-top: 2px; }
    .err { color: #ff9b8a; }
    h2 { font-weight: normal; font-size: 20px; margin: 56px 0 12px; color: var(--dim); }
    ul.told { list-style: none; padding: 0; margin: 0; display: grid; gap: 12px; grid-template-columns: repeat(auto-fill, minmax(190px, 1fr)); }
    ul.told a { display: block; color: var(--ink); text-decoration: none; border-radius: 10px; overflow: hidden; background: #0f1530;
      border: 1px solid var(--line); }
    ul.told img { display: block; width: 100%; aspect-ratio: 16 / 9; object-fit: cover; opacity: .85; }
    ul.told span { display: block; padding: 8px 10px; font-size: 15px; }
  </style>
</head>
<body>
<main>
  <h1><%= titleWords.join(' ') %><% if (titleLast) { %> <span><%= titleLast %></span><% } %></h1>
  <p class="sub"><%= t.site.caption %></p>

  <% if (closed) { %><p class="closed"><%= closed.message %></p><% } %>
  <form id="ask"<% if (closed) { %> class="off"<% } %>>
    <div id="custom" hidden>
      <textarea id="prompt" maxlength="500" placeholder="<%= t.site.placeholder %>"<% if (closed) { %> disabled<% } %>></textarea>
    </div>
    <div class="row">
      <% if (L.maxMinutes > L.minMinutes) { %>
      <label class="length">Short
        <input id="minutes" type="range" min="<%= L.minMinutes %>" max="<%= L.maxMinutes %>" step="1" value="<%= start.minutes %>"
          data-per-minute="<%= L.picturesPerMinute %>" data-max-pictures="<%= L.maxPictures %>" aria-label="Story length">
        Long</label>
      <% } else { %>
      <input id="minutes" type="hidden" value="<%= start.minutes %>" data-per-minute="<%= L.picturesPerMinute %>" data-max-pictures="<%= L.maxPictures %>">
      <% } %>
      <span class="note"><b id="length-label"><%= start.minutes %> min · <%= start.pictures %> <%= start.pictures === 1 ? 'picture' : 'pictures' %></b></span>
    </div>
    <div class="row">
      <button id="go"<% if (closed) { %> disabled<% } %>>Tell me a story</button>
      <span class="note" id="msg"><%= t.site.note %></span>
    </div>
    <% if (!closed) { %><p class="mode"><a href="#" id="mode">Or ask for a story about something</a></p><% } %>
  </form>

  <section id="making"<% if (!making.length) { %> hidden<% } %>>
    <h2>Being made now</h2>
    <ul class="making" id="making-list">
      <% for (const s of making) { %>
      <li><a href="/story/<%= s.id %>"><span class="moonlet"></span><b><%= s.getString('title') || s.getString('prompt') %></b>
        <small><%= s.getString('progress') %></small></a></li>
      <% } %>
    </ul>
  </section>

  <% if (told.length) { %>
  <h2>Stories told, most listened to first</h2>
  <ul class="told">
    <% for (const s of told) {
      const pics = s.get('pictures') || [] %>
    <li><a href="/story/<%= s.id %>">
      <% if (pics.length) { %><img src="/api/files/stories/<%= s.id %>/<%= pics[0] %>?thumb=480x270" alt="" loading="lazy"><% } %>
      <span><%= s.getString('title') || s.getString('prompt') %>
        <small><%= s.getInt('minutes') %> min<% if (s.getInt('views')) { %> · <%= s.getInt('views') %> <%= s.getInt('views') === 1 ? 'listen' : 'listens' %><% } %></small></span>
    </a></li>
    <% } %>
  </ul>
  <% } %>
</main>
<script>
  const form = document.getElementById('ask'), prompt = document.getElementById('prompt'),
    go = document.getElementById('go'), msg = document.getElementById('msg')
  // stories being made: refresh their progress every few seconds; when one finishes, reload to show it with the others
  const makingList = document.getElementById('making-list'), makingBox = document.getElementById('making')
  let shownIds = <%- JSON.stringify(making.map((s) => s.id)) %>
  const esc = (t) => String(t || '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c])
  const watch = async () => {
    try {
      const filter = encodeURIComponent("status != 'done' && status != 'failed'")
      const r = await (await fetch(`/api/collections/stories/records?filter=${filter}&sort=-created&perPage=10&fields=id,title,prompt,progress,updated`)).json()
      const items = r.items.filter((s) => Date.now() - new Date(s.updated.replace(' ', 'T')).getTime() < <%- ctx.resolve('story').STALE_MS %>)
      const ids = items.map((s) => s.id)
      if (shownIds.some((id) => !ids.includes(id))) return location.reload() // one finished
      shownIds = ids
      makingBox.hidden = !items.length
      makingList.innerHTML = items.map((s) => `<li><a href="/story/${esc(s.id)}"><span class="moonlet"></span><b>${esc(s.title || s.prompt)}</b>
        <small>${esc(s.progress)}</small></a></li>`).join('')
    } catch (_) {}
    setTimeout(watch, 3000)
  }
  setTimeout(watch, 3000)

  // the prompt box is shown only for a story about something; otherwise Tell me a story picks the topic itself
  const custom = document.getElementById('custom'), mode = document.getElementById('mode')
  if (mode) mode.addEventListener('click', (e) => {
    e.preventDefault()
    custom.hidden = !custom.hidden
    mode.textContent = custom.hidden ? 'Or ask for a story about something' : 'Never mind, you pick'
    if (!custom.hidden) prompt.focus()
  })
  // the slider says how long, and how many pictures that length gets
  const minutesInput = document.getElementById('minutes')
  const lengthLabel = document.getElementById('length-label')
  const showLength = () => {
    const m = Number(minutesInput.value)
    const p = Math.max(1, Math.min(Number(minutesInput.dataset.maxPictures), Math.round(m * Number(minutesInput.dataset.perMinute))))
    lengthLabel.textContent = `${m} min · ${p} ${p === 1 ? 'picture' : 'pictures'}`
  }
  minutesInput.addEventListener('input', showLength)

  form.addEventListener('submit', async (e) => {
    e.preventDefault()
    go.disabled = true
    msg.className = 'note'
    msg.textContent = 'Starting...'
    try {
      const res = await fetch('/story', { method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt: custom.hidden ? '' : prompt.value, minutes: document.getElementById('minutes').value }) })
      const body = await res.json()
      if (!res.ok) throw new Error(body.message || res.statusText)
      location.href = '/story/' + body.id
    } catch (err) {
      msg.className = 'note err'
      msg.textContent = err.message
      go.disabled = false
    }
  })
</script>
</body>
</html>
