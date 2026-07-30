package queue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GateAdmission controls expensive deterministic gate invocations independently
// of the short queue-state serializer. Implementations must release capacity
// when a process exits so a dead worker cannot strand a slot.
type GateAdmission interface {
	Acquire(context.Context, GateAdmissionRequest) (release func(), err error)
}

type GateAdmissionRequest struct {
	ProjectID, TargetRef, Tier, WorkerID string
}

// defaultGateCapacityRootOverride is test-only injection for hermetic package
// processes. Production never assigns it.
var defaultGateCapacityRootOverride string

// DefaultGateCapacityRoot is the per-user host authority shared by every
// repository on this workstation/controller. UserCacheDir is owner-scoped on
// supported hosts, avoiding a predictable world-writable /tmp authority.
func DefaultGateCapacityRoot() string {
	if filepath.IsAbs(defaultGateCapacityRootOverride) {
		return defaultGateCapacityRootOverride
	}
	if root, err := os.UserCacheDir(); err == nil && filepath.IsAbs(root) {
		return filepath.Join(root, "kitsoki", "runtime")
	}
	return filepath.Join(os.TempDir(), "kitsoki-runtime")
}

// DefaultFileGateCapacity makes resource serialization fail-safe rather than
// opt-in. Operators may raise Max or select a different named physical pool,
// but an unconfigured worker always shares one host-level slot.
func DefaultFileGateCapacity() FileGateCapacity {
	if filepath.IsAbs(defaultGateCapacityRootOverride) {
		// Hermetic package tests exercise queue state, leases, retries, and
		// fencing in parallel; they are not competing for a physical machine
		// gate. Keep the file-lock implementation but remove artificial
		// in-process serialization. Explicit capacity tests construct their own
		// FileGateCapacity with the desired bound.
		return FileGateCapacity{Root: DefaultGateCapacityRoot(), Pool: "default", Max: 256}
	}
	return productionDefaultFileGateCapacity()
}

func productionDefaultFileGateCapacity() FileGateCapacity {
	return FileGateCapacity{Root: DefaultGateCapacityRoot(), Pool: "default", Max: 1}
}

// FileGateCapacity is a workstation/controller-wide process-shared capacity
// pool. Each slot is a kernel file lock: contenders across daemon processes see
// the same limit, and the kernel releases a dead worker's slot automatically.
// All repositories and tiers naming the same physical Pool contend.
type FileGateCapacity struct {
	Root string
	// Pool is the operator-configured physical resource authority (for
	// example "workstation-1" or "vm-pool-sgp1"). Every repository and target
	// using the same Root+Pool shares these slots.
	Pool         string
	Max          int
	PollInterval time.Duration
}

func (c FileGateCapacity) Acquire(ctx context.Context, in GateAdmissionRequest) (func(), error) {
	root := filepath.Clean(strings.TrimSpace(c.Root))
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("queue: gate capacity root must be absolute")
	}
	pool := strings.TrimSpace(c.Pool)
	if pool == "" || strings.ContainsAny(pool, "/\\\n\r\t") {
		return nil, fmt.Errorf("queue: gate capacity pool is required")
	}
	max := c.Max
	if max < 1 {
		max = 1
	}
	interval := c.PollInterval
	if interval <= 0 {
		interval = 25 * time.Millisecond
	}
	sum := sha256.Sum256([]byte(pool))
	dir := filepath.Join(root, "gate-capacity", hex.EncodeToString(sum[:16]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("queue: create gate capacity authority: %w", err)
	}
	for {
		for slot := 0; slot < max; slot++ {
			release, err := lock(filepath.Join(dir, fmt.Sprintf("slot-%03d.lock", slot)), 0)
			if err == nil {
				return release, nil
			}
			if !errors.Is(err, ErrBusy) {
				return nil, err
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
