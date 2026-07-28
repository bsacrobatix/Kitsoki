package campaign

import (
	"context"
	"sync"
	"testing"
	"time"

	"kitsoki/internal/dbruntime/pgtest"
)

func openPGCampaignStore(t *testing.T) *SQLStore {
	t.Helper()
	store, err := NewPostgresStore(pgtest.Open(t))
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	return store
}

func TestPostgresStorePersistsScheduleAndInterruptsProcessWork(t *testing.T) {
	ctx := context.Background()
	store := openPGCampaignStore(t)
	start := time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC)
	if err := store.Reconcile(ctx, "runner", []Definition{testDefinition()}, start); err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimDue(ctx, "runner", start, 10)
	if err != nil || len(claims) != 1 {
		t.Fatalf("ClaimDue = %#v, %v", claims, err)
	}
	if n, err := store.InterruptRunning(ctx, "daemon_restarted", start.Add(time.Minute)); err != nil || n != 1 {
		t.Fatalf("InterruptRunning = %d, %v", n, err)
	}
	schedules, err := store.List(ctx, "runner", 10)
	if err != nil || len(schedules) != 1 {
		t.Fatalf("List = %#v, %v", schedules, err)
	}
	if schedules[0].Running != 0 || schedules[0].LastStatus != "interrupted" {
		t.Fatalf("restored schedule = %#v", schedules[0])
	}
}

func TestPostgresStoreClaimDueSkipsLockedSchedule(t *testing.T) {
	ctx := context.Background()
	store := openPGCampaignStore(t)
	start := time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC)
	if err := store.Reconcile(ctx, "runner", []Definition{testDefinition()}, start); err != nil {
		t.Fatal(err)
	}

	startGate := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan []Claim, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-startGate
			claims, err := store.ClaimDue(ctx, "runner", start, 1)
			if err != nil {
				errs <- err
				return
			}
			results <- claims
		}()
	}
	close(startGate)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	claimed := 0
	for claims := range results {
		claimed += len(claims)
	}
	if claimed != 1 {
		t.Fatalf("concurrent ClaimDue claimed %d schedules, want exactly 1", claimed)
	}
}

func TestPostgresStoreInterruptIsOwnerScoped(t *testing.T) {
	ctx := context.Background()
	db := pgtest.Open(t)
	first, err := NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC)
	if err := first.Reconcile(
		ctx, "runner", []Definition{testDefinition()}, start,
	); err != nil {
		t.Fatal(err)
	}
	claims, err := first.ClaimDue(ctx, "runner", start, 1)
	if err != nil || len(claims) != 1 {
		t.Fatalf("first claim = %#v, %v", claims, err)
	}
	if n, err := second.InterruptRunning(
		ctx, "second_daemon_started", start.Add(time.Minute),
	); err != nil || n != 0 {
		t.Fatalf("second interrupt = %d, %v", n, err)
	}
	schedules, err := second.List(ctx, "runner", 1)
	if err != nil || len(schedules) != 1 || schedules[0].Running != 1 {
		t.Fatalf("active owner schedule = %#v, %v", schedules, err)
	}
	if err := first.Complete(
		ctx, claims[0], "job-one", nil, start.Add(2*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStoreExpiredOwnerClaimIsRecovered(t *testing.T) {
	ctx := context.Background()
	db := pgtest.Open(t)
	start := time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC)
	current := start
	first, err := NewPostgresStore(db, WithClock(func() time.Time {
		return current
	}))
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewPostgresStore(db, WithClock(func() time.Time {
		return current
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Reconcile(
		ctx, "runner", []Definition{testDefinition()}, start,
	); err != nil {
		t.Fatal(err)
	}
	claims, err := first.ClaimDue(ctx, "runner", start, 1)
	if err != nil || len(claims) != 1 {
		t.Fatalf("first claim = %#v, %v", claims, err)
	}
	afterExpiry := start.Add(DefaultDispatchLease + time.Second)
	current = afterExpiry
	recovered, err := second.ClaimDue(ctx, "runner", afterExpiry, 1)
	if err != nil || len(recovered) != 1 {
		t.Fatalf("recovered claim = %#v, %v", recovered, err)
	}
	if recovered[0].IdempotencyKey == claims[0].IdempotencyKey ||
		recovered[0].OwnerID == claims[0].OwnerID {
		t.Fatalf("recovered claim identities = old %#v new %#v", claims[0], recovered[0])
	}
}

func TestPostgresStoreLeaseIgnoresCallerClockSkew(t *testing.T) {
	ctx := context.Background()
	store := openPGCampaignStore(t)
	start := time.Date(2026, 7, 26, 1, 0, 0, 0, time.UTC)
	if err := store.Reconcile(
		ctx, "runner", []Definition{testDefinition()}, start,
	); err != nil {
		t.Fatal(err)
	}
	claims, err := store.ClaimDue(ctx, "runner", start, 1)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim = %#v, %v", claims, err)
	}
	// A fast daemon clock must not expire the database-owned lease or allow a
	// second cadence claim past max concurrency.
	future := start.Add(48 * time.Hour)
	reclaimed, err := store.ClaimDue(ctx, "runner", future, 1)
	if err != nil || len(reclaimed) != 0 {
		t.Fatalf("clock-skew reclaim = %#v, %v", reclaimed, err)
	}
	if err := store.Complete(ctx, claims[0], "job-one", nil, future); err != nil {
		t.Fatal(err)
	}
	schedules, err := store.List(ctx, "runner", 1)
	if err != nil || len(schedules) != 1 ||
		schedules[0].LastStatus != "done" || schedules[0].Running != 0 {
		t.Fatalf("clock-skew completion = %#v, %v", schedules, err)
	}
}
