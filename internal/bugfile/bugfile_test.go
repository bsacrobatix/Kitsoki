package bugfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveTargetRootPrefersManagedSourceCheckout(t *testing.T) {
	base := t.TempDir()
	sourceRoot := filepath.Join(base, "main-checkout")
	workspaceRoot := filepath.Join(base, "capsule-workspace")
	nested := filepath.Join(workspaceRoot, "nested", "dir")

	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	manifest := `{
  "capsule_name": "test",
  "workspace": "` + workspaceRoot + `",
  "source": {"repo": "` + sourceRoot + `"}
}
`
	if err := os.WriteFile(filepath.Join(workspaceRoot, "capsule-manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, ".kitsoki-capsule"), []byte("test\n"), 0o644); err != nil {
		t.Fatalf("write sentinel: %v", err)
	}

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		if chdirErr := os.Chdir(oldWd); chdirErr != nil {
			t.Fatalf("restore cwd: %v", chdirErr)
		}
	})
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	gotStory, err := ResolveTargetRoot("story", "")
	if err != nil {
		t.Fatalf("ResolveTargetRoot story: %v", err)
	}
	if gotStory != sourceRoot {
		t.Fatalf("ResolveTargetRoot story = %q, want %q", gotStory, sourceRoot)
	}

	gotKitsoki, err := ResolveTargetRoot("kitsoki", "")
	if err != nil {
		t.Fatalf("ResolveTargetRoot kitsoki: %v", err)
	}
	if gotKitsoki != sourceRoot {
		t.Fatalf("ResolveTargetRoot kitsoki = %q, want %q", gotKitsoki, sourceRoot)
	}
}
