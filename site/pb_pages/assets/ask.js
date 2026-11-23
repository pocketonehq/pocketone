// The ask box: type a question, press Enter, get a short answer citing the docs pages it came from (POST /ask,
// pb_pages/ask.ts, as server-sent events). The pages arrive first, then the answer as it's written; [n] in the
// answer links to passage n. Open with the header button, "/" or Cmd/Ctrl-K.
;(() => {
  const dialog = document.getElementById('search')
  if (!dialog) return
  const form = dialog.querySelector('form')
  const input = dialog.querySelector('input')
  const out = dialog.querySelector('.ask-answer')
  const list = dialog.querySelector('.search-results')
  const status = dialog.querySelector('.search-status')
  const more = dialog.querySelector('.ask-more')
  let sources = []
  let run = 0 // the latest question; an older one's events are dropped

  // The reader's code language: the docs' TS | Go tabs remember it (assets/docs.js); TypeScript unless they picked Go.
  const lang = () => {
    try {
      return localStorage.getItem('code-lang') === 'go' ? 'go' : 'ts'
    } catch {
      return 'ts'
    }
  }

  const esc = (s) => s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c])

  // [1], [2, 3] and [1][2] become links to those passages' pages; a number with no passage drops.
  const cite = (s) =>
    s.replace(/\[(\d+(?:\s*,\s*\d+)*)\]/g, (_, ns) =>
      ns
        .split(/\s*,\s*/)
        .map((n) => sources.find((p) => p.n === Number(n)))
        .filter(Boolean)
        .map((p) => `<a class="cite" href="${esc(p.url)}" title="${esc(p.title + (p.heading ? ' › ' + p.heading : ''))}">${p.n}</a>`)
        .join('')
    )

  // Markdown for the answer: paragraphs, lists, ``` blocks, `code`, **bold**, *italic*. Everything is escaped first; the
  // model's own links lose their URL (the citations are the links).
  const inline = (s) => {
    const codes = []
    s = esc(s).replace(/`([^`\n]+)`/g, (_, c) => `\u0000${codes.push(c) - 1}\u0000`)
    s = s
      .replace(/\[([^\]\n]+)\]\([^)\s]*\)/g, '$1')
      .replace(/\*\*([^*\n]+)\*\*/g, '<b>$1</b>')
      .replace(/(^|[^*\w])\*([^*\n]+)\*(?!\w)/g, '$1<i>$2</i>')
    s = cite(s)
    return s.replace(/\u0000(\d+)\u0000/g, (_, n) => `<code>${codes[n]}</code>`)
  }
  const md = (text) => {
    const html = []
    let kind = null
    let para = []
    const endPara = () => {
      if (para.length) html.push(`<p>${para.map(inline).join(' ')}</p>`)
      para = []
    }
    const endList = () => {
      if (kind) html.push(`</${kind}>`)
      kind = null
    }
    // code fences, with their language (a fence still being written runs to the end)
    const parts = []
    const fence = /^```([\w+-]*)[^\n]*\n([\s\S]*?)(?:^```[ \t]*$|(?![\s\S]))/gm
    let last = 0
    for (const m of text.matchAll(fence)) {
      parts.push(text.slice(last, m.index), { lang: m[1].toLowerCase(), code: m[2].replace(/\n$/, '') })
      last = m.index + m[0].length
    }
    parts.push(text.slice(last))
    parts.forEach((part) => {
      if (typeof part !== 'string') {
        endPara()
        endList()
        const lang = window.Prism?.languages[part.lang] ? part.lang : ''
        html.push(`<pre><code${lang ? ` class="language-${lang}"` : ''}>${esc(part.code)}</code></pre>`)
        return
      }
      for (const line of part.split('\n')) {
        let m
        if (!line.trim()) {
          endPara()
          endList()
        } else if ((m = line.match(/^\s*([-*+]|\d+[.)])\s+(.*)$/))) {
          const k = /\d/.test(m[1]) ? 'ol' : 'ul'
          endPara()
          if (kind !== k) {
            endList()
            html.push(`<${k}>`)
            kind = k
          }
          html.push(`<li>${inline(m[2])}</li>`)
        } else if ((m = line.match(/^#{1,4}\s+(.*)$/))) {
          endPara()
          endList()
          html.push(`<p><b>${inline(m[1])}</b></p>`)
        } else {
          endList()
          para.push(line.trim())
        }
      }
    })
    endPara()
    endList()
    return html.join('')
  }

  // A passage's text as a plain one-line snippet: no code blocks, link targets or Markdown marks.
  const plain = (t) =>
    t
      .replace(/```[\s\S]*?(```|$)/g, ' ')
      .replace(/!?\[([^\]]*)\]\([^)]*\)/g, '$1')
      .replace(/[`*_#>|]+/g, '')
      .replace(/\s+/g, ' ')
      .trim()

  // Colors the answer's code blocks with the site's Prism (the same theme and copy button as the docs).
  const highlight = () => {
    if (!window.Prism) return
    out.querySelectorAll('pre > code[class*="language-"]').forEach((code) => window.Prism.highlightElement(code))
  }

  // The pages, one row each (passages from the same page and section show once); once the answer is in, the ones it
  // cites come first, in the order it cites them.
  const showSources = (answer = '') => {
    const cited = [...answer.matchAll(/\[(\d+(?:\s*,\s*\d+)*)\]/g)].flatMap((m) => m[1].split(/\s*,\s*/).map(Number))
    const rank = (p) => {
      const i = cited.indexOf(p.n)
      return i < 0 ? 1000 + p.n : i
    }
    const seen = new Set()
    list.innerHTML = [...sources]
      .sort((a, b) => rank(a) - rank(b))
      .filter((p) => !seen.has(p.url) && seen.add(p.url))
      .map(
        (p) => `<li><a href="${esc(p.url)}">
  <span class="hit-title">${esc(p.title)}${p.heading ? ` <span aria-hidden="true">›</span> ${esc(p.heading)}` : ''}</span>
  <span class="hit-text">${esc(plain(p.text).slice(0, 160))}…</span></a></li>`
      )
      .join('')
  }

  // POSTs body and calls onEvent(event, data) for each server-sent event (data parsed as JSON).
  const streamPost = async (url, body, onEvent) => {
    const res = await fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
    if (!res.ok || !res.body) throw new Error(`HTTP ${res.status}`)
    const reader = res.body.pipeThrough(new TextDecoderStream()).getReader()
    let buf = ''
    for (;;) {
      const { value, done } = await reader.read()
      if (done) break
      buf += value
      let i
      while ((i = buf.indexOf('\n\n')) >= 0) {
        const block = buf.slice(0, i)
        buf = buf.slice(i + 2)
        let event = 'message'
        let data = ''
        for (const line of block.split('\n')) {
          if (line.startsWith('event:')) event = line.slice(6).trim()
          else if (line.startsWith('data:')) data += line.slice(5).trimStart()
        }
        if (data) onEvent(event, JSON.parse(data))
      }
    }
  }

  const reset = () => {
    sources = []
    out.replaceChildren()
    list.replaceChildren()
    status.textContent = ''
    more.hidden = true
  }

  const ask = async (q) => {
    const me = ++run
    reset()
    status.textContent = 'Looking through the docs…'
    let written = ''
    let failed = ''
    try {
      await streamPost('/ask', { q, lang: lang() }, (event, data) => {
        if (me !== run) return
        if (event === 'sources') {
          sources = data
          showSources()
          status.textContent = sources.length ? 'Writing an answer…' : ''
          if (!sources.length) out.innerHTML = `<p class="ask-note">Nothing in the docs matches that.</p>`
        } else if (event === 'text') {
          written += data
          status.textContent = ''
          out.innerHTML = md(written) + '<span class="ask-caret"> ▍</span>'
          highlight()
        } else if (event === 'error') {
          failed = data.code === 'limit' ? data.message : `No answer right now (${data.message}).`
        }
      })
    } catch (err) {
      failed = `No answer right now (${err.message}).`
    }
    if (me !== run) return
    status.textContent = ''
    if (written) {
      out.innerHTML = md(written)
      highlight()
      showSources(written)
    }
    if (failed) out.insertAdjacentHTML('beforeend', `<p class="ask-note">${esc(failed)}${sources.length ? ' These pages match your question:' : ''}</p>`)
    more.hidden = false
  }

  form.addEventListener('submit', (e) => {
    e.preventDefault()
    const q = input.value.trim()
    if (q) ask(q)
  })

  const open = () => {
    if (!dialog.open) dialog.showModal()
    input.select()
  }

  dialog.addEventListener('click', (e) => {
    if (e.target.closest('a[href]')) dialog.close()
    // a click on the backdrop (the dialog element itself, outside its box) closes it
    if (e.target === dialog) dialog.close()
  })
  document.querySelectorAll('[data-search-open]').forEach((b) => b.addEventListener('click', open))
  document.addEventListener('keydown', (e) => {
    if (dialog.open) return
    const typing = e.target.closest?.('input, textarea, select, [contenteditable]')
    if ((e.key === 'k' && (e.metaKey || e.ctrlKey)) || (e.key === '/' && !typing && !e.metaKey && !e.ctrlKey && !e.altKey)) {
      e.preventDefault()
      open()
    }
  })
})()
