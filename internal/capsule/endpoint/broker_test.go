package endpoint

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAllocateIsTransactionalAndReservationIsGeneric(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	b := Broker{Store: NewMemoryStore(), Now: func() time.Time { return now }}
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

func TestConcurrentAllocationsNoOverlapOrCrossKill(t *testing.T) {
	ctx := context.Background()
	b := Broker{Store: NewMemoryStore()}
	const n = 32
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := b.Allocate(ctx, []Request{request(fmt.Sprintf("runtime-%d", i), "app", "http", endpoint(uint16(6000+i)))})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	leases, err := b.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != n {
		t.Fatalf("leases=%d, want %d", len(leases), n)
	}
	if err := b.Release(ctx, leases[0].Handle()); err != nil {
		t.Fatal(err)
	}
	leases, err = b.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != n-1 {
		t.Fatalf("release removed %d leases, want exactly one", n-len(leases))
	}
	if _, err := b.Allocate(ctx, []Request{request("contender", "app", "http", leases[0].Endpoint)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected active endpoint conflict, got %v", err)
	}
}

func TestConcurrentSameEndpointHasOneWinner(t *testing.T) {
	ctx := context.Background()
	b := Broker{Store: NewMemoryStore()}
	const contenders = 24
	var wg sync.WaitGroup
	results := make(chan error, contenders)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := b.Allocate(ctx, []Request{request(fmt.Sprintf("contender-%d", i), "app", "http", endpoint(6150))})
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
			continue
		}
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("unexpected allocation result: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("same endpoint successes=%d, want 1", successes)
	}
}

func TestLeaseOwnerGenerationRenewReleaseAndReap(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	b := Broker{Store: NewMemoryStore(), Now: func() time.Time { return now }, Liveness: LivenessFunc(func(context.Context, string, uint64) bool { return false })}
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
	now = now.Add(2 * time.Minute)
	reaped, err := b.Reap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(reaped) != 1 || reaped[0].ID != lease.ID {
		t.Fatalf("unexpected reaped leases %#v", reaped)
	}
}

func TestListenerPolicyRejectsPortFallbackAndUndeclaredListeners(t *testing.T) {
	lease := Lease{RuntimeID: "runtime", Generation: 2, Endpoint: endpoint(6300)}
	if err := ValidateListenerPolicy(lease, ListenerAttestation{RuntimeID: "runtime", Generation: 2, Listeners: []Endpoint{endpoint(6301)}}); !errors.Is(err, ErrListenerPolicy) {
		t.Fatalf("expected typed listener error, got %v", err)
	}
	if err := ValidateListenerPolicy(lease, ListenerAttestation{RuntimeID: "runtime", Generation: 2, Listeners: []Endpoint{endpoint(6300), endpoint(6301)}}); !errors.Is(err, ErrListenerPolicy) {
		t.Fatalf("expected undeclared listener rejection, got %v", err)
	}
	if err := ValidateListenerPolicy(lease, ListenerAttestation{RuntimeID: "runtime", Generation: 2, Listeners: []Endpoint{endpoint(6300)}}); err != nil {
		t.Fatal(err)
	}
}

func TestSQLiteStoreSharesMachineAuthorityAcrossBrokerInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "endpoints.db")
	one, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	two, err := OpenSQLite(path)
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
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

func endpoint(port uint16) Endpoint {
	return Endpoint{Protocol: "tcp", Address: "127.0.0.1", Port: port, Exposure: "review"}
}
func request(runtime, service, role string, e Endpoint) Request {
	return Request{RuntimeID: runtime, Service: service, Role: role, Owner: "runtime-owner", Generation: 1, SourceDigest: "sha256:source", Endpoint: e, TTL: time.Minute}
}
