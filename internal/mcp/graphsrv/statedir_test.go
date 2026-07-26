package graphsrv

import (
	"path/filepath"
	"testing"

	"kitsoki/internal/statedir"
)

// With KITSOKI_STATE_DIR set, the feedback/receipts sink leaves the catalog
// repo's .artifacts/graph-mcp and re-roots at <state>/graph-mcp. The unset
// repo-anchored behavior stays covered by the pre-existing feedback and
// receipts tests, which run without the env var.
func TestFeedbackSinkDirHonorsStateDir(t *testing.T) {
	state := t.TempDir()
	t.Setenv(statedir.EnvStateDir, state)
	want := filepath.Join(state, "graph-mcp")
	if got := feedbackSinkDir("/some/repo/root"); got != want {
		t.Fatalf("feedbackSinkDir() = %q, want %q", got, want)
	}
}

func TestFeedbackSinkDirUnsetStaysRepoAnchored(t *testing.T) {
	t.Setenv(statedir.EnvStateDir, "")
	want := filepath.Join("/some/repo/root", ".artifacts", "graph-mcp")
	if got := feedbackSinkDir("/some/repo/root"); got != want {
		t.Fatalf("feedbackSinkDir() = %q, want %q", got, want)
	}
}
