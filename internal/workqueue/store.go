// Package workqueue provides a durable, story-neutral leased work broker.
package workqueue

import (
	"context"
	"errors"
	"time"
)

type State string

const (
	ReceiptSchema        = "kitsoki/work-queue-receipt/v1"
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
	ProducesCode                             bool
	AvailableAt, CreatedAt, UpdatedAt        time.Time
	LeaseOwner                               string
	LeaseExpiresAt                           *time.Time
	Fence                                    int64
	Receipt                                  *Receipt
	// Replayed is a transient enqueue result and is never persisted.
	Replayed bool
}

type EnqueueRequest struct {
	ApplicationID, Queue, IdempotencyKey string
	Payload                              []byte
	RequiredCapabilities                 []string
	Priority, MaxAttempts                int
	ProducesCode                         bool
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
	Schema               string    `json:"schema"`
	Outcome              string    `json:"outcome"`
	Reason               string    `json:"reason,omitempty"`
	JobID                string    `json:"job_id"`
	ApplicationID        string    `json:"application_id,omitempty"`
	Queue                string    `json:"queue"`
	WorkerID             string    `json:"worker_id,omitempty"`
	Attempt              int       `json:"attempt"`
	Fence                int64     `json:"fence"`
	FinishedAt           time.Time `json:"finished_at"`
	TraceRef             string    `json:"trace_ref,omitempty"`
	ArtifactHandles      []string  `json:"artifact_handles,omitempty"`
	BundleRef            string    `json:"bundle_ref,omitempty"`
	BundleDigest         string    `json:"bundle_digest,omitempty"`
	BundleKind           string    `json:"bundle_kind,omitempty"`
	RequiredCapabilities []string  `json:"required_capabilities,omitempty"`
	Countable            bool      `json:"countable"`
}

type Failure struct {
	Retryable bool
	Message   string
	Receipt   Receipt
}

// BundleValidator proves that code evidence is retained by a durable,
// independently resolvable store and that the receipt digest matches it.
// Syntax alone is never sufficient to make work countable.
type BundleValidator interface {
	ValidateBundle(context.Context, string, string) (string, error)
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
