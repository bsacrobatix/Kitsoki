//go:build !windows

package atomicfile

import (
	"fmt"
	"os"
	"syscall"
)

func inheritFileIdentity(file *os.File, source os.FileInfo) error {
	uid, gid, err := fileIdentity(source)
	if err != nil {
		return err
	}
	if err := file.Chown(uid, gid); err != nil {
		return fmt.Errorf("atomicfile: inherit file identity: %w", err)
	}
	return nil
}

func inheritPathIdentity(path string, source os.FileInfo) error {
	uid, gid, err := fileIdentity(source)
	if err != nil {
		return err
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("atomicfile: inherit directory identity for %s: %w", path, err)
	}
	return nil
}

func fileIdentity(info os.FileInfo) (int, int, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("atomicfile: inspect filesystem identity")
	}
	return int(stat.Uid), int(stat.Gid), nil
}
