// Package reviewedfeedback adopts reviewed application feedback into a
// daemon-owned, application-scoped dispatch backend.
package reviewedfeedback

import (
	"context"
	"time"

	appplatform "kitsoki/internal/application"
	"kitsoki/internal/host"
)

const ServerActor = "kitsoki.feedback-daemon"

type Binding struct {
	SourceApplication string
	TargetApplication string
	TargetHandler     string
	TargetAction      string
}

type ResolvedReport struct {
	Projection host.ReviewedFeedbackReport
	Frame      uint64
}

type Ledger interface {
	ListReviewed(context.Context, host.FeedbackScope, int) ([]host.ReviewedFeedbackReport, error)
	ResolveReviewed(context.Context, host.FeedbackScope, string) (ResolvedReport, error)
}

type ResolvedLocators struct {
	WorkspacePath  string
	RetryBriefPath string
}

type LocatorResolver interface {
	Resolve(resumeMode, workspaceRef, retryBriefRef string) (ResolvedLocators, error)
}

type DispatchPlan struct {
	JobID             string
	DispatchID        string
	SourceApplication string
	TargetApplication string
	TargetHandler     string
	TargetAction      string
	Report            ResolvedReport
	ResumeMode        string
	WorkspacePath     string
	RetryBriefPath    string
	ServerActor       string
	IdempotencyKey    string
}

type DispatchResult struct {
	JobID    string
	Receipts []appplatform.Receipt
}

type Dispatcher interface {
	Dispatch(context.Context, DispatchPlan) (DispatchResult, error)
}

type DispatchStatus string

const (
	DispatchPending     DispatchStatus = "pending"
	DispatchCompleted   DispatchStatus = "completed"
	DispatchInterrupted DispatchStatus = "interrupted"
)

type DispatchState struct {
	ScopeID           string
	DispatchID        string
	RequestDigest     string
	JobID             string
	Status            DispatchStatus
	Claimed           bool
	Receipts          []appplatform.Receipt
	Attempt           int
	InterruptedReason string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type DispatchStore interface {
	Claim(context.Context, DispatchState) (DispatchState, error)
	Complete(context.Context, string, string, string, []appplatform.Receipt) error
	Interrupt(context.Context, string, string, string, string) error
	InterruptPending(context.Context, string) (int64, error)
}
