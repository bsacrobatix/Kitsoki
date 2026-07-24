//go:build !windows

package record

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPersistInheritsSharedCIStoreIdentity(t *testing.T) {
	root := t.TempDir()
	ciDir := filepath.Join(root, ".capsules", "ci")
	if err := os.MkdirAll(ciDir, 0o750); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(ciDir)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("platform does not expose Unix file ownership")
	}
	uid, primaryGID := int(stat.Uid), int(stat.Gid)
	alternateGID := -1
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, gid := range groups {
		if gid != primaryGID {
			alternateGID = gid
			break
		}
	}
	if alternateGID < 0 && os.Geteuid() == 0 {
		alternateGID = primaryGID + 1
	}
	if alternateGID < 0 {
		t.Skip("no alternate writable group available")
	}
	if err := os.Chown(ciDir, uid, alternateGID); err != nil {
		t.Skipf("cannot assign alternate CI-store group: %v", err)
	}

	stored, err := Persist(root, validRunResult("shared-job", "sha256:source"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{stored.TracePath, stored.ReceiptPath} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatal("filesystem identity unavailable")
		}
		if int(stat.Uid) != uid || int(stat.Gid) != alternateGID {
			t.Fatalf("%s owner=%d:%d, want %d:%d", path, stat.Uid, stat.Gid, uid, alternateGID)
		}
	}
}
