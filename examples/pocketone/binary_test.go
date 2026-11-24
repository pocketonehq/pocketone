package main

// Tests of the pocketone binary itself (its flags, config file, startup, the standard set wired together) on the real
// binary through ext/extkit/modetest. Each extension's own behavior, in every mode it runs in, is tested in its folder
// (ext/<name>/modes_test.go).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pocketone.dev/ext/extkit/modetest"
)

// serveOne runs body on one single app configured by in.
func serveOne(t *testing.T, in modetest.Inst, body func(t *testing.T, s *modetest.Site)) {
	t.Helper()
	in.ID = "app"
	modetest.Run(t, modetest.Topology{Instances: []modetest.Inst{in}}, []modetest.Mode{modetest.Single}, func(t *testing.T, w *modetest.World) {
		body(t, w.Instance("app"))
	})
}

// One stock app with everything on, checked from the outside: what it says at startup, what it serves (pb_hooks,
// pb_pages, pb_public), MCP and OAuth discovery, contracts with API tokens and their scopes, settings, and a restart
// asked for over remote/v1.
func TestSingleSite(t *testing.T) {
	serveOne(t, modetest.Inst{Files: map[string]string{
		"pb_hooks/probe.pb.js":    `routerAdd("GET", "/probe", (e) => e.json(200, {mcp: typeof $mcp, tokens: typeof $apiTokens, bulkmail: typeof $bulkmail}))`,
		"pb_pages/hello-page.ejs": `<p>pages <%= 1 + 1 %></p>`,
		"pb_public/static.txt":    "static file",
	}}, func(t *testing.T, s *modetest.Site) {
		t.Run("startup lines", func(t *testing.T) {
			log := s.Proc.Log.String()
			for _, want := range []string{
				"standard: on: settings, templates, cache, pages, storage, s3redirect, pitr, backups, uicache, kv, api-tokens, remotes, mcp, bulkmail, deploy, wasm, llm, llm-search, llm-chat",
				"dev mail trap is active", s.TrapURL(),
				"restore/v1 ← pitr", "oauth/v1 ← api-tokens", "deploy/v1 ← deploy",
			} {
				if !strings.Contains(log, want) {
					t.Errorf("startup log lacks %q", want)
				}
			}
		})

		t.Run("powered by header", func(t *testing.T) {
			res, err := s.Client().Get(s.URL + "/api/health")
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if h := res.Header.Get("X-Powered-By"); !strings.HasPrefix(h, "PocketOne ") {
				t.Fatalf("X-Powered-By %q", h)
			}
		})

		t.Run("hooks see the standard JS APIs", func(t *testing.T) {
			var got map[string]string
			s.MustJSON(http.MethodGet, "/probe", "", nil, 200, &got)
			if got["mcp"] != "object" || got["tokens"] != "object" || got["bulkmail"] != "object" {
				t.Fatalf("/probe: %v", got)
			}
		})

		t.Run("pages and public", func(t *testing.T) {
			if got := s.Get("/hello-page"); !strings.Contains(got, "pages 2") {
				t.Fatalf("pb_pages: %s", got)
			}
			if got := strings.TrimSpace(s.Get("/static.txt")); got != "static file" {
				t.Fatalf("pb_public: %s", got)
			}
		})

		t.Run("mcp and oauth discovery", func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, s.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			req.Header.Set("Content-Type", "application/json")
			res, err := s.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != 401 || !strings.Contains(res.Header.Get("WWW-Authenticate"), "resource_metadata") {
				t.Fatalf("/mcp anonymous: %d %q", res.StatusCode, res.Header.Get("WWW-Authenticate"))
			}
			var md map[string]any
			s.MustJSON(http.MethodGet, "/.well-known/oauth-authorization-server", "", nil, 200, &md)
			if md["authorization_endpoint"] == nil || md["token_endpoint"] == nil {
				t.Fatalf("oauth metadata: %v", md)
			}
			tools := mcpCall(t, s, s.Token(), "tools/list", nil)
			for _, want := range []string{`"restore_points"`, `"pb_list_records"`} {
				if !strings.Contains(tools, want) {
					t.Errorf("tools/list lacks %s:\n%.2000s", want, tools)
				}
			}
		})

		t.Run("contracts with API tokens", func(t *testing.T) {
			full := s.Token()
			var w struct {
				Via       string   `json:"via"`
				Contracts []string `json:"contracts"`
			}
			s.MustJSON(http.MethodGet, "/api/_c/remote/v1", full, nil, 200, &w)
			if w.Via != "token" {
				t.Fatalf("whoami via %q", w.Via)
			}
			got := strings.Join(w.Contracts, " ")
			for _, c := range []string{"bulkmail/v1", "deploy/v1", "oauth/v1", "remote/v1", "restore/v1", "settings/v1", "api-tokens/v1", "mail.suppression/v1"} {
				if !strings.Contains(got, c) {
					t.Errorf("whoami contracts lack %s: %s", c, got)
				}
			}
			var st map[string]any
			s.MustJSON(http.MethodGet, "/api/_c/remote/v1/status", full, nil, 200, &st)
			if st["running"] != true {
				t.Fatalf("status %v", st)
			}

			if code, b := s.Raw(http.MethodGet, "/api/_c/restore/v1", "", nil); code != 401 {
				t.Fatalf("restore/v1 anonymous: %d %s", code, b)
			}
			ro := s.Token("restore:read")
			s.MustJSON(http.MethodGet, "/api/_c/restore/v1", ro, nil, 200, nil)
			if code, b := s.Raw(http.MethodPost, "/api/_c/restore/v1/restore", ro, map[string]string{"at": time.Now().Format(time.RFC3339)}); code != 403 {
				t.Fatalf("restore with a restore:read token: %d %s", code, b)
			}
			if code, b := s.Raw(http.MethodGet, "/api/collections/users/records", ro, nil); code < 400 {
				t.Fatalf("records API with a scoped token: %d %s", code, b)
			}
			if code, b := s.Raw(http.MethodPost, "/mcp", ro, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); code < 400 {
				t.Fatalf("/mcp with a scoped token: %d %s", code, b)
			}
		})

		t.Run("settings", func(t *testing.T) {
			var out struct {
				Sections []struct {
					Extension string `json:"extension"`
				} `json:"sections"`
			}
			s.MustJSON(http.MethodGet, "/api/_c/settings/v1", s.Token("settings:read"), nil, 200, &out)
			var names []string
			for _, sec := range out.Sections {
				names = append(names, sec.Extension)
			}
			if !strings.Contains(strings.Join(names, " "), "pitr") {
				t.Fatalf("settings sections: %v", names)
			}
		})

		// last: it restarts the process
		t.Run("restart over remote/v1", func(t *testing.T) {
			var rr map[string]any
			s.MustJSON(http.MethodPost, "/api/_c/remote/v1/restart", s.Token("remote"), map[string]any{}, 200, &rr)
			if rr["restarting"] != true {
				t.Fatalf("restart: %v", rr)
			}
			s.WaitRestarted("remote/v1 restart", 20*time.Second)
			if got := s.Get("/probe"); !strings.Contains(got, `"mcp":"object"`) {
				t.Fatalf("after restart /probe: %s", got)
			}
		})
	})
}

// mcpCall sends one JSON-RPC call to /mcp and returns the raw answer.
func mcpCall(t *testing.T, s *modetest.Site, auth, method string, params any) string {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "method": method, "id": 1}
	if params != nil {
		body["params"] = params
	}
	j, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, s.URL+"/mcp", strings.NewReader(string(j)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+auth)
	res, err := s.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 32<<10)
	for {
		n, err := res.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	if res.StatusCode != 200 {
		t.Fatalf("mcp %s: %d %s", method, res.StatusCode, sb.String())
	}
	return sb.String()
}

// The config file: found in the working directory or named with --config; a switch in it takes effect; an unknown
// section stops startup with its name.
func TestConfigFile(t *testing.T) {
	t.Run("pocketone.yaml in the app folder", func(t *testing.T) {
		serveOne(t, modetest.Inst{Files: map[string]string{"pocketone.yaml": "pitr:\n  enabled: false\n"}}, func(t *testing.T, s *modetest.Site) {
			assertRestoreProvider(t, s, "backups")
		})
	})

	t.Run("--config elsewhere", func(t *testing.T) {
		serveOne(t, modetest.Inst{
			Files: map[string]string{"conf/custom.yaml": "pitr:\n  enabled: false\n"},
			Args:  []string{"--config", "conf/custom.yaml"},
		}, func(t *testing.T, s *modetest.Site) {
			assertRestoreProvider(t, s, "backups")
		})
	})

	t.Run("pitr on by default", func(t *testing.T) {
		serveOne(t, modetest.Inst{}, func(t *testing.T, s *modetest.Site) {
			assertRestoreProvider(t, s, "pitr")
		})
	})

	t.Run("unknown section stops startup", func(t *testing.T) {
		dir := t.TempDir()
		modetest.WriteFile(t, filepath.Join(dir, "pocketone.yaml"), "not_a_section:\n  x: 1\n")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		out, err := modetest.Command(ctx, t, dir, modetest.Env(t), "serve", "--dir=pb_data", "--http="+modetest.FreeAddr(t)).CombinedOutput()
		if ctx.Err() != nil {
			t.Fatalf("serve kept running with an unknown section:\n%s", out)
		}
		var ee *exec.ExitError
		if err == nil || !errors.As(err, &ee) || !strings.Contains(string(out), "not_a_section") {
			t.Fatalf("serve: %v\n%s", err, out)
		}
	})
}

func assertRestoreProvider(t *testing.T, s *modetest.Site, want string) {
	t.Helper()
	var pts struct {
		Provider string `json:"provider"`
	}
	s.MustJSON(http.MethodGet, "/api/_c/restore/v1", s.JWT(), nil, 200, &pts)
	if pts.Provider != want {
		t.Fatalf("restore/v1 provider %q, want %q", pts.Provider, want)
	}
}

// --dev=false: no dev mail trap and no SQL log lines.
func TestProductionMode(t *testing.T) {
	serveOne(t, modetest.Inst{Production: true}, func(t *testing.T, s *modetest.Site) {
		s.MustJSON(http.MethodGet, "/api/_c/remote/v1", s.JWT(), nil, 200, nil)
		log := s.Proc.Log.String()
		if strings.Contains(log, "dev mail trap") || strings.Contains(log, "SELECT ") {
			t.Fatalf("production mode log has dev output:\n%s", modetest.Tail(log, 40))
		}
		c := &http.Client{Timeout: 2 * time.Second}
		if res, err := c.Get(s.TrapURL() + "/api/messages"); err == nil {
			res.Body.Close()
			t.Fatalf("mail trap answers in production mode: %d", res.StatusCode)
		}
	})
}

// Flags are on the commands that read them: the root lists only --config and --env-file, serve lists the serve and
// app flags, an app command the app flags, and a remote command refuses an app or serve flag.
func TestFlagScopes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	help := func(args ...string) string {
		out, err := modetest.Command(ctx, t, dir, modetest.Env(t), append(args, "--help")...).CombinedOutput()
		if err != nil {
			t.Fatalf("%v --help: %v\n%s", args, err, out)
		}
		return string(out)
	}
	for _, c := range []struct {
		args       []string
		has, lacks []string
	}{
		{nil, []string{"--config", "--env-file"}, []string{"--dir", "--hooksDir", "--pitr", "--autotls"}},
		{[]string{"serve"}, []string{"--dir", "--hooksDir", "--publicDir", "--pitr", "--autotls", "--http", "--config"}, nil},
		{[]string{"migrate"}, []string{"--dir", "--hooksDir", "--migrationsDir"}, []string{"--publicDir", "--pitr"}},
		{[]string{"deploy"}, []string{"--config"}, []string{"--dir", "--hooksDir", "--pitr"}},
	} {
		out := help(c.args...)
		for _, f := range c.has {
			if !strings.Contains(out, f+" ") && !strings.Contains(out, f+"[") {
				t.Errorf("%v --help lacks %s", c.args, f)
			}
		}
		for _, f := range c.lacks {
			if strings.Contains(out, f+" ") || strings.Contains(out, f+"[") {
				t.Errorf("%v --help lists %s", c.args, f)
			}
		}
	}
	out, err := modetest.Command(ctx, t, dir, modetest.Env(t), "deploy", "--pitr=false").CombinedOutput()
	if err == nil || !strings.Contains(string(out), "unknown flag: --pitr") {
		t.Fatalf("deploy --pitr=false: %v\n%s", err, out)
	}
}
