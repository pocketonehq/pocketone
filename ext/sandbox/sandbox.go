// Package sandbox confines one PocketBase app's JS hooks and migrations so many apps can share one process.
//
// It plugs into the jsvm extension (ext/jsvm) as one of its extensions:
//
//	jsvm.Register(app, jsvm.Config{Extensions: []any{sb}})
//
// The engine itself enforces the per-app memory cap, the CPU and wall-clock budget per call, the stack limit and the
// sleep cap (jsvm.Config.VM); the sandbox confines what JS can reach.
package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/apis"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/filesystem"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/hook"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/template"
	"pocketone.dev/ext/extkit/js"
)

// Sandbox confines an app's JS hooks and migrations so several apps can share one process:
//
//   - file access ($os, $filepath, $filesystem, $template, $apis.static, require) is limited to Dir, enforced with
//     an [os.Root] (the kernel resolves every path beneath Dir; ".." and escaping symlinks are refused);
//   - host primitives are disabled: $os.cmd/exec, $os.exit, $os.args, $filesystem.local;
//   - $os.getenv and process.env see only Env;
//   - Go methods that reach the process (restart, bootstrap, raw database handles, hook binding at runtime, the HTTP
//     server, ...) are hidden from JS;
//   - every JS call is interrupted after MaxExecution.
type Sandbox struct {
	// Dir is the app's folder (pb_data, pb_hooks, pb_migrations, pb_public). Required.
	Dir string

	// Env is the only environment JS can read.
	Env map[string]string

	// MaxExecution is the wall time one JS call (hook, route, cron job, migration) may take, Go calls included.
	// Zero means 30 seconds; negative disables the limit. The host applies it (Limits).
	MaxExecution time.Duration

	// MaxSleep caps the sleep() binding (default 10 seconds). The host applies it (Limits).
	MaxSleep time.Duration

	// MaxAllocation caps one file read through the sandbox ($os.readFile, $filesystem.fileFromPath) and
	// readerToString/toBytes. Default 64 MiB. JS memory as a whole is the engine's per-app cap (VMConfig.MemoryLimit).
	MaxAllocation int64

	// Console, when set, receives console.log/info/debug ("stdout") and console.warn/error ("stderr") output
	// instead of the process's stdout/stderr, so each app's output stays its own.
	Console func(stream, message string)

	// HTTPSendMaxTimeout (seconds) and HTTPSendMaxBody (bytes) cap $http.send for this app; 0 = no cap
	// (upstream: the caller's timeout, default 120 s, and any body size).
	HTTPSendMaxTimeout int
	HTTPSendMaxBody    int64
	clientOnce         sync.Once
	client             *http.Client

	once      sync.Once
	openErr   error
	dir       string
	realDir   string // dir with symlinks resolved, when different
	root      *os.Root
	templates *template.Registry // the app's, like jsvm's own
}

// ErrSandboxed is returned by bindings that are disabled in the sandbox.
var ErrSandboxed = errors.New("not available on this host (sandboxed)")

func (s *Sandbox) open() error {
	s.once.Do(func() {
		if s.Dir == "" {
			s.openErr = errors.New("sandbox: Dir is required")
			return
		}
		s.dir, s.openErr = filepath.Abs(s.Dir)
		if s.openErr != nil {
			return
		}
		// the folder's real path, for paths that arrive with symlinks resolved (require() resolves them; on macOS
		// /var/folders is /private/var/folders)
		if real, err := filepath.EvalSymlinks(s.dir); err == nil && real != s.dir {
			s.realDir = real
		}
		s.root, s.openErr = os.OpenRoot(s.dir)
		s.templates = template.NewRegistry()
	})
	return s.openErr
}

func (s *Sandbox) maxExecution() time.Duration {
	switch {
	case s == nil || s.MaxExecution < 0:
		return 0
	case s.MaxExecution == 0:
		return 30 * time.Second
	default:
		return s.MaxExecution
	}
}

func (s *Sandbox) maxAllocation() int64 {
	if s.MaxAllocation > 0 {
		return s.MaxAllocation
	}
	return 64 << 20
}

func (s *Sandbox) maxSleep() time.Duration {
	if s.MaxSleep > 0 {
		return s.MaxSleep
	}
	return 10 * time.Second
}

// rel turns a JS path (relative to Dir, or absolute under Dir) into a path relative to Dir.
// Anything outside Dir is rejected here; os.Root rejects escapes through ".." and symlinks.
func (s *Sandbox) rel(p string) (string, error) {
	if p == "" {
		return ".", nil
	}
	if filepath.IsAbs(p) {
		clean := filepath.Clean(p)
		for _, base := range []string{s.dir, s.realDir} {
			if base == "" {
				continue
			}
			if r, err := filepath.Rel(base, clean); err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) {
				return r, nil
			}
		}
		return "", fmt.Errorf("%s: path is outside the app folder", p)
	}
	return filepath.Clean(p), nil
}

// fsPath is rel in io/fs form (slash separated, "." for the root).
func (s *Sandbox) fsPath(p string) (string, error) {
	r, err := s.rel(p)
	if err != nil {
		return "", err
	}
	r = filepath.ToSlash(r)
	if !fs.ValidPath(r) {
		return "", fmt.Errorf("%s: path is outside the app folder", p)
	}
	return r, nil
}

func (s *Sandbox) abs(rel string) string {
	return filepath.Join(s.dir, filepath.FromSlash(rel))
}

// Limits are the engine limits for this app, for the host to put in jsvm.Config.VM: the wall time one call may take
// (0 = no limit) and the sleep cap.
func (s *Sandbox) Limits() (execution, sleep time.Duration) { return s.maxExecution(), s.maxSleep() }

// Modules implements the jsvm extension method: require() reads through the root, under Dir.
func (s *Sandbox) Modules() (string, func(string) ([]byte, error)) {
	if err := s.open(); err != nil {
		return "", func(string) ([]byte, error) { return nil, err }
	}
	return filepath.Join(s.dir, "pb_hooks"), func(p string) ([]byte, error) {
		r, err := s.rel(p)
		if err != nil {
			return nil, fs.ErrNotExist
		}
		return s.root.ReadFile(r)
	}
}

// OnInit implements the jsvm extension method: it replaces the host-reaching bindings (it runs after jsvm's
// standard binds and before Config.OnInit).
func (s *Sandbox) OnInit(rt js.Runtime) {
	if err := s.open(); err != nil {
		panic(err)
	}
	templates := s.templates
	disabled := func(name string) js.NativeFunc {
		return func(js.Call) (any, error) { return nil, fmt.Errorf("%s: %w", name, ErrSandboxed) }
	}
	ns := func(name string) js.Object {
		if o := rt.Get(name).Object(); o != nil {
			return o
		}
		return nil
	}

	// $os
	_ = rt.Set("$os", map[string]any{
		"args":   []string{},
		"exec":   disabled("$os.exec"),
		"cmd":    disabled("$os.cmd"),
		"exit":   disabled("$os.exit"),
		"getenv": func(key string) string { return s.Env[key] },
		"dirFS": func(p string) (fs.FS, error) {
			r, err := s.fsPath(p)
			if err != nil {
				return nil, err
			}
			return fs.Sub(s.root.FS(), r)
		},
		"stat": func(p string) (os.FileInfo, error) {
			r, err := s.rel(p)
			if err != nil {
				return nil, err
			}
			return s.root.Stat(r)
		},
		"readFile": func(p string) ([]byte, error) { return s.readFileCapped(p) },
		"writeFile": func(p string, data []byte, perm os.FileMode) error {
			r, err := s.rel(p)
			if err != nil {
				return err
			}
			return s.root.WriteFile(r, data, perm)
		},
		"readDir": func(p string) ([]os.DirEntry, error) {
			r, err := s.fsPath(p)
			if err != nil {
				return nil, err
			}
			return fs.ReadDir(s.root.FS(), r)
		},
		"tempDir": func() string {
			_ = s.root.MkdirAll(filepath.Join("pb_data", ".pocker_tmp"), 0o755)
			return s.abs("pb_data/.pocker_tmp")
		},
		"truncate": func(p string, size int64) error {
			r, err := s.rel(p)
			if err != nil {
				return err
			}
			f, err := s.root.OpenFile(r, os.O_WRONLY, 0)
			if err != nil {
				return err
			}
			defer f.Close()
			return f.Truncate(size)
		},
		"getwd": func() (string, error) { return s.dir, nil },
		"mkdir": func(p string, perm os.FileMode) error {
			r, err := s.rel(p)
			if err != nil {
				return err
			}
			return s.root.Mkdir(r, perm)
		},
		"mkdirAll": func(p string, perm os.FileMode) error {
			r, err := s.rel(p)
			if err != nil {
				return err
			}
			return s.root.MkdirAll(r, perm)
		},
		"rename": func(from, to string) error {
			rf, err := s.rel(from)
			if err != nil {
				return err
			}
			rto, err := s.rel(to)
			if err != nil {
				return err
			}
			return s.root.Rename(rf, rto)
		},
		"remove": func(p string) error {
			r, err := s.rel(p)
			if err != nil {
				return err
			}
			return s.root.Remove(r)
		},
		"removeAll": func(p string) error {
			r, err := s.rel(p)
			if err != nil {
				return err
			}
			return s.root.RemoveAll(r)
		},
		"openRoot": func(p string) (*os.Root, error) {
			r, err := s.rel(p)
			if err != nil {
				return nil, err
			}
			return s.root.OpenRoot(r)
		},
		"openInRoot": func(dir, name string) (*os.File, error) {
			r, err := s.rel(dir)
			if err != nil {
				return nil, err
			}
			sub, err := s.root.OpenRoot(r)
			if err != nil {
				return nil, err
			}
			defer sub.Close()
			return sub.Open(name)
		},
	})

	// process.env: only Env
	if proc := ns("process"); proc != nil {
		env := map[string]any{}
		for k, v := range s.Env {
			env[k] = v
		}
		_ = proc.Set("env", env)
	}

	// $filepath: pure helpers stay; the ones that read the disk go through the root
	if fp := ns("$filepath"); fp != nil {
		_ = fp.Set("glob", func(pattern string) ([]string, error) {
			r, err := s.fsPath(pattern)
			if err != nil {
				return nil, err
			}
			matches, err := fs.Glob(s.root.FS(), r)
			if err != nil {
				return nil, err
			}
			for i, m := range matches {
				matches[i] = s.abs(m)
			}
			return matches, nil
		})
		_ = fp.Set("walkDir", func(start string, fn fs.WalkDirFunc) error {
			r, err := s.fsPath(start)
			if err != nil {
				return err
			}
			return fs.WalkDir(s.root.FS(), r, func(p string, d fs.DirEntry, err error) error {
				return fn(s.abs(p), d, err)
			})
		})
		_ = fp.Set("walk", func(start string, fn filepath.WalkFunc) error {
			r, err := s.fsPath(start)
			if err != nil {
				return err
			}
			return fs.WalkDir(s.root.FS(), r, func(p string, d fs.DirEntry, err error) error {
				// like filepath.Walk: a file that can't be stat'ed (gone mid-walk) reaches fn with its error, never a nil
				// info and a nil error
				var info fs.FileInfo
				if d != nil {
					var ierr error
					if info, ierr = d.Info(); err == nil {
						err = ierr
					}
				}
				return fn(s.abs(p), info, err)
			})
		})
	}

	// $filesystem
	if fsObj := ns("$filesystem"); fsObj != nil {
		_ = fsObj.Set("local", disabled("$filesystem.local"))
		_ = fsObj.Set("fileFromPath", func(p string) (*filesystem.File, error) {
			b, err := s.readFileCapped(p)
			if err != nil {
				return nil, err
			}
			return filesystem.NewFileFromBytes(b, filepath.Base(p))
		})
	}

	// $apis.static: a string path is resolved inside the app folder
	if apisObj := ns("$apis"); apisObj != nil {
		_ = apisObj.Set("static", func(dirOrFS any, indexFallback bool) (func(*core.RequestEvent) error, error) {
			switch v := dirOrFS.(type) {
			case fs.FS:
				return apis.Static(v, indexFallback), nil
			case string:
				r, err := s.fsPath(v)
				if err != nil {
					return nil, err
				}
				sub, err := fs.Sub(s.root.FS(), r)
				if err != nil {
					return nil, err
				}
				return apis.Static(sub, indexFallback), nil
			default:
				return nil, errors.New("$apis.static expects the first argument to be either a plain string path or fs.FS value")
			}
		})
	}

	// $template: loadFiles reads through the root
	tpl := map[string]any{
		"loadFiles": func(filenames ...string) (*template.Renderer, error) {
			rels := make([]string, len(filenames))
			for i, f := range filenames {
				r, err := s.fsPath(f)
				if err != nil {
					return nil, err
				}
				rels[i] = r
			}
			return templates.LoadFS(s.root.FS(), rels...), nil
		},
		"loadFS":     templates.LoadFS,
		"loadString": templates.LoadString,
	}
	tpl["addFuncs"] = func(funcs map[string]any) map[string]any {
		templates.AddFuncs(funcs)
		return tpl
	}
	_ = rt.Set("$template", tpl)

	// Go-side readers take a JS-supplied limit: clamp it
	max := s.maxAllocation()
	clamp := func(n int) int {
		if n <= 0 || int64(n) > max {
			return int(max)
		}
		return n
	}
	if orig, ok := rt.Get("readerToString").Export().(func(io.Reader, int) (string, error)); ok {
		_ = rt.Set("readerToString", func(r io.Reader, n int) (string, error) { return orig(r, clamp(n)) })
	}
	if orig, ok := rt.Get("toBytes").Export().(func(any, int) ([]byte, error)); ok {
		_ = rt.Set("toBytes", func(raw any, n int) ([]byte, error) { return orig(raw, clamp(n)) })
	}

	// console: the app's own sink instead of the shared process output
	if s.Console != nil {
		sink := s.Console
		format := func(c js.Call) string {
			parts := make([]string, c.Len())
			for i := range parts {
				a := c.Arg(i)
				if o := a.Object(); o != nil && o.Func() == nil && o.Get("stack").Nullish() {
					if b, err := json.Marshal(a.Export()); err == nil {
						parts[i] = string(b)
						continue
					}
				}
				parts[i] = a.String()
			}
			return strings.Join(parts, " ")
		}
		con := map[string]any{}
		for name, stream := range map[string]string{"log": "stdout", "info": "stdout", "debug": "stdout", "warn": "stderr", "error": "stderr"} {
			stream := stream
			con[name] = js.NativeFunc(func(c js.Call) (any, error) {
				sink(stream, format(c))
				return nil, nil
			})
		}
		_ = rt.Set("console", con)
	}
}

// readFileCapped reads a file inside the root, refusing files larger than MaxAllocation.
func (s *Sandbox) readFileCapped(p string) ([]byte, error) {
	r, err := s.rel(p)
	if err != nil {
		return nil, err
	}
	f, err := s.root.Open(r)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	max := s.maxAllocation()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s: file larger than %d bytes", p, max)
	}
	return b, nil
}

// The sandbox hides Go members that let JS reach the process instead of its own app (a js.Hider: the engine shows a
// hidden member as undefined).

// hiddenPackages: every field and method of types from these packages is hidden.
var hiddenPackages = map[string]bool{
	"database/sql/driver": true,
	"modernc.org/sqlite":  true,
	"os/exec":             true,
	"net":                 true, // raw listeners and connections
	"syscall":             true,
}

// hiddenTypes: types (package path + name) whose members are hidden and that no method may return to JS.
// sql.Result and sql.Rows stay usable; the handles that can open or run arbitrary connections do not.
var hiddenTypes = map[string]bool{
	"database/sql.DB":   true,
	"database/sql.Conn": true,
	"database/sql.Tx":   true,
	"database/sql.Stmt": true,
}

func typeKey(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.PkgPath() + "." + t.Name()
}

func isHiddenType(t reflect.Type) bool {
	return hiddenPackages[pkgOf(t)] || hiddenTypes[typeKey(t)]
}

// hiddenMethods: method names hidden on types from the given package.
var hiddenMethods = map[string]map[string]bool{
	"ganigeorgiev.com/pocketbase/pocketbase/core": {
		// process lifecycle: one app must not restart, re-bootstrap or migrate the shared process
		"Restart": true, "RestoreBackup": true, "Bootstrap": true, "ResetBootstrapState": true,
		"RunAllMigrations": true, "RunSystemMigrations": true, "RunAppMigrations": true,
		// process-level hooks and the cron scheduler (JS functions run on other goroutines; use cronAdd). OnServe is
		// not among them: each app has its own, fired once when that app starts serving (Pocker's buildHandler), and
		// hook files bind it to register a superuser UI extension (e.uiExtensions) or routes; the event's Server,
		// Listener and CertManager stay hidden (hiddenFields)
		"OnTerminate": true, "OnBootstrap": true, "Cron": true,
		// hookless copy of the app bypasses every hook, including the platform's
		"UnsafeWithoutHooks": true,
	},
	"ganigeorgiev.com/pocketbase/pocketbase/tools/hook": {
		// hooks are bound only by the onX binds as hook files load; binding, unbinding or firing them from a
		// running handler would add handlers the engine never registered or fire events for code that isn't ready
		"Bind": true, "BindFunc": true, "Unbind": true, "UnbindAll": true, "Trigger": true,
	},
	"os": {
		// *os.Root / *os.File handed out by the sandbox: links and ownership could plant a way out for code that
		// doesn't use the root (PocketBase's storage follows symlinks); Chdir changes the whole process's cwd.
		"Symlink": true, "Link": true, "Chown": true, "Lchown": true, "Chdir": true, "Fd": true,
	},
	"ganigeorgiev.com/pocketbase/dbx": {
		"DB": true, // *sql.DB: a raw connection pool that can open any file
	},
	"net/http": {
		// *http.Server and friends reached from events
		"Shutdown": true, "Close": true, "ListenAndServe": true, "ListenAndServeTLS": true, "Serve": true,
		"ServeTLS": true, "SetKeepAlivesEnabled": true, "RegisterOnShutdown": true,
	},
}

// hiddenFields: struct fields hidden on types from the given package.
var hiddenFields = map[string]map[string]bool{
	"ganigeorgiev.com/pocketbase/pocketbase/core": {
		"Server": true, "CertManager": true, "Listener": true, "InstallerFunc": true,
	},
}

// methodPkgs is the package of t and of every embedded type (recursively) that has the method name.
func methodPkgs(t reflect.Type, name string, depth int) []string {
	out := []string{pkgOf(t)}
	st := t
	for st.Kind() == reflect.Pointer {
		st = st.Elem()
	}
	if st.Kind() != reflect.Struct || depth > 5 {
		return out
	}
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		if !f.Anonymous {
			continue
		}
		ft := f.Type
		if _, ok := ft.MethodByName(name); ok {
			out = append(out, methodPkgs(ft, name, depth+1)...)
		} else if ft.Kind() != reflect.Pointer {
			if _, ok := reflect.PointerTo(ft).MethodByName(name); ok {
				out = append(out, methodPkgs(reflect.PointerTo(ft), name, depth+1)...)
			}
		}
	}
	return out
}

func pkgOf(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.PkgPath()
}

var serveHookType = reflect.TypeOf((*hook.Hook[*core.ServeEvent])(nil))

// serveHook reports whether t is the app's OnServe hook.
func serveHook(t reflect.Type) bool {
	return t == serveHookType || (t.Kind() != reflect.Pointer && reflect.PointerTo(t) == serveHookType)
}

// HideField implements js.Hider.
func (s *Sandbox) HideField(t reflect.Type, f reflect.StructField) bool {
	return isHiddenType(t) || hiddenFields[pkgOf(t)][f.Name] || isHiddenType(f.Type)
}

// HideMethod implements js.Hider. A method promoted from an embedded type (a PocketBase app embeds *core.BaseApp)
// is judged by every type that declares it.
func (s *Sandbox) HideMethod(t reflect.Type, meth reflect.Method) bool {
	if isHiddenType(t) {
		return true
	}
	// the app's own serve hook may be bound: it fires only when this app starts serving, so a handler bound later
	// waits for the next start instead of running for code that isn't ready
	if serveHook(t) && (meth.Name == "Bind" || meth.Name == "BindFunc") {
		return false
	}
	for _, pkg := range methodPkgs(t, meth.Name, 0) {
		if hiddenMethods[pkg][meth.Name] {
			return true
		}
	}
	// any method returning a hidden type (e.g. dbx.DB.DB() *sql.DB)
	for i := 0; i < meth.Type.NumOut(); i++ {
		if isHiddenType(meth.Type.Out(i)) {
			return true
		}
	}
	return false
}
