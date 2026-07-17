//go:build unix

package ci

// pid_unix.go holds the unix implementation of processAlive, used by
// FileRunStore.ReconcileOrphaned to detect a "host" executor run whose
// driving process died without writing a terminal run.json (e.g. SIGTERM to
// the process group while a gate script was still running). Split into
// build-tagged files because `syscall.Kill` does not exist on GOOS=windows;
// pid_windows.go supplies the Win32 equivalent. Mirrors cmd/kitsoki/pid_unix.go.

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
