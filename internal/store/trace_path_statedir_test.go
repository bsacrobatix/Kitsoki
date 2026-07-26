package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/statedir"
)

// With KITSOKI_STATE_DIR set, every session-trace path resolver must resolve
// under <state>/sessions. The unset behavior is covered by the pre-existing
// tests in trace_path_test.go, which run without the env var and pin the
// legacy home-anchored paths.

func TestSessionsDirHonorsStateDir(t *testing.T) {
	state := t.TempDir()
	t.Setenv(statedir.EnvStateDir, state)
	want := filepath.Join(state, "sessions")
	if got := SessionsDir(); got != want {
		t.Fatalf("SessionsDir() = %q, want %q", got, want)
	}
}

func TestDefaultTracePathHonorsStateDir(t *testing.T) {
	state := t.TempDir()
	t.Setenv(statedir.EnvStateDir, state)
	got := DefaultTracePath("myapp", "web", "thread-1")
	wantPrefix := filepath.Join(state, "sessions", "myapp") + string(filepath.Separator)
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("DefaultTracePath() = %q, want prefix %q", got, wantPrefix)
	}
	if !strings.HasSuffix(got, ".jsonl") {
		t.Fatalf("DefaultTracePath() = %q, want .jsonl suffix", got)
	}
}

func TestDefaultTracePathStateDirKeepsKeyScheme(t *testing.T) {
	// The state root must change only the root: the <app>/<sha8>-<slug>.jsonl
	// tail is identical to the legacy layout so resume keys stay stable.
	t.Setenv(statedir.EnvStateDir, "")
	legacy := DefaultTracePath("myapp", "web", "thread-1")
	legacyTail := strings.TrimPrefix(legacy, SessionsDir())

	state := t.TempDir()
	t.Setenv(statedir.EnvStateDir, state)
	rooted := DefaultTracePath("myapp", "web", "thread-1")
	rootedTail := strings.TrimPrefix(rooted, filepath.Join(state, "sessions"))

	if legacyTail != rootedTail {
		t.Fatalf("trace path tail changed under state dir: legacy %q vs rooted %q", legacyTail, rootedTail)
	}
}

func TestDefaultRunTracePathHonorsStateDir(t *testing.T) {
	state := t.TempDir()
	t.Setenv(statedir.EnvStateDir, state)
	got := DefaultRunTracePath("myapp")
	if got == "" {
		t.Fatal("DefaultRunTracePath() = \"\", want a path")
	}
	wantDir := filepath.Join(state, "sessions")
	if filepath.Dir(got) != wantDir {
		t.Fatalf("DefaultRunTracePath() = %q, want parent %q", got, wantDir)
	}
	// Parent must exist so the caller can open the file immediately.
	if fi, err := os.Stat(wantDir); err != nil || !fi.IsDir() {
		t.Fatalf("sessions dir %q not created: fi=%v err=%v", wantDir, fi, err)
	}
	if !strings.HasSuffix(got, "-myapp.jsonl") {
		t.Fatalf("DefaultRunTracePath() = %q, want -myapp.jsonl suffix", got)
	}
}
