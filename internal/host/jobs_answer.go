// Package host — host.jobs.answer_clarification built-in handler.
package host

import (
	"context"
	"fmt"
	"log/slog"

	"kitsoki/internal/trace"
)

// ClarificationAnswerer is the subset of *jobs.JobStore required by the
// host.jobs.answer_clarification handler.  Defined here to avoid importing
// the jobs package from host (which would create an import cycle).
// *jobs.JobStore satisfies this interface via structural typing.
type ClarificationAnswerer interface {
	// AnswerClarification stores the user's answer and flips the job back to
	// running status.  The answer is any JSON-serialisable value.
	AnswerClarification(ctx context.Context, id string, answer any) error
}

// clarificationAnswererKey is the unexported context key for the injected
// ClarificationAnswerer.
type clarificationAnswererKey struct{}

// WithClarificationAnswerer injects a ClarificationAnswerer into ctx so that
// host.jobs.answer_clarification can call AnswerClarification without
// importing the jobs package.  The orchestrator calls this before dispatching
// foreground host effects.
func WithClarificationAnswerer(ctx context.Context, ca ClarificationAnswerer) context.Context {
	return context.WithValue(ctx, clarificationAnswererKey{}, ca)
}

// ClarificationAnswererFromContext retrieves the ClarificationAnswerer from
// ctx, or nil if none was injected.
func ClarificationAnswererFromContext(ctx context.Context) ClarificationAnswerer {
	v, _ := ctx.Value(clarificationAnswererKey{}).(ClarificationAnswerer)
	return v
}

// AnswerClarificationHandler implements host.jobs.answer_clarification.
//
// Required args (passed via with:):
//   - job_id (string): the job awaiting clarification.
//   - answer (any):    the user's answer (string, number, or map).
//
// The handler calls ClarificationAnswerer.AnswerClarification which flips the
// DB row back to running and stores the answer.  The handler goroutine (blocked
// in host.RequestClarification's poll loop) will observe the answer on the next
// 200 ms tick and return.
//
// Returns Result{} on success; Result{Error: ...} on expected errors.
func AnswerClarificationHandler(ctx context.Context, args map[string]any) (Result, error) {
	ca := ClarificationAnswererFromContext(ctx)
	if ca == nil {
		return Result{Error: "host.jobs.answer_clarification: no ClarificationAnswerer in context (orchestrator wiring missing)"}, nil
	}

	jobID, _ := args["job_id"].(string)
	if jobID == "" {
		return Result{Error: "host.jobs.answer_clarification: job_id argument is required"}, nil
	}

	answer, hasAnswer := args["answer"]
	if !hasAnswer {
		return Result{Error: "host.jobs.answer_clarification: answer argument is required"}, nil
	}

	if err := ca.AnswerClarification(ctx, jobID, answer); err != nil {
		return Result{Error: fmt.Sprintf("host.jobs.answer_clarification: %v", err)}, nil
	}

	trace.FromContext(ctx).DebugContext(ctx, trace.EvJobClarificationAnswered,
		slog.String("job_id", jobID),
	)

	return Result{Data: map[string]any{"job_id": jobID, "answered": true}}, nil
}
