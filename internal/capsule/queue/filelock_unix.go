//go:build !windows

package queue

import (
	"errors"
	"os"
	"syscall"
)

// tryExclusiveFileLock asks the kernel to own the serializer lock for the
// lifetime of f. The lock file itself is deliberately persistent: unlinking it
// while another process has the old inode open can split contenders across two
// independently locked files.
func tryExclusiveFileLock(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return false, err
}

func releaseExclusiveFileLock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
