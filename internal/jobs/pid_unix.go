//go:build unix

package jobs

// pid_unix.go holds the unix implementation of processAlive, used by
// JobStore.SweepStaleJobs to decide whether a non-terminal job row's
// recorded owner process is still alive (rows owned by live sibling
// schedulers on the same database must not be swept). Split into
// build-tagged files because `syscall.Kill` does not exist on GOOS=windows;
// pid_windows.go supplies the Win32 equivalent. Mirrors internal/capsule/ci/pid_unix.go.

import (
	"errors"
	"syscall"
)

// processAlive uses signal 0 to check whether a process with the given PID
// is alive on this host.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	if errors.Is(err, syscall.EPERM) {
		// EPERM means the process exists but we lack permission to signal it.
		return true
	}
	return false
}
