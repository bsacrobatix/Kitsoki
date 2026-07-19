package vmpool

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// stubProvisioner is a minimal, private Provisioner test double. Each
// behavior defaults to a harmless success; tests override only the hook(s)
// they need to script.
type stubProvisioner struct {
	mu sync.Mutex

	create  func(ctx context.Context, params CreateParams) (Instance, error)
	destroy func(ctx context.Context, instanceID string) error
	get     func(ctx context.Context, instanceID string) (Instance, bool, error)
	list    func(ctx context.Context, tag string) ([]Instance, error)

	destroyed []string
}

func (s *stubProvisioner) Create(ctx context.Context, params CreateParams) (Instance, error) {
	if s.create != nil {
		return s.create(ctx, params)
	}
	return Instance{ID: "inst-" + params.Name, Status: "new"}, nil
}

func (s *stubProvisioner) Destroy(ctx context.Context, instanceID string) error {
	s.mu.Lock()
	s.destroyed = append(s.destroyed, instanceID)
	s.mu.Unlock()
	if s.destroy != nil {
		return s.destroy(ctx, instanceID)
	}
	return nil
}

func (s *stubProvisioner) Get(ctx context.Context, instanceID string) (Instance, bool, error) {
	if s.get != nil {
		return s.get(ctx, instanceID)
	}
	return Instance{}, false, nil
}

func (s *stubProvisioner) ListByTag(ctx context.Context, tag string) ([]Instance, error) {
	if s.list != nil {
		return s.list(ctx, tag)
	}
	return nil, nil
}

func (s *stubProvisioner) Snapshot(ctx context.Context, instanceID, name string) (string, error) {
	return "", fmt.Errorf("stubProvisioner: Snapshot not implemented")
}

func (s *stubProvisioner) ResolveImage(ctx context.Context, ref string) (string, error) {
	return ref, nil
}

func (s *stubProvisioner) wasDestroyed(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.destroyed {
		if d == id {
			return true
		}
	}
	return false
}

func (s *stubProvisioner) destroyCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.destroyed)
}

func testConfig() Config {
	return Config{
		Tag:              "test-tag",
		NamePrefix:       "test-worker-",
		MaxConcurrent:    5,
		Region:           "sgp1",
		Size:             "s-1vcpu-1gb",
		Image:            "test-image",
		ProvisionTimeout: time.Hour,
		ActivityTimeout:  time.Hour,
		MaxLifetime:      24 * time.Hour,
	}
}

func newTestPool(root string, cfg Config, prov Provisioner, clock func() time.Time) *Pool {
	return &Pool{
		Store:       &Store{ProjectRoot: root},
		Provisioner: prov,
		Config:      cfg,
		Now:         clock,
	}
}

func TestAcquireSlotGating(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConcurrent = 1
	stub := &stubProvisioner{}
	pool := newTestPool(t.TempDir(), cfg, stub, nil)

	if _, err := pool.Acquire(context.Background(), "job1"); err != nil {
		t.Fatal(err)
	}
	_, err := pool.Acquire(context.Background(), "job2")
	if !errors.Is(err, ErrPoolFull) {
		t.Fatalf("err=%v, want ErrPoolFull", err)
	}
}

func TestAcquireRejectsDuplicateJob(t *testing.T) {
	cfg := testConfig()
	stub := &stubProvisioner{}
	pool := newTestPool(t.TempDir(), cfg, stub, nil)

	first, err := pool.Acquire(context.Background(), "job1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Acquire(context.Background(), "job1")
	if !errors.Is(err, ErrJobActive) {
		t.Fatalf("err=%v, want ErrJobActive", err)
	}

	workers, err := pool.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 1 || workers[0].ID != first.ID {
		t.Fatalf("workers=%#v, want only the original worker", workers)
	}
}

// TestAcquireIsCrashSafeRecordBeforeCreate verifies the durable worker
// record is created (in StatusCreating) before the cloud Create call is
// made, and that a failing Create marks that same record StatusFailed
// rather than leaving no local trace.
func TestAcquireIsCrashSafeRecordBeforeCreate(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig()
	createErr := errors.New("cloud provider rejected the request")

	var sawRecordBeforeCreate bool
	stub := &stubProvisioner{
		create: func(ctx context.Context, params CreateParams) (Instance, error) {
			// The durable record must already exist, in StatusCreating,
			// at the moment the cloud call is made.
			store := Store{ProjectRoot: root}
			state, err := store.Load()
			if err == nil {
				if w, ok := state.WorkerByID(workerID("job1")); ok && w.Status == StatusCreating {
					sawRecordBeforeCreate = true
				}
			}
			return Instance{}, createErr
		},
	}
	pool := newTestPool(root, cfg, stub, nil)

	_, err := pool.Acquire(context.Background(), "job1")
	if err == nil || !errors.Is(err, createErr) {
		t.Fatalf("err=%v, want wrapped %v", err, createErr)
	}
	if !sawRecordBeforeCreate {
		t.Fatal("worker record was not durable (StatusCreating) before the Create call")
	}

	workers, err := pool.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 1 {
		t.Fatalf("workers=%#v, want the failed record to remain", workers)
	}
	w := workers[0]
	if w.Status != StatusFailed {
		t.Fatalf("status=%s, want failed", w.Status)
	}
	if w.Error == "" {
		t.Fatal("expected worker.Error to record the create failure")
	}
}

func TestPollTransitionsCreatingToProvisioningToReady(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	getCalls := 0
	stub := &stubProvisioner{
		create: func(ctx context.Context, params CreateParams) (Instance, error) {
			return Instance{ID: "inst-1", Status: "new"}, nil
		},
		get: func(ctx context.Context, id string) (Instance, bool, error) {
			getCalls++
			if getCalls == 1 {
				return Instance{ID: id, Status: "new"}, true, nil
			}
			return Instance{ID: id, Status: "active", PublicIP: "203.0.113.5", PrivateIP: "10.0.0.5"}, true, nil
		},
	}
	pool := newTestPool(root, cfg, stub, clock)

	acquired, err := pool.Acquire(context.Background(), "job1")
	if err != nil {
		t.Fatal(err)
	}
	if acquired.Status != StatusCreating || acquired.InstanceID != "inst-1" {
		t.Fatalf("acquired=%#v", acquired)
	}

	// First poll: instance exists but not yet active -> still creating.
	updated, err := pool.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 1 || updated[0].Status != StatusCreating {
		t.Fatalf("after poll 1: %#v", updated)
	}

	// Second poll: instance active with a public IP -> provisioning.
	updated, err = pool.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 1 || updated[0].Status != StatusProvisioning {
		t.Fatalf("after poll 2: %#v", updated)
	}
	if updated[0].PublicIP != "203.0.113.5" || updated[0].PrivateIP != "10.0.0.5" {
		t.Fatalf("IPs not recorded: %#v", updated[0])
	}

	// Third poll: default nil Health always succeeds -> ready.
	updated, err = pool.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 1 || updated[0].Status != StatusReady {
		t.Fatalf("after poll 3: %#v", updated)
	}
	if updated[0].ReadyAt.IsZero() || updated[0].LastActivityAt.IsZero() {
		t.Fatalf("ReadyAt/LastActivityAt not stamped: %#v", updated[0])
	}
}

func TestPollProvisionTimeout(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig()
	cfg.ProvisionTimeout = 10 * time.Minute
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start
	clock := func() time.Time { return now }

	stub := &stubProvisioner{
		create: func(ctx context.Context, params CreateParams) (Instance, error) {
			return Instance{ID: "inst-1", Status: "new"}, nil
		},
	}
	pool := newTestPool(root, cfg, stub, clock)

	if _, err := pool.Acquire(context.Background(), "job1"); err != nil {
		t.Fatal(err)
	}

	now = start.Add(cfg.ProvisionTimeout + time.Minute)
	updated, err := pool.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 1 || updated[0].Status != StatusFailed {
		t.Fatalf("updated=%#v", updated)
	}
	if updated[0].Error != "provisioning timed out" {
		t.Fatalf("error=%q", updated[0].Error)
	}
	if !stub.wasDestroyed("inst-1") {
		t.Fatal("expected the instance to be destroyed on provisioning timeout")
	}
}

func TestPollActivityTimeout(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig()
	cfg.ActivityTimeout = 10 * time.Minute
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start
	clock := func() time.Time { return now }

	store := &Store{ProjectRoot: root}
	pool := &Pool{Store: store, Provisioner: &stubProvisioner{}, Config: cfg, Now: clock}

	err := store.Update(func(state *State) error {
		state.Workers = append(state.Workers, Worker{
			ID: "vm-job1", JobID: "job1", InstanceID: "inst-1",
			Status: StatusReady, CreatedAt: start, ReadyAt: start, LastActivityAt: start,
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	now = start.Add(cfg.ActivityTimeout + time.Minute)
	updated, err := pool.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 1 || updated[0].Status != StatusFailed || updated[0].Error != "activity timed out" {
		t.Fatalf("updated=%#v", updated)
	}
}

func TestPollMaxLifetimeTimeout(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig()
	cfg.MaxLifetime = 2 * time.Hour
	cfg.ActivityTimeout = 10 * time.Minute // shorter than the created-at gap below but reset by recent activity
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := start.Add(3 * time.Hour) // created 3h ago: past MaxLifetime
	clock := func() time.Time { return now }

	store := &Store{ProjectRoot: root}
	pool := &Pool{Store: store, Provisioner: &stubProvisioner{}, Config: cfg, Now: clock}

	err := store.Update(func(state *State) error {
		state.Workers = append(state.Workers, Worker{
			ID: "vm-job1", JobID: "job1", InstanceID: "inst-1",
			Status: StatusReady, CreatedAt: start, ReadyAt: start,
			LastActivityAt: now, // just touched: activity timeout must not fire
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	updated, err := pool.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(updated) != 1 || updated[0].Status != StatusFailed {
		t.Fatalf("updated=%#v", updated)
	}
	if updated[0].Error != "exceeded max lifetime" {
		t.Fatalf("error=%q, want exceeded max lifetime", updated[0].Error)
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig()
	stub := &stubProvisioner{}
	store := &Store{ProjectRoot: root}
	pool := &Pool{Store: store, Provisioner: stub, Config: cfg}

	err := store.Update(func(state *State) error {
		state.Workers = append(state.Workers, Worker{
			ID: "vm-job1", JobID: "job1", InstanceID: "inst-1", Status: StatusReady,
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := pool.Release(context.Background(), "vm-job1"); err != nil {
		t.Fatal(err)
	}
	if err := pool.Release(context.Background(), "vm-job1"); err != nil {
		t.Fatalf("second release should be a no-op, got %v", err)
	}
	if got := stub.destroyCount(); got != 1 {
		t.Fatalf("destroy called %d times, want exactly 1", got)
	}

	workers, err := pool.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 1 || workers[0].Status != StatusDestroyed || workers[0].TerminalAt.IsZero() {
		t.Fatalf("workers=%#v", workers)
	}
}

func TestReconcileOrphansAndLost(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig()
	store := &Store{ProjectRoot: root}
	stub := &stubProvisioner{
		list: func(ctx context.Context, tag string) ([]Instance, error) {
			return []Instance{{ID: "inst-a"}, {ID: "inst-c"}}, nil
		},
	}
	pool := &Pool{Store: store, Provisioner: stub, Config: cfg}

	err := store.Update(func(state *State) error {
		state.Workers = append(state.Workers,
			Worker{ID: "vm-a", JobID: "job-a", InstanceID: "inst-a", Status: StatusReady},
			Worker{ID: "vm-b", JobID: "job-b", InstanceID: "inst-b", Status: StatusReady},
		)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	report, err := pool.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Active) != 1 || report.Active[0] != "inst-a" {
		t.Fatalf("Active=%#v", report.Active)
	}
	if len(report.Orphans) != 1 || report.Orphans[0] != "inst-c" {
		t.Fatalf("Orphans=%#v", report.Orphans)
	}
	if len(report.Lost) != 1 || report.Lost[0] != "vm-b" {
		t.Fatalf("Lost=%#v", report.Lost)
	}
	if !stub.wasDestroyed("inst-c") {
		t.Fatal("orphan instance was not destroyed")
	}
	if stub.wasDestroyed("inst-a") {
		t.Fatal("active instance must not be destroyed")
	}

	workers, err := pool.List()
	if err != nil {
		t.Fatal(err)
	}
	w, ok := State{Workers: workers}.WorkerByID("vm-b")
	if !ok || w.Status != StatusFailed || w.Error != "instance lost" {
		t.Fatalf("vm-b=%#v ok=%v", w, ok)
	}
	w, ok = State{Workers: workers}.WorkerByID("vm-a")
	if !ok || w.Status != StatusReady {
		t.Fatalf("vm-a should be untouched: %#v ok=%v", w, ok)
	}
}

func TestTouchAndMarkRunning(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig()
	store := &Store{ProjectRoot: root}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	pool := &Pool{Store: store, Provisioner: &stubProvisioner{}, Config: cfg, Now: func() time.Time { return start }}

	err := store.Update(func(state *State) error {
		state.Workers = append(state.Workers, Worker{
			ID: "vm-job1", JobID: "job1", InstanceID: "inst-1", Status: StatusReady, CreatedAt: start,
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := pool.MarkRunning(context.Background(), "vm-job1", "job1"); err != nil {
		t.Fatal(err)
	}
	if err := pool.MarkRunning(context.Background(), "vm-job1", "job1"); err == nil {
		t.Fatal("expected error marking an already-running worker running again")
	}
	if err := pool.Touch(context.Background(), "vm-job1"); err != nil {
		t.Fatal(err)
	}

	workers, err := pool.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 1 || workers[0].Status != StatusRunning {
		t.Fatalf("workers=%#v", workers)
	}
}
