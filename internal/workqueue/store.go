// Package workqueue provides a durable, story-neutral leased work broker.
package workqueue

import (
	"context"
	"errors"
	"time"
)

type State string

const (
	ReceiptSchema        = "kitsoki/workqueue-receipt/v1"
	StateQueued    State = "queued"
	StateLeased    State = "leased"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	StateCancelled State = "cancelled"
)

var (
	ErrNotFound   = errors.New("workqueue: not found")
	ErrConflict   = errors.New("workqueue: idempotency conflict")
	ErrStaleLease = errors.New("workqueue: stale lease")
	ErrInvalid    = errors.New("workqueue: invalid request")
)

// Job is the durable, operator-visible work record. A lease is never proof
// that its process survived a restart; only expiry makes it claimable again.
type Job struct {
	ID, ApplicationID, Queue, IdempotencyKey string
	Payload, PayloadDigest                   []byte
	RequiredCapabilities                     []string
	State                                    State
	Priority, Attempts, MaxAttempts          int
	AvailableAt, CreatedAt, UpdatedAt        time.Time
	LeaseOwner                               string
	LeaseExpiresAt                           *time.Time
	Fence                                    int64
	Receipt                                  *Receipt
}

type EnqueueRequest struct {
	ApplicationID, Queue, IdempotencyKey string
	Payload                              []byte
	RequiredCapabilities                 []string
	Priority, MaxAttempts                int
	AvailableAt                          time.Time
}

type ClaimRequest struct {
	ApplicationID, Queue, WorkerID string
	Capabilities                   []string
	AvailableCapacity              int
	LeaseDuration                  time.Duration
}

type Lease struct{ Job Job }

type Receipt struct {
	Schema, Outcome, JobID, BundleRef, BundleDigest string
	Attempt                                         int
	Fence                                           int64
}

type Failure struct {
	Retryable bool
	Message   string
	Receipt   Receipt
}

type ListFilter struct {
	ApplicationID, Queue string
	States               []State
	Limit                int
}

type Store interface {
	Enqueue(context.Context, EnqueueRequest) (Job, error)
	ClaimNext(context.Context, ClaimRequest) (*Lease, error)
	Heartbeat(context.Context, string, string, int64, time.Duration) (Job, error)
	Complete(context.Context, string, string, int64, Receipt) (Job, error)
	Fail(context.Context, string, string, int64, Failure) (Job, error)
	Cancel(context.Context, string, Receipt) (Job, error)
	Get(context.Context, string) (Job, error)
	List(context.Context, ListFilter) ([]Job, error)
}
