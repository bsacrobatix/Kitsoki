//go:build unix

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/capsule/queue"
)

// TestQueueWorkerSurvivesLockContentionAtConcurrencyOne is the P0.1
// regression test. Production symptom: com.pog.kitsoki-queue's worker
// crash-looped 162 times starting 2026-07-26. Root cause: queueWorkerCmd's
// RunE only set Store.LockWait when --concurrency > 1 (queue.go:329-343 as
// it stood before this fix); at the default --concurrency 1, LockWait
// stayed zero, so the very first time the worker's state.lock collided with
// any other reader/writer of the queue (a concurrent `queue status`,
// `promote`, or `prune`), queue.lock returned queue.ErrBusy on the spot
// (queue.go:809-833) and runQueueWorkerLoop propagated it as a terminal
// error. launchd then restarted the process straight back into the same
// race.
//
// This test reproduces exactly that contention — an external holder of the
// on-disk state.lock file, exactly as `queue status`/promote would hold it
// — against a single (--concurrency 1) worker loop, and proves the fix:
// with queueWorkerLockWait applied (as queueWorkerCmd's RunE now does
// unconditionally), the worker waits out the contention and completes its
// step instead of dying.
func TestQueueWorkerSurvivesLockContentionAtConcurrencyOne(t *testing.T) {
	sha := strings.Repeat("a", 40)
	project := t.TempDir()
	store := queue.Store{ProjectRoot: project}
	candidate, err := store.Submit(queue.Submit{Branch: "agent/a", SHA: sha, Receipt: testCLIReceipt(t, sha)})
	require.NoError(t, err)

	lockPath := filepath.Join(project, ".capsules", "queue", "state.lock")
	release := holdExternalLock(t, lockPath, 300*time.Millisecond)

	deps := queue.ProcessDeps{
		Integration: fakeCLIIntegration{speculate: func(_ context.Context, c queue.Candidate, _ []queue.Candidate) (queue.Speculation, error) {
			return queue.Speculation{SHA: "tree-" + c.SHA, BaseSHA: "base-" + c.SHA}, nil
		}},
		Gate: fakeCLIGate{}, GateVersion: "test/v1",
	}

	// queueWorkerStore is the exact function queueWorkerCmd's RunE calls to
	// build the Store for every --concurrency, including the default
	// (--concurrency 1) path exercised here via runQueueWorkerLoop. Before
	// the fix this function set LockWait only when concurrency > 1, so a
	// concurrency-1 Store built the same way would have LockWait's zero
	// value, and this call would return queue.ErrBusy the instant it raced
	// holdExternalLock's holder instead of waiting it out.
	workerStore := queueWorkerStore(project, "")

	done := make(chan error, 1)
	go func() { done <- runQueueWorkerLoop(context.Background(), workerStore, deps, true, nil) }()

	select {
	case err := <-done:
		require.NoError(t, err, "a --concurrency 1 worker must wait out lock contention, not exit/crash on it")
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not complete within bound while contending for the lock; it is hanging or the fix regressed")
	}
	<-release

	state, err := store.List()
	require.NoError(t, err)
	require.Len(t, state.Candidates, 1)
	require.Equal(t, candidate.ID, state.Candidates[0].ID)
	require.Equal(t, queue.ReadyToFinalize, state.Candidates[0].Phase, "the claim/prepare step must have actually progressed the candidate, not merely returned nil")
}

// TestQueueWorkerZeroLockWaitReproducesTheCrashLoop pins down the bug this
// fix closes: with the pre-fix zero LockWait, the exact same external lock
// contention that TestQueueWorkerSurvivesLockContentionAtConcurrencyOne
// waits out instead makes the worker's first RunOnce fail fast with
// queue.ErrBusy — the error runQueueWorkerLoop returns verbatim, and which
// a supervisor restart cannot fix because the race just repeats. If a
// future change accidentally reintroduces a concurrency-conditional
// LockWait, this documents what breaks and why.
func TestQueueWorkerZeroLockWaitReproducesTheCrashLoop(t *testing.T) {
	sha := strings.Repeat("b", 40)
	project := t.TempDir()
	store := queue.Store{ProjectRoot: project}
	_, err := store.Submit(queue.Submit{Branch: "agent/b", SHA: sha, Receipt: testCLIReceipt(t, sha)})
	require.NoError(t, err)

	lockPath := filepath.Join(project, ".capsules", "queue", "state.lock")
	release := holdExternalLock(t, lockPath, 300*time.Millisecond)
	defer func() { <-release }()

	deps := queue.ProcessDeps{
		Integration: fakeCLIIntegration{speculate: func(_ context.Context, c queue.Candidate, _ []queue.Candidate) (queue.Speculation, error) {
			return queue.Speculation{SHA: "tree-" + c.SHA, BaseSHA: "base-" + c.SHA}, nil
		}},
		Gate: fakeCLIGate{}, GateVersion: "test/v1",
	}

	// store.LockWait is deliberately left at its zero value here: this is
	// the pre-fix concurrency-1 configuration.
	done := make(chan error, 1)
	go func() { done <- runQueueWorkerLoop(context.Background(), store, deps, true, nil) }()

	select {
	case err := <-done:
		require.Truef(t, errors.Is(err, queue.ErrBusy), "want queue.ErrBusy (the fatal error runQueueWorkerLoop propagates on contention with zero LockWait), got %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("worker with zero LockWait should fail fast on contention, not hang")
	}
}

// holdExternalLock takes the same OS-level exclusive flock on path that the
// queue package's own serializer takes (see internal/capsule/queue's
// filelock_unix.go), simulating a concurrent `queue status`/promote/prune
// process (or another worker) already holding the queue's state.lock. It is
// released automatically after held, and the returned channel closes once
// the lock has been released.
func holdExternalLock(t *testing.T, path string, held time.Duration) <-chan struct{} {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	require.NoError(t, syscall.Flock(int(f.Fd()), syscall.LOCK_EX))

	release := make(chan struct{})
	go func() {
		defer close(release)
		time.Sleep(held)
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}()
	return release
}
