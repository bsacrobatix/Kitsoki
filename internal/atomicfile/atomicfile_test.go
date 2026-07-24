package atomicfile

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

func TestWriteFileInheritsDirectoryIdentityAndPreservesExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no Unix owner/group identity")
	}
	root := t.TempDir()
	parent := filepath.Join(root, "shared")
	if err := os.Mkdir(parent, 0o750); err != nil {
		t.Fatal(err)
	}
	parentInfo, err := os.Stat(parent)
	if err != nil {
		t.Fatal(err)
	}
	parentStat, ok := parentInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("platform does not expose Unix file ownership")
	}
	uid, primaryGID := int(parentStat.Uid), int(parentStat.Gid)
	alternateGID := alternateGroup(t, primaryGID)
	if err := os.Chown(parent, uid, alternateGID); err != nil {
		t.Skipf("cannot assign alternate shared-store group: %v", err)
	}

	path := filepath.Join(parent, "nested", "state.json")
	if err := WriteFile(path, []byte("first\n"), 0o600, 0o750); err != nil {
		t.Fatal(err)
	}
	assertIdentity(t, filepath.Dir(path), uid, alternateGID, 0o750)
	assertIdentity(t, path, uid, alternateGID, 0o600)

	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, uid, primaryGID); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("second\n"), 0o600, 0o750); err != nil {
		t.Fatal(err)
	}
	assertIdentity(t, path, uid, alternateGID, 0o640)
}

func alternateGroup(t *testing.T, primary int) int {
	t.Helper()
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, gid := range groups {
		if gid != primary {
			return gid
		}
	}
	if os.Geteuid() == 0 {
		return primary + 1
	}
	t.Skip("no alternate writable group available")
	return -1
}

func assertIdentity(t *testing.T, path string, uid, gid int, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("filesystem identity unavailable")
	}
	if int(stat.Uid) != uid || int(stat.Gid) != gid {
		t.Fatalf("%s owner=%d:%d, want %d:%d", path, stat.Uid, stat.Gid, uid, gid)
	}
	if got := info.Mode().Perm(); got != mode {
		t.Fatalf("%s mode=%#o, want %#o", path, got, mode)
	}
}
