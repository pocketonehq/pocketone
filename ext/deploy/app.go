package deploy

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	apitokensv1 "pocketone.dev/ext/contracts/api-tokens/v1"
	deployv1 "pocketone.dev/ext/contracts/deploy/v1"
	fsv1 "pocketone.dev/ext/contracts/instance/fs/v1"
	"pocketone.dev/ext/extkit"
	"slices"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
)

// Scopes the server registers with the tokens extension.
const (
	ScopeDeploy = "deploy"      // deploy, upload, delete, read
	ScopeRead   = "deploy:read" // whoami, list and download files, logs
)

// AppInstanceID is the one instance a single app serves.
const AppInstanceID = "app"

// Config configures the server half on a single app.
type Config struct {
	// Dir is the folder deploys write into (pb_hooks, pb_migrations, pb_public, node_modules live there). Default:
	// the parent of the data dir, PocketBase's default layout.
	Dir string

	// StoreDir keeps uploaded blobs and past manifests, a cache the next deploy rebuilds if it's deleted. Default:
	// pb_cache/deploy next to pb_data (never backed up, deployed or reachable through the file API).
	StoreDir string

	// Prefix is the API path (default deploy/v1's, /api/_c/deploy/v1).
	Prefix string

	// Restart restarts the app when a client asks (POST /restart, or a commit with "restart": true) so it applies
	// migrations and packages. Default: app.Restart() shortly after the response is sent (re-executes the process;
	// not on Windows).
	Restart func(app core.App) error

	// DisableServer leaves only the client commands (e.g. a binary that deploys elsewhere but must not accept
	// deploys).
	DisableServer bool
}

// MustRegister is [Register] that panics on error.
func MustRegister(app core.App, cfg Config) *API {
	a, err := Register(app, cfg)
	if err != nil {
		panic(err)
	}
	return a
}

// Register adds the deploy API to a single app (its own files, auth by API tokens with the deploy scope or a
// superuser JWT, history in _deploy_deploys, logs from the app's _logs) and, on a *pocketbase.PocketBase, the client
// commands deploy, dev, pull, logs and restart. It returns nil API when cfg.DisableServer.
func Register(app core.App, cfg Config) (*API, error) {
	if pb, ok := app.(*pocketbase.PocketBase); ok {
		for _, c := range Commands() {
			pb.RootCmd.AddCommand(c)
		}
	}
	if cfg.DisableServer {
		return nil, nil
	}
	cfg.Prefix = deployv1.Prefix
	if cfg.Restart == nil {
		cfg.Restart = func(app core.App) error {
			go func() {
				time.Sleep(time.Second) // let the commit response reach the client
				if err := app.Restart(); err != nil {
					app.Logger().Error("deploy: restart", "error", err)
				}
			}()
			return nil
		}
	}
	// instance.fs/v1: the app's own folder, as the default (a host provides it for its instances instead)
	fsv1.Contract.ProvideDefault(app, AppFiles(app, &cfg))
	api := New(Options{
		Prefix:  cfg.Prefix,
		Backend: &AppBackend{App: app},
		Files: func() fsv1.Files { return fsv1.Contract.Must(app) },
		Logger: slog.New(slogHandler{app}),
	})
	// deploy/v1: the contracts' seal mounts its routes under /api/_c/deploy/v1/ (a multisite host provides its own)
	deployv1.Contract.ProvideDefault(app, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var auth requestAuth
		if e := extkit.RequestEvent(r); e != nil {
			auth = requestAuth{record: e.Auth, token: apitokensv1.FromRequest(e)}
		}
		api.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestAuthKey{}, auth)))
	}))

	app.OnBootstrap().BindFunc(func(e *core.BootstrapEvent) error {
		if err := e.Next(); err != nil {
			return err
		}
		if cfg.Dir == "" {
			cfg.Dir = filepath.Dir(e.App.DataDir())
		}
		if cfg.StoreDir == "" {
			cfg.StoreDir = filepath.Join(extkit.CacheRoot(e.App), Name)
		}
		if t, ok := apitokensv1.Contract.Of(e.App); ok {
			t.RegisterScope(apitokensv1.Scope{Name: ScopeDeploy, Description: "Deploy code (hooks, migrations, public files, packages) and read files",
				Paths: []string{cfg.Prefix + "/"}, Collections: []string{core.CollectionNameSuperusers}})
			t.RegisterScope(apitokensv1.Scope{Name: ScopeRead, Description: "Read deployed files",
				Paths: []string{cfg.Prefix + "/"}, Collections: []string{core.CollectionNameSuperusers}})
		}
		return nil
	})

	return api, nil
}

type requestAuthKey struct{}

type requestAuth struct {
	record *core.Record
	token  *apitokensv1.Grant
}

// AppBackend is the default backend on a single app: one instance (the app), superusers only. A superuser JWT
// (including an impersonate token) has full access; an API token needs the deploy scope (or none), and
// deploy:read gives read-only access.
type AppBackend struct {
	App core.App
}

// Lookup implements [Backend]: every request is for the app.
func (b *AppBackend) Lookup(*http.Request) (Instance, bool) {
	return Instance{ID: AppInstanceID, Subdomain: b.App.Settings().Meta.AppName, Power: true}, true
}

// Authenticate implements [Backend] from the auth PocketBase resolved for the request.
func (b *AppBackend) Authenticate(r *http.Request, _ Instance) (Auth, error) {
	ra, _ := r.Context().Value(requestAuthKey{}).(requestAuth)
	if ra.record == nil {
		return Auth{}, &AuthError{401, "Sign in first: " + extkit.Command + " login <this URL>, or send a superuser API token."}
	}
	if !ra.record.IsSuperuser() {
		return Auth{}, &AuthError{403, "Only superusers can use the deploy API."}
	}
	if ra.token == nil {
		return Auth{UserID: ra.record.Id, Scopes: ReadWrite, Via: "jwt"}, nil
	}
	a := Auth{UserID: ra.record.Id, Via: "token", TokenLabel: ra.token.Name}
	switch {
	case len(ra.token.Scopes) == 0 || slices.Contains(ra.token.Scopes, ScopeDeploy):
		a.Scopes = ReadWrite
	case slices.Contains(ra.token.Scopes, ScopeRead):
		a.Scopes = []string{"read"}
	default:
		return Auth{}, &AuthError{403, "This API token has no deploy scope."}
	}
	return a, nil
}

// AppFiles is a single app's instance.fs/v1: its own folder (cfg.Dir, set at bootstrap), deploy's cache in
// cfg.StoreDir and other extensions' in the app's pb_cache. It can't stop itself, so a restart applies the change while it runs (each rename is atomic) and then
// restarts it (cfg.Restart).
func AppFiles(app core.App, cfg *Config) fsv1.Files {
	l := &fsv1.Local{
		Folder: func() string { return cfg.Dir },
		Cache: func(name string) string {
			if name == Name && cfg.StoreDir != "" {
				return cfg.StoreDir
			}
			return filepath.Join(extkit.CacheRoot(app), name)
		},
		Start: func(context.Context) error { return cfg.Restart(app) },
	}
	return appFiles{l}
}

type appFiles struct{ fs *fsv1.Local }

func (f appFiles) Instance(_ context.Context, id string) (fsv1.FS, error) {
	if id != fsv1.Self {
		return nil, fsv1.ErrNoInstance
	}
	return f.fs, nil
}

type slogHandler struct{ app core.App }

func (h slogHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.app.Logger().Handler().Enabled(ctx, l)
}
func (h slogHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.app.Logger().Handler().Handle(ctx, r)
}
func (h slogHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return h.app.Logger().Handler().WithAttrs(a)
}
func (h slogHandler) WithGroup(n string) slog.Handler { return h.app.Logger().Handler().WithGroup(n) }
