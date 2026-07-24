//go:build !windows

package queue

import (
	"fmt"
	"os"
	"syscall"
)

func preserveFileOwner(file *os.File, source os.FileInfo) error {
	uid, gid, ok := fileOwner(source)
	if !ok {
		return fmt.Errorf("queue: inspect durable state ownership")
	}
	if err := file.Chown(uid, gid); err != nil {
		return fmt.Errorf("queue: preserve durable state ownership: %w", err)
	}
	return nil
}

func fileOwner(info os.FileInfo) (int, int, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, false
	}
	return int(stat.Uid), int(stat.Gid), true
}
