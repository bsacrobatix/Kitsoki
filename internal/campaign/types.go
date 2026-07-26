// Package campaign implements daemon-owned, graph-declared standing work.
//
// Campaign definitions are project data. The engine only knows the generic
// graph node contract in this package and dispatches declared story intents;
// it never executes product commands or scripts.
package campaign

import (
	"context"
	"time"
)

const (
	SchemaV1              = "kitsoki/campaign-snapshot/v1"
	DefaultTypeID         = "campaign"
	DefaultMaxDefinitions = 200
	DefaultMaxBytes       = 256 * 1024
)

// Action is the only executable campaign action. Story paths are resolved by
// the daemon's ordinary story registry, and Intent is dispatched exactly.
type Action struct {
	Story  string
	Intent string
	Input  map[string]any
}

// Budget bounds one campaign independently of any model/provider budget.
type Budget struct {
	MaxTicksPerDay int
	MaxConcurrency int
}

// Definition is one validated campaign node discovered from a project graph.
type Definition struct {
	ID             string
	AppID          string
	Title          string
	Enabled        bool
	Paused         bool
	Cadence        time.Duration
	Budget         Budget
	Action         Action
	SourceDigest   string
	DefinitionHash string
}

// Schedule is durable control state for one app-scoped campaign.
type Schedule struct {
	Definition
	NextDueAt      time.Time
	LastDispatchAt *time.Time
	TicksDay       string
	TicksToday     int
	Running        int
	LastJobRef     string
	LastStatus     string
	LastError      string
	UpdatedAt      time.Time
}

// Claim is a transactionally reserved cadence slot.
type Claim struct {
	Definition
	IdempotencyKey string
	DueAt          time.Time
}

// Dispatch records one claimed tick and its durable artifact-job reference.
type Dispatch struct {
	IdempotencyKey string
	AppID          string
	CampaignID     string
	DueAt          time.Time
	JobRef         string
	Status         string
	Error          string
	StartedAt      time.Time
	FinishedAt     *time.Time
}

// Store persists campaign schedules and tick claims across daemon restarts.
type Store interface {
	Reconcile(context.Context, string, []Definition, time.Time) error
	ClaimDue(context.Context, string, time.Time, int) ([]Claim, error)
	Complete(context.Context, Claim, string, error, time.Time) error
	InterruptRunning(context.Context, string, time.Time) (int64, error)
	List(context.Context, string, int) ([]Schedule, error)
}

// Source discovers validated definitions from one daemon-fixed graph source.
type Source interface {
	Discover(context.Context, string) ([]Definition, error)
}

// Dispatcher starts a declared story and invokes its exact intent. The
// returned ref must name a durable artifact job.
type Dispatcher interface {
	Dispatch(context.Context, Claim) (string, error)
}

// Scheduler owns process-bound watcher execution. Durable schedule state lives
// in Store and is therefore not confused with this process lifecycle.
type Scheduler interface {
	Submit(context.Context, string, func(context.Context) error) (string, error)
	Cancel(context.Context, string) error
	WaitIdle(context.Context) error
}
