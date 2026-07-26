package dbruntime

import (
	"path/filepath"
	"testing"

	"kitsoki/internal/statedir"
)

// With KITSOKI_STATE_DIR set, the embedded-postgres base (binary cache and
// any defaulted data dir) re-roots at <state>/cache/embedded-pg so nothing
// lands under os.UserCacheDir() on a read-only rootfs.
func TestDefaultBaseDirHonorsStateDir(t *testing.T) {
	state := t.TempDir()
	t.Setenv(statedir.EnvStateDir, state)
	got, err := defaultBaseDir()
	if err != nil {
		t.Fatalf("defaultBaseDir() error: %v", err)
	}
	want := filepath.Join(state, "cache", "embedded-pg")
	if got != want {
		t.Fatalf("defaultBaseDir() = %q, want %q", got, want)
	}
}
