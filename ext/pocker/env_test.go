package pocker

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pocketone.dev/ext/contracts/instance/source/v1/sourcetest"
)

// A host's Env reaches the app's $os.getenv and wins over a secret of the same name; other secrets still read.
func TestHostEnvOverSecrets(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "app", "pb_hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	hooks := `routerAdd("GET", "/env", (e) => e.string(200, $os.getenv("HOST_FACT") + "|" + $os.getenv("API_KEY") + "|" + process.env.HOST_FACT))`
	if err := os.WriteFile(filepath.Join(root, "app", "pb_hooks/main.pb.js"), []byte(hooks), 0o644); err != nil {
		t.Fatal(err)
	}
	inst := Instance{ID: "app", Hosts: []string{"app.example.com"}, Secrets: map[string]string{"API_KEY": "k", "HOST_FACT": "spoofed"}}
	_, scope := sourcetest.Scope(inst)
	m := NewManager(scope, ManagerOptions{Standard: testStandard,
		PBRoot: root,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Env:    func(in Instance) map[string]string { return map[string]string{"HOST_FACT": "real-" + in.ID} },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)
	defer m.Shutdown()

	get := func() string {
		w := httptest.NewRecorder()
		m.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://app.example.com/env", nil))
		return strings.TrimSpace(w.Body.String())
	}
	want := "real-app|k|real-app"
	deadline := time.Now().Add(5 * time.Second)
	for got := get(); got != want; got = get() {
		if time.Now().After(deadline) {
			t.Fatalf("got %q, want %q", got, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
