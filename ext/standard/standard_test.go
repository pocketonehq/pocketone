package standard_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ganigeorgiev.com/pocketbase/pocketbase"
	"ganigeorgiev.com/pocketbase/pocketbase/apis"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	_ "ganigeorgiev.com/pocketbase/pocketbase/migrations"
	"pocketone.dev/ext/extkit"
	"pocketone.dev/ext/jsvm"
	"pocketone.dev/ext/standard"
)

// newApp is a fresh single app the way the stock binary builds one: the set, then pb_hooks, then the seal, then
// bootstrap. It returns the app, its startup lines and its HTTP handler.
func newApp(t *testing.T, set *standard.Set, hooks map[string]string) (*pocketbase.PocketBase, http.Handler) {
	t.Helper()
	dir := t.TempDir()
	hooksDir := filepath.Join(dir, "pb_hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, src := range hooks {
		if err := os.WriteFile(filepath.Join(hooksDir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: filepath.Join(dir, "pb_data"), HideStartBanner: true})
	if err := set.Register(app, jsvm.Config{HooksDir: hooksDir, HooksPoolSize: 1}); err != nil {
		t.Fatal(err)
	}
	if err := app.Bootstrap(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = app.OnTerminate().Trigger(&core.TerminateEvent{App: app}, func(e *core.TerminateEvent) error {
			return e.App.ResetBootstrapState()
		})
	})
	r, err := apis.NewRouter(app)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.OnServe().Trigger(&core.ServeEvent{App: app, Router: r}, func(*core.ServeEvent) error { return nil }); err != nil {
		t.Fatal(err)
	}
	mux, err := r.BuildMux()
	if err != nil {
		t.Fatal(err)
	}
	return app, mux
}

func get(t *testing.T, h http.Handler, path string) (int, string, http.Header) {
	return do(t, h, http.MethodGet, path)
}

func do(t *testing.T, h http.Handler, method, path string) (int, string, http.Header) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)))
	b, _ := io.ReadAll(rec.Body)
	return rec.Code, string(b), rec.Header()
}

func TestEveryAppHasTheStandardSet(t *testing.T) {
	hooks := map[string]string{"probe.pb.js": `$app.store().set("js", typeof $mcp + "," + typeof $apiTokens + "," + typeof $bulkmail)`}
	app, h := newApp(t, standard.New(), hooks)

	if got := app.Store().Get("js"); got != "object,object,object" {
		t.Fatalf("pb_hooks globals: %v", got)
	}
	code, _, hdr := do(t, h, http.MethodPost, "/mcp")
	if code != http.StatusUnauthorized || !strings.Contains(hdr.Get("WWW-Authenticate"), "resource_metadata") {
		t.Fatalf("/mcp: %d %q", code, hdr.Get("WWW-Authenticate"))
	}
	if code, body, _ := get(t, h, "/.well-known/oauth-authorization-server"); code != 200 || !strings.Contains(body, "authorization_endpoint") {
		t.Fatalf("oauth metadata: %d %s", code, body)
	}
}

func TestStartupLines(t *testing.T) {
	set := standard.New()
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir(), HideStartBanner: true})
	plan, err := set.Prepare(app, nil)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := set.Finish(app, plan)
	if err != nil {
		t.Fatal(err)
	}
	// on, in dependency order: api-tokens keeps its OAuth key in kv/v1, so it comes after kv
	on := lines[0]
	if !strings.HasPrefix(on, "standard: on: settings, templates, cache, ") || !strings.Contains(on, "deploy") ||
		!strings.Contains(on, ", kv, ") || strings.Index(on, " api-tokens,") < strings.Index(on, " kv,") {
		t.Fatalf("lines %q", lines)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "api-tokens/v1 ← api-tokens") {
		t.Fatalf("lines %q", lines)
	}
}

func TestRemotesOffTurnsDeployOff(t *testing.T) {
	set := standard.New()
	app := pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir(), HideStartBanner: true})
	layers := func(name string) extkit.Config {
		if name == "remotes" {
			return extkit.Config{{"enabled": false}}
		}
		return nil
	}
	plan, err := set.Prepare(app, layers)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := set.Finish(app, plan)
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(lines, "\n")
	for _, want := range []string{"remotes off: enabled: false", "deploy off: needs remote/v1, which nothing provides"} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q in\n%s", want, all)
		}
	}
	if strings.Contains(lines[0], "deploy") || !strings.Contains(lines[0], "mcp") {
		t.Fatalf("on: %s", lines[0])
	}
}

func TestMembersCantBeRemoved(t *testing.T) {
	set := standard.New()
	set.PITR = nil
	_, err := set.Prepare(pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir()}), nil)
	if err == nil || !strings.Contains(err.Error(), "Set.PITR is nil; members can be reconfigured, not removed") {
		t.Fatalf("err = %v", err)
	}
}

type extra struct{ name string }

func (e extra) Name() string { return e.name }
func (e extra) Register(app core.App, _ extkit.Config) error {
	app.Store().Set("extra:"+e.name, true)
	return nil
}

func TestExtras(t *testing.T) {
	set := standard.New().Add(extra{"hello"})
	app, _ := newApp(t, set, nil)
	if app.Store().Get("extra:hello") != true {
		t.Fatal("extra didn't register")
	}

	dup := standard.New().Add(extra{"pitr"})
	_, err := dup.Prepare(pocketbase.NewWithConfig(pocketbase.Config{DefaultDataDir: t.TempDir()}), nil)
	if err == nil || !strings.Contains(err.Error(), `two extensions are named "pitr"`) {
		t.Fatalf("err = %v", err)
	}
}
