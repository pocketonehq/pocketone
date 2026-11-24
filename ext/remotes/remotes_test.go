package remotes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	apitokensv1 "pocketone.dev/ext/contracts/api-tokens/v1"
	remoteclient "pocketone.dev/ext/contracts/remote/v1/remoteclient"
	"strings"
	"testing"

	"ganigeorgiev.com/pocketbase/pocketbase/apis"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"ganigeorgiev.com/pocketbase/pocketbase/tests"
	"pocketone.dev/ext/api-tokens"
	"pocketone.dev/ext/extkit"
)

// serve runs app's router (OnServe included) on a local test server.
func serve(t *testing.T, app *tests.TestApp) string {
	t.Helper()
	router, err := apis.NewRouter(app)
	if err != nil {
		t.Fatal(err)
	}
	se := &core.ServeEvent{App: app, Router: router}
	var srv *httptest.Server
	err = app.OnServe().Trigger(se, func(e *core.ServeEvent) error {
		mux, err := e.Router.BuildMux()
		if err != nil {
			return err
		}
		srv = httptest.NewServer(mux)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	return srv.URL
}

func newApp(t *testing.T) (*tests.TestApp, string) {
	t.Helper()
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)
	exts := []extkit.Extension{New(Config{}), apitokens.New(apitokens.Config{})}
	// registered and sealed the way every app is (the standard set does this), so remote/v1's routes mount
	if _, err := extkit.Setup(app, exts, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := apitokens.EnsureSchema(app); err != nil {
		t.Fatal(err)
	}
	// the test app's superuser test@example.com has password 1234567890
	return app, serve(t, app)
}

func isolate(t *testing.T) string {
	t.Helper()
	t.Setenv("P1_CONFIG_DIR", t.TempDir())
	t.Setenv("P1_TOKEN", "")
	return t.TempDir()
}

func TestProjectFile(t *testing.T) {
	dir := isolate(t)
	p, err := remoteclient.LoadProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Resolve(""); err == nil || !strings.Contains(err.Error(), "remote add") {
		t.Fatalf("empty project: %v", err)
	}
	if _, err := p.Add("bad name", "x.test"); err == nil {
		t.Fatal("bad name accepted")
	}
	if _, err := p.Add("prod", "prod.example.test/"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Add("staging", "http://127.0.0.1:8090"); err != nil {
		t.Fatal(err)
	}
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}

	sub := filepath.Join(dir, "pb_hooks", "deep")
	os.MkdirAll(sub, 0o755)
	p2, err := remoteclient.LoadProject(sub) // found by walking up
	if err != nil {
		t.Fatal(err)
	}
	if p2.Default != "prod" || p2.Root != dir {
		t.Fatalf("default %q root %q", p2.Default, p2.Root)
	}
	r, _ := p2.Resolve("")
	if r.URL != "https://prod.example.test" {
		t.Fatalf("url %q", r.URL)
	}
	if r, _ := p2.Resolve("http://other.test"); r.URL != "http://other.test" {
		t.Fatal("URL as a name")
	}
	if _, err := p2.Resolve("nope"); err == nil || !strings.Contains(err.Error(), "prod, staging") {
		t.Fatalf("unknown: %v", err)
	}
}

func TestEnvTokens(t *testing.T) {
	dir := isolate(t)
	p, err := remoteclient.LoadProject(dir)
	if err != nil {
		t.Fatal(err)
	}
	p.Add("prod", "prod.test")
	if err := p.Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("P1_TOKEN", "general")
	t.Setenv("P1_TOKEN_PROD", "named") // per-remote variables are not read
	if tok, src := remoteclient.EnvToken(); tok != "general" || src != "P1_TOKEN" {
		t.Fatal(tok, src)
	}
	c, err := remoteclient.Open(dir, "prod")
	if err != nil || c.Token != "general" {
		t.Fatalf("open: %v", err)
	}
}

func TestCredentialsFile(t *testing.T) {
	isolate(t)
	c, _ := remoteclient.LoadCredentials()
	c.ByURL["https://x.test"] = &remoteclient.Credential{Token: "pbt_x", Via: "token"}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(c.Path())
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("perm %v %v", st.Mode(), err)
	}
	c2, _ := remoteclient.LoadCredentials()
	if c2.ByURL["https://x.test"].Token != "pbt_x" {
		t.Fatal("not saved")
	}
}

// apiToken signs in as the test superuser and creates an API token, the way a user makes one in the dashboard.
func apiToken(t *testing.T, cl *remoteclient.Client) (token, id string) {
	t.Helper()
	ctx := context.Background()
	anon := *cl
	anon.Token = ""
	var auth struct {
		Token string `json:"token"`
	}
	if err := anon.JSON(ctx, http.MethodPost, "/api/collections/_superusers/auth-with-password",
		map[string]string{"identity": "test@example.com", "password": "1234567890"}, &auth); err != nil {
		t.Fatal(err)
	}
	session := *cl
	session.Token = auth.Token
	var created struct {
		Token  string `json:"token"`
		Record struct {
			Id string `json:"id"`
		} `json:"record"`
	}
	if err := session.JSON(ctx, http.MethodPost, apitokensv1.Prefix, map[string]any{"name": "test cli"}, &created); err != nil {
		t.Fatal(err)
	}
	return created.Token, created.Record.Id
}

func TestWhoamiAndTokenLogin(t *testing.T) {
	dir := isolate(t)
	_, url := newApp(t)
	cl := &remoteclient.Client{Remote: &remoteclient.Remote{Name: "local", URL: url}, HTTP: remoteclient.NewHTTPClient()}
	ctx := context.Background()

	w, err := cl.Whoami(ctx)
	if err != nil || w.Auth != nil || !w.Offers("remote/v1") || !w.Offers("api-tokens/v1") {
		t.Fatalf("anon whoami %+v %v", w, err)
	}
	token, tokenId := apiToken(t, cl)
	res, err := remoteclient.LoginWithToken(ctx, cl, token)
	if err != nil || res.Credential.Email != "test@example.com" || res.TokenId != tokenId || res.Warning != "" {
		t.Fatalf("token login %+v %v", res, err)
	}
	if err := remoteclient.SaveLogin(cl, res); err != nil {
		t.Fatal(err)
	}

	// a project remote pointing at it now opens with the saved login
	p, _ := remoteclient.LoadProject(dir)
	p.Add("local", url)
	p.Save()
	authed, err := remoteclient.Open(dir, "local")
	if err != nil {
		t.Fatal(err)
	}
	w, err = authed.Whoami(ctx)
	if err != nil || w.Auth == nil || w.Auth.Email != "test@example.com" || w.Via != "token" || w.TokenId != tokenId {
		t.Fatalf("authed whoami %+v %v", w, err)
	}
	if _, err := remoteclient.LoginWithToken(ctx, cl, "pbt_"+strings.Repeat("z", 40)); err == nil {
		t.Fatal("bad token accepted")
	}

	// logout forgets a pasted token but leaves it working: it's the user's to revoke
	out, err := runCmd(t, dir, "logout", "local")
	if err != nil || !strings.Contains(out, "Logged out") {
		t.Fatalf("logout: %v %s", err, out)
	}
	probe := *cl
	probe.Token = token
	if w, _ = probe.Whoami(ctx); w.Auth == nil {
		t.Fatal("logout revoked a pasted token")
	}
}

func TestNoWhoamiRemoteTakesTokenAsIs(t *testing.T) {
	isolate(t)
	srv := httptest.NewServer(http.NotFoundHandler()) // e.g. a PocketHost edge today
	defer srv.Close()
	cl := &remoteclient.Client{Remote: &remoteclient.Remote{Name: "ph", URL: srv.URL}, HTTP: remoteclient.NewHTTPClient()}
	res, err := remoteclient.LoginWithToken(context.Background(), cl, "ph_pat_abc")
	if err != nil || res.Credential.Token != "ph_pat_abc" {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestCommands(t *testing.T) {
	dir := isolate(t)
	_, url := newApp(t)
	if out, err := runCmd(t, dir, "remote", "add", "prod", url); err != nil || !strings.Contains(out, "Added prod") {
		t.Fatalf("%v %s", err, out)
	}
	token, _ := apiToken(t, &remoteclient.Client{Remote: &remoteclient.Remote{URL: url}, HTTP: remoteclient.NewHTTPClient()})
	if out, err := runCmd(t, dir, "login", "prod", "--token", token); err != nil || !strings.Contains(out, "Logged in") {
		t.Fatalf("login: %v %s", err, out)
	}
	out, err := runCmd(t, dir, "remote", "list")
	if err != nil || !strings.Contains(out, "test@example.com (token)") || !strings.Contains(out, "*") {
		t.Fatalf("list: %v %s", err, out)
	}
	if out, err = runCmd(t, dir, "remote", "whoami"); err != nil || !strings.Contains(out, "test@example.com in _superusers, via token") {
		t.Fatalf("whoami: %v %s", err, out)
	}
	if out, err = runCmd(t, dir, "link", url, "--name", "two", "--token", "pbt_"+strings.Repeat("z", 40)); err == nil {
		t.Fatalf("link with a bad token: %s", out)
	}
	var proj remoteclient.Project
	b, _ := os.ReadFile(filepath.Join(dir, remoteclient.ProjectDir, remoteclient.ProjectFile))
	json.Unmarshal(b, &proj)
	if proj.Remotes["two"] == nil || proj.Default != "prod" {
		t.Fatalf("link kept the remote, default unchanged: %s", b)
	}
}

func runCmd(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	var out bytes.Buffer
	for _, c := range Commands() {
		if c.Name() != args[0] {
			continue
		}
		c.SetOut(&out)
		c.SetErr(&out)
		c.SetIn(strings.NewReader(""))
		c.SetArgs(args[1:])
		c.SetContext(context.Background())
		err := c.Execute()
		return out.String(), err
	}
	t.Fatalf("no command %s", args[0])
	return "", nil
}

// A build shipped under another command name (extkit.Command) gets its own config folder; the variables stay
// PocketOne's (P1_), since the prefix names the layer that reads them.
func TestConfigFolderFollowsCommandVariablesDont(t *testing.T) {
	old := extkit.Command
	t.Cleanup(func() { extkit.Command = old })
	extkit.Command = "my-tool"
	home := t.TempDir()
	t.Setenv("P1_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", home)
	if d, _ := remoteclient.ConfigDir(); d != filepath.Join(home, "my-tool") {
		t.Fatal(d)
	}
	t.Setenv("P1_CONFIG_DIR", "/elsewhere")
	if d, _ := remoteclient.ConfigDir(); d != "/elsewhere" {
		t.Fatal(d)
	}
	t.Setenv("P1_TOKEN", "t")
	if tok, src := remoteclient.EnvToken(); tok != "t" || src != "P1_TOKEN" {
		t.Fatal(tok, src)
	}
}
