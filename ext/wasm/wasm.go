// Package wasm runs untrusted WebAssembly modules inside a PocketBase app. A module is a pure function:
// input in, output out, or an error. It never touches the database, the files or the network; the app's pb_hooks give
// it what it needs and decide what to do with the result. pb_hooks ($wasm) are the only way to call a module: no
// Go API, no endpoint, no form.
//
//	onRecordCreate(async (e) => {
//	  const out = await $wasm.run.async("summary", { title: e.record.get("title"), body: e.record.get("body") })
//	  e.record.set("summary", out.text)
//	  return e.next()
//	}, "posts")
//
// A module is a plain WASI program (wasip1: Go, Rust, Zig, C, ...): it reads its input from stdin, writes its output
// to stdout, and exits non-zero when it fails, the way a command-line tool would; the function asked for is its
// first argument. It runs in wazero, in the PocketBase process, with no files, network, environment or processes, a
// memory cap and a time limit, in a fresh instance for every call. The module has no way out: whatever it needs
// comes in as input, and the network stays with the app's code ($http.send in pb_hooks).
//
// A module is one file beside the app's JS hooks: pb_hooks/<name>.pb.wasm, called as <name>. It says what it is
// itself, when run as `<name> --describe` (see [Manifest]). It ships with the rest of the app's code (git,
// `pocketone deploy`, SFTP). Like `node script.js`, every call reads the module's file as it is then, and nothing
// stays in memory between calls. (A change in pb_hooks also restarts the app, as for a .pb.js; under pocker the new
// app takes over once it has started.) To turn a module off, remove or rename it. Copy a new version in under another
// name and rename it over the old one, so no call reads it half written (files starting with "." are left alone).
//
// A standalone app:
//
//	wasm.MustRegister(app, wasm.Config{})
//
// or [New] as an [extkit.Extension], added to the standard set (see [Extension]).
package wasm

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/hook"
	"github.com/tetratelabs/wazero"
)

// Name is the extension's name: its key under an instance's `extensions:` block.
const Name = "wasm"

const storeKey = "ext/wasm"

// Config is the extension's settings. Zero values mean the defaults.
type Config struct {
	// Dir is the module folder (default pb_hooks beside the app's data dir, with the JS hooks).
	Dir string

	// MaxCallMemoryMB caps what each call's program may allocate (default 32): its heap, stack and input. Every call
	// has its own, not shared: 8 calls at once may take 8 times that. A call that needs more crashes alone, and the
	// app goes on.
	MaxCallMemoryMB int

	// MaxConcurrent is how many module calls run at once in the app (default 8); more wait, up to Timeout.
	MaxConcurrent int

	// MaxModulesMB is how many MB of modules run at once in the app (default 10): the .wasm sizes of the
	// calls running, added up. Compiling takes time and memory in step with a module's size, so with 10, two calls of
	// a Go module (~4.4 MB) run at once, and MaxConcurrent calls of a Rust or Zig one (~0.1 MB). More wait, up to
	// Timeout. It's also the largest module that runs.
	MaxModulesMB int

	// Timeout is the time limit of each module call (default 5s).
	Timeout time.Duration

	// MaxPayload is the largest input or output of a call, in bytes (default 4 MB).
	MaxPayload int

	// CacheDir is where compiled modules are kept between calls and restarts (default the app's pb_cache/wasm, which
	// a multisite host such as Pocker keeps outside the instance's folder). A module's first call compiles it; later calls load
	// the machine code from there, 20 to 30 times faster. Every app and instance naming the same folder shares it,
	// keyed by each module's bytes. "off" compiles every call. The process runs what's in this folder as machine
	// code, so keep it where only the process writes: never in a folder an app's users or tenants can write to
	// (pb_data, pb_hooks). Only the host sets it; instances can't.
	CacheDir string
}

func (c Config) withDefaults() Config {
	orDefault(&c.MaxCallMemoryMB, 32)
	orDefault(&c.MaxConcurrent, 8)
	orDefault(&c.MaxModulesMB, 10)
	orDefault(&c.Timeout, 5*time.Second)
	orDefault(&c.MaxPayload, 4<<20)
	return c
}

// orDefault sets *v to def when it's zero or negative.
func orDefault[T int | time.Duration](v *T, def T) {
	if *v <= 0 {
		*v = def
	}
}

// MustRegister is [Register] that panics.
func MustRegister(app core.App, cfg Config) *Host {
	h, err := Register(app, cfg)
	if err != nil {
		panic(err)
	}
	return h
}

// Host runs one app's modules.
type Host struct {
	app      core.App
	cfg      Config
	cache    wazero.CompilationCache // nil: every call compiles
	cacheErr error                   // why there's no cache, logged when the app serves

	mu      sync.RWMutex
	serving bool
	checked map[string]checked // what checking each module's file found, while it stays the same

	slotMu  sync.Mutex
	running int           // calls running, at most MaxConcurrent
	bytes   int           // their modules' sizes added up, at most MaxModulesMB
	freed   chan struct{} // closed (and replaced) whenever a call ends, for the calls waiting for room
}

// Info is how one module in the folder is doing ($wasm.modules() in pb_hooks).
type Info struct {
	Name        string   `json:"name"`
	Running     bool     `json:"running"`
	Description string   `json:"description,omitempty"`
	Version     string   `json:"version,omitempty"` // as its manifest (--describe) says
	Functions   []string `json:"functions,omitempty"`
	// Language is what it's written in: what the module itself says (Go always does, Rust unless stripped), else its
	// manifest's language.
	Language   string    `json:"language,omitempty"`
	Repository string    `json:"repository,omitempty"` // where its source is, as its manifest says
	Examples   []Example `json:"examples,omitempty"`
	Size       int       `json:"size,omitempty"` // its module's size in bytes: what a call of it takes of MaxModulesMB
	// Error is why it can't run (Running false), or, when it runs, what's wrong with what it printed for --describe.
	Error string `json:"error,omitempty"`
}

// Limits is what every module call in the app runs within ($wasm.limits() in pb_hooks).
type Limits struct {
	MaxCallMemoryMB int   `json:"maxCallMemoryMB"`
	TimeoutMs       int64 `json:"timeoutMs"`
	MaxConcurrent   int   `json:"maxConcurrent"`
	MaxModulesMB    int   `json:"maxModulesMB"`
	MaxPayload      int   `json:"maxPayload"`
}

// Limits is what every module call in the app runs within.
func (h *Host) Limits() Limits {
	c := h.cfg
	return Limits{MaxCallMemoryMB: c.MaxCallMemoryMB, TimeoutMs: c.Timeout.Milliseconds(), MaxConcurrent: c.MaxConcurrent,
		MaxModulesMB: c.MaxModulesMB, MaxPayload: c.MaxPayload}
}

// For returns the app's Host, or nil.
func For(app core.App) *Host {
	h, _ := app.Store().Get(storeKey).(*Host)
	return h
}

// Register sets ext/wasm up on app. Modules run only while the app serves; each call reads its module's file from the
// module folder. Nothing watches the folder, and nothing runs between calls.
func Register(app core.App, cfg Config) (*Host, error) {
	cfg = cfg.withDefaults()
	if cfg.Dir == "" {
		cfg.Dir = filepath.Join(app.DataDir(), "..", "pb_hooks")
	}
	dir, err := filepath.Abs(cfg.Dir)
	if err != nil {
		return nil, err
	}
	cfg.Dir = dir
	h := &Host{app: app, cfg: cfg, checked: map[string]checked{}, freed: make(chan struct{})}
	if cfg.CacheDir == "" {
		if d, err := defaultCacheDir(app); err != nil {
			h.cacheErr = fmt.Errorf("no cache folder (%w): set CacheDir", err)
		} else {
			h.cfg.CacheDir = d
		}
	}
	if h.cfg.CacheDir != "" && h.cfg.CacheDir != "off" {
		h.cache, h.cacheErr = cacheFor(h.cfg.CacheDir)
	}
	app.Store().Set(storeKey, h)
	addMCP(app, h)

	// modules only run while serving, so other commands (migrate, superuser, ...) never compile them
	app.OnServe().Bind(&hook.Handler[*core.ServeEvent]{
		Id: "ext-wasm-serve",
		Func: func(e *core.ServeEvent) error {
			if err := e.Next(); err != nil {
				return err
			}
			h.mu.Lock()
			h.serving = true
			h.mu.Unlock()
			if h.cacheErr != nil {
				h.app.Logger().Warn("wasm: no compilation cache, every call compiles its module", "dir", h.cfg.CacheDir, "error", h.cacheErr)
			}
			h.Modules() // checks every module once, so the log says at start which ones can't run
			return nil
		},
		Priority: -1000,
	})
	app.OnTerminate().BindFunc(func(e *core.TerminateEvent) error {
		h.mu.Lock()
		h.serving = false
		h.checked = map[string]checked{}
		h.mu.Unlock()
		return e.Next()
	})
	return h, nil
}

// Dir is the module folder.
func (h *Host) Dir() string { return h.cfg.Dir }

// Modules is every module in the folder now, by name, and whether it can run (or why not).
func (h *Host) Modules() []Info {
	names, err := h.list()
	if err != nil {
		h.app.Logger().Warn("wasm: can't read the module folder", "dir", h.cfg.Dir, "error", err)
		return nil
	}
	infos := make([]Info, 0, len(names))
	for name := range names {
		infos = append(infos, h.check(name))
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}

// checked is what checking a module's file found, kept while it stays the same (the same size and modification
// time), so listing the folder doesn't check every module again. It holds no code: a call reads the file itself.
type checked struct {
	stamp string
	info  Info
}

// check is what the module's file says now: whether it's a module that can run, and its manifest (checkCode).
func (h *Host) check(name string) Info {
	stamp := h.stamp(name)
	h.mu.RLock()
	c, ok := h.checked[name]
	h.mu.RUnlock()
	if ok && c.stamp == stamp {
		return c.info
	}
	info := Info{Name: name}
	if c, err := h.checkFile(name); err != nil {
		info.Error = err.Error()
		h.app.Logger().Error("wasm: module can't run", "module", name, "error", err)
	} else {
		m := c.manifest
		info = Info{Name: name, Running: true, Description: m.Description, Version: m.Version,
			Functions: m.functions(), Language: cmp.Or(c.language, m.Language), Repository: m.Repository,
			Examples: m.Examples, Size: c.size}
		if c.describeErr != nil {
			info.Error = describeFlag + ": " + c.describeErr.Error()
			h.app.Logger().Warn("wasm: module's --describe isn't a manifest; it runs with the defaults", "module", name,
				"error", c.describeErr)
		}
		h.app.Logger().Info("wasm: module checked", "module", name, "functions", m.functions())
	}
	h.mu.Lock()
	h.checked[name] = checked{stamp: stamp, info: info}
	h.mu.Unlock()
	return info
}

// stamp is the size and modification time of the module's file, to tell when it changes.
func (h *Host) stamp(name string) string {
	if fi, err := os.Stat(h.path(name)); err == nil {
		return fmt.Sprintf("%d@%d", fi.Size(), fi.ModTime().UnixNano())
	}
	return "-"
}

// fileSuffix is what a module's file name ends with: <name>.pb.wasm, beside the JS hooks' <name>.pb.js.
const fileSuffix = ".pb.wasm"

// path is the module called name's file.
func (h *Host) path(name string) string { return filepath.Join(h.cfg.Dir, name+fileSuffix) }

// list is the names of the modules in the folder: its <name>.pb.wasm files. Others (the JS hooks, hidden files, an
// upload's temporary file) are left alone; a .pb.wasm whose name can't be called is reported.
func (h *Host) list() (map[string]bool, error) {
	files, err := os.ReadDir(h.cfg.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, f := range files {
		name, ok := strings.CutSuffix(f.Name(), fileSuffix)
		if !ok || f.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		if !validName.MatchString(name) {
			h.app.Logger().Warn("wasm: not a module name (letters, digits, - and _)", "file", f.Name())
			continue
		}
		names[name] = true
	}
	return names, nil
}

// fileCheck is what checkFile found in a module's file that can run.
type fileCheck struct {
	size        int
	language    string // what the module says it's written in, or ""
	manifest    Manifest
	describeErr error // what's wrong with what it printed for --describe
}

// checkFile reads the module's file and checks it, for Modules: its code validated, and its manifest.
func (h *Host) checkFile(name string) (fileCheck, error) {
	m, err := h.open(name)
	if err != nil {
		return fileCheck{}, err
	}
	code, err := m.read()
	if err != nil {
		return fileCheck{}, err
	}
	c := fileCheck{size: len(code)}
	c.language, c.manifest, c.describeErr, err = checkCode(code, name, h.cfg.MaxCallMemoryMB, h.cfg.Timeout)
	if err != nil {
		return fileCheck{}, fmt.Errorf("module: %w", err)
	}
	return c, nil
}

// open is the module called name as its file is now: its size (the code itself is read later, by read,
// once a call has room). The error says why it can't run; fs.ErrNotExist when there's no <name>.pb.wasm.
func (h *Host) open(name string) (*module, error) {
	if !validName.MatchString(name) {
		return nil, fs.ErrNotExist
	}
	fi, err := os.Stat(h.path(name))
	if err == nil && fi.IsDir() {
		err = fs.ErrNotExist
	}
	if err != nil {
		return nil, err
	}
	if max := int64(h.cfg.MaxModulesMB) << 20; fi.Size() > max {
		return nil, fmt.Errorf("module of %s, over MaxModulesMB (%d MB)", size(int(fi.Size())), h.cfg.MaxModulesMB)
	}
	return &module{host: h, name: name, size: int(fi.Size())}, nil
}

// read reads the module's file, which may have changed since open: still within MaxModulesMB.
func (m *module) read() ([]byte, error) {
	code, err := os.ReadFile(m.host.path(m.name))
	if err != nil {
		return nil, err
	}
	if max := m.host.cfg.MaxModulesMB << 20; len(code) > max {
		return nil, fmt.Errorf("module of %s, over MaxModulesMB (%d MB)", size(len(code)), m.host.cfg.MaxModulesMB)
	}
	return code, nil
}
