package queue

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyProtectedLocalConfigPreservesExactSnapshotAndMode(t *testing.T) {
	root := t.TempDir()
	destination := localConfigDestination(t, root)
	source := filepath.Join(root, ".kitsoki.local.yaml")
	want := []byte("default_profile: exact\n")
	if err := os.WriteFile(source, want, 0o440); err != nil {
		t.Fatal(err)
	}
	copied, err := copyProtectedLocalConfig(root, destination)
	if err != nil {
		t.Fatal(err)
	}
	if !copied {
		t.Fatal("expected local config to be copied")
	}
	target := filepath.Join(destination, ".kitsoki.local.yaml")
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("content=%q, want %q", got, want)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o440 {
		t.Fatalf("mode=%#o, want %#o", gotMode, os.FileMode(0o440))
	}
}

func TestCopyProtectedLocalConfigLeavesAbsentConfigAbsent(t *testing.T) {
	root := t.TempDir()
	destination := localConfigDestination(t, root)
	copied, err := copyProtectedLocalConfig(root, destination)
	if err != nil {
		t.Fatal(err)
	}
	if copied {
		t.Fatal("absent config reported copied")
	}
	if _, err := os.Lstat(filepath.Join(destination, ".kitsoki.local.yaml")); !os.IsNotExist(err) {
		t.Fatalf("destination config err=%v, want absent", err)
	}
}

func TestCopyProtectedLocalConfigAbsentRemovesStaleRegularDestination(t *testing.T) {
	root := t.TempDir()
	destination := localConfigDestination(t, root)
	target := filepath.Join(destination, ".kitsoki.local.yaml")
	if err := os.WriteFile(target, []byte("stale: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := copyProtectedLocalConfig(root, destination)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("stale destination removal was not reported")
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("stale destination survived: %v", err)
	}
}

func TestCopyProtectedLocalConfigAbsentRejectsStaleDestinationSymlink(t *testing.T) {
	root := t.TempDir()
	destination := localConfigDestination(t, root)
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	if err := os.WriteFile(outside, []byte("outside: unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(destination, ".kitsoki.local.yaml")); err != nil {
		t.Fatal(err)
	}
	if changed, err := copyProtectedLocalConfig(root, destination); err == nil || changed {
		t.Fatalf("changed=%v err=%v, want absent-source symlink refusal", changed, err)
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != "outside: unchanged\n" {
		t.Fatalf("outside target changed: content=%q err=%v", got, err)
	}
}

func TestCopyProtectedLocalConfigRejectsSymlinkSource(t *testing.T) {
	root := t.TempDir()
	destination := localConfigDestination(t, root)
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	if err := os.WriteFile(outside, []byte("secret: outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".kitsoki.local.yaml")); err != nil {
		t.Fatal(err)
	}
	if copied, err := copyProtectedLocalConfig(root, destination); err == nil || copied {
		t.Fatalf("copied=%v err=%v, want fail-closed symlink refusal", copied, err)
	}
	if _, err := os.Lstat(filepath.Join(destination, ".kitsoki.local.yaml")); !os.IsNotExist(err) {
		t.Fatalf("destination config err=%v, want absent after refusal", err)
	}
}

func TestCopyProtectedLocalConfigRejectsSpecialSource(t *testing.T) {
	root := t.TempDir()
	destination := localConfigDestination(t, root)
	if err := os.Mkdir(filepath.Join(root, ".kitsoki.local.yaml"), 0o700); err != nil {
		t.Fatal(err)
	}
	if copied, err := copyProtectedLocalConfig(root, destination); err == nil || copied {
		t.Fatalf("copied=%v err=%v, want fail-closed special-file refusal", copied, err)
	}
}

func TestCopyProtectedLocalConfigSnapshotsOpenedIdentityAcrossAtomicUpdate(t *testing.T) {
	root := t.TempDir()
	destination := localConfigDestination(t, root)
	source := filepath.Join(root, ".kitsoki.local.yaml")
	oldContent := []byte("default_profile: old\n")
	newContent := []byte("default_profile: new\n")
	if err := os.WriteFile(source, oldContent, 0o440); err != nil {
		t.Fatal(err)
	}
	copied, err := copyProtectedLocalConfigWithHook(root, destination, func() {
		replacement := filepath.Join(root, "replacement.yaml")
		if writeErr := os.WriteFile(replacement, newContent, 0o440); writeErr != nil {
			t.Fatal(writeErr)
		}
		if renameErr := os.Rename(replacement, source); renameErr != nil {
			t.Fatal(renameErr)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !copied {
		t.Fatal("expected opened config snapshot to be copied")
	}
	got, err := os.ReadFile(filepath.Join(destination, ".kitsoki.local.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(oldContent) {
		t.Fatalf("copied content=%q, want opened immutable snapshot %q", got, oldContent)
	}
	if current, err := os.ReadFile(source); err != nil || string(current) != string(newContent) {
		t.Fatalf("source update current=%q err=%v", current, err)
	}
}

func TestCopyProtectedLocalConfigRejectsDestinationPathEscapeAndTargetSymlink(t *testing.T) {
	t.Run("destination escape", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, ".kitsoki.local.yaml"), []byte("safe: true\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		link := filepath.Join(root, "escaped-instance")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		if copied, err := copyProtectedLocalConfig(root, link); err == nil || copied {
			t.Fatalf("copied=%v err=%v, want destination-escape refusal", copied, err)
		}
	})
	t.Run("target symlink", func(t *testing.T) {
		root := t.TempDir()
		destination := localConfigDestination(t, root)
		if err := os.WriteFile(filepath.Join(root, ".kitsoki.local.yaml"), []byte("safe: true\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "outside.yaml")
		if err := os.WriteFile(outside, []byte("outside: unchanged\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(destination, ".kitsoki.local.yaml")); err != nil {
			t.Fatal(err)
		}
		if copied, err := copyProtectedLocalConfig(root, destination); err == nil || copied {
			t.Fatalf("copied=%v err=%v, want target-symlink refusal", copied, err)
		}
		got, err := os.ReadFile(outside)
		if err != nil || string(got) != "outside: unchanged\n" {
			t.Fatalf("outside target changed: content=%q err=%v", got, err)
		}
	})
}

func localConfigDestination(t *testing.T, root string) string {
	t.Helper()
	destination := filepath.Join(root, ".capsules", "sync", "instance")
	if err := os.MkdirAll(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	return destination
}
