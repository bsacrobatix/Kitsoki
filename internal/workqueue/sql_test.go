package workqueue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"kitsoki/internal/dbruntime/pgtest"

	_ "modernc.org/sqlite"
)

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

func TestSQLiteExpiredLeaseReclaimed(t *testing.T) {
	ctx := context.Background()
	db, _ := sql.Open("sqlite", ":memory:")
	defer db.Close()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s, _ := NewSQLiteStore(db, WithClock(func() time.Time { return now }), WithIDGenerator(func() string { return "id" }))
	j, err := s.Enqueue(ctx, EnqueueRequest{ApplicationID: "a", Queue: "q", IdempotencyKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.ClaimNext(ctx, ClaimRequest{ApplicationID: "a", Queue: "q", WorkerID: "dead", AvailableCapacity: 1, LeaseDuration: time.Second})
	if err != nil || l == nil {
		t.Fatalf("first claim: %#v %v", l, err)
	}
	now = now.Add(2 * time.Second)
	n, err := s.ClaimNext(ctx, ClaimRequest{ApplicationID: "a", Queue: "q", WorkerID: "new", AvailableCapacity: 1, LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if n == nil || n.Job.ID != j.ID || n.Job.Fence != l.Job.Fence+1 {
		t.Fatalf("reclaim %#v", n)
	}
}
