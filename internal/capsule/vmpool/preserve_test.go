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

// TestConfigWithDefaultsFillsPreserveFailedTTL covers the new knob's default
// wiring: an unset PreserveFailedTTL resolves to DefaultPreserveFailedTTL, an
// explicit positive value is left untouched, matching every other Config
// duration field's WithDefaults contract.
func TestConfigWithDefaultsFillsPreserveFailedTTL(t *testing.T) {
	if got := (Config{}).WithDefaults().PreserveFailedTTL; got != DefaultPreserveFailedTTL {
		t.Fatalf("PreserveFailedTTL default = %v, want %v", got, DefaultPreserveFailedTTL)
	}
	if got := (Config{PreserveFailedTTL: 90 * time.Minute}).WithDefaults().PreserveFailedTTL; got != 90*time.Minute {
		t.Fatalf("PreserveFailedTTL override = %v, want 90m", got)
	}
}

// Defect 1(b): a preserved worker must become reapable once
// Config.PreserveFailedTTL elapses, rather than staying protected forever.
// Reconcile is the enforcement point (Poll's contract only ever revisits
// non-terminal workers, and a preserved failure is terminal by definition).
func TestReconcileReclaimsExpiredPreservedWorker(t *testing.T) {
	ctx := context.Background()
	fake := NewFake()
	store := &Store{ProjectRoot: t.TempDir(), LockWait: 2 * time.Second}
	base := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	clock := base
	pool := &Pool{
		Store:          store,
		Provisioner:    fake,
		Config:         Config{ProvisionTimeout: time.Minute, PreserveFailedTTL: time.Hour},
		Now:            func() time.Time { return clock },
		PreserveFailed: true,
	}

	worker, err := pool.Acquire(ctx, "pm-ttl-job")
	if err != nil {
		t.Fatal(err)
	}
	// Blow past ProvisionTimeout -> preserved failure, exactly like
	// TestPreserveFailedKeepsInstanceUntilRelease above.
	clock = base.Add(2 * time.Minute)
	if _, err := pool.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	state, _ := store.Load()
	cur, ok := state.WorkerByID(worker.ID)
	if !ok || !cur.Preserved {
		t.Fatalf("worker not preserved: %+v", cur)
	}

	// Still within the TTL: Reconcile must leave it alone.
	clock = base.Add(2*time.Minute + 30*time.Minute)
	report, err := pool.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.ExpiredPreserved) != 0 || len(fake.Destroyed()) != 0 {
		t.Fatalf("preserved worker reclaimed before its TTL elapsed: report=%+v destroyed=%v", report, fake.Destroyed())
	}
	state, _ = store.Load()
	cur, _ = state.WorkerByID(worker.ID)
	if !cur.Preserved {
		t.Fatalf("preservation cleared before TTL elapsed: %+v", cur)
	}

	// Past the TTL: Reconcile reclaims it -- the instance is destroyed and
	// the durable record's Preserved flag is cleared, so it never bills
	// forever behind a human's back.
	clock = base.Add(2*time.Minute + 61*time.Minute)
	report, err = pool.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.ExpiredPreserved) != 1 || report.ExpiredPreserved[0] != worker.ID {
		t.Fatalf("expected %s reported as expired-preserved, got %+v", worker.ID, report)
	}
	if got := fake.Destroyed(); len(got) != 1 {
		t.Fatalf("expired preserved instance not destroyed: %v", got)
	}
	state, _ = store.Load()
	cur, _ = state.WorkerByID(worker.ID)
	if cur.Preserved {
		t.Fatalf("preservation not cleared after TTL reclaim: %+v", cur)
	}
	if cur.Status != StatusFailed {
		t.Fatalf("status changed unexpectedly during reclaim: %+v", cur)
	}

	// Idempotent: a later Reconcile does not re-report or re-destroy it.
	report, err = pool.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.ExpiredPreserved) != 0 {
		t.Fatalf("already-reclaimed worker re-reported as expired: %+v", report)
	}
	if got := fake.Destroyed(); len(got) != 1 {
		t.Fatalf("already-reclaimed instance destroyed again: %v", got)
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
