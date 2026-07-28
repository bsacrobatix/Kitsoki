package workqueue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/dbruntime/pgtest"

	_ "modernc.org/sqlite"
)

type bundleValidatorFunc func(context.Context, string, string) error

func (fn bundleValidatorFunc) ValidateBundle(
	ctx context.Context,
	ref, digest string,
) (string, error) {
	if err := fn(ctx, ref, digest); err != nil {
		return "", err
	}
	return ref, nil
}

func TestSQLiteLifecycleAndFencing(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	ids := 0
	s, err := NewSQLiteStore(db, WithClock(func() time.Time { return now }), WithIDGenerator(func() string { ids++; return string(rune('a' + ids)) }))
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.Enqueue(ctx, EnqueueRequest{ApplicationID: "app", Queue: "work", IdempotencyKey: "same", Payload: []byte("one"), RequiredCapabilities: []string{"go"}, Priority: 3, MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Enqueue(ctx, EnqueueRequest{ApplicationID: "app", Queue: "work", IdempotencyKey: "same", Payload: []byte("one"), RequiredCapabilities: []string{"go"}, Priority: 3, MaxAttempts: 2})
	if err != nil || got.ID != j.ID {
		t.Fatalf("idempotency: %#v %v", got, err)
	}
	if !got.Replayed {
		t.Fatal("idempotent enqueue did not report replay")
	}
	now = now.Add(time.Minute)
	got, err = s.Enqueue(ctx, EnqueueRequest{ApplicationID: "app", Queue: "work", IdempotencyKey: "same", Payload: []byte("one"), RequiredCapabilities: []string{"go"}, Priority: 3, MaxAttempts: 2})
	if err != nil || got.ID != j.ID || !got.Replayed {
		t.Fatalf("later idempotency: %#v %v", got, err)
	}
	now = now.Add(-time.Minute)
	if _, err := s.Enqueue(ctx, EnqueueRequest{ApplicationID: "app", Queue: "work", IdempotencyKey: "same", Payload: []byte("two")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict=%v", err)
	}
	if l, err := s.ClaimNext(ctx, ClaimRequest{ApplicationID: "app", Queue: "work", WorkerID: "w", AvailableCapacity: 1, LeaseDuration: time.Minute}); err != nil || l != nil {
		t.Fatalf("capability claim: %#v %v", l, err)
	}
	l, err := s.ClaimNext(ctx, ClaimRequest{ApplicationID: "app", Queue: "work", WorkerID: "w", Capabilities: []string{"go"}, AvailableCapacity: 1, LeaseDuration: time.Minute})
	if err != nil || l == nil {
		t.Fatal(err)
	}
	if _, err := s.Heartbeat(ctx, j.ID, "other", l.Job.Fence, time.Minute); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("heartbeat=%v", err)
	}
	if _, err := s.Fail(ctx, j.ID, "w", l.Job.Fence, Failure{Retryable: true}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	l, err = s.ClaimNext(ctx, ClaimRequest{ApplicationID: "app", Queue: "work", WorkerID: "w2", Capabilities: []string{"go"}, AvailableCapacity: 1, LeaseDuration: time.Minute})
	if err != nil || l == nil || l.Job.Fence != 2 {
		t.Fatalf("retry claim %#v %v", l, err)
	}
	if _, err := s.Complete(ctx, j.ID, "w", 1, Receipt{Schema: ReceiptSchema, Outcome: "done", JobID: j.ID, Attempt: 1, Fence: 1}); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("stale completion=%v", err)
	}
	_, err = s.Complete(ctx, j.ID, "w2", 2, Receipt{Schema: ReceiptSchema, Outcome: "shipped", JobID: j.ID, Attempt: 2, Fence: 2, BundleRef: "bundle", BundleDigest: "sha256:x"})
	if err != nil {
		t.Fatal(err)
	}
	done, err := s.Get(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Receipt == nil || done.Receipt.Countable ||
		done.Receipt.ApplicationID != "app" || done.Receipt.Queue != "work" ||
		done.Receipt.WorkerID != "w2" || done.Receipt.FinishedAt.IsZero() {
		t.Fatalf("terminal receipt = %#v", done.Receipt)
	}
}

func TestSQLiteMigratesPreProducesCodeSchema(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE workqueue_jobs (
		id TEXT PRIMARY KEY,
		application_id TEXT NOT NULL,
		queue_name TEXT NOT NULL,
		idempotency_key TEXT NOT NULL,
		payload BLOB NOT NULL,
		payload_digest TEXT NOT NULL,
		capabilities TEXT NOT NULL,
		state TEXT NOT NULL,
		priority INTEGER NOT NULL,
		attempts INTEGER NOT NULL,
		max_attempts INTEGER NOT NULL,
		available_at INTEGER NOT NULL,
		lease_owner TEXT NOT NULL DEFAULT '',
		lease_expires_at INTEGER,
		fence INTEGER NOT NULL DEFAULT 0,
		receipt TEXT,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		UNIQUE(application_id, queue_name, idempotency_key)
	) STRICT`); err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(context.Background(), EnqueueRequest{
		ApplicationID: "app", Queue: "q", IdempotencyKey: "migration",
		ProducesCode: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !job.ProducesCode {
		t.Fatalf("migrated enqueue = %#v", job)
	}
}

func TestPostgresClaimUsesLeaseAndCapacity(t *testing.T) {
	ctx := context.Background()
	db := pgtest.Open(t)
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	n := 0
	s, err := NewPostgresStore(db, WithClock(func() time.Time { return now }), WithIDGenerator(func() string { n++; return fmt.Sprintf("pg-%d", n) }))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"one", "two"} {
		if _, err := s.Enqueue(ctx, EnqueueRequest{ApplicationID: "app", Queue: "q", IdempotencyKey: key, RequiredCapabilities: []string{"linux"}}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ClaimNext(ctx, ClaimRequest{ApplicationID: "app", Queue: "q", WorkerID: "worker", Capabilities: []string{"linux"}, AvailableCapacity: 1, LeaseDuration: time.Minute})
	if err != nil || first == nil {
		t.Fatalf("first claim: %#v %v", first, err)
	}
	second, err := s.ClaimNext(ctx, ClaimRequest{ApplicationID: "app", Queue: "q", WorkerID: "worker", Capabilities: []string{"linux"}, AvailableCapacity: 1, LeaseDuration: time.Minute})
	if err != nil || second != nil {
		t.Fatalf("capacity claim: %#v %v", second, err)
	}
	if _, err := s.Complete(ctx, first.Job.ID, "worker", first.Job.Fence, Receipt{Schema: ReceiptSchema, Outcome: "done", JobID: first.Job.ID, Attempt: first.Job.Attempts, Fence: first.Job.Fence}); err != nil {
		t.Fatal(err)
	}
	second, err = s.ClaimNext(ctx, ClaimRequest{ApplicationID: "app", Queue: "q", WorkerID: "worker", Capabilities: []string{"linux"}, AvailableCapacity: 1, LeaseDuration: time.Minute})
	if err != nil || second == nil {
		t.Fatalf("claim after completion: %#v %v", second, err)
	}
}

func TestPostgresConcurrentClaimsRespectWorkerCapacity(t *testing.T) {
	ctx := context.Background()
	db := pgtest.Open(t)
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	n := 0
	s, err := NewPostgresStore(
		db,
		WithClock(func() time.Time { return now }),
		WithIDGenerator(func() string {
			n++
			return fmt.Sprintf("capacity-%d", n)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"one", "two"} {
		if _, err := s.Enqueue(ctx, EnqueueRequest{
			ApplicationID: "app", Queue: "q", IdempotencyKey: key,
		}); err != nil {
			t.Fatal(err)
		}
	}

	start := make(chan struct{})
	results := make(chan *Lease, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			lease, err := s.ClaimNext(ctx, ClaimRequest{
				ApplicationID: "app", Queue: "q", WorkerID: "same-worker",
				AvailableCapacity: 1, LeaseDuration: time.Minute,
			})
			results <- lease
			errs <- err
		}()
	}
	close(start)
	claimed := 0
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if lease := <-results; lease != nil {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("concurrent claims = %d, want 1", claimed)
	}
}

func TestSQLiteExpiredLeaseReclaimed(t *testing.T) {
	ctx := context.Background()
	db, _ := sql.Open("sqlite", ":memory:")
	defer db.Close()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s, _ := NewSQLiteStore(db, WithClock(func() time.Time { return now }), WithIDGenerator(func() string { return "id" }))
	j, err := s.Enqueue(ctx, EnqueueRequest{
		ApplicationID: "a", Queue: "q", IdempotencyKey: "k", MaxAttempts: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.ClaimNext(ctx, ClaimRequest{ApplicationID: "a", Queue: "q", WorkerID: "dead", AvailableCapacity: 1, LeaseDuration: time.Second})
	if err != nil || l == nil {
		t.Fatalf("first claim: %#v %v", l, err)
	}
	now = now.Add(2 * time.Second)
	if _, err := s.Heartbeat(
		ctx, j.ID, "dead", l.Job.Fence, time.Second,
	); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("expired heartbeat = %v", err)
	}
	if _, err := s.Complete(ctx, j.ID, "dead", l.Job.Fence, Receipt{
		Schema: ReceiptSchema, Outcome: "done", JobID: j.ID,
		Attempt: l.Job.Attempts, Fence: l.Job.Fence,
	}); !errors.Is(err, ErrStaleLease) {
		t.Fatalf("expired completion = %v", err)
	}
	n, err := s.ClaimNext(ctx, ClaimRequest{ApplicationID: "a", Queue: "q", WorkerID: "new", AvailableCapacity: 1, LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if n != nil {
		t.Fatalf("expired lease bypassed retry backoff: %#v", n)
	}
	now = now.Add(time.Second)
	n, err = s.ClaimNext(ctx, ClaimRequest{ApplicationID: "a", Queue: "q", WorkerID: "new", AvailableCapacity: 1, LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if n == nil || n.Job.ID != j.ID || n.Job.Fence != l.Job.Fence+1 {
		t.Fatalf("reclaim %#v", n)
	}
}

func TestExpiredLeaseExhaustionIsTerminalOnBothDialects(t *testing.T) {
	tests := []struct {
		name string
		open func(*testing.T, func() time.Time) *SQLStore
	}{
		{
			name: "sqlite",
			open: func(t *testing.T, now func() time.Time) *SQLStore {
				db, err := sql.Open("sqlite", ":memory:")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				store, err := NewSQLiteStore(
					db, WithClock(now),
					WithIDGenerator(func() string { return "expired-sqlite" }),
				)
				if err != nil {
					t.Fatal(err)
				}
				return store
			},
		},
		{
			name: "postgres",
			open: func(t *testing.T, now func() time.Time) *SQLStore {
				store, err := NewPostgresStore(
					pgtest.Open(t), WithClock(now),
					WithIDGenerator(func() string { return "expired-postgres" }),
				)
				if err != nil {
					t.Fatal(err)
				}
				return store
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			current := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
			store := tt.open(t, func() time.Time { return current })
			job, err := store.Enqueue(ctx, EnqueueRequest{
				ApplicationID: "app", Queue: "q", IdempotencyKey: "one",
				MaxAttempts: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			lease, err := store.ClaimNext(ctx, ClaimRequest{
				ApplicationID: "app", Queue: "q", WorkerID: "worker",
				AvailableCapacity: 1, LeaseDuration: time.Second,
			})
			if err != nil || lease == nil {
				t.Fatalf("claim = %#v, %v", lease, err)
			}
			current = current.Add(2 * time.Second)
			reclaimed, err := store.ClaimNext(ctx, ClaimRequest{
				ApplicationID: "app", Queue: "q", WorkerID: "other",
				AvailableCapacity: 1, LeaseDuration: time.Second,
			})
			if err != nil || reclaimed != nil {
				t.Fatalf("exhausted reclaim = %#v, %v", reclaimed, err)
			}
			failed, err := store.Get(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if failed.State != StateFailed || failed.Attempts != 1 ||
				failed.Receipt == nil ||
				failed.Receipt.Reason != "lease_expired_max_attempts" {
				t.Fatalf("exhausted job = %#v", failed)
			}
		})
	}
}

func TestCodeProducingWorkRequiresRecoverableBundle(t *testing.T) {
	ctx := context.Background()
	db, _ := sql.Open("sqlite", ":memory:")
	defer db.Close()
	now := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	s, err := NewSQLiteStore(
		db,
		WithClock(func() time.Time { return now }),
		WithIDGenerator(func() string { return "code-job" }),
		WithBundleValidator(bundleValidatorFunc(func(
			_ context.Context, ref, digest string,
		) error {
			if ref != "bundle:portable" ||
				digest != "sha256:"+strings.Repeat("a", 64) {
				return errors.New("bundle is not retained")
			}
			return nil
		})),
	)
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.Enqueue(ctx, EnqueueRequest{
		ApplicationID: "app", Queue: "code", IdempotencyKey: "one",
		ProducesCode: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s.ClaimNext(ctx, ClaimRequest{
		ApplicationID: "app", Queue: "code", WorkerID: "worker",
		AvailableCapacity: 1, LeaseDuration: time.Minute,
	})
	if err != nil || lease == nil {
		t.Fatalf("claim = %#v, %v", lease, err)
	}
	receipt := Receipt{
		Schema: ReceiptSchema, Outcome: "done", JobID: job.ID,
		Attempt: lease.Job.Attempts, Fence: lease.Job.Fence,
	}
	if _, err := s.Complete(
		ctx, job.ID, "worker", lease.Job.Fence, receipt,
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bundle-free code completion = %v", err)
	}
	receipt.BundleRef = "bundle:portable"
	receipt.BundleDigest = "sha256:abc123"
	if _, err := s.Complete(
		ctx, job.ID, "worker", lease.Job.Fence, receipt,
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("short bundle digest completion = %v", err)
	}
	receipt.BundleDigest = "sha256:" + strings.Repeat("a", 64)
	receipt.BundleRef = "bundle:missing"
	if _, err := s.Complete(
		ctx, job.ID, "worker", lease.Job.Fence, receipt,
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unretained bundle completion = %v", err)
	}
	receipt.BundleRef = "bundle:portable"
	done, err := s.Complete(ctx, job.ID, "worker", lease.Job.Fence, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if done.Receipt == nil || !done.Receipt.Countable ||
		done.Receipt.BundleRef != "bundle:portable" {
		t.Fatalf("code receipt = %#v", done.Receipt)
	}
}
