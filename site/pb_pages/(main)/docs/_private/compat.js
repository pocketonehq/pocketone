// Live results for /docs/compatibility: the "PocketBase tests" workflow's recent runs on main, from GitHub's public
// API, and the latest finished run's counts per job (run.sh publishes them as a "pocketone-test-counts" annotation).
// Kept in the app store for 10 minutes: about 4 API calls per refresh, well under GitHub's 60 an hour without a token.
// When GitHub can't be reached, the last good result is shown with its age.
const REPO = 'pocketonehq/pocketone'
const WORKFLOW = 'upstream-tests.yml'
const TTL = 10 * 60 * 1000
const KEY = 'docs-compatibility'

const get = (url) => {
  const res = $http.send({
    url,
    method: 'GET',
    headers: { Accept: 'application/vnd.github+json', 'User-Agent': 'pocketone.dev' },
    timeout: 5,
  })
  if (res.statusCode === 404) return null
  if (res.statusCode !== 200) throw new Error(`GitHub answered ${res.statusCode} for ${url}`)
  return res.json
}

const fetchAll = () => {
  const list = get(`https://api.github.com/repos/${REPO}/actions/workflows/${WORKFLOW}/runs?branch=main&per_page=10`)
  const runs = (list?.workflow_runs || []).map((r) => ({
    id: r.id,
    url: r.html_url,
    sha: r.head_sha.slice(0, 9),
    shaUrl: `https://github.com/${REPO}/commit/${r.head_sha}`,
    event: r.event,
    result: r.status === 'completed' ? r.conclusion : r.status,
    at: r.run_started_at || r.created_at,
    seconds: Math.max(0, Math.round((Date.parse(r.updated_at) - Date.parse(r.run_started_at || r.created_at)) / 1000)),
  }))
  const latest = runs.find((r) => r.result !== 'in_progress' && r.result !== 'queued' && r.result !== 'cancelled')
  let jobs = []
  if (latest) {
    jobs = (get(`https://api.github.com/repos/${REPO}/actions/runs/${latest.id}/jobs`)?.jobs || []).map((j) => {
      let counts = null
      try {
        const note = (get(`${j.check_run_url}/annotations`) || []).find((a) => a.title === 'pocketone-test-counts')
        if (note) counts = JSON.parse(note.message)
      } catch (e) {}
      return { name: j.name, url: j.html_url, result: j.conclusion || j.status, counts }
    })
  }
  return { runs, latest, jobs }
}

module.exports = (ctx) => {
  const now = Date.now()
  const cached = ctx.store(KEY)
  if (cached && now - cached.at < TTL) return { ...cached.data, at: cached.at }
  try {
    const data = fetchAll()
    ctx.store(KEY, { at: now, data })
    return { ...data, at: now }
  } catch (e) {
    ctx.log.warn('compatibility: ' + e)
    if (cached) return { ...cached.data, at: cached.at, stale: true }
    return { runs: [], latest: null, jobs: [], at: now, error: String(e) }
  }
}
