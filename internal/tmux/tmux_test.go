package tmux_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"kitsoki/internal/tmux"
)

// fakeTmuxBin returns the path to testdata/fake-tmux.sh, the bash
// emulator that backs the test suite — keeps the suite hermetic so
// the tests pass on systems without a real tmux.
func fakeTmuxBin(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(thisFile), "testdata", "fake-tmux.sh")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("fake-tmux.sh not found: %v", err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Fatalf("fake-tmux.sh not executable")
	}
	return path
}

// newTestClient sets up a Client wired to the fake-tmux script and
// returns it along with the per-test state dir so assertions can
// inspect what sessions the fake "created."
func newTestClient(t *testing.T) (*tmux.Client, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-tmux.sh requires bash")
	}
	stateDir := t.TempDir()
	t.Setenv(tmux.TmuxBinEnv, fakeTmuxBin(t))
	t.Setenv("KITSOKI_FAKE_TMUX_STATE_DIR", stateDir)

	socket := filepath.Join(t.TempDir(), "tmux.sock")
	c, err := tmux.New(socket)
	if err != nil {
		t.Fatalf("tmux.New: %v", err)
	}
	if err := c.EnsureSocketDir(); err != nil {
		t.Fatalf("EnsureSocketDir: %v", err)
	}
	return c, stateDir
}

func TestNewSessionThenHasSession(t *testing.T) {
	c, stateDir := newTestClient(t)
	ctx := context.Background()

	if err := c.NewSession(ctx, tmux.NewSessionOptions{
		Name:       "kitsoki-chat-X",
		WorkingDir: "/tmp",
		Command:    "sleep 1",
	}); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	// State-dir reflects the new session.
	if _, err := os.Stat(filepath.Join(stateDir, "kitsoki-chat-X")); err != nil {
		t.Errorf("session file should exist: %v", err)
	}

	has, err := c.HasSession(ctx, "kitsoki-chat-X")
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if !has {
		t.Error("HasSession returned false for an existing session")
	}

	has, err = c.HasSession(ctx, "kitsoki-chat-MISSING")
	if err != nil {
		t.Fatalf("HasSession missing: %v", err)
	}
	if has {
		t.Error("HasSession returned true for a missing session")
	}
}

func TestNewSessionRejectsEmpty(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()
	cases := []struct {
		name string
		opts tmux.NewSessionOptions
	}{
		{"empty name", tmux.NewSessionOptions{Command: "x"}},
		{"empty command", tmux.NewSessionOptions{Name: "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := c.NewSession(ctx, tc.opts); err == nil {
				t.Errorf("expected error, got nil")
			}
		})
	}
}

func TestKillSessionRemovesIt(t *testing.T) {
	c, stateDir := newTestClient(t)
	ctx := context.Background()
	_ = c.NewSession(ctx, tmux.NewSessionOptions{
		Name: "doomed", Command: "true",
	})

	if err := c.KillSession(ctx, "doomed"); err != nil {
		t.Fatalf("KillSession: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "doomed")); !os.IsNotExist(err) {
		t.Errorf("expected session file gone, stat err = %v", err)
	}
}

func TestKillSessionMissingReturnsErrSessionNotFound(t *testing.T) {
	c, _ := newTestClient(t)
	err := c.KillSession(context.Background(), "NEVER-EXISTED")
	if !errors.Is(err, tmux.ErrSessionNotFound) {
		t.Errorf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestListSessions(t *testing.T) {
	c, _ := newTestClient(t)
	ctx := context.Background()
	// Empty server first.
	got, err := c.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions empty: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty list, got %v", got)
	}

	// Create three; verify they all come back.
	for _, n := range []string{"alpha", "beta", "gamma"} {
		if err := c.NewSession(ctx, tmux.NewSessionOptions{Name: n, Command: "true"}); err != nil {
			t.Fatalf("NewSession %s: %v", n, err)
		}
	}
	got, err = c.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	sort.Strings(got)
	want := []string{"alpha", "beta", "gamma"}
	if len(got) != 3 {
		t.Fatalf("expected 3 sessions, got %d (%v)", len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ListSessions[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestHasSessionRejectsEmpty(t *testing.T) {
	c, _ := newTestClient(t)
	if _, err := c.HasSession(context.Background(), ""); err == nil {
		t.Error("expected error for empty name")
	}
}

func TestNewReportsUnavailableWhenBinaryMissing(t *testing.T) {
	t.Setenv(tmux.TmuxBinEnv, "")
	// Force LookPath to fail by clearing PATH.
	t.Setenv("PATH", "/var/empty-does-not-exist")
	_, err := tmux.New(filepath.Join(t.TempDir(), "tmux.sock"))
	if !errors.Is(err, tmux.ErrTmuxUnavailable) {
		t.Errorf("expected ErrTmuxUnavailable, got %v", err)
	}
}

func TestDefaultSocketPathRespectsXDG(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	got := tmux.DefaultSocketPath()
	want := filepath.Join(dir, "kitsoki", "tmux.sock")
	if got != want {
		t.Errorf("DefaultSocketPath = %q, want %q", got, want)
	}
}

// TestSetStatusRight pushes a status-right value and confirms the
// fake-tmux script recorded it. Also covers the missing-session
// case → ErrSessionNotFound.
func TestSetStatusRight(t *testing.T) {
	c, stateDir := newTestClient(t)
	ctx := context.Background()
	if err := c.NewSession(ctx, tmux.NewSessionOptions{
		Name: "kitsoki-chat-X", Command: "true",
	}); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	const want = "kitsoki | 2 notifications"
	if err := c.SetStatusRight(ctx, "kitsoki-chat-X", want); err != nil {
		t.Fatalf("SetStatusRight: %v", err)
	}
	gotBytes, err := os.ReadFile(filepath.Join(stateDir, "kitsoki-chat-X.status-right"))
	if err != nil {
		t.Fatalf("read recorded status-right: %v", err)
	}
	if got := string(gotBytes); got != want {
		t.Errorf("status-right = %q, want %q", got, want)
	}

	// Missing session → ErrSessionNotFound.
	if err := c.SetStatusRight(ctx, "kitsoki-chat-MISSING", "x"); !errors.Is(err, tmux.ErrSessionNotFound) {
		t.Errorf("missing-session SetStatusRight: expected ErrSessionNotFound, got %v", err)
	}
}

func TestEnsureSocketDirCreatesParents(t *testing.T) {
	c, _ := newTestClient(t)
	// Re-point socket to a nested dir that doesn't exist yet.
	socket := filepath.Join(t.TempDir(), "a", "b", "tmux.sock")
	c2, err := tmux.New(socket)
	if err != nil {
		t.Fatalf("tmux.New: %v", err)
	}
	if err := c2.EnsureSocketDir(); err != nil {
		t.Fatalf("EnsureSocketDir: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(socket)); err != nil {
		t.Errorf("parent dir not created: %v", err)
	}
	_ = c
}
