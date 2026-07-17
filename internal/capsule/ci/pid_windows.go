//go:build windows

package ci

// pid_windows.go is the windows counterpart to pid_unix.go. Windows has no
// signal-0 liveness probe (os.Process.Signal(0) unconditionally errors
// there), so processAlive is implemented honestly via OpenProcess +
// GetExitCodeProcess. Mirrors cmd/kitsoki/pid_windows.go.

import (
	"errors"

	"golang.org/x/sys/windows"
)

// stillActive is the GetExitCodeProcess exit code for a process that has not
// terminated (STILL_ACTIVE, 0x103 — not exported by x/sys/windows).
const stillActive = 259

// processAlive reports whether a process with the given PID is alive on this
// host. Equivalent in spirit to the unix signal-0 probe: an access-denied
// error from OpenProcess means the process exists but we lack permission
// (the EPERM analogue), which counts as alive.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return errors.Is(err, windows.ERROR_ACCESS_DENIED)
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}
