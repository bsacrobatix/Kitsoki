// Package environment defines the provider-neutral environment execution
// boundary used by deployment Stories. It deliberately contains no provider,
// remote-execution, credential, or process implementation.
package environment

import (
	"context"
	"time"
)

// Host is the narrow Story-facing boundary. Implementations may adapt a cloud
// provider, an on-prem topology, or a deterministic fake, but Stories only
// reason about resources, operations, postconditions, and durable receipts.
//
// Submit is idempotent by IdempotencyKey within a host. Poll is deliberately
// separate from Submit: a caller must retain and inspect the returned receipt
// rather than infer success from request acceptance.
type Host interface {
	Read(context.Context, ReadRequest) (ReadResult, error)
	Submit(context.Context, SubmitRequest) (SubmitResult, error)
	Poll(context.Context, PollRequest) (PollResult, error)
	Verify(context.Context, VerifyRequest) (VerifyResult, error)
}

type ReadRequest struct {
	ResourceRef string
}

type ReadResult struct {
	Outcome  Outcome
	Resource Resource
}

// Action is intentionally provider-neutral. Kind and Input are owned by a
// topology/provider adapter, while TargetRef and IdempotencyKey make retries
// observable to the generic Story layer.
type Action struct {
	Kind      string
	TargetRef string
	Input     map[string]string
}

type SubmitRequest struct {
	Action         Action
	IdempotencyKey string
}

type SubmitResult struct {
	Outcome Outcome
	Receipt Receipt
}

type PollRequest struct {
	OperationRef string
}

type PollResult struct {
	Outcome   Outcome
	Operation Operation
}

// Postcondition is a generic, explicit deploy proof. Healthy and Current are
// separate on purpose: a prior deployment can be healthy without matching the
// requested version/configuration.
type Postcondition struct {
	ResourceRef    string
	RequireHealthy bool
	RequireCurrent bool
	Attributes     map[string]string
}

type VerifyRequest struct {
	Postcondition Postcondition
}

type VerifyResult struct {
	Outcome  Outcome
	Resource Resource
}

type Resource struct {
	Ref        string
	Kind       string
	Healthy    bool
	Current    bool
	Attributes map[string]string
}

type OperationState string

const (
	OperationPending   OperationState = "pending"
	OperationRunning   OperationState = "running"
	OperationSucceeded OperationState = "succeeded"
	OperationFailed    OperationState = "failed"
)

type Operation struct {
	Ref       string
	Action    Action
	State     OperationState
	Submitted time.Time
	Updated   time.Time
	Outcome   Outcome
}

// Receipt proves that a request was accepted or replayed. It never claims the
// operation succeeded; callers must Poll and Verify the required proof.
type Receipt struct {
	RequestRef     string
	OperationRef   string
	IdempotencyKey string
	AcceptedAt     time.Time
	Replayed       bool
}

type OutcomeStatus string

const (
	OutcomePassed  OutcomeStatus = "passed"
	OutcomeBlocked OutcomeStatus = "blocked"
	OutcomeFailed  OutcomeStatus = "failed"
)

type ReasonCode string

const (
	ReasonNone              ReasonCode = ""
	ReasonNotFound          ReasonCode = "resource_not_found"
	ReasonUnavailable       ReasonCode = "environment_host_unavailable"
	ReasonOperationPending  ReasonCode = "operation_pending"
	ReasonOperationFailed   ReasonCode = "operation_failed"
	ReasonNotHealthy        ReasonCode = "resource_not_healthy"
	ReasonNotCurrent        ReasonCode = "resource_not_current"
	ReasonAttributeMismatch ReasonCode = "postcondition_attribute_mismatch"
	ReasonInvalidRequest    ReasonCode = "invalid_environment_request"
)

type Reason struct {
	Code     ReasonCode
	Message  string
	Recovery string
}

type Evidence struct {
	Kind   string
	Ref    string
	Detail string
}

// Outcome is the stable, diagnostic-bearing result surface. Expected refusal
// conditions use it rather than Go errors so Story guards retain named reasons.
type Outcome struct {
	Status   OutcomeStatus
	Reason   Reason
	Evidence []Evidence
}

func Passed(evidence ...Evidence) Outcome {
	return Outcome{Status: OutcomePassed, Evidence: evidence}
}

func Blocked(code ReasonCode, message, recovery string, evidence ...Evidence) Outcome {
	return Outcome{Status: OutcomeBlocked, Reason: Reason{Code: code, Message: message, Recovery: recovery}, Evidence: evidence}
}

func Failed(code ReasonCode, message, recovery string, evidence ...Evidence) Outcome {
	return Outcome{Status: OutcomeFailed, Reason: Reason{Code: code, Message: message, Recovery: recovery}, Evidence: evidence}
}
