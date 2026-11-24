package modetest

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Proc is one running `pocketone serve`: a single app, or a multisite host.
type Proc struct {
	t       *testing.T
	ID      string // the host id, or the instance id for a single app
	Dir     string // where it runs (the app folder, or the host folder holding pocketone.yaml and pb_root)
	Addr    string // host:port it listens on
	Env     []string
	Log     *Buffer // stdout and stderr
	TrapURL string  // the dev mail trap's inbox for this process (dev mode)
	args    []string
	cmd     *exec.Cmd
	exited  chan error
	stopped bool
}

// newProc prepares a process in dir with its own ports and environment; start runs it.
func newProc(t *testing.T, id, dir string, env []string) *Proc {
	trapHTTP, trapSMTP := FreeAddr(t), FreeAddr(t)
	p := &Proc{t: t, ID: id, Dir: dir, Addr: FreeAddr(t), Log: &Buffer{}, TrapURL: "http://" + trapHTTP}
	p.Env = append(baseEnv(t), "P1_MAILTRAP_HTTP="+trapHTTP, "P1_MAILTRAP_SMTP="+trapSMTP)
	p.Env = append(p.Env, env...)
	return p
}

// Env is a clean environment for a pocketone process the test runs itself (the CLI: link, deploy, pages create): its
// own config folder (saved logins), no tokens from the developer's shell, never "over SSH".
func Env(t *testing.T) []string { return baseEnv(t) }

// Command is a pocketone command in dir with env, stopped when ctx ends.
func Command(ctx context.Context, t *testing.T, dir string, env []string, args ...string) *exec.Cmd {
	t.Helper()
	return command(ctx, Bin(t), dir, env, args...)
}

func baseEnv(t *testing.T) []string {
	return append(os.Environ(),
		"P1_CONFIG_DIR="+t.TempDir(), "P1_TOKEN=", "SSH_CONNECTION=", "SSH_TTY=")
}

// superuser creates the superuser in a data folder (relative to p.Dir) before the first serve.
func (p *Proc) superuser(dataDir string) {
	p.t.Helper()
	Step(p.t, "superuser upsert --dir="+dataDir)
	out, err := p.Run("superuser", "upsert", SuperuserEmail, SuperuserPassword, "--dir="+dataDir)
	if err != nil {
		p.t.Fatalf("superuser upsert %s: %v\n%s", dataDir, err, out)
	}
}

// start runs `pocketone serve args...` and fails at once if it exits.
func (p *Proc) start(args ...string) {
	p.t.Helper()
	p.args = args
	p.cmd = command(context.Background(), Bin(p.t), p.Dir, p.Env, append([]string{"serve"}, args...)...)
	p.cmd.Stdout, p.cmd.Stderr = p.Log, p.Log
	Step(p.t, fmt.Sprintf("serve %s (%s) %s", p.ID, p.Addr, strings.Join(args, " ")))
	if err := p.cmd.Start(); err != nil {
		p.t.Fatal(err)
	}
	exited := make(chan error, 1)
	p.exited = exited
	go func() { exited <- p.cmd.Wait() }()
	p.t.Cleanup(func() {
		if !p.stopped {
			_ = p.cmd.Process.Kill()
			select {
			case <-exited:
			case <-time.After(5 * time.Second):
			}
		}
		if p.t.Failed() {
			p.t.Logf("%s log (%s):\n%s", p.ID, p.Addr, Tail(p.Log.String(), 120))
		}
	})
}

// waitHTTP waits until GET path (with this Host header) answers 200, failing at once if the process exits.
func (p *Proc) waitHTTP(what, host, path string, limit time.Duration) {
	p.t.Helper()
	WaitFor(p.t, what, limit, func() (bool, string) {
		select {
		case err := <-p.exited:
			p.exited <- err
			p.t.Fatalf("%s: the process exited (%v):\n%s", p.ID, err, Tail(p.Log.String(), 60))
		default:
		}
		code, b := request(p.Addr, "http://"+hostOr(host, p.Addr)+path, "GET", "", nil)
		return code == 200, fmt.Sprintf("%d %.80s", code, b)
	})
}

// Stop ends the process (SIGINT, then a kill after 10s) and waits for it.
func (p *Proc) Stop() {
	p.t.Helper()
	Step(p.t, "stop "+p.ID)
	p.stopped = true
	_ = p.cmd.Process.Signal(os.Interrupt)
	select {
	case <-p.exited:
	case <-time.After(10 * time.Second):
		_ = p.cmd.Process.Kill()
		<-p.exited
	}
	Step(p.t, "stopped "+p.ID)
}

// restart starts a stopped process again with the same flags (without waiting).
func (p *Proc) restart() {
	p.t.Helper()
	if !p.stopped {
		p.t.Fatalf("modetest: %s is running; stop it first", p.ID)
	}
	p.stopped = false
	p.start(p.args...)
}

// Run runs a pocketone command in the process's folder with its environment (60s limit).
func (p *Proc) Run(args ...string) (string, error) {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := command(ctx, Bin(p.t), p.Dir, p.Env, args...).CombinedOutput()
	return string(out), err
}

// Write writes a file under the process's folder.
func (p *Proc) Write(rel, content string) { WriteFile(p.t, filepath.Join(p.Dir, rel), content) }

func command(ctx context.Context, bin, dir string, env []string, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, bin, args...)
	c.Dir, c.Env, c.Stdin = dir, env, nil
	c.WaitDelay = 5 * time.Second
	return c
}

func hostOr(host, addr string) string {
	if host != "" {
		return host
	}
	return addr
}

// Step logs a timestamped progress line.
func Step(t testing.TB, what string) {
	t.Helper()
	t.Logf("%s %s", time.Now().Format("15:04:05.000"), what)
}

// WaitFor polls ok every 200ms, logging each new answer, and fails after limit.
func WaitFor(t testing.TB, what string, limit time.Duration, ok func() (bool, string)) {
	t.Helper()
	Step(t, "wait: "+what)
	deadline := time.Now().Add(limit)
	last := ""
	for {
		good, got := ok()
		if good {
			Step(t, "ready: "+what)
			return
		}
		if got != last {
			t.Logf("  still waiting (%s): %.200s", what, got)
			last = got
		}
		if time.Now().After(deadline) {
			t.Fatalf("gave up after %s waiting for %s; last answer: %s", limit, what, got)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// WriteFile writes content to path, making its folder.
func WriteFile(t testing.TB, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// FreeAddr is a free 127.0.0.1 port.
func FreeAddr(t testing.TB) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// Tail is the last lines of s.
func Tail(s string, lines int) string {
	parts := strings.Split(s, "\n")
	if len(parts) > lines {
		parts = append([]string{fmt.Sprintf("... (%d lines before)", len(parts)-lines)}, parts[len(parts)-lines:]...)
	}
	return strings.Join(parts, "\n")
}

// Buffer is a bytes.Buffer safe for a process's output and the test reading it at once.
type Buffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *Buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
