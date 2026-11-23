// The page driver: one call per request. Go passes a getter for `rt` (request primitives and the route); this loads
// +config.js and its plugins, builds `ctx`, runs plugins' onRequest/extendContext, middleware (root first), the
// route's .ts code (load, then the method's handler), the template and its layouts, and sends the response. See SPEC.md section 3.
// Templates, modules and components are called from here, not from Go, so JS values and exceptions stay as they are;
// Go only finds, compiles and caches them (rt.template, rt.module, ...).
(function (getRT) {
  var rt = getRT()
  var route = rt.route
  var cache = rt.cache // per VM: compiled functions and module exports

  // tell Go what a JS error carries; a Go error comes back to Go as itself, without these properties
  function report(e) {
    if (e !== null && typeof e === 'object') {
      if (e.__ppHttp) rt.httpError(Number(e.__ppHttp.status), String(e.__ppHttp.message))
      else if (e.__ppWhere) rt.setWhere(String(e.__ppWhere))
    }
    throw e
  }

  // isPromise reports a thenable: what an async loader, handler, middleware or template returns.
  function isPromise(v) {
    return v !== null && typeof v === 'object' && typeof v.then === 'function'
  }
  // then calls fn with v, or with what v settles to if it's a promise. Sync page code never makes a promise, so it
  // runs exactly as before; once something returns one, the rest of the request chains on it and the driver returns
  // a promise, which the engine awaits (running the event loop) before Go sends the response.
  function then(v, fn) {
    return isPromise(v) ? v.then(fn) : fn(v)
  }

  var out
  try {
    out = main()
  } catch (e) {
    report(e)
  }
  if (isPromise(out)) return out.then(undefined, report)

  function main() {
    // ---- escaping, used by compiled templates ----
    var escRe = /[&<>"']/g
    var escMap = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&#34;', "'": '&#39;' }
    function str(v) {
      if (v === null || v === undefined) return ''
      // a sync template printing an async include (or any promise) would print [object Promise]
      if (typeof v === 'object' && typeof v.then === 'function') {
        throw new Error('a promise was printed; await it (<%- await include(...) %>, <%= await ... %>)')
      }
      return String(v)
    }
    function esc(v) {
      return str(v).replace(escRe, function (c) {
        return escMap[c]
      })
    }

    // ---- modules (CommonJS, cached per VM by content) ----
    // L is what loads files: this request's rt, or (for a module's own require) the per-VM cache.loader, since the
    // module and its require() outlive the request that first loaded it.
    function load(file, L) {
      L = L || rt
      var key = L.moduleKey(file)
      if (key in cache) return cache[key]
      // A require cycle (a -> b -> a) gets a's exports as they are so far, as in Node and 0.x.
      var loading = cache['l:' + key]
      if (loading) return loading.exports
      var m = L.module(file)
      if (m.json !== undefined) {
        var v
        try {
          v = JSON.parse(m.json)
        } catch (e) {
          throw new Error(file + ': ' + e.message)
        }
        cache[key] = v
        return v
      }
      var module = { exports: {} }
      cache['l:' + key] = module
      try {
        var h, frag
        if (m.ts) {
          h = jsx(L).h
          frag = jsx(L).Fragment
        }
        var req = function (name) {
          return requireFrom(file, String(name), cache.loader)
        }
        m.fn(module, module.exports, req, m.filename, m.dirname, h, frag)
      } finally {
        delete cache['l:' + key]
      }
      var out = module.exports
      // an ES module whose only export is default (TS `export default ...`) behaves like module.exports = ...
      if (out !== null && typeof out === 'object' && out.__esModule) {
        var keys = Object.keys(out)
        if (keys.length === 1 && keys[0] === 'default') out = out['default']
      }
      cache[key] = out
      return out
    }

    // requireFrom resolves name for the pb_pages file from (require.go), then loads it here or with the app's loader.
    function requireFrom(from, name, L) {
      L = L || rt
      var res = L.resolveRequire(from, name)
      if (res.file !== undefined) return load(res.file, L)
      var global = globalThis.require
      if (typeof global !== 'function') throw new Error('require(' + JSON.stringify(name) + '): no module loader')
      if (res.notFound === undefined) return global(res.global)
      try {
        return global(res.global)
      } catch (e) {
        if (e && typeof e.message === 'string' && e.message.indexOf("Cannot find module '" + res.global + "'") === 0) {
          throw new Error(res.notFound)
        }
        throw e
      }
    }

    function jsx(L) {
      if (!cache.jsx) cache.jsx = (L || rt).program('jsx')(esc)
      return cache.jsx
    }

    function sdk() {
      if (!cache.sdk) {
        var m = { exports: {} }
        cache.sdk = rt.program('sdk')(m, m.exports)
      }
      return cache.sdk
    }

    // ---- templates ----
    var pp // what compiled templates receive; set below

    // render renders a template (page, layout or partial) and returns its text.
    // It returns a promise instead when the template is async (uses await, or renders an async component).
    function render(file, locals) {
      rt.enter(file)
      var pending = false
      try {
        var out = /\.(jsx|tsx)$/.test(file) ? renderJSX(file, locals) : rt.template(file)(pp, file, locals)
        if (isPromise(out)) {
          pending = true
          return out.then(
            function (s) {
              rt.leave()
              return finishRender(file, s)
            },
            function (e) {
              rt.leave()
              throw e
            }
          )
        }
        return finishRender(file, out)
      } finally {
        if (!pending) rt.leave()
      }
    }

    function finishRender(file, out) {
      out = String(out)
      if (/\.md$/.test(file)) out = rt.markdown(file, out)
      return out
    }

    function include(from, name, locals) {
      return render(rt.includeFile(from, String(name)), locals)
    }

    // renderJSX renders a .jsx/.tsx template: its default export is called with props and the result turned to
    // HTML. Props: ctx, data, params, meta, asset, include, slot, children (the inner content, for layouts), plus an
    // include's locals.
    function renderJSX(file, locals) {
      var exports = load(file)
      var comp = exports !== null && exports !== undefined && exports['default'] !== undefined ? exports['default'] : exports
      if (typeof comp !== 'function') throw new Error(file + ': the default export must be a component function')
      var j = jsx()
      var props = {}
      if (locals !== undefined && locals !== null) {
        var lo = Object(locals)
        Object.keys(lo).forEach(function (k) {
          props[k] = lo[k]
        })
      }
      props.ctx = ctx
      props.data = ctx.data
      props.params = ctx.params
      props.meta = ctx.meta
      props.asset = function (p) {
        return rt.asset(file, String(p))
      }
      props.include = function (name, l) {
        var out = include(file, name, l)
        return isPromise(out) ? out.then(j.raw) : j.raw(out)
      }
      props.slot = function (name, content) {
        if (name === undefined || name === null) return j.raw(rt.wrapped())
        if (arguments.length > 1) {
          var html = j.toHTML(content)
          if (isPromise(html)) {
            // async content: this render waits for it before it returns
            if (!pendingSlots) pendingSlots = []
            pendingSlots.push(
              html.then(function (h) {
                rt.setSlot(String(name), h)
              })
            )
            return undefined
          }
          rt.setSlot(String(name), html)
          return undefined
        }
        // an empty slot is '' (falsy), so slot('title') || 'fallback' falls back
        var got = rt.getSlot(String(name))
        return got === undefined || got === null || got === '' ? '' : j.raw(got)
      }
      props.children = j.raw(rt.wrapped())
      var pendingSlots = null
      var out = j.toHTML(comp(props))
      if (!pendingSlots && !isPromise(out)) return out
      return Promise.resolve(out).then(function settle(html) {
        if (!pendingSlots || !pendingSlots.length) return html
        var wait = pendingSlots
        pendingSlots = []
        return Promise.all(wait).then(function () {
          return settle(html)
        })
      })
    }

    // ---- +config.js ----
    // 0.x plugin hooks v1 doesn't call, with what to use instead (SPEC 9.2). A plugin that still has one is an error
    // rather than silently half-working.
    var oldPluginHooks = {
      onExtendContextApi: 'use extendContext(ctx)',
      onRender: 'use transform({ html, ctx, file })',
      onResponse: 'use transform, or ctx.res in onRequest',
      handles: 'add a page or a .ts endpoint for the path instead',
    }
    // the plugins turned on by name (SPEC 9.1)
    var builtinPlugins = { auth: true, realtime: true }

    // requirePlugin loads a plugin listed by module name. Its export (or its `default`, for a module compiled from
    // ESM) must be a plugin factory; loadConfig checks that.
    function requirePlugin(file, name) {
      var v
      try {
        v = requireFrom(file, name)
      } catch (e) {
        var why = e !== null && typeof e === 'object' && typeof e.message === 'string' ? e.message : String(e)
        throw new Error(file + ': plugin ' + JSON.stringify(name) + ': require failed (built-in plugins: auth, realtime): ' + why)
      }
      if (v !== null && typeof v === 'object' && typeof v['default'] === 'function') return v['default']
      return v
    }

    // loadConfig runs +config.js (if any), validates it and returns { debug, builtins, plugins }.
    function loadConfig(file) {
      var cfg = { debug: false, builtins: [], plugins: [] }
      if (!file) return cfg
      var v = load(file)
      if (typeof v === 'function') {
        // 0.x allowed module.exports = (api) => ({...}); a function has no keys, so it would be silently ignored
        throw new Error(file + ': must export an object (module.exports = { ... }), not a function')
      }
      if (v === null || typeof v !== 'object') throw new Error(file + ': must export an object')
      var seen = {}
      Object.keys(v).forEach(function (key) {
        switch (key) {
          case 'debug':
            cfg.debug = !!v.debug
            break
          case 'plugins':
            var list = v.plugins
            if (!Array.isArray(list)) {
              if (list !== undefined) throw new Error(file + ': plugins must be an array')
              break
            }
            for (var i = 0; i < list.length; i++) {
              var item = list[i]
              // A string is a module name, required from +config.js (SPEC 7): a _private file, a node_modules
              // package or a path. Only when no module by that name exists in pb_pages is it a built-in.
              if (typeof item === 'string' && (!builtinPlugins[item] || rt.moduleExists(file, item))) {
                item = requirePlugin(file, item)
              } else if (typeof item === 'string') {
                if (seen[item]) throw new Error(file + ': plugin ' + JSON.stringify(item) + ' listed twice')
                seen[item] = true
                cfg.builtins.push(item)
                continue
              }
              var factory = item,
                options = {}
              if (item !== null && (typeof item === 'object' || typeof item === 'function') && item.factory !== undefined) {
                factory = item.factory
                Object.keys(item).forEach(function (k) {
                  if (k !== 'factory') options[k] = item[k]
                })
              }
              if (typeof factory !== 'function') {
                throw new Error(file + ': plugins[' + i + '] must be a built-in name, a module name or a plugin factory')
              }
              var p = factory(options)
              if (p === null || (typeof p !== 'object' && typeof p !== 'function')) {
                throw new Error(file + ': plugins[' + i + ']: the factory must return an object')
              }
              if (p.name === undefined || p.name === null || String(p.name) === '') {
                throw new Error(file + ': plugins[' + i + ']: a plugin needs a name')
              }
              var name = String(p.name)
              Object.keys(p).forEach(function (k) {
                if (Object.prototype.hasOwnProperty.call(oldPluginHooks, k)) {
                  throw new Error(
                    file + ': plugin ' + JSON.stringify(name) + ': ' + k + ' is a 0.x hook that v1 never calls; ' + oldPluginHooks[k]
                  )
                }
              })
              if (seen[name]) throw new Error(file + ': plugin ' + JSON.stringify(name) + ' listed twice')
              seen[name] = true
              cfg.plugins.push(p)
            }
            break
          default:
            throw new Error(file + ': unknown key ' + JSON.stringify(key))
        }
      })
      return cfg
    }

    var config = loadConfig(rt.config)
    // Go applies debug and the built-ins (reading the pb_auth cookie for auth) and returns what they give
    var conf = rt.configure(config.debug, config.builtins)

    // ---- ctx ----
    var ctx = {}
    var queryVals = rt.query()
    var cookieVals = null
    function loadCookies() {
      if (!cookieVals) cookieVals = rt.cookies()
      return cookieVals
    }

    ctx.req = {
      method: rt.method,
      url: rt.url,
      query: function (name, opts) {
        if (name === undefined) {
          var o = {}
          for (var k in queryVals) o[k] = queryVals[k][0]
          return o
        }
        var v = queryVals[name]
        if (opts && opts.all) return v ? Array.prototype.slice.call(v) : []
        return v ? v[0] : undefined
      },
      header: function (name) {
        return rt.header(name)
      },
      cookies: function () {
        var c = loadCookies()
        var o = {}
        for (var k in c) o[k] = c[k]
        return o
      },
      cookie: function (name, opts) {
        var v = loadCookies()[name]
        if (v === undefined) return undefined
        if (opts && opts.json) {
          try {
            return JSON.parse(v)
          } catch (e) {
            return undefined
          }
        }
        return v
      },
      body: function () {
        return rt.body()
      },
      files: function (name) {
        return rt.files(name)
      },
      ip: rt.ip,
      event: rt.event,
    }

    // cookieOpts copies a cookie's options with a Date expires as milliseconds, which Go reads (a Date doesn't
    // cross into Go as a time)
    function cookieOpts(opts) {
      var o = {}
      for (var k in opts || {}) o[k] = opts[k]
      if (o.expires instanceof Date) o.expires = o.expires.getTime()
      return o
    }

    function sendWith(type, a, b, conv) {
      if (b === undefined) rt.send(200, type, conv(a))
      else rt.send(a, type, conv(b))
      return ctx.res
    }
    ctx.res = {
      status: function (code) {
        rt.setStatus(code)
        return ctx.res
      },
      header: function (name, value) {
        if (value === undefined) return rt.getHeader(name)
        rt.setHeader(name, String(value))
        return ctx.res
      },
      cookie: function (name, value, opts) {
        rt.setCookie(name, typeof value === 'string' ? value : JSON.stringify(value), cookieOpts(opts))
        return ctx.res
      },
      clearCookie: function (name, opts) {
        rt.clearCookie(name, opts || {})
        return ctx.res
      },
      redirect: function (location, opts) {
        // redirect(location, 301) as in 0.x, or redirect(location, { status, flash })
        if (typeof opts === 'number') opts = { status: opts }
        opts = opts || {}
        var status = opts.status === undefined ? 302 : opts.status
        if (typeof status !== 'number' || status < 300 || status > 399 || status % 1 !== 0) {
          throw new Error('ctx.res.redirect: status must be a 3xx code, got ' + status)
        }
        if (opts.flash !== undefined) rt.setCookie('pp_flash', JSON.stringify(opts.flash), { maxAge: 300 })
        rt.redirect(status, String(location))
        return ctx.res
      },
      json: function (a, b) {
        if (b === undefined) rt.sendJSON(200, a)
        else rt.sendJSON(a, b)
        return ctx.res
      },
      html: function (a, b) {
        return sendWith('text/html; charset=utf-8', a, b, String)
      },
      text: function (a, b) {
        return sendWith('text/plain; charset=utf-8', a, b, String)
      },
      file: function (path) {
        rt.sendFile(String(path))
        return ctx.res
      },
      sse: function (opts) {
        return rt.sse(opts || {})
      },
      layout: function (on) {
        rt.setLayout(!!on)
        return ctx.res
      },
    }
    Object.defineProperty(ctx.res, 'sent', {
      get: function () {
        return rt.sent()
      },
    })

    ctx.data = {}
    ctx.params = rt.params
    ctx.route = { path: route.path, name: route.name, file: route.file }
    ctx.auth = conf.auth
    ctx.app = $app
    ctx.dev = conf.dev
    ctx.pagesDir = rt.pagesDir
    ctx.env = function (name) {
      return $os.getenv(name) || ''
    }
    ctx.log = {
      debug: function () {
        rt.log('debug', Array.prototype.slice.call(arguments))
      },
      info: function () {
        rt.log('info', Array.prototype.slice.call(arguments))
      },
      warn: function () {
        rt.log('warn', Array.prototype.slice.call(arguments))
      },
      error: function () {
        rt.log('error', Array.prototype.slice.call(arguments))
      },
    }
    ctx.meta = function (key, value) {
      if (arguments.length < 2) return rt.metaGet(key)
      rt.metaSet(key, value)
      return value
    }
    ctx.asset = function (path) {
      return rt.asset(route.file, String(path))
    }
    ctx.include = function (name, locals) {
      return include(route.file, name, locals)
    }
    ctx.resolve = function (name, opts) {
      var mode = (opts && opts.mode) || 'require'
      var file = rt.resolveFile(route.file, String(name))
      switch (mode) {
        case 'require':
          return load(file)
        case 'raw':
          return rt.readText(file)
        case 'script':
          return '<script>\n' + rt.readText(file) + '\n</script>'
        case 'style':
          return '<style>\n' + rt.readText(file) + '\n</style>'
      }
      throw new Error('resolve: unknown mode ' + JSON.stringify(String(mode)))
    }
    ctx.store = function (name, value) {
      if (value === undefined) return $app.store().get(name)
      $app.store().set(name, value)
      return value
    }
    var flashRead = false,
      flashValue
    ctx.flash = function () {
      if (!flashRead) {
        flashRead = true
        var raw = rt.flash()
        if (raw !== undefined && raw !== null) {
          try {
            flashValue = JSON.parse(raw)
          } catch (e) {
            flashValue = raw
          }
        }
      }
      return flashValue
    }
    if (conf.realtime) {
      ctx.realtime = {
        // send(topic, data, { filter(client) }) sends to the clients subscribed to topic (that filter accepts)
        send: function (topic, data, opts) {
          var to = null
          if (opts && typeof opts.filter === 'function') {
            to = []
            var clients = rt.realtimeClients(String(topic))
            for (var i = 0; i < clients.length; i++) {
              if (opts.filter(clients[i])) to.push(clients[i])
            }
          }
          rt.realtimeSend(String(topic), data, to)
        },
        client: function (id) {
          return rt.realtimeClient(String(id))
        },
        // subscribe(topic, { filter, expand, fields }) listens like a browser on /api/realtime, as this visitor:
        // `for await (const msg of sub)`. Each next() waits off the VM's loop; the subscription ends with the request.
        subscribe: function (topic, opts) {
          var sub = rt.realtimeSubscribe(String(topic), opts || {})
          var ended = { done: true, value: undefined }
          var it = {
            next: function () {
              return sub.next.async().then(function (data) {
                return data === '' ? ended : { done: false, value: JSON.parse(data) }
              })
            },
            return: function () {
              sub.close()
              return Promise.resolve(ended)
            },
            close: function () {
              sub.close()
            },
          }
          it[Symbol.asyncIterator] = function () {
            return it
          }
          return it
        },
      }
    }
    if (conf.session) {
      var session = conf.session
      ctx.session = {}
      Object.keys(session).forEach(function (name) {
        if (name === 'current') return
        ctx.session[name] = function () {
          var out = session[name].apply(null, arguments)
          ctx.auth = session.current()
          return out
        }
      })
    }
    var pbClient = null,
      pbToken = null
    // sdkFetch is the SDK's transport. The body is parsed here, not in Go, so the SDK gets real JS arrays and objects:
    // a Go-backed list isn't spread by concat, which nested getFullList's records ([[...]]).
    function sdkFetch(opts) {
      var res = rt.apiSend(opts)
      var json = null
      if (res.raw) {
        try {
          json = JSON.parse(res.raw)
        } catch (e) {}
      }
      return { statusCode: res.statusCode, headers: res.headers, raw: res.raw, json: json }
    }
    ctx.pb = function () {
      var token = rt.authToken()
      if (pbClient && pbToken === token) return pbClient
      var Client = sdk()
      var pb = new Client(rt.sdkBaseURL)
      if (token) pb.authStore.save(token, JSON.parse(rt.authRecordJSON()))
      pb.beforeSend = function (url, options) {
        options.fetch = sdkFetch
        return { url: url, options: options }
      }
      pbClient = pb
      pbToken = token
      return pb
    }
    ctx.httpError = function (status, message) {
      var e = new Error(message === undefined ? '' : String(message))
      e.__ppHttp = { status: status, message: message === undefined ? '' : String(message) }
      return e
    }

    // ---- what compiled templates receive ----
    pp = {
      ctx: ctx,
      include: include, // one JS frame less per nesting level: the C stack bounds how deep includes go
      require: function (from, name) {
        return requireFrom(from, String(name))
      },
      asset: rt.asset,
      setSlot: rt.setSlot,
      getSlot: rt.getSlot,
      wrapped: rt.wrapped,
      esc: esc,
      str: str,
      rethrow: function (e, file, line) {
        if (e && typeof e === 'object') {
          if (!e.__ppHttp && !e.__ppWhere) {
            try {
              e.__ppWhere = file + ':' + line
            } catch (_) {}
          }
          return e
        }
        var x = new Error(String(e))
        x.__ppWhere = file + ':' + line
        return x
      },
    }

    // renderError renders +error.ejs inside the root layout, for Go's error responses (render.go).
    rt.renderError = function (status, message, errorTpl, rootLayout) {
      ctx.error = { status: status, message: message }
      return then(render(errorTpl, null), function (html) {
        if (!rootLayout) return html
        rt.setWrapped(html)
        return render(rootLayout, null)
      })
    }

    // ---- plugins ----
    var plugins = config.plugins
    for (var pi = 0; pi < plugins.length; pi++) {
      if (typeof plugins[pi].extendContext === 'function') plugins[pi].extendContext(ctx)
    }
    for (pi = 0; pi < plugins.length; pi++) {
      if (typeof plugins[pi].onRequest === 'function') plugins[pi].onRequest(ctx)
      if (rt.sent()) return
    }
    function transform(html, file) {
      for (var i = 0; i < plugins.length; i++) {
        if (typeof plugins[i].transform === 'function') {
          var out = plugins[i].transform({ html: html, ctx: ctx, file: file })
          if (typeof out === 'string') html = out
        }
      }
      return html
    }

    // ---- the request ----
    var methods = ['get', 'post', 'put', 'patch', 'delete']
    function assign(v) {
      if (v && typeof v === 'object' && v !== ctx.res && !rt.sent()) {
        for (var k in v) ctx.data[k] = v[k]
      }
    }

    // handle runs the route's .ts module (load, then the method's handler), then renders the template and its
    // layouts. Each step may return a promise; the next one runs once it settles.
    function handle() {
      var mod = route.handler ? load(route.handler) : null
      var m = rt.method.toLowerCase()
      if (m === 'head') m = 'get'

      if (mod && typeof mod.load === 'function') {
        return then(mod.load(ctx), function (v) {
          assign(v)
          if (rt.sent()) return
          return runMethod(mod, m)
        })
      }
      return runMethod(mod, m)
    }

    function runMethod(mod, m) {
      var h = mod && mod[m]
      if (typeof h === 'function') {
        return then(h(ctx), function (v) {
          assign(v)
          if (rt.sent()) return
          return respond(m)
        })
      } else if (route.kind === 'endpoint') {
        // nothing to render, so a method without a handler is a 405. A page with a template renders for any
        // method (like PHP); the template can branch on ctx.req.method.
        var allow = ['GET', 'HEAD']
        for (var i = 1; i < methods.length; i++) {
          if (mod && typeof mod[methods[i]] === 'function') allow.push(methods[i].toUpperCase())
        }
        if (!(mod && typeof mod.get === 'function')) allow = allow.slice(2)
        rt.methodNotAllowed(allow.join(', '))
        return
      }
      return respond(m)
    }

    function respond(m) {
      if (route.kind === 'endpoint') {
        if (!rt.sent()) throw new Error(route.file + ': the ' + m + ' handler returned without sending a response')
        return
      }
      return then(render(route.file, null), function (html) {
        html = transform(html, route.file)
        if (rt.sent()) return
        return wrapLayouts(html, rt.layoutsOn() ? 0 : route.layouts.length)
      })
    }

    // wrapLayouts wraps html in the layouts from index li outward, then sends the page.
    function wrapLayouts(html, li) {
      for (; li < route.layouts.length; li++) {
        rt.setWrapped(html)
        var out = render(route.layouts[li], null)
        if (isPromise(out)) {
          var at = li
          return out.then(function (h) {
            h = transform(h, route.layouts[at])
            if (rt.sent()) return
            return wrapLayouts(h, at + 1)
          })
        }
        html = transform(out, route.layouts[li])
        if (rt.sent()) return
      }
      rt.sendPage(html)
    }

    // next runs the next +middleware (root first), then handle. A middleware may be async and await next(); a sync
    // one that calls next() without returning it still has the rest of the request awaited after it.
    var mi = 0
    function next() {
      if (mi < route.middlewares.length) {
        var file = route.middlewares[mi++]
        var fn = load(file)
        if (typeof fn !== 'function') throw new Error(file + ' must export a function (ctx, next)')
        var nextCalled = false
        var down // what next() returned, if the rest of the request went async
        var downDone = false
        var out = fn(ctx, function () {
          nextCalled = true
          var v = next()
          if (isPromise(v)) {
            down = v
            v.then(
              function () {
                downDone = true
              },
              function () {
                downDone = true
              }
            )
          }
          return v
        })
        var check = function (v) {
          if (!nextCalled && !rt.sent()) {
            throw new Error(file + ' ended the request without calling next() or sending a response')
          }
          // An async middleware that awaited next() has seen it settle (and maybe caught its error); one that didn't
          // await it leaves it pending, so wait for it here.
          if (down && !downDone && down !== v) {
            return down.then(function () {
              return v
            })
          }
          return v
        }
        if (isPromise(out)) return out.then(check)
        if (!nextCalled && !rt.sent()) {
          throw new Error(file + ' ended the request without calling next() or sending a response')
        }
        if (down && down !== out) {
          return down.then(function () {
            return out
          })
        }
        return out
      }
      return handle()
    }
    return next()
  }
})
