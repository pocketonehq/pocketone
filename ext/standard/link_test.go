package standard_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ganigeorgiev.com/pocketbase/pocketbase"
	"ganigeorgiev.com/pocketbase/pocketbase/core"
	"pocketone.dev/ext/api-tokens/apitokenstest"
	"pocketone.dev/ext/contracts/remote/v1/remoteclient"
	"pocketone.dev/ext/standard"
)

// Two stock apps: app1's `link` signs in to app2 through app2's OAuth server (a browser grant), never a password,
// and what it saves is a revocable API token that works on app2's contracts.
func TestLinkSignsInThroughOAuth(t *testing.T) {
	t.Setenv("P1_CONFIG_DIR", t.TempDir())
	t.Setenv("P1_TOKEN", "")
	t.Chdir(t.TempDir()) // app1's project folder: link writes .pocketbase/remotes.json here

	app1, _ := newApp(t, standard.New(), nil)
	app2, h2 := newApp(t, standard.New(), nil)
	addSuperuser(t, app2, "admin@example.com", "password1234")
	srv2 := httptest.NewServer(h2)
	t.Cleanup(srv2.Close)

	// The "browser": when the CLI opens the sign-in URL, sign in on app2's pages as the superuser, check the consent
	// screen, approve, and follow the redirect back to the CLI's loopback listener (tokenstest).
	browserErr := make(chan error, 1)
	oldOpen, oldCan := remoteclient.OpenBrowser, remoteclient.CanOpenBrowser
	t.Cleanup(func() { remoteclient.OpenBrowser, remoteclient.CanOpenBrowser = oldOpen, oldCan })
	remoteclient.CanOpenBrowser = func() bool { return true } // a CI runner has no display
	remoteclient.OpenBrowser = func(u string) error {
		go func() {
			// on failure it also fails the CLI's sign-in, so link returns now instead of waiting out its timeout
			browserErr <- apitokenstest.ApproveOrFail(u, apitokenstest.Login{Collection: core.CollectionNameSuperusers, Email: "admin@example.com", Password: "password1234"})
		}()
		return nil
	}

	// the password flags are gone
	if out, err := run(t, app1, "link", srv2.URL, "--email", "admin@example.com"); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("link --email: %v\n%s", err, out)
	}

	// link runs beside the browser, so a browser failure fails the test at once, whatever link is waiting on
	type linkResult struct {
		out string
		err error
	}
	linked := make(chan linkResult, 1)
	go func() {
		out, err := run(t, app1, "link", srv2.URL)
		linked <- linkResult{out, err}
	}()
	var lr linkResult
	select {
	case berr := <-browserErr:
		if berr != nil {
			t.Fatalf("browser: %v", berr)
		}
		select {
		case lr = <-linked:
		case <-time.After(10 * time.Second):
			t.Fatal("the browser signed in but link didn't finish")
		}
	case lr = <-linked:
		select {
		case berr := <-browserErr:
			if berr != nil {
				t.Fatalf("browser: %v\nlink: %v\n%s", berr, lr.err, lr.out)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("link finished without the browser grant: %v\n%s", lr.err, lr.out)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("link never opened the browser")
	}
	out, err := lr.out, lr.err
	if err != nil {
		t.Fatalf("link: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Linked default → "+srv2.URL) || !strings.Contains(out, "as admin@example.com (oauth)") {
		t.Fatalf("link output:\n%s", out)
	}

	// saved: an API token from the grant, with a refresh token, never a password or a PocketBase session
	creds, err := remoteclient.LoadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	cr := creds.ByURL[srv2.URL]
	if cr == nil || cr.Via != "oauth" || !strings.HasPrefix(cr.Token, "pbt_") || !strings.HasPrefix(cr.RefreshToken, "pbr_") ||
		cr.Email != "admin@example.com" || cr.Collection != core.CollectionNameSuperusers || cr.Expires.IsZero() {
		t.Fatalf("saved login: %+v", cr)
	}

	// the saved login works on app2: whoami sees an API token, and a contract operation runs with it
	cl, err := remoteclient.Open(".", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	w, err := cl.Whoami(ctx)
	if err != nil || w.Auth == nil || w.Auth.Email != "admin@example.com" || w.Via != "token" || w.TokenId == "" {
		t.Fatalf("whoami: %+v %v", w, err)
	}
	var status map[string]any
	if err := cl.JSON(ctx, http.MethodGet, "/api/_c/remote/v1/status", nil, &status); err != nil || status["running"] != true {
		t.Fatalf("remote/v1 status: %v %v", status, err)
	}
	if out, err := run(t, app1, "remote", "whoami"); err != nil || !strings.Contains(out, "admin@example.com in _superusers, via token (saved login (oauth))") {
		t.Fatalf("remote whoami: %v\n%s", err, out)
	}

	// logout revokes the grant on app2
	if out, err := run(t, app1, "logout"); err != nil || !strings.Contains(out, "Logged out") {
		t.Fatalf("logout: %v\n%s", err, out)
	}
	probe := *cl
	probe.Token = cr.Token
	// PocketBase v0.42 answers a revoked token with 401 (pbRejectUnknownToken) instead of serving it as a guest
	if w, err := probe.Whoami(ctx); err == nil {
		t.Fatalf("token still works after logout: %+v", w)
	}
}

func addSuperuser(t *testing.T, app core.App, email, password string) {
	t.Helper()
	col, err := app.FindCollectionByNameOrId(core.CollectionNameSuperusers)
	if err != nil {
		t.Fatal(err)
	}
	rec := core.NewRecord(col)
	rec.SetEmail(email)
	rec.SetPassword(password)
	if err := app.Save(rec); err != nil {
		t.Fatal(err)
	}
}

// run executes one of app's commands (as `pocketone <args>` would) and returns what it printed.
func run(t *testing.T, app *pocketbase.PocketBase, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	app.RootCmd.SetOut(&out)
	app.RootCmd.SetErr(&out)
	app.RootCmd.SetIn(strings.NewReader(""))
	app.RootCmd.SetArgs(args)
	// cobra keeps the first context on each subcommand, so every run shares the test's (login has its own timeouts)
	_, err := app.RootCmd.ExecuteContextC(t.Context())
	return out.String(), err
}
