// Package headroom refuses local Capsule materialization when the filesystem
// cannot retain the configured safety reserve. It deliberately plans no
// cleanup: reclaiming data is an explicit operator action.
package headroom

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// MinimumFloorBytes is the non-negotiable local materialization reserve.
const MinimumFloorBytes int64 = 20 << 30

// EnvFloorBytes permits a stricter local reserve. Values below the minimum
// are invalid rather than weakening the safety guarantee.
const EnvFloorBytes = "KITSOKI_LOCAL_CAPSULE_HEADROOM_BYTES"

const FailureClass = "local_disk_headroom"

// Error is a typed, operator-actionable refusal. Remediation is intentionally
// the read-only planner only; this guard never deletes or reorders work.
type Error struct {
	Path        string
	FreeBytes   int64
	FloorBytes  int64
	Remediation string
	Cause       error
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("capsule %s: cannot determine free space for %q: %v; remediation: %s", FailureClass, e.Path, e.Cause, e.Remediation)
	}
	return fmt.Sprintf("capsule %s: free bytes %d below required %d for %q; remediation: %s", FailureClass, e.FreeBytes, e.FloorBytes, e.Path, e.Remediation)
}

func (e *Error) Unwrap() error { return e.Cause }

// Class identifies this failure for queue retry policy and durable reporting.
func (e *Error) Class() string { return FailureClass }

type FreeBytesFunc func(string) (int64, error)

// Guard may inject FreeBytes in deterministic tests. A zero FloorBytes uses
// the configured default; an explicit value below MinimumFloorBytes fails
// closed.
type Guard struct {
	Enabled    bool
	FloorBytes int64
	FreeBytes  FreeBytesFunc
}

func Default() Guard { return Guard{Enabled: true} }

func (g Guard) Ensure(projectRoot string) error {
	if !g.Enabled {
		return nil
	}
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return err
	}
	floor, err := g.floor()
	if err != nil {
		return err
	}
	remediation := "kitsoki capsule cleanup plan --project " + shellQuote(root) + " --json=true"
	probe := g.FreeBytes
	if probe == nil {
		probe = freeBytes
	}
	free, err := probe(root)
	if err != nil {
		return &Error{Path: root, FloorBytes: floor, Remediation: remediation, Cause: err}
	}
	if free < floor {
		return &Error{Path: root, FreeBytes: free, FloorBytes: floor, Remediation: remediation}
	}
	return nil
}

func (g Guard) floor() (int64, error) {
	if g.FloorBytes != 0 {
		if g.FloorBytes < MinimumFloorBytes {
			return 0, fmt.Errorf("capsule %s: configured floor %d is below minimum %d", FailureClass, g.FloorBytes, MinimumFloorBytes)
		}
		return g.FloorBytes, nil
	}
	raw := strings.TrimSpace(os.Getenv(EnvFloorBytes))
	if raw == "" {
		return MinimumFloorBytes, nil
	}
	floor, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || floor < MinimumFloorBytes {
		return 0, fmt.Errorf("capsule %s: %s must be an integer >= %d", FailureClass, EnvFloorBytes, MinimumFloorBytes)
	}
	return floor, nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
