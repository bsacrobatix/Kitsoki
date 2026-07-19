package endpoint

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryStore has the same all-or-nothing semantics as a daemon store and is
// intended for hermetic runtime/provider tests.
type MemoryStore struct {
	mu           sync.Mutex
	reservations map[string]Reservation
	leases       map[string]Lease
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{reservations: map[string]Reservation{}, leases: map[string]Lease{}}
}

func (s *MemoryStore) Reserve(_ context.Context, r Reservation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := endpointKey(r.Endpoint)
	if lease, ok := s.findEndpoint(r.Endpoint); ok {
		return fmt.Errorf("%w: %s is leased by %s", ErrConflict, r.Endpoint, lease.RuntimeID)
	}
	if existing, ok := s.reservations[key]; ok && existing != r {
		return fmt.Errorf("%w: %s", ErrReserved, r.Endpoint)
	}
	s.reservations[key] = r
	return nil
}
func (s *MemoryStore) Allocate(_ context.Context, now time.Time, requests []Request) ([]Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range requests {
		if _, ok := s.reservations[endpointKey(r.Endpoint)]; ok {
			return nil, fmt.Errorf("%w: %s", ErrReserved, r.Endpoint)
		}
		for j := 0; j < i; j++ {
			if endpointKey(requests[j].Endpoint) == endpointKey(r.Endpoint) {
				return nil, fmt.Errorf("%w: duplicate requested endpoint %s", ErrConflict, r.Endpoint)
			}
		}
		if existing, ok := s.findRole(r); ok {
			if !sameRequest(existing, r) {
				return nil, fmt.Errorf("%w: logical role %s/%s/%s is owned by another lease", ErrConflict, r.RuntimeID, r.Service, r.Role)
			}
			continue
		}
		if existing, ok := s.findEndpoint(r.Endpoint); ok {
			return nil, fmt.Errorf("%w: %s is leased by %s", ErrConflict, r.Endpoint, existing.RuntimeID)
		}
	}
	leases := make([]Lease, 0, len(requests))
	for _, r := range requests {
		if existing, ok := s.findRole(r); ok {
			leases = append(leases, existing)
			continue
		}
		lease := Lease{Schema: Schema, ID: leaseID(r), RuntimeID: r.RuntimeID, Service: r.Service, Role: r.Role, Owner: r.Owner, Generation: r.Generation, SourceDigest: r.SourceDigest, Endpoint: r.Endpoint, AcquiredAt: now, ExpiresAt: now.Add(r.TTL)}
		s.leases[lease.ID] = lease
		leases = append(leases, lease)
	}
	return leases, nil
}
func (s *MemoryStore) Renew(_ context.Context, now time.Time, h Handle, ttl time.Duration) (Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lease, ok := s.leases[h.ID]
	if !ok {
		return Lease{}, fmt.Errorf("%w: %s", ErrLease, h.ID)
	}
	if lease.Owner != h.Owner || lease.Generation != h.Generation {
		return Lease{}, fmt.Errorf("%w: %s", ErrLease, h.ID)
	}
	lease.ExpiresAt = now.Add(ttl)
	s.leases[h.ID] = lease
	return lease, nil
}
func (s *MemoryStore) Release(_ context.Context, h Handle) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	lease, ok := s.leases[h.ID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrLease, h.ID)
	}
	if lease.Owner != h.Owner || lease.Generation != h.Generation {
		return fmt.Errorf("%w: %s", ErrLease, h.ID)
	}
	delete(s.leases, h.ID)
	return nil
}
func (s *MemoryStore) List(_ context.Context) ([]Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return sortedLeases(s.leases), nil
}
func (s *MemoryStore) Reap(ctx context.Context, now time.Time, live Liveness) ([]Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var reaped []Lease
	for id, lease := range s.leases {
		if !lease.ExpiresAt.After(now) && !live.Live(ctx, lease.RuntimeID, lease.Generation) {
			delete(s.leases, id)
			reaped = append(reaped, lease)
		}
	}
	sort.Slice(reaped, func(i, j int) bool { return reaped[i].ID < reaped[j].ID })
	return reaped, nil
}
func (s *MemoryStore) findEndpoint(endpoint Endpoint) (Lease, bool) {
	for _, l := range s.leases {
		if endpointKey(l.Endpoint) == endpointKey(endpoint) {
			return l, true
		}
	}
	return Lease{}, false
}
func (s *MemoryStore) findRole(r Request) (Lease, bool) {
	for _, l := range s.leases {
		if l.RuntimeID == r.RuntimeID && l.Service == r.Service && l.Role == r.Role {
			return l, true
		}
	}
	return Lease{}, false
}
func endpointKey(e Endpoint) string {
	return e.Protocol + "\x00" + e.Address + "\x00" + fmt.Sprint(e.Port)
}
func leaseID(r Request) string { return r.RuntimeID + ":" + r.Service + ":" + r.Role }
func sameRequest(l Lease, r Request) bool {
	return l.Owner == r.Owner && l.Generation == r.Generation && l.SourceDigest == r.SourceDigest && endpointKey(l.Endpoint) == endpointKey(r.Endpoint)
}
func sortedLeases(values map[string]Lease) []Lease {
	out := make([]Lease, 0, len(values))
	for _, l := range values {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
