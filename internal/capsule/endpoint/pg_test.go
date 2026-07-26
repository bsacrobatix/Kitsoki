// Postgres-store tests: the same broker behaviors broker_test.go pins on the
// memory and SQLite stores, run against PostgresStore over a per-test
// database from internal/dbruntime/pgtest (skips when no Postgres can start).
package endpoint

import (
	"context"
	"errors"
	"testing"
	"time"

	"kitsoki/internal/dbruntime/pgtest"
)

func newPGStore(t *testing.T) *PostgresStore {
	t.Helper()
	store, err := NewPostgresStore(pgtest.Open(t))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestPostgresAllocateIsTransactionalAndReservationIsGeneric(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	b := Broker{Store: newPGStore(t), Now: func() time.Time { return now }}
	reserved := endpoint(5100)
	if err := b.Reserve(ctx, Reservation{Endpoint: reserved, Description: "protected preview"}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Allocate(ctx, []Request{request("protected-main", "portal", "http", reserved), request("protected-main", "api", "http", endpoint(5201))}); !errors.Is(err, ErrReserved) {
		t.Fatalf("expected reservation error, got %v", err)
	}
	leases, err := b.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 0 {
		t.Fatalf("transaction leaked %d leases", len(leases))
	}
}

func TestPostgresLeaseOwnerGenerationRenewReleaseAndReap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	store := newPGStore(t)
	b := Broker{Store: store, Now: func() time.Time { return now }, Liveness: LivenessFunc(func(context.Context, string, uint64) bool { return false })}
	leases, err := b.Allocate(ctx, []Request{request("runtime", "app", "http", endpoint(6200))})
	if err != nil {
		t.Fatal(err)
	}
	lease := leases[0]
	wrong := lease.Handle()
	wrong.Generation++
	if _, err := b.Renew(ctx, wrong, time.Minute); !errors.Is(err, ErrLease) {
		t.Fatalf("expected stale generation rejection, got %v", err)
	}
	renewed, err := b.Renew(ctx, lease.Handle(), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !renewed.ExpiresAt.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("renewed expiry %v, want %v", renewed.ExpiresAt, now.Add(5*time.Minute))
	}
	now = now.Add(10 * time.Minute)
	reaped, err := b.Reap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(reaped) != 1 || reaped[0].ID != lease.ID {
		t.Fatalf("unexpected reaped leases %#v", reaped)
	}
	if err := b.Release(ctx, lease.Handle()); !errors.Is(err, ErrLease) {
		t.Fatalf("expected release of reaped lease to fail, got %v", err)
	}
}

func TestPostgresIdempotentRoleReallocationAndConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b := Broker{Store: newPGStore(t)}
	req := request("runtime", "app", "http", endpoint(6250))
	first, err := b.Allocate(ctx, []Request{req})
	if err != nil {
		t.Fatal(err)
	}
	// Re-allocating the identical logical role returns the existing lease.
	again, err := b.Allocate(ctx, []Request{req})
	if err != nil {
		t.Fatal(err)
	}
	if again[0].ID != first[0].ID {
		t.Fatalf("idempotent reallocation minted a new lease: %s vs %s", again[0].ID, first[0].ID)
	}
	// Same logical role at a different endpoint is a conflict.
	moved := req
	moved.Endpoint = endpoint(6251)
	if _, err := b.Allocate(ctx, []Request{moved}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected logical role conflict, got %v", err)
	}
	// A different runtime contending for the held endpoint is a conflict.
	if _, err := b.Allocate(ctx, []Request{request("contender", "app", "http", endpoint(6250))}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected endpoint conflict, got %v", err)
	}
}

func TestPostgresStoreSharesMachineAuthorityAcrossBrokerInstances(t *testing.T) {
	t.Parallel()
	db := pgtest.Open(t)
	one, err := NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	two, err := NewPostgresStore(db)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	b1, b2 := Broker{Store: one}, Broker{Store: two}
	if _, err := b1.Allocate(ctx, []Request{request("one", "app", "http", endpoint(6400))}); err != nil {
		t.Fatal(err)
	}
	if _, err := b2.Allocate(ctx, []Request{request("two", "app", "http", endpoint(6400))}); !errors.Is(err, ErrConflict) {
		t.Fatalf("want cross-broker conflict, got %v", err)
	}
	if err := b2.Reserve(ctx, Reservation{Endpoint: endpoint(6400), Description: "late"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("want reservation lease conflict, got %v", err)
	}
}
