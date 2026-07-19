package endpoint

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Broker composes machine-wide persistence with the runtime liveness authority.
// It contains no project-specific endpoint or port policy.
type Broker struct {
	Store    Store
	Now      func() time.Time
	Liveness Liveness
}

func (b Broker) Reserve(ctx context.Context, r Reservation) error {
	if b.Store == nil {
		return fmt.Errorf("%w: store is required", ErrInvalid)
	}
	if err := r.Endpoint.Validate(); err != nil {
		return err
	}
	return b.Store.Reserve(ctx, r)
}

func (b Broker) Allocate(ctx context.Context, requests []Request) ([]Lease, error) {
	if b.Store == nil {
		return nil, fmt.Errorf("%w: store is required", ErrInvalid)
	}
	if len(requests) == 0 {
		return nil, fmt.Errorf("%w: at least one endpoint is required", ErrInvalid)
	}
	for _, request := range requests {
		if err := request.Validate(); err != nil {
			return nil, err
		}
	}
	return b.Store.Allocate(ctx, b.now(), requests)
}

func (b Broker) Renew(ctx context.Context, h Handle, ttl time.Duration) (Lease, error) {
	if b.Store == nil {
		return Lease{}, fmt.Errorf("%w: store is required", ErrInvalid)
	}
	if h.ID == "" || h.Owner == "" || h.Generation == 0 || ttl <= 0 {
		return Lease{}, fmt.Errorf("%w: valid handle and ttl are required", ErrInvalid)
	}
	return b.Store.Renew(ctx, b.now(), h, ttl)
}
func (b Broker) Release(ctx context.Context, h Handle) error {
	if b.Store == nil {
		return fmt.Errorf("%w: store is required", ErrInvalid)
	}
	return b.Store.Release(ctx, h)
}
func (b Broker) List(ctx context.Context) ([]Lease, error) {
	if b.Store == nil {
		return nil, fmt.Errorf("%w: store is required", ErrInvalid)
	}
	leases, err := b.Store.List(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(leases, func(i, j int) bool { return leases[i].ID < leases[j].ID })
	return leases, nil
}
func (b Broker) Reap(ctx context.Context) ([]Lease, error) {
	if b.Store == nil {
		return nil, fmt.Errorf("%w: store is required", ErrInvalid)
	}
	if b.Liveness == nil {
		return nil, fmt.Errorf("%w: liveness is required for reap", ErrInvalid)
	}
	return b.Store.Reap(ctx, b.now(), b.Liveness)
}
func (b Broker) now() time.Time {
	if b.Now != nil {
		return b.Now().UTC()
	}
	return time.Now().UTC()
}
