package vmpool

import (
	"context"
	"strings"
	"testing"
	"time"
)

// Preserve-on-failure: a failed worker's instance survives for post-mortem,
// the reaper treats it as known, and Release is its reclaim path.
func TestPreserveFailedKeepsInstanceUntilRelease(t *testing.T) {
	ctx := context.Background()
	fake := NewFake()
	store := &Store{ProjectRoot: t.TempDir(), LockWait: 2 * time.Second}
	base := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	clock := base
	pool := &Pool{
		Store:          store,
		Provisioner:    fake,
		Config:         Config{ProvisionTimeout: time.Minute},
		Now:            func() time.Time { return clock },
		PreserveFailed: true,
	}

	worker, err := pool.Acquire(ctx, "pm-job")
	if err != nil {
		t.Fatal(err)
	}
	// Blow past the provisioning timeout; Poll must fail the worker but keep
	// the instance.
	clock = base.Add(2 * time.Minute)
	if _, err := pool.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	state, _ := store.Load()
	cur, ok := state.WorkerByID(worker.ID)
	if !ok || cur.Status != StatusFailed || !cur.Preserved {
		t.Fatalf("worker = %+v, want preserved failure", cur)
	}
	if !strings.Contains(cur.Error, "preserved for post-mortem") {
		t.Fatalf("error missing post-mortem hint: %q", cur.Error)
	}
	if got := fake.Destroyed(); len(got) != 0 {
		t.Fatalf("instance destroyed despite preserve mode: %v", got)
	}

	// The reaper must not treat the preserved instance as an orphan.
	report, err := pool.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Orphans) != 0 {
		t.Fatalf("preserved instance reaped as orphan: %+v", report)
	}

	// Release reclaims: destroys the instance and clears preservation.
	if err := pool.Release(ctx, worker.ID); err != nil {
		t.Fatal(err)
	}
	if got := fake.Destroyed(); len(got) != 1 {
		t.Fatalf("release did not destroy preserved instance: %v", got)
	}
	state, _ = store.Load()
	cur, _ = state.WorkerByID(worker.ID)
	if cur.Preserved {
		t.Fatalf("preservation not cleared after release: %+v", cur)
	}
}

// Without preserve mode the timeout path destroys as before.
func TestTimeoutDestroysWhenNotPreserving(t *testing.T) {
	ctx := context.Background()
	fake := NewFake()
	store := &Store{ProjectRoot: t.TempDir(), LockWait: 2 * time.Second}
	base := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	clock := base
	pool := &Pool{Store: store, Provisioner: fake, Config: Config{ProvisionTimeout: time.Minute}, Now: func() time.Time { return clock }}
	worker, err := pool.Acquire(ctx, "pm-job2")
	if err != nil {
		t.Fatal(err)
	}
	clock = base.Add(2 * time.Minute)
	if _, err := pool.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	state, _ := store.Load()
	cur, _ := state.WorkerByID(worker.ID)
	if cur.Preserved || len(fake.Destroyed()) != 1 {
		t.Fatalf("expected plain destroy: worker=%+v destroyed=%v", cur, fake.Destroyed())
	}
}
