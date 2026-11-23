package pages

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"gopkg.in/yaml.v3"

	cachev1 "pocketone.dev/ext/contracts/cache/v1"
	"pocketone.dev/ext/extkit/js"
)

//go:embed driver.js
var driverSource string

// callSource is how Go calls a JS function it doesn't hold: get() returns the object (a NativeFunc can return a JS
// value, while Func.Call can only pass Go values), name is the method. One js.Func per VM, kept for its lifetime.
const callSource = "(function (get, name) { var o = get(); return o[name].apply(o, Array.prototype.slice.call(arguments, 2)) })"

// maxIncludeDepth guards against templates including themselves. It stays well below what the VM's C stack allows
// (about 50 levels at jsvm's default 256 KiB), so a loop gets this error rather than a stack overflow.
const maxIncludeDepth = 32

// compiledFile is a template or module compiled to JS source, as kept in the pages.compiled cache bucket (cache/v1)
// under its kind and content hash. Each VM evaluates a source once and keeps the function in its own cache (see
// vmCache), so the bucket is only read the first time a VM meets a file.
type compiledFile struct {
	src         string         // evaluates to a function
	fmRaw       string         // .md pages: the front matter's YAML, as written
	frontMatter map[string]any // fmRaw parsed
}

// encode is c as a cache value: the front matter's length (4 bytes), the front matter, the source.
func (c *compiledFile) encode() []byte {
	b := make([]byte, 4, 4+len(c.fmRaw)+len(c.src))
	binary.BigEndian.PutUint32(b, uint32(len(c.fmRaw)))
	return append(append(b, c.fmRaw...), c.src...)
}

func decodeCompiled(b []byte) (*compiledFile, error) {
	if len(b) < 4 || uint64(binary.BigEndian.Uint32(b)) > uint64(len(b)-4) {
		return nil, errors.New("pages: bad compiled cache entry")
	}
	n := 4 + int(binary.BigEndian.Uint32(b))
	c := &compiledFile{fmRaw: string(b[4:n]), src: string(b[n:])}
	if c.fmRaw != "" {
		if err := yaml.Unmarshal([]byte(c.fmRaw), &c.frontMatter); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// compiledBucketName is pages' cache/v1 bucket of compiled templates and modules.
const compiledBucketName = "pages.compiled"

// compiledBucket opens pages.compiled on first use (at request time, after contracts are sealed). Without a cache/v1
// provider it keeps nothing, and each VM compiles a file the first time it meets it.
func (p *Pages) compiledBucket() cachev1.Bucket {
	p.bucketOnce.Do(func() {
		b, err := cachev1.Open(p.app, compiledBucketName, cachev1.Options{MaxMemory: 32 << 20})
		if err != nil {
			p.logger().Warn("pages: compiled files won't be cached", "error", err)
			b = cachev1.None{}
		}
		p.bucket = b
	})
	return p.bucket
}

// compiled returns key's compiled file from the cache, else builds it with build and caches it. A cache error is a
// miss: the file is compiled again.
func (p *Pages) compiled(ctx context.Context, key string, build func() (*compiledFile, error)) (*compiledFile, error) {
	b := p.compiledBucket()
	if v, ok, _ := b.Get(ctx, key); ok {
		if c, err := decodeCompiled(v); err == nil {
			return c, nil
		}
	}
	c, err := build()
	if err != nil {
		return nil, err
	}
	_ = b.Set(ctx, key, c.encode(), 0)
	return c, nil
}

// httpError is a ctx.httpError() thrown by page code.
type httpError struct {
	status  int
	message string
}

// vmState is pages' Go state for one VM, kept in the VM's cache object so it lives and dies with the VM.
type vmState struct {
	call        js.Func                   // callSource
	frontMatter map[string]map[string]any // .md pages' front matter by template key, beside their functions
}

// request is one page request's state.
type request struct {
	p      *Pages
	e      *core.RequestEvent
	root   *os.Root
	route  *Route
	params map[string]string
	rt     js.Runtime
	cache  js.Object // per-VM cache of compiled functions and module exports
	st     *vmState
	obj    js.Object // the rt object the driver receives

	status   int
	sent     bool
	layouts  bool
	wrapped  string
	slots    map[string]string
	meta     map[string]any
	depth    int
	dev      bool
	builtins map[string]bool
	auth     *core.Record
	token    string
	tokenFor string
	flashed  bool
	where    string     // template location of the error that failed the request (__ppWhere)
	httpErr  *httpError // a ctx.httpError() that failed the request
}

// render runs a page or API route in one call into the app's JS VM.
func (p *Pages) render(e *core.RequestEvent, root *os.Root, route *Route, params map[string]string) error {
	return p.renderStatus(e, root, route, params, 0)
}

// renderStatus is render with a default response status (0 = 200), e.g. 404 for +404.ejs.
func (p *Pages) renderStatus(e *core.RequestEvent, root *os.Root, route *Route, params map[string]string, status int) error {
	t := p.Table()
	dev := p.dev()
	if ct := pageContentType(route); ct != "" {
		e.Response.Header().Set("Content-Type", ct)
	}

	err := js.Run(p.app, func(rt js.Runtime) error {
		r := &request{
			p: p, e: e, root: root, route: route, params: params, rt: rt,
			status: status, layouts: true, slots: map[string]string{}, meta: map[string]any{}, dev: dev,
		}
		if err := r.run(t); err != nil {
			r.fail(err)
		}
		r.release()
		return nil
	})
	if errors.Is(err, js.ErrNoEngine) {
		return e.String(http.StatusInternalServerError, "ext/pages: the jsvm extension must be registered on this app\n")
	}
	return err
}

// release drops the request's Go references to its JS objects. The rt object's methods are Go closures over r, and
// the VM's handle table keeps those alive, so while r holds rt (or anything that reaches it) neither the JS GC nor
// Go's can free either side: every request would stay in memory until the VM ran out of it.
func (r *request) release() {
	if r.obj != nil {
		js.Release(r.obj)
	}
	if r.cache != nil {
		js.Release(r.cache)
	}
	r.obj, r.cache, r.st = nil, nil, nil
}

// pageContentType is the Content-Type a page's inner extension implies (robots.txt.ejs → text/plain), or "" for
// HTML pages. Page code can still set another one.
func pageContentType(route *Route) string {
	if route.Kind != KindPage {
		return ""
	}
	ext := strings.ToLower(path.Ext(route.Name))
	if ext == "" || ext == ".html" || ext == ".htm" {
		return ""
	}
	return mime.TypeByExtension(ext)
}

// vmCache returns the VM's cache object (the __ppCache global) and pages' state for the VM, making them on first use.
func (r *request) vmCache() (js.Object, *vmState, error) {
	if o := r.rt.Get("__ppCache").Object(); o != nil {
		if st, ok := o.Get("go").Export().(*vmState); ok {
			return o, st, nil
		}
	}
	call, err := r.rt.Eval(callSource, "ext/pages/call.js")
	if err != nil {
		return nil, nil, err
	}
	driver, err := r.rt.Eval(driverSource, "ext/pages/driver.js")
	if err != nil {
		return nil, nil, err
	}
	o := r.rt.NewObject()
	st := &vmState{call: call.Func(), frontMatter: map[string]map[string]any{}}
	if err := errors.Join(o.Set("driver", driver), o.Set("go", st), o.Set("loader", r.p.loader(r.rt)), r.rt.Set("__ppCache", o)); err != nil {
		return nil, nil, err
	}
	return o, st, nil
}

// loader is the module loader the driver gives each module's require(), one per VM. Modules are cached per VM, so a
// require() inside one runs on later requests too (in a handler, or a function called at request time); it can't use
// the request that first loaded the module, whose pb_pages root is closed and whose cache is released when it ends.
// Each call opens its own root and works outside any request.
func (p *Pages) loader(rt js.Runtime) js.Object {
	o := rt.NewObject()
	with := func(fn func(r *request) (any, error)) (any, error) {
		root, err := os.OpenRoot(p.dir)
		if err != nil {
			return nil, err
		}
		defer root.Close()
		cache := rt.Get("__ppCache").Object()
		if cache == nil {
			return nil, errors.New("ext/pages: module loader used before the page driver")
		}
		defer js.Release(cache)
		return fn(&request{p: p, rt: rt, root: root, cache: cache})
	}
	_ = errors.Join(
		o.Set("resolveRequire", js.NativeFunc(func(c js.Call) (any, error) {
			from, name := c.Arg(0).String(), c.Arg(1).String()
			return with(func(r *request) (any, error) { return r.resolveRequire(from, name) })
		})),
		o.Set("module", js.NativeFunc(func(c js.Call) (any, error) {
			file := c.Arg(0).String()
			return with(func(r *request) (any, error) { return r.module(file) })
		})),
		o.Set("moduleKey", js.NativeFunc(func(c js.Call) (any, error) {
			file := c.Arg(0).String()
			return with(func(r *request) (any, error) { return r.moduleKey(file) })
		})),
		o.Set("program", js.NativeFunc(func(c js.Call) (any, error) {
			name := c.Arg(0).String()
			return with(func(r *request) (any, error) { return r.builtinProgram(name) })
		})),
	)
	return o
}

// context is the request's context, or a background one outside a request (the module loader).
func (r *request) context() context.Context {
	if r.e == nil {
		return context.Background()
	}
	return r.e.Request.Context()
}

// callJS calls the method name of the JS object o with Go args, and returns its result exported.
func (r *request) callJS(o js.Object, name string, args ...any) (any, error) {
	get := js.NativeFunc(func(js.Call) (any, error) { return o, nil })
	return r.st.call.Call(append([]any{get, name}, args...)...)
}

// run calls the driver.
func (r *request) run(t *Table) error {
	cache, st, err := r.vmCache()
	if err != nil {
		return err
	}
	r.cache, r.st = cache, st
	r.obj = r.rtObject(t)
	_, err = r.callJS(r.cache, "driver", js.NativeFunc(func(js.Call) (any, error) { return r.obj, nil }))
	return err
}

// program returns the function src evaluates to on this VM, evaluating it once per VM (cached under key).
func (r *request) program(key, name, src string) (js.Value, error) {
	if v := r.cache.Get(key); !v.Nullish() {
		return v, nil
	}
	v, err := r.rt.Eval(src, name)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if err := r.cache.Set(key, v); err != nil {
		return nil, err
	}
	return v, nil
}

// ---- files ----

func (r *request) readFile(file string) ([]byte, error) {
	b, err := r.root.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return b, nil
}

// template returns the template function for file (page, layout or partial), and merges a .md page's front matter
// into ctx.meta.
func (r *request) template(file string) (js.Value, error) {
	hash := r.p.hashes.get(r.root, file)
	if hash == "" {
		return nil, fmt.Errorf("%s: not found", file)
	}
	key := "t:" + hash
	if v := r.cache.Get(key); !v.Nullish() { // this VM has it
		for k, val := range r.st.frontMatter[key] {
			r.meta[k] = val
		}
		return v, nil
	}
	c, err := r.p.compiled(r.context(), key, func() (*compiledFile, error) {
		src, err := r.readFile(file)
		if err != nil {
			return nil, err
		}
		text := string(src)
		c := &compiledFile{}
		if path.Ext(file) == ".md" {
			raw, body, ok := cutFrontMatter(text)
			if ok {
				var fm map[string]any
				if err := yaml.Unmarshal([]byte(raw), &fm); err != nil {
					return nil, fmt.Errorf("%s: front matter: %w", file, err)
				}
				c.fmRaw, c.frontMatter, text = raw, fm, body
			}
		}
		code, err := CompileEJS(text)
		if err == nil && path.Ext(file) == ".ets" {
			code, err = stripTemplateTypes(code)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		c.src = code
		return c, nil
	})
	if err != nil {
		return nil, err
	}
	if c.frontMatter != nil {
		r.st.frontMatter[key] = c.frontMatter
	}
	for k, v := range c.frontMatter {
		r.meta[k] = v
	}
	return r.program(key, file, c.src)
}

// enter counts one level of template nesting, refusing past maxIncludeDepth; leave undoes it.
func (r *request) enter(file string) error {
	if r.depth >= maxIncludeDepth {
		return fmt.Errorf("%s: includes nested more than %d deep", file, maxIncludeDepth)
	}
	r.depth++
	return nil
}

func (r *request) leave() { r.depth-- }

// includeExts are tried, in order, for an include name without an extension.
var includeExts = []string{".ets", ".ejs", ".md", ".jsx", ".tsx"}

// includeFile finds the _private partial name, looked up from file from (SPEC 4.2).
func (r *request) includeFile(from, name string) (string, error) {
	file, err := r.lookupPrivate(from, name, includeExts)
	if err != nil {
		return "", fmt.Errorf("%s: %w", from, err)
	}
	return file, nil
}

// lookupPrivate finds name in _private folders from the folder of from upwards (SPEC 4.2). exts are tried when
// name has no extension.
func (r *request) lookupPrivate(from, name string, exts []string) (string, error) {
	if name == "" || strings.Contains(name, "..") || strings.Contains(name, "\\") {
		return "", fmt.Errorf("include %q: invalid name", name)
	}
	withExts := func(base string) []string {
		var out []string
		if path.Ext(base) != "" {
			out = append(out, base) // exact first; a dotted name (nav.main) still gets the extensions below
		}
		for _, e := range exts {
			if e != "" || path.Ext(base) == "" {
				out = append(out, base+e)
			}
		}
		return out
	}
	candidates := func(dir string) []string {
		return withExts(path.Join(dir, "_private", strings.TrimPrefix(name, "/")))
	}
	exists := func(f string) bool {
		st, err := r.root.Stat(f)
		return err == nil && st.Mode().IsRegular()
	}
	if strings.HasPrefix(name, "/") {
		// 0.x's form names the _private folder itself, from the top of pb_pages: /_private/x, /blog/_private/x
		if rel := strings.TrimPrefix(name, "/"); strings.HasPrefix(rel, "_private/") || strings.Contains(rel, "/_private/") {
			for _, c := range withExts(path.Clean(rel)) {
				if exists(c) {
					return c, nil
				}
			}
			return "", fmt.Errorf("%q not found", name)
		}
		for _, c := range candidates(".") {
			if exists(c) {
				return c, nil
			}
		}
		return "", fmt.Errorf("%q not found in _private/", name)
	}
	dir := path.Dir(from)
	// a file inside a _private folder starts at the folder containing it
	if i := strings.LastIndex("/"+dir+"/", "/_private/"); i >= 0 {
		dir = path.Clean(strings.TrimSuffix(("/" + dir + "/")[:i], "/"))
		dir = strings.TrimPrefix(dir, "/")
		if dir == "" {
			dir = "."
		}
	}
	for {
		for _, c := range candidates(dir) {
			if exists(c) {
				return c, nil
			}
		}
		if dir == "." || dir == "" {
			break
		}
		dir = path.Dir(dir)
	}
	return "", fmt.Errorf("%q not found in any _private/ folder from %s up", name, path.Dir(from))
}

// moduleKey is the per-VM cache key of file's exports, which are reused until the file changes.
func (r *request) moduleKey(file string) (string, error) {
	hash := r.p.hashes.get(r.root, file)
	if hash == "" {
		return "", fmt.Errorf("%s: not found", file)
	}
	return "e:" + file + ":" + hash, nil
}

// module returns what the driver needs to run a JS module (CommonJS) from pb_pages: { fn, filename, dirname, ts },
// or { json } (the file's text) for a .json file.
func (r *request) module(file string) (js.Object, error) {
	hash := r.p.hashes.get(r.root, file)
	if hash == "" {
		return nil, fmt.Errorf("%s: not found", file)
	}
	o := r.rt.NewObject()
	if path.Ext(file) == ".json" {
		src, err := r.readFile(file)
		if err != nil {
			return nil, err
		}
		return o, o.Set("json", string(src))
	}
	key := "m:" + hash
	fn := r.cache.Get(key) // this VM's, when it has met the file
	if fn.Nullish() {
		c, err := r.p.compiled(r.context(), key, func() (*compiledFile, error) {
			src, err := r.readFile(file)
			if err != nil {
				return nil, err
			}
			code, err := transpile(file, src)
			if err != nil {
				return nil, err
			}
			return &compiledFile{src: "(function (module, exports, require, __filename, __dirname, __h, __Fragment) {\n" + code + "\n})"}, nil
		})
		if err != nil {
			return nil, err
		}
		if fn, err = r.program(key, file, c.src); err != nil {
			return nil, err
		}
	}
	_, ts := tsExts[path.Ext(file)]
	return o, errors.Join(o.Set("fn", fn), o.Set("filename", r.absPath(file)), o.Set("dirname", r.absPath(path.Dir(file))), o.Set("ts", ts))
}

// absPath turns a pb_pages slash path into an absolute OS path (for __filename and __dirname, as in Node and 0.x).
func (r *request) absPath(p string) string {
	base, err := filepath.Abs(r.p.dir)
	if err != nil {
		base = r.p.dir
	}
	return filepath.Join(base, filepath.FromSlash(p))
}

// ---- responses ----

func (r *request) isHead() bool { return r.e.Request.Method == http.MethodHead }

var errSent = errors.New("a response was already sent")

func (r *request) write(status int, contentType string, body []byte) error {
	if r.sent {
		return errSent
	}
	r.sent = true
	h := r.e.Response.Header()
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	r.e.Response.WriteHeader(status)
	if !r.isHead() {
		_, _ = r.e.Response.Write(body)
	}
	return nil
}

func (r *request) sendPage(html string) error {
	status := r.status
	if status == 0 {
		status = http.StatusOK
	}
	ct := r.e.Response.Header().Get("Content-Type")
	if ct == "" {
		ct = "text/html; charset=utf-8"
	}
	return r.write(status, ct, []byte(html))
}

// ---- cookies ----

func encodeURIComponent(s string) string {
	const keep = "-_.!~*'()"
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(keep, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func (r *request) secure() bool {
	return r.e.Request.TLS != nil || strings.EqualFold(r.e.Request.Header.Get("X-Forwarded-Proto"), "https")
}

func (r *request) setCookie(name, value string, opts map[string]any) {
	c := &http.Cookie{Name: name, Value: encodeURIComponent(value), Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.secure()}
	applyCookieOpts(c, opts)
	http.SetCookie(r.e.Response, c)
}

func applyCookieOpts(c *http.Cookie, opts map[string]any) {
	for k, v := range opts {
		switch k {
		case "path":
			c.Path = fmt.Sprint(v)
		case "domain":
			c.Domain = fmt.Sprint(v)
		case "maxAge":
			if n, ok := toInt(v); ok {
				if n <= 0 {
					c.MaxAge = -1
				} else {
					c.MaxAge = n
				}
			}
		case "expires":
			if t, ok := toTime(v); ok {
				c.Expires = t.UTC()
			}
		case "httpOnly":
			c.HttpOnly, _ = v.(bool)
		case "secure":
			c.Secure, _ = v.(bool)
		case "sameSite":
			switch strings.ToLower(fmt.Sprint(v)) {
			case "strict":
				c.SameSite = http.SameSiteStrictMode
			case "none":
				c.SameSite = http.SameSiteNoneMode
			default:
				c.SameSite = http.SameSiteLaxMode
			}
		}
	}
}

// toTime reads a cookie's expires option: milliseconds since the epoch (as Date.now() gives; the driver passes a JS
// Date this way), a time.Time, or a date string (RFC 3339 or an HTTP date).
func toTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, !t.IsZero()
	case int64:
		return time.UnixMilli(t), true
	case int:
		return time.UnixMilli(int64(t)), true
	case float64:
		return time.UnixMilli(int64(t)), true
	case string:
		if p, err := time.Parse(time.RFC3339, t); err == nil {
			return p, true
		}
		if p, err := http.ParseTime(t); err == nil {
			return p, true
		}
	}
	return time.Time{}, false
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}

func (r *request) cookies() map[string]string {
	out := map[string]string{}
	for _, c := range r.e.Request.Cookies() {
		v, err := url.PathUnescape(c.Value)
		if err != nil {
			v = c.Value
		}
		if _, ok := out[c.Name]; !ok {
			out[c.Name] = v
		}
	}
	return out
}

// ---- errors ----

// asHTTPError returns the ctx.httpError() that failed the request: as the driver reported it, or from the thrown
// value when it reached Go another way.
func (r *request) asHTTPError(err error) (*httpError, bool) {
	if r.httpErr != nil {
		return r.httpErr, true
	}
	if v, ok := js.Thrown(err); ok {
		if m, ok := v.(map[string]any); ok {
			if h, ok := m["__ppHttp"].(map[string]any); ok {
				return httpErrorFrom(h), true
			}
		}
	}
	return nil, false
}

func httpErrorFrom(m map[string]any) *httpError {
	st, _ := toInt(m["status"])
	if st < 400 || st > 599 {
		st = http.StatusInternalServerError
	}
	msg, _ := m["message"].(string)
	return &httpError{status: st, message: msg}
}

// errorWhere returns the template location attached to the error that failed the request, if any.
func (r *request) errorWhere(err error) string {
	if r.where != "" {
		return r.where
	}
	if v, ok := js.Thrown(err); ok {
		if m, ok := v.(map[string]any); ok {
			if w, ok := m["__ppWhere"].(string); ok {
				return w
			}
		}
	}
	return ""
}

// fail answers a request whose page code threw.
func (r *request) fail(err error) {
	if he, ok := r.asHTTPError(err); ok {
		r.sendError(he.status, he.message, nil)
		return
	}
	where := r.errorWhere(err)
	r.p.logger().Error("pages: request failed", "path", r.e.Request.URL.Path, "file", r.route.File, "where", where, "error", err.Error())
	r.sendError(http.StatusInternalServerError, "", err)
}

// sendError writes an error response: +error.ejs when there is one (not in dev for 500s), else a plain page.
func (r *request) sendError(status int, message string, cause error) {
	if r.sent {
		return
	}
	// reset what the failed attempt set
	h := r.e.Response.Header()
	h.Del("Content-Type")

	if r.jsonErrors() {
		if message == "" {
			message = http.StatusText(status)
			if cause != nil && r.dev {
				message = cause.Error()
			}
		}
		body, _ := json.Marshal(map[string]any{"status": status, "message": message})
		r.write(status, "application/json", body)
		return
	}

	if cause != nil && r.dev {
		where := r.errorWhere(cause)
		var b strings.Builder
		b.WriteString("<!doctype html><html><body><h1>Pages error</h1>")
		if where != "" {
			b.WriteString("<p><code>" + html.EscapeString(where) + "</code></p>")
		}
		b.WriteString("<pre><code>" + html.EscapeString(cause.Error()) + "</code></pre></body></html>")
		r.write(status, "text/html; charset=utf-8", []byte(b.String()))
		return
	}

	if message == "" {
		message = "Something went wrong."
		if status != http.StatusInternalServerError {
			message = http.StatusText(status)
		}
	}
	if t := r.p.Table(); t != nil && t.ErrorTpl != "" && r.depth == 0 {
		if out, ok := r.renderErrorPage(t, status, message); ok {
			r.write(status, "text/html; charset=utf-8", []byte(out))
			return
		}
	}
	page := fmt.Sprintf("<!doctype html><html><head><title>%d</title></head><body><p>%s</p></body></html>", status, html.EscapeString(message))
	r.write(status, "text/html; charset=utf-8", []byte(page))
}

// jsonErrors reports whether errors go out as JSON ({status, message}) rather than a page: for an endpoint, unless the
// request's Accept puts HTML first (a browser navigating to it). A page's errors are always pages.
func (r *request) jsonErrors() bool {
	if r.route.Kind != KindEndpoint {
		return false
	}
	first, _, _ := strings.Cut(r.e.Request.Header.Get("Accept"), ",")
	first, _, _ = strings.Cut(first, ";")
	switch strings.ToLower(strings.TrimSpace(first)) {
	case "text/html", "application/xhtml+xml":
		return false
	}
	return true
}

// renderErrorPage renders +error.ejs inside the root layout (the driver's rt.renderError). It returns false if that
// fails too, or if the driver failed before it could render templates.
func (r *request) renderErrorPage(t *Table, status int, message string) (string, bool) {
	if r.obj == nil || r.obj.Get("renderError").Nullish() {
		return "", false
	}
	r.slots = map[string]string{}
	r.layouts = true
	out, err := r.callJS(r.obj, "renderError", status, message, t.ErrorTpl, t.RootLayout)
	if err != nil {
		r.p.logger().Error("pages: +error.ejs failed", "error", err.Error())
		return "", false
	}
	s, _ := out.(string)
	return s, true
}

// ---- the rt object the driver receives ----

func (r *request) rtObject(t *Table) js.Object {
	e := r.e
	o := r.rt.NewObject()
	set := func(name string, v any) { _ = o.Set(name, v) }

	layouts := r.route.Layouts
	if layouts == nil {
		layouts = []string{}
	}
	middlewares := r.route.Middlewares
	if middlewares == nil {
		middlewares = []string{}
	}
	set("cache", r.cache)
	set("config", t.Config)
	set("route", map[string]any{
		"path": r.route.Pattern, "name": r.route.Name, "file": r.route.File, "kind": r.route.Kind.String(),
		"handler": r.route.Handler, "middlewares": middlewares, "layouts": layouts,
	})
	params := map[string]any{}
	for k, v := range r.params {
		params[k] = v
	}
	set("params", params)
	set("method", e.Request.Method)
	u := e.Request.URL
	scheme := "http"
	if r.secure() {
		scheme = "https"
	}
	origin := scheme + "://" + e.Request.Host
	search := ""
	if u.RawQuery != "" {
		search = "?" + u.RawQuery
	}
	set("url", map[string]any{"href": origin + u.RequestURI(), "origin": origin, "pathname": u.Path, "search": search})
	set("query", func() map[string]any {
		out := map[string]any{}
		for k, vs := range u.Query() {
			arr := make([]any, len(vs))
			for i, v := range vs {
				arr[i] = v
			}
			out[k] = arr
		}
		return out
	})
	set("header", func(name string) string { return e.Request.Header.Get(name) })
	set("cookies", func() map[string]string { return r.cookies() })
	set("body", func() (any, error) {
		info, err := e.RequestInfo()
		if err != nil {
			return nil, err
		}
		return info.Body, nil
	})
	set("files", func(name string) any {
		files, err := e.FindUploadedFiles(name)
		if err != nil {
			return []any{}
		}
		return files
	})
	set("ip", e.RealIP())
	set("event", e)
	set("pagesDir", r.p.dir)

	// configure applies +config.js (loaded by the driver) and returns { dev, auth, realtime, session }.
	set("configure", js.NativeFunc(func(c js.Call) (any, error) {
		r.dev = r.dev || c.Arg(0).Export() == true
		r.builtins = map[string]bool{}
		if list, ok := c.Arg(1).Export().([]any); ok {
			for _, name := range list {
				if s, ok := name.(string); ok {
					r.builtins[s] = true
				}
			}
		}
		r.auth = e.Auth
		// an Authorization header login wins over the pb_auth cookie (as in 0.x)
		if r.builtins["auth"] && r.auth == nil {
			r.readAuthCookie()
		}
		out := r.rt.NewObject()
		err := errors.Join(out.Set("dev", r.dev), out.Set("auth", r.auth), out.Set("realtime", r.builtins["realtime"]))
		if r.builtins["auth"] {
			err = errors.Join(err, out.Set("session", r.sessionObject()))
		}
		return out, err
	}))
	set("moduleExists", func(file, name string) bool { return r.moduleExists(file, name) })
	set("httpError", func(status int, message string) {
		r.httpErr = httpErrorFrom(map[string]any{"status": status, "message": message})
	})
	set("setWhere", func(where string) { r.where = where })

	set("setStatus", func(code int) { r.status = code })
	set("getHeader", func(name string) string { return e.Response.Header().Get(name) })
	set("setHeader", func(name, value string) { e.Response.Header().Set(name, value) })
	set("setCookie", func(name, value string, opts map[string]any) { r.setCookie(name, value, opts) })
	set("clearCookie", func(name string, opts map[string]any) {
		if opts == nil {
			opts = map[string]any{}
		}
		opts["maxAge"] = 0
		r.setCookie(name, "", opts)
	})
	set("redirect", func(status int, location string) error {
		if r.sent {
			return errSent
		}
		r.sent = true
		http.Redirect(e.Response, e.Request, location, status)
		return nil
	})
	set("send", func(status int, contentType, body string) error { return r.write(status, contentType, []byte(body)) })
	set("sendJSON", func(status int, data any) error {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(data); err != nil {
			return err
		}
		return r.write(status, "application/json", bytes.TrimRight(buf.Bytes(), "\n"))
	})
	set("sendFile", func(p string) error {
		f := path.Clean(strings.TrimPrefix(p, "/"))
		if strings.HasPrefix(f, "..") {
			return fmt.Errorf("file %q leaves pb_pages", p)
		}
		fh, err := r.root.Open(f)
		if err != nil {
			return err
		}
		defer fh.Close()
		body, err := io.ReadAll(fh)
		if err != nil {
			return err
		}
		ct := mime.TypeByExtension(path.Ext(f))
		if ct == "" {
			ct = http.DetectContentType(body)
		}
		return r.write(http.StatusOK, ct, body)
	})
	set("sse", js.NativeFunc(func(js.Call) (any, error) { return r.sse() }))
	set("setLayout", func(on bool) { r.layouts = on })
	set("layoutsOn", func() bool {
		if !r.layouts {
			return false
		}
		ct := e.Response.Header().Get("Content-Type")
		return ct == "" || strings.HasPrefix(ct, "text/html")
	})
	set("sent", func() bool { return r.sent })
	set("methodNotAllowed", func(allow string) error {
		e.Response.Header().Set("Allow", allow)
		return r.write(http.StatusMethodNotAllowed, "text/plain; charset=utf-8", []byte("Method Not Allowed\n"))
	})
	set("sendPage", func(html string) error { return r.sendPage(html) })

	set("log", func(level string, args []any) {
		msg := strings.TrimSpace(fmt.Sprintln(args...))
		l := r.p.app.Logger().With("extension", "pages", "file", r.route.File)
		switch level {
		case "debug":
			if r.dev {
				l.Debug(msg)
			}
		case "info":
			l.Info(msg)
		case "warn":
			l.Warn(msg)
		default:
			l.Error(msg)
		}
	})
	set("metaGet", func(key string) any { return r.meta[key] })
	set("metaSet", func(key string, value any) { r.meta[key] = value })
	set("asset", func(from, ref string) string { return r.p.Asset(path.Dir(from), ref) })

	// templates and modules: Go finds and compiles them, the driver calls them
	set("enter", func(file string) error { return r.enter(file) })
	set("leave", func() { r.leave() })
	set("template", js.NativeFunc(func(c js.Call) (any, error) { return r.template(c.Arg(0).String()) }))
	set("markdown", func(file, text string) (string, error) {
		out, err := renderMarkdown(text, func(ref string) string { return r.p.Asset(path.Dir(file), ref) })
		if err != nil {
			return "", fmt.Errorf("%s: markdown: %w", file, err)
		}
		return out, nil
	})
	set("includeFile", func(from, name string) (string, error) { return r.includeFile(from, name) })
	set("resolveFile", func(from, name string) (string, error) {
		file, err := r.lookupPrivate(from, name, []string{"", ".js", ".ts", ".json"})
		if err != nil {
			return "", fmt.Errorf("%s: %w", from, err)
		}
		return file, nil
	})
	set("readText", func(file string) (string, error) {
		b, err := r.readFile(file)
		return string(b), err
	})
	set("moduleKey", func(file string) (string, error) { return r.moduleKey(file) })
	set("module", js.NativeFunc(func(c js.Call) (any, error) { return r.module(c.Arg(0).String()) }))
	set("resolveRequire", js.NativeFunc(func(c js.Call) (any, error) {
		return r.resolveRequire(c.Arg(0).String(), c.Arg(1).String())
	}))
	set("program", js.NativeFunc(func(c js.Call) (any, error) { return r.builtinProgram(c.Arg(0).String()) }))

	set("realtimeClients", r.realtimeClients)
	set("realtimeSend", js.NativeFunc(r.realtimeSend))
	set("realtimeClient", func(id string) any { return r.realtimeClient(id) })

	set("authToken", func() (string, error) { return r.authToken() })
	set("authRecordJSON", func() string { return r.authRecordJSON() })
	set("apiSend", func(opts map[string]any) (map[string]any, error) { return r.apiSend(opts) })
	set("sdkBaseURL", sdkBaseURL)
	set("setWrapped", func(s string) { r.wrapped = s })
	set("wrapped", func() string { return r.wrapped })
	set("setSlot", func(name, content string) { r.slots[name] = content })
	set("getSlot", func(name string) string { return r.slots[name] })
	set("flash", func() any {
		if r.flashed {
			return nil
		}
		r.flashed = true
		v, ok := r.cookies()["pp_flash"]
		if !ok {
			return nil
		}
		r.setCookie("pp_flash", "", map[string]any{"maxAge": 0})
		return v
	})
	return o
}
