package pocker

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"ganigeorgiev.com/pocketbase/dbx"
	"ganigeorgiev.com/pocketbase/pocketbase/apis"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	_ "ganigeorgiev.com/pocketbase/pocketbase/migrations" // registers PocketBase's system migrations, as pocketbase.go does
	"ganigeorgiev.com/pocketbase/pocketbase/plugins/migratecmd"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/hook"
	"ganigeorgiev.com/pocketbase/pocketbase/tools/security"
	"ganigeorgiev.com/pocketbase/pocketbase/ui"
	"pocketone.dev/ext/extkit"
	"pocketone.dev/ext/extkit/js"
	"pocketone.dev/ext/jsvm"
	"pocketone.dev/ext/sandbox"
)

// InstanceHeader names the instance on every message it sends (the dev mail trap and relays show and filter by it).
const InstanceHeader = "X-Pocker-Instance"

// runningApp is a bootstrapped PocketBase app and the HTTP handler that serves it.
type runningApp struct {
	app     *core.BaseApp
	handler http.Handler
	started time.Time
}

// appOptions are the process-wide settings applied to every app.
type appOptions struct {
	PBRoot        string
	HooksPoolSize int
	ForceDev      bool // Pocker dev mode: every app runs in PocketBase dev mode
	// Defaults are the process-wide limits; an instance's own Limits override them.
	Defaults Limits
	// Init, if set, runs for each app right after it is created, before its hooks load and before it bootstraps
	// (opens its databases): the place to bind OnBootstrap/OnTerminate handlers.
	Init func(inst Instance, app core.App) error
	// InitJS, if set, adds globals to each JS runtime after the sandbox set it up.
	InitJS func(inst Instance, app core.App, rt js.Runtime)
	// Setup, if set, runs for each app after migrations and before it serves (host application Go code).
	Setup func(inst Instance, app core.App) error
	// Env, if set, adds host values to each app's environment ($os.getenv, process.env), over the instance's own
	// secrets: a secret can't override them.
	Env func(inst Instance) map[string]string
	// IsolateConfig is every instance's base config, one section per extension, under its own extensions.<name>.
	IsolateConfig map[string]map[string]any
	// Standard, if set, is registered on every app (ext/standard's Set): before its hooks load, with the instance's
	// `extensions.<name>` settings; its JS APIs bound on every runtime; its contracts sealed after the hooks load.
	Standard extkit.AppSet
	// Host is the host's contract scope: contracts the host provides for its instances (remote/v1, deploy/v1) step
	// aside inside each instance's app.
	Host *extkit.Scope
}

func instanceDir(opts appOptions, inst Instance) string {
	if inst.Dir != "" {
		return inst.Dir
	}
	return filepath.Join(opts.PBRoot, inst.ID)
}

// startApp creates, bootstraps and migrates the instance's PocketBase app and builds its router, the same way
// `pocketbase serve` does, but without opening a listener: Pocker's own server dispatches to the returned handler.
func startApp(opts appOptions, inst Instance) (r *runningApp, err error) {
	// PocketBase's JS plugin panics on a broken hooks file; one instance's bad code must be an error, not a crash
	defer func() {
		if p := recover(); p != nil {
			r, err = nil, fmt.Errorf("%v", p)
		}
	}()

	dir, err := filepath.Abs(instanceDir(opts, inst))
	if err != nil {
		return nil, err
	}
	for _, sub := range []string{"pb_data", "pb_hooks", "pb_migrations", "pb_public"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}

	lim := inst.Limits.Over(opts.Defaults)
	dev := inst.Dev || opts.ForceDev

	app := core.NewBaseApp(core.BaseAppConfig{
		DBConnect:     sandbox.GuardedDBConnect(lim.MaxQueryDuration), // SQL stays inside this app's files
		DataDir:       filepath.Join(dir, "pb_data"),
		IsDev:         dev,
		EncryptionEnv: encryptionEnv(inst),
		AppMigrations: &core.MigrationsList{}, // this app's JS migrations only
		OnLog:         appLogToInstanceLog(openInstanceLog(dir)),
	})
	// every message names its instance (the dev mail trap and relays show it); first, so later hooks see it
	app.OnMailerSend().Bind(&hook.Handler[*core.MailerEvent]{
		Id:       "__pockerInstanceHeader__",
		Priority: -1 << 30,
		Func: func(e *core.MailerEvent) error {
			if e.Message.Headers == nil {
				e.Message.Headers = map[string]string{}
			}
			e.Message.Headers[InstanceHeader] = inst.ID
			return e.Next()
		},
	})
	sandbox.BindMail(app) // the app's own SMTP goes through the egress policy
	if opts.Init != nil {
		if err := opts.Init(inst, app); err != nil {
			return nil, fmt.Errorf("init: %w", err)
		}
	}

	env := make(map[string]string, len(inst.Secrets))
	for k, v := range inst.Secrets {
		env[k] = v
	}
	if opts.Env != nil {
		for k, v := range opts.Env(inst) {
			env[k] = v
		}
	}

	sb := &sandbox.Sandbox{
		Dir:                dir,
		Env:                env,
		MaxExecution:       lim.MaxJSExecution,
		MaxAllocation:      int64(lim.MaxJSAllocation),
		MaxSleep:           lim.MaxSleep,
		HTTPSendMaxTimeout: int(lim.HTTPSendMaxTimeout / time.Second),
		HTTPSendMaxBody:    int64(lim.HTTPSendMaxBody),
		Console:            openInstanceLog(dir).Write,
	}
	extkit.SetAppID(app, inst.ID) // before anything registers: the standard set keeps per-instance state under it
	if opts.Host != nil {
		extkit.AttachHost(app, opts.Host, inst.ID)
	}
	// every instance gets the standard extensions; there's no way to make one without them
	if opts.Standard == nil {
		return nil, errNoStandard
	}
	// the instance's own settings for an extension are what its owner typed: one that isn't a map stops the start,
	// rather than running the extension on pocker's defaults without saying so
	var settingsErr error
	plan, err := opts.Standard.Prepare(app, func(name string) extkit.Config {
		own, err := extkit.Layer(inst.ExtensionConfig(name))
		if err != nil && settingsErr == nil {
			settingsErr = fmt.Errorf("instance settings for %s: %w", name, err)
		}
		return extkit.Config{opts.IsolateConfig[name], own} // pocker's isolate_config, then the instance's own
	})
	if err == nil {
		err = settingsErr
	}
	if err != nil {
		return nil, fmt.Errorf("standard: %w", err)
	}
	jsExt := append(hookExtensions(sb, dev, openInstanceLog(dir)), opts.Standard.JSExtensions(app)...)
	execLimit, sleepLimit := sb.Limits()
	err = jsvm.Register(app, jsvm.Config{
		HooksDir:      filepath.Join(dir, "pb_hooks"),
		MigrationsDir: filepath.Join(dir, "pb_migrations"),
		HooksWatch:    false, // PocketBase's watcher would open an inotify instance per app; the Manager's shared watcher restarts just this app
		// the sandbox (OnInit, Modules, HTTPClient, hidden members)
		Extensions: jsExt,
		// one JS VM per instance, with hard limits: memory (JS heap plus Go objects JS holds), wall time per call
		VM: jsvm.VMConfig{TaskWall: execLimit, MaxSleep: sleepLimit, MaxConcurrent: lim.MaxConcurrentJS},
		OnInit: func(rt js.Runtime) {
			opts.Standard.BindJS(app, rt)
			if opts.InitJS != nil {
				opts.InitJS(inst, app, rt)
			}
		},
	})
	if err != nil {
		return nil, fmt.Errorf("load hooks: %w", err)
	}
	// automigrate, as a PocketHost docker instance and a single pocketone app have it: a collection changed over the
	// API writes its JS migration into pb_migrations (not watched, so no restart). No command tree: no CLI here.
	if err := migratecmd.Register(app, nil, migratecmd.Config{
		TemplateLang: migratecmd.TemplateLangJS,
		Automigrate:  true,
		Dir:          filepath.Join(dir, "pb_migrations"),
	}); err != nil {
		return nil, fmt.Errorf("automigrate: %w", err)
	}
	lines, err := opts.Standard.Finish(app, plan)
	if err != nil {
		return nil, fmt.Errorf("standard: %w", err)
	}
	for _, l := range lines { // the instance's own log, where its owner reads it
		openInstanceLog(dir).Write("stdout", l)
	}

	if err := app.Bootstrap(); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	if err := app.RunAllMigrations(); err != nil {
		return nil, fmt.Errorf("migrations: %w", err)
	}
	if err := syncSuperusers(app, inst.Superusers); err != nil {
		return nil, fmt.Errorf("superusers: %w", err)
	}
	if opts.Setup != nil {
		if err := opts.Setup(inst, app); err != nil {
			return nil, fmt.Errorf("setup: %w", err)
		}
	}

	handler, err := buildHandler(app, filepath.Join(dir, "pb_public"), inst.Access.CORSOrigins)
	if err != nil {
		return nil, err
	}
	return &runningApp{app: app, handler: handler, started: time.Now()}, nil
}

// buildHandler mirrors apis.Serve: router, CORS, dashboard, OnServe hooks (customer routes, pb_public) and the
// final mux. The OnServe event gets a listener that is never served, so no port is opened per app.
func buildHandler(app *core.BaseApp, publicDir string, corsOrigins []string) (http.Handler, error) {
	router, err := apis.NewRouter(app)
	if err != nil {
		return nil, err
	}

	if len(corsOrigins) == 0 {
		corsOrigins = []string{"*"}
	}
	router.Bind(apis.CORS(apis.CORSConfig{
		AllowOrigins: corsOrigins,
		AllowMethods: []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodPatch, http.MethodPost, http.MethodDelete},
	}))

	if ui.DistDirFS != nil {
		router.GET("/_/{path...}", apis.Static(ui.DistDirFS, false)).Bind(apis.Gzip())
	}

	server := &http.Server{}
	event := &core.ServeEvent{
		App:      app,
		Router:   router,
		Server:   server,
		Listener: idleListener{},
	}

	var handler http.Handler
	err = app.OnServe().Trigger(event, func(e *core.ServeEvent) error {
		// like `pocketbase serve`: pb_public is served unless a hook already took the catch-all route
		if !e.Router.HasRoute(http.MethodGet, "/{path...}") {
			e.Router.GET("/{path...}", apis.Static(os.DirFS(publicDir), true))
		}
		mux, err := e.Router.BuildMux()
		if err != nil {
			return err
		}
		handler = mux
		return nil
	})
	if err != nil {
		return nil, err
	}
	if handler == nil {
		return nil, errors.New("OnServe did not complete; a hook may not have called e.next()")
	}
	return handler, nil
}

// syncSuperusers upserts the provider's superusers (id, email, token key, password hash) into _superusers.
func syncSuperusers(app core.App, users []Superuser) error {
	for _, u := range users {
		if u.Email == "" || u.PasswordHash == "" {
			continue
		}
		id := u.ID
		if id == "" {
			id = "pocker" + security.RandomStringWithAlphabet(10, "abcdefghijklmnopqrstuvwxyz0123456789")
			if existing, err := app.FindAuthRecordByEmail(core.CollectionNameSuperusers, u.Email); err == nil {
				id = existing.Id
			}
		}
		tokenKey := u.TokenKey
		if tokenKey == "" {
			tokenKey = security.RandomString(50)
		}
		_, err := app.DB().NewQuery(
			"INSERT OR REPLACE INTO {{_superusers}} ([[id]], [[email]], [[tokenKey]], [[password]]) VALUES ({:id}, {:email}, {:tokenKey}, {:password})",
		).Bind(dbx.Params{"id": id, "email": u.Email, "tokenKey": tokenKey, "password": u.PasswordHash}).Execute()
		if err != nil {
			return fmt.Errorf("%s: %w", u.Email, err)
		}
	}
	return nil
}

// stopApp runs the app's terminate hooks and closes its databases.
func stopApp(r *runningApp) {
	_ = r.app.OnTerminate().Trigger(&core.TerminateEvent{App: r.app}, func(e *core.TerminateEvent) error {
		return e.App.ResetBootstrapState()
	})
}

// idleListener satisfies core.ServeEvent.Listener; Pocker never accepts on it.
type idleListener struct{}

func (idleListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (idleListener) Close() error              { return nil }
func (idleListener) Addr() net.Addr            { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

// encryptionEnv puts the instance's settings encryption key in a process environment variable of its own and returns
// its name, for upstream's per-app BaseAppConfig.EncryptionEnv; "" (no encryption) without a key. JS never sees it:
// the sandbox gives JS only the instance's own Env. The variable stays set until the process exits (a restart of the
// same instance reuses it).
func encryptionEnv(inst Instance) string {
	if inst.EncryptionKey == "" {
		return ""
	}
	name := "POCKER_ENC_" + hex.EncodeToString([]byte(inst.ID))
	_ = os.Setenv(name, inst.EncryptionKey)
	return name
}
