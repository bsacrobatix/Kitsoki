package main

import (
	"path/filepath"
	"testing"

	"kitsoki/internal/statedir"
)

// With KITSOKI_STATE_DIR set, the default sessions.db moves to
// <state>/sessions.db — which also pins the embedded-postgres data dir
// derived from its parent (startEmbeddedPG: dir(dbPath)/pg → <state>/pg).
// The unset behavior stays covered by the pre-existing XDG/home defaults.
func TestDefaultDBPathHonorsStateDir(t *testing.T) {
	state := t.TempDir()
	t.Setenv(statedir.EnvStateDir, state)
	// XDG_DATA_HOME must NOT win over the state root.
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	want := filepath.Join(state, "sessions.db")
	if got := defaultDBPath(); got != want {
		t.Fatalf("defaultDBPath() = %q, want %q", got, want)
	}
}

func TestDefaultDBPathUnsetKeepsXDGDefault(t *testing.T) {
	t.Setenv(statedir.EnvStateDir, "")
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)
	want := filepath.Join(xdg, "kitsoki", "sessions.db")
	if got := defaultDBPath(); got != want {
		t.Fatalf("defaultDBPath() = %q, want %q", got, want)
	}
}
