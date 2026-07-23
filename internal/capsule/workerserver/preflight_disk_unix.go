//go:build !windows

package workerserver

import "golang.org/x/sys/unix"

// preflightDiskFree reports free bytes on the filesystem containing path.
// Mirrors internal/capsule/hygiene's disk_unix.go readDiskUsage (same
// syscall, same platform split) — kept local rather than imported so this
// package does not take on hygiene's broader retention-planning surface for
// one syscall.
func preflightDiskFree(path string) (freeBytes int64, known bool, err error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, false, err
	}
	return int64(stat.Bavail) * int64(stat.Bsize), true, nil
}
