package queue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	lease, err := c.AcquireLease(ctx, in)
	if err != nil {
		return nil, err
	}
	return lease.Release, nil
}

// FileGateLease is one kernel-owned capacity slot. The lock descriptor is never
// inherited by a gate command: only the owner may keep capacity occupied.
// Nested gate-run wrappers borrow a cheap liveness marker and create their own
// supervised process group, avoiding both self-deadlock and orphan-held locks.
type FileGateLease struct {
	file        *os.File
	custody     *os.File
	root        string
	pool        string
	custodyPath string
	ownerPID    int
	borrow      gateBorrowMarker
	inherited   bool
	once        sync.Once
}

// Release relinquishes an owned slot. A borrowed lease never owns a kernel
// capacity lock.
func (l *FileGateLease) Release() {
	if l == nil || l.file == nil || l.inherited {
		return
	}
	l.once.Do(func() {
		if l.custody != nil {
			_ = l.custody.Close()
		}
		_ = releaseExclusiveFileLock(l.file)
		_ = l.file.Close()
	})
}

// RunCommand starts cmd under crash-safe supervision. Darwin and Linux use a
// kernel-authenticated liveness socket plus a dedicated process group: if this
// process is killed, the socket reaches EOF and an external watchdog terminates
// the entire group. The capacity locks are never exposed to descendants.
func (l *FileGateLease) RunCommand(ctx context.Context, cmd *exec.Cmd) error {
	if l == nil || (l.file == nil && !l.inherited) {
		return fmt.Errorf("queue: gate capacity lease is required")
	}
	if l.file != nil {
		if _, err := l.file.Stat(); err != nil {
			return fmt.Errorf("queue: gate capacity lease is closed: %w", err)
		}
		if l.custody == nil {
			return fmt.Errorf("queue: gate capacity custody lock is required")
		}
	} else if !gateBorrowAlive(l.borrow) {
		return fmt.Errorf("queue: inherited gate capacity owner is no longer alive")
	}
	if cmd == nil {
		return fmt.Errorf("queue: gate command is required")
	}
	if err := runSupervisedGateCommand(ctx, cmd, l); err != nil {
		return err
	}
	return nil
}

func (l *FileGateLease) validate() error {
	if l == nil || (l.file == nil && !l.inherited) {
		return fmt.Errorf("queue: gate capacity lease is required")
	}
	if l.file != nil {
		if _, err := l.file.Stat(); err != nil {
			return fmt.Errorf("queue: gate capacity lease is closed: %w", err)
		}
		if l.custody == nil {
			return fmt.Errorf("queue: gate capacity custody lock is required")
		}
	} else if !gateBorrowAlive(l.borrow) {
		return fmt.Errorf("queue: inherited gate capacity owner is no longer alive")
	}
	return nil
}

func (l *FileGateLease) commandEnv(env []string, markerFD int) ([]string, error) {
	if err := l.validate(); err != nil {
		return nil, err
	}
	if markerFD < 3 {
		return nil, fmt.Errorf("queue: invalid gate liveness descriptor")
	}
	if env == nil {
		env = os.Environ()
	}
	env = setEnv(env, "KITSOKI_GATE_CAPACITY_FD", fmt.Sprintf("%d", markerFD))
	env = setEnv(env, "KITSOKI_GATE_CAPACITY_ROOT", l.root)
	env = setEnv(env, "KITSOKI_GATE_CAPACITY_POOL", l.pool)
	ownerPID, custodyPath := l.ownerPID, l.custodyPath
	if l.inherited {
		ownerPID, custodyPath = l.borrow.ownerPID, l.borrow.custodyPath
	}
	env = setEnv(env, "KITSOKI_GATE_CAPACITY_OWNER_PID", fmt.Sprintf("%d", ownerPID))
	env = setEnv(env, "KITSOKI_GATE_CAPACITY_CUSTODY_PATH", custodyPath)
	return env, nil
}

func setEnv(env []string, name, value string) []string {
	prefix := name + "="
	out := env[:0]
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return append(out, prefix+value)
}

// AcquireLease obtains or safely borrows a process-inherited slot.
func (c FileGateCapacity) AcquireLease(ctx context.Context, in GateAdmissionRequest) (*FileGateLease, error) {
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
	if marker, ok := inheritedGateBorrow(root, pool, dir); ok {
		return &FileGateLease{
			root: root, pool: pool, custodyPath: marker.custodyPath,
			ownerPID: marker.ownerPID, borrow: marker, inherited: true,
		}, nil
	}
	for {
		for slot := 0; slot < max; slot++ {
			path := filepath.Join(dir, fmt.Sprintf("slot-%03d.lock", slot))
			file, err := lockFile(path, 0)
			if err == nil {
				custodyPath := filepath.Join(dir, fmt.Sprintf("slot-%03d.custody", slot))
				custody, custodyErr := acquireGateCustody(custodyPath)
				if custodyErr == nil {
					return &FileGateLease{
						file: file, custody: custody, root: root, pool: pool,
						custodyPath: custodyPath, ownerPID: os.Getpid(),
					}, nil
				}
				_ = releaseExclusiveFileLock(file)
				_ = file.Close()
				if !errors.Is(custodyErr, ErrBusy) {
					return nil, custodyErr
				}
				continue
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

type gateCapacityLeaseContextKey struct{}

func withGateCapacityLease(ctx context.Context, lease *FileGateLease) context.Context {
	return context.WithValue(ctx, gateCapacityLeaseContextKey{}, lease)
}

func gateCapacityLeaseFromContext(ctx context.Context) *FileGateLease {
	lease, _ := ctx.Value(gateCapacityLeaseContextKey{}).(*FileGateLease)
	return lease
}
