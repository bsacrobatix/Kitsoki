package bucketsource_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/capsule/bucketsource"
	"kitsoki/internal/objectstore"
)

// initWIPRepo creates a real temporary git repo with one commit and returns
// its path and HEAD sha.
func initWIPRepo(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	wipGit(t, dir, "init", "-q")
	wipGit(t, dir, "config", "user.name", "Capsule WIP Test")
	wipGit(t, dir, "config", "user.email", "wip@example.invalid")
	write(t, filepath.Join(dir, "a.txt"), "a\n")
	wipGit(t, dir, "add", "a.txt")
	wipGit(t, dir, "commit", "-q", "-m", "first")
	head := strings.TrimSpace(wipGit(t, dir, "rev-parse", "HEAD"))
	return dir, head
}

func wipGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func TestExportWIPCleanWorkspaceReturnsNil(t *testing.T) {
	dir, head := initWIPRepo(t)
	store := objectstore.NewFake()
	export, err := bucketsource.ExportWIP(context.Background(), store, "", "exec-clean", dir, head)
	if err != nil {
		t.Fatalf("ExportWIP: %v", err)
	}
	if export != nil {
		t.Fatalf("ExportWIP on clean workspace = %+v, want nil", export)
	}
}

// TestExportWIPNewCommitExports covers the durability-critical path: the
// worker made a new commit after the source was sealed. The uploaded bundle
// must actually clone back to that new HEAD.
func TestExportWIPNewCommitExports(t *testing.T) {
	dir, sealedHead := initWIPRepo(t)
	write(t, filepath.Join(dir, "b.txt"), "b\n")
	wipGit(t, dir, "add", "b.txt")
	wipGit(t, dir, "commit", "-q", "-m", "second")
	newHead := strings.TrimSpace(wipGit(t, dir, "rev-parse", "HEAD"))

	store := objectstore.NewFake()
	export, err := bucketsource.ExportWIP(context.Background(), store, "", "exec-wip-1", dir, sealedHead)
	if err != nil {
		t.Fatalf("ExportWIP: %v", err)
	}
	if export == nil {
		t.Fatal("ExportWIP = nil, want export")
	}
	if export.Schema != bucketsource.WIPExportSchema {
		t.Fatalf("Schema = %q", export.Schema)
	}
	if export.Head != newHead || export.SealedHead != sealedHead {
		t.Fatalf("export = %+v, want head=%s sealed=%s", export, newHead, sealedHead)
	}
	if export.Dirty {
		t.Fatalf("export.Dirty = true, want false (worktree was clean)")
	}
	wantKey := "runs/exec-wip-1/wip/refs.bundle"
	if export.BundleKey != wantKey {
		t.Fatalf("BundleKey = %q, want %q", export.BundleKey, wantKey)
	}
	if export.Size <= 0 {
		t.Fatalf("Size = %d, want > 0", export.Size)
	}

	// Sidecar is also published and matches the returned export.
	var sidecar bucketsource.WIPExport
	readJSONObject(t, store, "runs/exec-wip-1/wip/wip.json", &sidecar)
	if sidecar.Head != newHead || sidecar.BundleKey != wantKey {
		t.Fatalf("sidecar = %+v", sidecar)
	}

	// Fetch the bundle bytes back out of the store and prove they clone to
	// the new HEAD, exactly as a recovery flow would.
	rc, _, err := store.Get(context.Background(), wantKey)
	if err != nil {
		t.Fatalf("get bundle: %v", err)
	}
	defer rc.Close()
	bundleDir := t.TempDir()
	bundlePath := filepath.Join(bundleDir, "refs.bundle")
	out, err := os.Create(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.ReadFrom(rc); err != nil {
		t.Fatal(err)
	}
	out.Close()

	cloneDir := filepath.Join(bundleDir, "clone")
	cmd := exec.Command("git", "clone", "-q", bundlePath, cloneDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone bundle: %v: %s", err, out)
	}
	clonedHead := strings.TrimSpace(wipGit(t, cloneDir, "rev-parse", "HEAD"))
	if clonedHead != newHead {
		t.Fatalf("cloned bundle HEAD = %s, want %s", clonedHead, newHead)
	}
}

// TestExportWIPDirtyWorktreeFlagged covers the case where the worker never
// committed its changes at all: the bundle still contains only the sealed
// commit, but Dirty is set so recovery tooling knows uncommitted bytes were
// lost with the VM.
func TestExportWIPDirtyWorktreeFlagged(t *testing.T) {
	dir, sealedHead := initWIPRepo(t)
	write(t, filepath.Join(dir, "a.txt"), "a\nmutated\n")

	store := objectstore.NewFake()
	export, err := bucketsource.ExportWIP(context.Background(), store, "", "exec-dirty", dir, sealedHead)
	if err != nil {
		t.Fatalf("ExportWIP: %v", err)
	}
	if export == nil {
		t.Fatal("ExportWIP = nil, want export for dirty worktree")
	}
	if !export.Dirty {
		t.Fatalf("export.Dirty = false, want true")
	}
	if export.Head != sealedHead {
		t.Fatalf("Head = %s, want unchanged sealed head %s", export.Head, sealedHead)
	}
	if _, err := store.Head(context.Background(), "runs/exec-dirty/wip/refs.bundle"); err != nil {
		t.Fatalf("bundle not published: %v", err)
	}
}
