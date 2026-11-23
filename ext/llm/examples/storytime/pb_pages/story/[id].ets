<%
// One story: progress while it's being made (the page polls its record), then the player.
let rec: any
try {
  rec = ctx.app.findRecordById('stories', params.id)
} catch (_) {
  throw ctx.httpError(404, 'No such story')
}
const STALE_MS = ctx.resolve('story').STALE_MS
const status = rec.getString('status')
const making = ['queued', 'writing', 'making'].includes(status)
const stale = making && Date.now() - new Date(rec.getString('updated').replace(' ', 'T')).getTime() > STALE_MS
const files = (name: string) => `/api/files/stories/${rec.id}/${name}`
const story = JSON.parse(rec.getString('story') || 'null') // a json field reads back as raw JSON
// what the player needs: the voiceover with its timings, the pictures with their start times, and the song
const data = status === 'done' ? {
  id: rec.id,
  title: rec.getString('title'),
  voice: files(rec.getString('narration')),
  music: rec.getString('music') ? files(rec.getString('music')) : '',
  duration: story.duration,
  lines: story.lines,
  words: story.words,
  pictures: story.pictures.map((p: any) => ({ start: p.start, image: files(p.image) })),
  mix: ctx.resolve('story').tuning(ctx.app).mix,
} : null
const title = rec.getString('title') || ctx.resolve('story').tuning(ctx.app).site.title
// how it was made, for the splash: when, how long it took, what it cost, and the models (older stories lack some)
const made = (story && story.made) || null
const when = new Date(rec.getString('created').replace(' ', 'T'))
const clockOf = (s: number) => (s >= 60 ? `${Math.floor(s / 60)} min ${s % 60} s` : `${s} s`)
const madeLine = [
  // spelled out by hand: the JS engine has no locale formatting
  `Made ${['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'][when.getUTCMonth()]} ${when.getUTCDate()}, ${when.getUTCFullYear()}`,
  made ? `in ${clockOf(made.seconds)}` : '',
  rec.getFloat('cost') ? `for $${rec.getFloat('cost').toFixed(rec.getFloat('cost') < 0.1 ? 3 : 2)}` : '',
].filter(Boolean).join(' ')
%>
<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title><%= title %></title>
  <style>
    :root { color-scheme: dark; --ink: #f1ece0; --dim: #a39d8f; --glow: #f2c46d; }
    * { box-sizing: border-box; }
    html, body { margin: 0; height: 100%; background: #000; color: var(--ink); font: 17px/1.5 Georgia, 'Iowan Old Style', serif; overflow: hidden; }
    a { color: var(--dim); }

    /* the pictures: stacked, the current one faded in, each slowly panning and zooming (Ken Burns) */
    .stage { position: fixed; inset: 0; overflow: hidden; background: #000; }
    .pic { position: absolute; inset: -2%; background: center / cover no-repeat; opacity: 0; transition: opacity 3s ease;
      will-change: transform, opacity; animation: var(--kb) var(--d, 60s) linear both paused; }
    .pic.on { opacity: 1; }
    .playing .pic { animation-play-state: running; }
    @keyframes kb0 { from { transform: scale(1.02) translate(0, 0); } to { transform: scale(1.16) translate(-2.5%, -1.5%); } }
    @keyframes kb1 { from { transform: scale(1.16) translate(2.5%, 1%); } to { transform: scale(1.03) translate(0, 0); } }
    @keyframes kb2 { from { transform: scale(1.04) translate(2%, -1.5%); } to { transform: scale(1.15) translate(-2%, 1.5%); } }
    @keyframes kb3 { from { transform: scale(1.15) translate(-2%, 1.5%); } to { transform: scale(1.04) translate(1.5%, -1%); } }
    .shade { position: absolute; inset: 0; background: linear-gradient(to bottom, rgba(0,0,0,0) 55%, rgba(0,0,0,.7) 100%); pointer-events: none; }

    /* captions: the sentence being read, each word brightening as it's said */
    .caption { position: fixed; left: 0; right: 0; bottom: calc(7vh + 44px); padding: 0 16px; text-align: center; pointer-events: none;
      font-size: clamp(18px, 2.6vw, 30px); line-height: 1.4; text-shadow: 0 2px 12px rgba(0,0,0,.9); }
    .caption p { max-width: 900px; margin: 0 auto; }
    .caption .w { opacity: .45; transition: opacity .25s, color .25s; }
    .caption .w.said { opacity: 1; }
    .caption .w.now { color: var(--glow); }

    /* the controls: play/pause, a scrubber and the time; they fade while the story plays and the mouse rests */
    .controls { position: fixed; left: 0; right: 0; bottom: 0; display: flex; align-items: center; gap: 12px; padding: 14px 16px;
      background: linear-gradient(to top, rgba(0,0,0,.6), rgba(0,0,0,0)); transition: opacity .8s; }
    .playing.idle .controls { opacity: 0; }
    .playing.idle { cursor: none; }
    .controls button { width: 38px; height: 38px; flex: none; border: 0; border-radius: 50%; background: rgba(255,255,255,.14);
      color: var(--ink); font-size: 15px; cursor: pointer; }
    .controls input[type=range] { flex: 1; accent-color: var(--glow); cursor: pointer; }
    .controls .time { font: 13px/1 ui-monospace, Menlo, monospace; color: var(--dim); min-width: 92px; text-align: right; }

    /* the cover: title and Play (browsers play sound only after a click), and the end */
    .cover { position: fixed; inset: 0; display: flex; flex-direction: column; align-items: center; justify-content: center;
      gap: 18px; text-align: center; padding: 16px; background: radial-gradient(ellipse at center, rgba(10,14,32,.55), rgba(0,0,0,.85));
      transition: opacity 2s; }
    .cover.gone { opacity: 0; pointer-events: none; }
    .cover h1 { font-weight: normal; font-size: clamp(28px, 5vw, 52px); margin: 0; max-width: 900px; }
    .cover p { margin: 0; color: var(--dim); max-width: 560px; }
    .made { font-size: 13px; line-height: 1.6; color: var(--dim); max-width: 640px; }
    .made dl { display: inline-grid; grid-template-columns: auto auto; gap: 0 12px; margin: 6px 0 0; text-align: left; }
    .made dt { opacity: .8; }
    .made dd { margin: 0; font-family: ui-monospace, Menlo, monospace; font-size: 12px; }
    button.play { padding: 14px 30px; border: 0; border-radius: 999px; background: var(--glow); color: #1b1606; font: inherit;
      font-size: 19px; cursor: pointer; }

    /* while it's being made */
    .wait { min-height: 100%; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 14px;
      text-align: center; padding: 16px; background: radial-gradient(ellipse at top, #1a2140 0%, #0b1020 60%); }
    .moon { width: 64px; height: 64px; border-radius: 50%; box-shadow: inset -14px -6px 0 0 var(--glow); animation: bob 4s ease-in-out infinite; }
    @keyframes bob { 50% { transform: translateY(-8px) rotate(-6deg); } }
    .wait h1 { font-weight: normal; font-size: 28px; margin: 0; }
    .wait p { margin: 0; color: var(--dim); max-width: 520px; }
    .err { color: #ff9b8a; }
    ol.steps { list-style: none; padding: 0; margin: 4px 0; text-align: left; display: grid; gap: 4px; font-size: 15px; }
    ol.steps li { color: #5d6280; padding-left: 26px; position: relative; }
    ol.steps li::before { content: '○'; position: absolute; left: 4px; }
    ol.steps li.now { color: var(--ink); }
    ol.steps li.now::before { content: '◐'; color: var(--glow); animation: spin 1.6s linear infinite; display: inline-block; }
    ol.steps li.done { color: var(--dim); }
    ol.steps li.done::before { content: '●'; color: var(--glow); }
    @keyframes spin { to { transform: rotate(360deg); } }
  </style>
</head>
<body>
<% if (!data) { %>
  <div class="wait">
    <% if (status === 'failed' || stale) { %>
      <h1>This story didn't get finished</h1>
      <p class="err"><%= stale ? 'It stopped partway (the app restarted?).' : rec.getString('error') %></p>
      <p><a href="/">Try another</a></p>
    <% } else { %>
      <div class="moon"></div>
      <h1 id="title"><%= rec.getString('title') || 'Making your story' %></h1>
      <ol class="steps" id="steps">
        <li>Writing the story</li><li>Reading it aloud</li><li>Timing each word</li><li>Choosing the pictures</li>
        <li>Painting the pictures</li><li>Composing the music</li>
      </ol>
      <p id="progress"><%= rec.getString('progress') %></p>
      <p style="font-size:14px"><span id="elapsed"></span>You can keep this page open, or come back to it later.</p>
    <% } %>
  </div>
  <% if (making && !stale) { %>
  <script>
    // poll the record (open to anyone through the records API) until the story is done or failed
    const id = <%- JSON.stringify(rec.id) %>
    const started = new Date(<%- JSON.stringify(rec.getString('created').replace(' ', 'T')) %>).getTime()
    // which steps are done and which is running, read from the progress line the story saves
    const steps = [...document.querySelectorAll('#steps li')]
    const show = (progress) => {
      const p = progress || ''
      let now = 0
      if (/^Reading/.test(p)) now = 1
      else if (/^Listening/.test(p)) now = 2
      else if (/^Choosing/.test(p)) now = 3
      else if (/^Painting/.test(p)) now = 4
      const painted = /Painting pictures (\d+)\/(\d+)/.exec(p), song = /Song ready/.test(p)
      steps.forEach((li, i) => {
        let state = i < now ? 'done' : i === now ? 'now' : ''
        if (now === 4) state = i < 4 ? 'done' : i === 4 ? (painted && painted[1] === painted[2] ? 'done' : 'now') : (song ? 'done' : 'now')
        li.className = state
      })
      if (painted) steps[4].textContent = `Painting the pictures (${painted[1]} of ${painted[2]})`
    }
    show(<%- JSON.stringify(rec.getString('progress')) %>)
    const clock = () => {
      const s = Math.max(0, Math.round((Date.now() - started) / 1000))
      document.getElementById('elapsed').textContent = `Started ${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')} ago. `
    }
    clock()
    setInterval(clock, 1000)
    const tick = async () => {
      try {
        const r = await (await fetch('/api/collections/stories/records/' + id + '?fields=status,title,progress')).json()
        if (r.status === 'done' || r.status === 'failed') return location.reload()
        if (r.title) document.getElementById('title').textContent = r.title
        if (r.progress) document.getElementById('progress').textContent = r.progress
        show(r.progress)
      } catch (_) {}
      setTimeout(tick, 2000)
    }
    setTimeout(tick, 2000)
  </script>
  <% } %>
<% } else { %>
  <div class="stage" id="stage"><div class="shade"></div></div>
  <div class="caption"><p id="caption"></p></div>
  <div class="controls">
    <button id="toggle" title="Play or pause (space)">▶</button>
    <input id="scrub" type="range" min="0" step="0.1" value="0" aria-label="Position in the story">
    <span class="time" id="time">0:00 / 0:00</span>
  </div>
  <div class="cover" id="cover">
    <h1><%= data.title %></h1>
    <p><%= (story && story.topic) || rec.getString('prompt') %></p>
    <button class="play" id="play">Play the story</button>
    <div class="made">
      <div><%= madeLine %></div>
      <% if (made) { %>
      <dl>
        <% for (const step of ['writing', 'voice', 'timing', 'pictures', 'music']) { if (made.models[step]) { %><dt><%= step %></dt><dd><%= made.models[step] %></dd><% } } %>
        <dt>narrator</dt><dd><%= made.voice %></dd>
      </dl>
      <% } %>
    </div>
    <p style="font-size:14px"><a href="/">Tell another</a></p>
  </div>
  <script>
    const S = <%- JSON.stringify(data) %>
    // The final mix. Recordings come out at very different loudness (a cloned voice is as loud as its clip, a generated
    // one can be very hot, songs vary by 10 dB or more), so on the first play each file is measured once and run
    // through a gain: the voice to the settings' level (a limiter stops a big boost from clipping), the song that many
    // dB under it. Until the measurement is in (or if it fails), the song plays at MUSIC of the voice.
    const MUSIC = 0.16
    const VOICE_DB = S.mix.voiceDb, MUSIC_DB = S.mix.voiceDb - S.mix.musicUnderDb
    const $ = (id) => document.getElementById(id)
    const stage = $('stage'), cap = $('caption'), cover = $('cover'), scrub = $('scrub'), time = $('time'), btn = $('toggle')

    const voice = new Audio(S.voice)
    voice.preload = 'auto'
    const music = S.music ? new Audio(S.music) : null
    if (music) { music.loop = true; music.volume = 0 }
    scrub.max = S.duration

    // a layer per picture; picture i shows from its start to the next one's (the last to the end)
    const until = (i) => (i + 1 < S.pictures.length ? S.pictures[i + 1].start : S.duration + 4)
    const pics = S.pictures.map((p, i) => {
      const el = document.createElement('div')
      el.className = 'pic'
      el.style.backgroundImage = `url("${p.image}")`
      el.style.setProperty('--kb', 'kb' + (i % 4))
      el.style.setProperty('--d', (until(i) - p.start + 3) + 's')
      stage.insertBefore(el, stage.querySelector('.shade'))
      return el
    })
    const pictureAt = (t) => { let i = 0; S.pictures.forEach((p, j) => { if (t >= p.start) i = j }); return i }
    let shown = -1
    // show the picture for time t, its pan and zoom placed where it would be at t (a negative delay seeks a CSS animation)
    const place = (t, force) => {
      const i = pictureAt(t)
      if (i === shown && !force) return
      shown = i
      pics.forEach((el, j) => el.classList.toggle('on', j === i))
      const el = pics[i]
      el.style.animationName = 'none'
      void el.offsetWidth
      el.style.animationName = ''
      el.style.animationDelay = -(t - S.pictures[i].start) + 's'
    }
    place(0, true)

    const fade = (a, to, seconds) => {
      if (!a) return
      const from = a.volume, t0 = performance.now()
      const step = (now) => {
        const k = Math.max(0, Math.min(1, (now - t0) / (seconds * 1000))) // a frame's time can be just before t0
        a.volume = Math.max(0, Math.min(1, from + (to - from) * k))
        if (k < 1) requestAnimationFrame(step); else if (to === 0) a.pause()
      }
      requestAnimationFrame(step)
    }

    // the loudness of a file's sounding parts: the median of 50 ms windows above -50 dBFS
    const loudness = async (url) => {
      const decoder = new OfflineAudioContext(1, 1, 16000)
      const a = await decoder.decodeAudioData(await (await fetch(url)).arrayBuffer())
      const x = a.getChannelData(0), win = Math.round(a.sampleRate * 0.05), dbs = []
      for (let i = 0; i + win < x.length; i += win) {
        let sum = 0
        for (let j = i; j < i + win; j += 2) sum += x[j] * x[j]
        const db = 10 * Math.log10(sum / (win / 2) + 1e-12)
        if (db > -50) dbs.push(db)
      }
      dbs.sort((p, q) => p - q)
      return dbs.length ? dbs[dbs.length >> 1] : null
    }
    let levelled = false
    const level = () => {
      if (levelled || !window.AudioContext) return
      levelled = true
      const ac = new AudioContext()
      const route = (el, limit) => {
        const gain = ac.createGain()
        let node = ac.createMediaElementSource(el).connect(gain)
        if (limit) {
          const lim = ac.createDynamicsCompressor()
          lim.threshold.value = -3; lim.knee.value = 0; lim.ratio.value = 20; lim.attack.value = 0.003; lim.release.value = 0.1
          node = node.connect(lim)
        }
        node.connect(ac.destination)
        return gain
      }
      // the element volume does fades (music to MUSIC); the gain does the level, so for the song it makes up for MUSIC
      // each gain chosen is kept on the page (data-mix-voice, data-mix-music: the file's loudness and the gain)
      const set = (gain, url, target, under, which) => loudness(url).then((db) => {
        if (db === null) return
        gain.gain.value = Math.max(0.05, Math.min(16, Math.pow(10, (target - db) / 20) / under))
        document.body.dataset['mix' + which] = `${db.toFixed(1)} dBFS x${gain.gain.value.toFixed(2)}`
      }).catch(() => {})
      set(route(voice, true), S.voice, VOICE_DB, 1, 'Voice')
      if (music) set(route(music, false), S.music, MUSIC_DB, MUSIC, 'Music')
    }

    const clock = (s) => Math.floor(s / 60) + ':' + String(Math.floor(s % 60)).padStart(2, '0')
    let playing = false, dragging = false, lineShown = null, spans = []
    const bare = (x) => x.toLowerCase().replace(/[^\p{L}\p{N}]/gu, '')

    // the sentence being read, as written (the aligner's words have no punctuation), each piece timed by its word
    const showLine = (line) => {
      cap.textContent = ''
      spans = []
      if (!line) return
      const words = S.words.filter((w) => w.line === line.index)
      let j = 0, last = { start: line.start, end: line.start }
      for (const piece of line.text.split(/\s+/).filter(Boolean)) {
        const need = bare(piece).length
        if (need && j < words.length) {
          // one written word may be several spoken ones ("good-night"): take words until their letters cover it
          const first = words[j]
          let got = 0
          while (j < words.length && got < need) got += bare(words[j++].text).length || need
          last = { start: first.start, end: words[j - 1].end }
        }
        const el = document.createElement('span')
        el.className = 'w'
        el.textContent = piece
        cap.append(el, ' ')
        spans.push({ w: last, el })
      }
    }

    const render = () => {
      requestAnimationFrame(render)
      const t = dragging ? Number(scrub.value) : voice.currentTime
      if (!dragging) scrub.value = t
      time.textContent = clock(t) + ' / ' + clock(S.duration)
      place(t)
      let line = null
      for (const l of S.lines) if (t >= l.start - 0.15 && t <= l.end + 0.8) { line = l; break }
      const key = line ? line.index : null
      if (key !== lineShown) { lineShown = key; showLine(line) }
      for (const { w, el } of spans) {
        el.classList.toggle('said', t >= w.start)
        el.classList.toggle('now', t >= w.start && t < w.end + 0.05)
      }
    }
    requestAnimationFrame(render)

    const play = () => {
      level()
      cover.classList.add('gone')
      if (voice.ended) voice.currentTime = 0
      playing = true
      document.body.classList.add('playing')
      btn.textContent = '❚❚'
      voice.play()
      if (music) { music.play(); fade(music, MUSIC, 4) }
    }
    const pause = () => {
      playing = false
      document.body.classList.remove('playing')
      btn.textContent = '▶'
      voice.pause()
      if (music) music.pause()
    }
    const toggle = () => (playing ? pause() : play())

    // a view: 10 seconds actually heard (playing time, not where the scrubber is), counted once per visit
    let heard = 0, lastTick = 0, counted = false
    const listen = (now) => {
      requestAnimationFrame(listen)
      if (playing && !voice.paused && lastTick) heard += (now - lastTick) / 1000
      lastTick = now
      if (!counted && heard >= 10) {
        counted = true
        fetch('/view', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ id: S.id }) })
      }
    }
    requestAnimationFrame(listen)

    voice.addEventListener('ended', () => {
      pause()
      if (music) { music.play(); fade(music, 0, 8) }
      setTimeout(() => {
        cover.querySelector('h1').textContent = 'Good night.'
        cover.querySelector('p').textContent = S.title
        $('play').textContent = 'Play again'
        cover.classList.remove('gone')
      }, 3000)
    })

    // scrubbing: the picture, its pan and the caption follow the thumb; the voice jumps when it's let go
    scrub.addEventListener('input', () => { dragging = true })
    scrub.addEventListener('change', () => {
      dragging = false
      voice.currentTime = Number(scrub.value)
      place(voice.currentTime, true)
    })

    $('play').addEventListener('click', (e) => { e.stopPropagation(); voice.currentTime = 0; place(0, true); play() })
    btn.addEventListener('click', toggle)
    stage.addEventListener('click', toggle)
    document.addEventListener('keydown', (e) => {
      if (e.code === 'Space') { e.preventDefault(); toggle() }
      if (e.code === 'ArrowLeft' || e.code === 'ArrowRight') {
        voice.currentTime = Math.max(0, Math.min(S.duration, voice.currentTime + (e.code === 'ArrowLeft' ? -10 : 10)))
        place(voice.currentTime, true)
      }
    })

    // hide the controls while playing and the mouse rests
    let idle
    document.addEventListener('mousemove', () => {
      document.body.classList.remove('idle')
      clearTimeout(idle)
      idle = setTimeout(() => document.body.classList.add('idle'), 2500)
    })
  </script>
<% } %>
</body>
</html>
