package endpoint

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

const Schema = "capsule-endpoint-lease/v1"

var (
	ErrInvalid        = errors.New("capsule endpoint: invalid request")
	ErrConflict       = errors.New("capsule endpoint: endpoint conflict")
	ErrReserved       = errors.New("capsule endpoint: protected reservation")
	ErrLease          = errors.New("capsule endpoint: lease mismatch")
	ErrListenerPolicy = errors.New("capsule endpoint: listener policy")
)

// Endpoint is a concrete listener identity. The broker treats address as an
// exact bind address: wildcard and loopback bindings are deliberately distinct.
type Endpoint struct {
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Port     uint16 `json:"port"`
	Exposure string `json:"exposure,omitempty"` // internal|review|public
}

func (e Endpoint) String() string { return fmt.Sprintf("%s://%s:%d", e.Protocol, e.Address, e.Port) }

func (e Endpoint) Validate() error {
	if e.Protocol != "tcp" && e.Protocol != "udp" {
		return fmt.Errorf("%w: protocol %q", ErrInvalid, e.Protocol)
	}
	if e.Port == 0 {
		return fmt.Errorf("%w: port is required", ErrInvalid)
	}
	if strings.TrimSpace(e.Address) == "" {
		return fmt.Errorf("%w: bind address is required", ErrInvalid)
	}
	if e.Address != "*" && e.Address != "0.0.0.0" && e.Address != "::" {
		if _, err := netip.ParseAddr(e.Address); err != nil {
			return fmt.Errorf("%w: bind address %q: %v", ErrInvalid, e.Address, err)
		}
	}
	if e.Exposure != "" && e.Exposure != "internal" && e.Exposure != "review" && e.Exposure != "public" {
		return fmt.Errorf("%w: exposure %q", ErrInvalid, e.Exposure)
	}
	return nil
}

type Reservation struct {
	Endpoint    Endpoint `json:"endpoint"`
	Description string   `json:"description"`
}

// Request is an all-or-nothing runtime endpoint allocation. Roles are stable
// runtime identity; numeric ports are assigned deployment detail.
type Request struct {
	RuntimeID    string        `json:"runtime_id"`
	Service      string        `json:"service"`
	Role         string        `json:"role"`
	Owner        string        `json:"owner"`
	Generation   uint64        `json:"generation"`
	SourceDigest string        `json:"source_digest"`
	Endpoint     Endpoint      `json:"endpoint"`
	TTL          time.Duration `json:"ttl"`
}

func (r Request) Validate() error {
	for name, value := range map[string]string{"runtime_id": r.RuntimeID, "service": r.Service, "role": r.Role, "owner": r.Owner, "source_digest": r.SourceDigest} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: %s is required", ErrInvalid, name)
		}
	}
	if r.Generation == 0 {
		return fmt.Errorf("%w: generation is required", ErrInvalid)
	}
	if r.TTL <= 0 {
		return fmt.Errorf("%w: ttl must be positive", ErrInvalid)
	}
	return r.Endpoint.Validate()
}

type Lease struct {
	Schema       string    `json:"schema"`
	ID           string    `json:"id"`
	RuntimeID    string    `json:"runtime_id"`
	Service      string    `json:"service"`
	Role         string    `json:"role"`
	Owner        string    `json:"owner"`
	Generation   uint64    `json:"generation"`
	SourceDigest string    `json:"source_digest"`
	Endpoint     Endpoint  `json:"endpoint"`
	AcquiredAt   time.Time `json:"acquired_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type Handle struct {
	ID         string `json:"id"`
	Owner      string `json:"owner"`
	Generation uint64 `json:"generation"`
}

func (l Lease) Handle() Handle { return Handle{ID: l.ID, Owner: l.Owner, Generation: l.Generation} }

// Liveness is supplied by the runtime/process authority. A false response is
// a best-effort dead observation; reaping also requires lease expiry.
type Liveness interface {
	Live(context.Context, string, uint64) bool
}
type LivenessFunc func(context.Context, string, uint64) bool

func (f LivenessFunc) Live(ctx context.Context, id string, generation uint64) bool {
	return f(ctx, id, generation)
}

// Store is the durable machine-level authority. Allocate must atomically
// reserve every requested endpoint or persist none of them.
type Store interface {
	Reserve(context.Context, Reservation) error
	Allocate(context.Context, time.Time, []Request) ([]Lease, error)
	Renew(context.Context, time.Time, Handle, time.Duration) (Lease, error)
	Release(context.Context, Handle) error
	List(context.Context) ([]Lease, error)
	Reap(context.Context, time.Time, Liveness) ([]Lease, error)
}

// ListenerAttestation is returned by a runtime provider after strict-port
// startup. Listeners must enumerate every listener in the supervised process
// group, not just the intended endpoint.
type ListenerAttestation struct {
	RuntimeID  string
	Generation uint64
	Listeners  []Endpoint
}

func ValidateListenerPolicy(lease Lease, attestation ListenerAttestation) error {
	if attestation.RuntimeID != lease.RuntimeID || attestation.Generation != lease.Generation {
		return fmt.Errorf("%w: attestation belongs to another runtime generation", ErrListenerPolicy)
	}
	seen := false
	for _, listener := range attestation.Listeners {
		if err := listener.Validate(); err != nil {
			return fmt.Errorf("%w: invalid reported listener: %v", ErrListenerPolicy, err)
		}
		if listener.Protocol != lease.Endpoint.Protocol || listener.Address != lease.Endpoint.Address || listener.Port != lease.Endpoint.Port {
			return fmt.Errorf("%w: undeclared listener %s", ErrListenerPolicy, listener)
		}
		seen = true
	}
	if !seen {
		return fmt.Errorf("%w: assigned listener %s was not attested", ErrListenerPolicy, lease.Endpoint)
	}
	return nil
}
