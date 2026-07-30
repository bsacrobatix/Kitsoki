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
	"strconv"
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

// inheritedGateLeaseFiles intentionally retains borrowed marker descriptors for
// the process lifetime. os.NewFile installs an internal finalizer; without this
// stable reference a GC cycle can close the only inherited marker even though
// the parent gate still owns the underlying lock.
var inheritedGateLeaseFiles sync.Map
var inheritedGateLeaseFilesMu sync.Mutex

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

// FileGateLease is one kernel-owned capacity slot. ConfigureCommand passes the
// already-held lock into a child, allowing a nested gate-run wrapper to borrow
// the same slot instead of deadlocking on itself. The marker is intentionally
// cheap: an open descriptor plus exact root/pool/path metadata, not a signature.
type FileGateLease struct {
	file      *os.File
	root      string
	pool      string
	path      string
	inherited bool
	once      sync.Once
}

// Release relinquishes an owned slot. Borrowed inherited descriptors stay open
// for the process lifetime so concurrent nested calls cannot close each
// other's marker; the kernel closes them when that process exits.
func (l *FileGateLease) Release() {
	if l == nil || l.file == nil || l.inherited {
		return
	}
	l.once.Do(func() {
		_ = releaseExclusiveFileLock(l.file)
		_ = l.file.Close()
	})
}

// ConfigureCommand inherits this lease into cmd and records the child-side FD.
func (l *FileGateLease) ConfigureCommand(cmd *exec.Cmd) error {
	if l == nil || l.file == nil {
		return fmt.Errorf("queue: gate capacity lease is required")
	}
	if _, err := l.file.Stat(); err != nil {
		return fmt.Errorf("queue: gate capacity lease is closed: %w", err)
	}
	fd := 3 + len(cmd.ExtraFiles)
	cmd.ExtraFiles = append(cmd.ExtraFiles, l.file)
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = setEnv(cmd.Env, "KITSOKI_GATE_CAPACITY_FD", strconv.Itoa(fd))
	cmd.Env = setEnv(cmd.Env, "KITSOKI_GATE_CAPACITY_ROOT", l.root)
	cmd.Env = setEnv(cmd.Env, "KITSOKI_GATE_CAPACITY_POOL", l.pool)
	cmd.Env = setEnv(cmd.Env, "KITSOKI_GATE_CAPACITY_PATH", l.path)
	return nil
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
	if lease, ok := inheritedFileGateLease(root, pool, dir); ok {
		return lease, nil
	}
	for {
		for slot := 0; slot < max; slot++ {
			path := filepath.Join(dir, fmt.Sprintf("slot-%03d.lock", slot))
			file, err := lockFile(path, 0)
			if err == nil {
				return &FileGateLease{file: file, root: root, pool: pool, path: path}, nil
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

func inheritedFileGateLease(root, pool, dir string) (*FileGateLease, bool) {
	if os.Getenv("KITSOKI_GATE_CAPACITY_ROOT") != root ||
		os.Getenv("KITSOKI_GATE_CAPACITY_POOL") != pool {
		return nil, false
	}
	fd, err := strconv.Atoi(os.Getenv("KITSOKI_GATE_CAPACITY_FD"))
	if err != nil || fd < 3 {
		return nil, false
	}
	path := filepath.Clean(os.Getenv("KITSOKI_GATE_CAPACITY_PATH"))
	rel, err := filepath.Rel(dir, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.Dir(rel) != "." {
		return nil, false
	}
	key := strconv.Itoa(fd) + "\x00" + path
	inheritedGateLeaseFilesMu.Lock()
	defer inheritedGateLeaseFilesMu.Unlock()
	value, ok := inheritedGateLeaseFiles.Load(key)
	if !ok {
		file := os.NewFile(uintptr(fd), "inherited-gate-capacity")
		if file == nil {
			return nil, false
		}
		inheritedGateLeaseFiles.Store(key, file)
		value = file
	}
	file, ok := value.(*os.File)
	if !ok || file == nil {
		return nil, false
	}
	got, gotErr := file.Stat()
	want, wantErr := os.Stat(path)
	if gotErr != nil || wantErr != nil || !os.SameFile(got, want) {
		return nil, false
	}
	return &FileGateLease{file: file, root: root, pool: pool, path: path, inherited: true}, true
}

type gateCapacityLeaseContextKey struct{}

func withGateCapacityLease(ctx context.Context, lease *FileGateLease) context.Context {
	return context.WithValue(ctx, gateCapacityLeaseContextKey{}, lease)
}

func gateCapacityLeaseFromContext(ctx context.Context) *FileGateLease {
	lease, _ := ctx.Value(gateCapacityLeaseContextKey{}).(*FileGateLease)
	return lease
}
