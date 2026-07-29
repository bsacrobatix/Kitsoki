package reviewedfeedback

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	appplatform "kitsoki/internal/application"
	"kitsoki/internal/host"
)

type Backend struct {
	Binding    Binding
	Ledger     Ledger
	Locators   LocatorResolver
	Store      DispatchStore
	Dispatcher Dispatcher

	mu sync.Mutex
}

func (b *Backend) ListReviewed(
	ctx context.Context,
	request host.FeedbackListRequest,
) ([]host.ReviewedFeedbackReport, error) {
	if err := b.validate(request.Scope); err != nil {
		return nil, err
	}
	if request.Selector != "current" {
		return nil, fmt.Errorf("reviewed feedback selector cannot widen the configured application scope")
	}
	reports, err := b.Ledger.ListReviewed(ctx, request.Scope, request.Limit)
	if err != nil {
		return nil, fmt.Errorf("reviewed feedback ledger is unavailable: %w", err)
	}
	return reports, nil
}

func (b *Backend) Dispatch(
	ctx context.Context,
	request host.FeedbackDispatchRequest,
) (host.FeedbackDispatchReceipt, error) {
	if err := b.validate(request.Scope); err != nil {
		return host.FeedbackDispatchReceipt{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	report, err := b.Ledger.ResolveReviewed(ctx, request.Scope, request.ReportRef)
	if err != nil {
		return host.FeedbackDispatchReceipt{}, fmt.Errorf(
			"reviewed feedback report is unavailable in the resolved application scope",
		)
	}
	locators, err := b.Locators.Resolve(
		request.ResumeMode, request.ResumeWorkspace, request.RetryBrief,
	)
	if err != nil {
		return host.FeedbackDispatchReceipt{}, err
	}
	digest, err := dispatchDigest(b.Binding, request, report)
	if err != nil {
		return host.FeedbackDispatchReceipt{}, err
	}
	jobID := feedbackJobID(request.Scope.ApplicationID, request.DispatchID)
	state, err := b.Store.Claim(ctx, DispatchState{
		ScopeID: request.Scope.ApplicationID, DispatchID: request.DispatchID,
		RequestDigest: digest, JobID: jobID, Status: DispatchPending,
	})
	if err != nil {
		return host.FeedbackDispatchReceipt{}, fmt.Errorf("reviewed feedback dispatch state is unavailable: %w", err)
	}
	if state.RequestDigest != digest {
		return host.FeedbackDispatchReceipt{}, fmt.Errorf(
			"feedback dispatch id was already used for a different reviewed request",
		)
	}
	if state.JobID != jobID {
		return host.FeedbackDispatchReceipt{}, fmt.Errorf(
			"feedback dispatch state has an unstable durable job identity",
		)
	}
	if state.Status == DispatchCompleted {
		return host.FeedbackDispatchReceipt{JobID: state.JobID, Receipts: state.Receipts}, nil
	}
	if !state.Claimed {
		return host.FeedbackDispatchReceipt{}, fmt.Errorf(
			"reviewed feedback dispatch is already in progress",
		)
	}
	idempotencyKey := feedbackIdempotencyKey(
		request.Scope.ApplicationID, request.DispatchID,
	)
	dispatched, err := b.Dispatcher.Dispatch(ctx, DispatchPlan{
		JobID: jobID, DispatchID: request.DispatchID,
		SourceApplication: request.Scope.ApplicationID,
		TargetApplication: b.Binding.TargetApplication,
		TargetHandler:     b.Binding.TargetHandler, TargetAction: b.Binding.TargetAction,
		Report: report, ResumeMode: request.ResumeMode,
		WorkspacePath: locators.WorkspacePath, RetryBriefPath: locators.RetryBriefPath,
		ServerActor: ServerActor, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		_ = b.Store.Interrupt(
			ctx, request.Scope.ApplicationID, request.DispatchID, digest,
			"target_application_rejected",
		)
		return host.FeedbackDispatchReceipt{}, fmt.Errorf(
			"reviewed feedback target application rejected the dispatch",
		)
	}
	if dispatched.JobID != jobID {
		_ = b.Store.Interrupt(ctx, request.Scope.ApplicationID, request.DispatchID, digest, "dispatcher returned unstable job identity")
		return host.FeedbackDispatchReceipt{}, fmt.Errorf("feedback dispatcher returned unstable job identity")
	}
	if err := validateDispatchReceipts(
		dispatched.Receipts, jobID, b.Binding.TargetHandler,
		ServerActor, idempotencyKey,
	); err != nil {
		_ = b.Store.Interrupt(
			ctx, request.Scope.ApplicationID, request.DispatchID, digest,
			"noncanonical_application_receipt",
		)
		return host.FeedbackDispatchReceipt{}, err
	}
	if err := b.Store.Complete(
		ctx, request.Scope.ApplicationID, request.DispatchID, digest, dispatched.Receipts,
	); err != nil {
		return host.FeedbackDispatchReceipt{}, fmt.Errorf("reviewed feedback dispatch state is unavailable: %w", err)
	}
	return host.FeedbackDispatchReceipt{
		JobID: dispatched.JobID, Receipts: dispatched.Receipts,
	}, nil
}

func (b *Backend) validate(scope host.FeedbackScope) error {
	switch {
	case strings.TrimSpace(b.Binding.SourceApplication) == "" ||
		b.Binding.SourceApplication != scope.ApplicationID:
		return fmt.Errorf("reviewed feedback backend is outside the resolved application scope")
	case strings.TrimSpace(b.Binding.TargetApplication) == "" ||
		strings.TrimSpace(b.Binding.TargetHandler) == "" ||
		strings.TrimSpace(b.Binding.TargetAction) == "":
		return fmt.Errorf("reviewed feedback backend target is incomplete")
	case b.Ledger == nil || b.Locators == nil || b.Store == nil || b.Dispatcher == nil:
		return fmt.Errorf("reviewed feedback backend dependencies are unavailable")
	default:
		return nil
	}
}

func dispatchDigest(
	binding Binding,
	request host.FeedbackDispatchRequest,
	report ResolvedReport,
) (string, error) {
	raw, err := json.Marshal(struct {
		Schema             string
		Binding            Binding
		Scope              host.FeedbackScope
		Report             host.ReviewedFeedbackReport
		ResumeMode         string
		ResumeWorkspaceRef string
		RetryBriefRef      string
	}{
		Schema: "kitsoki/reviewed-feedback-dispatch/v1", Binding: binding,
		Scope: request.Scope, Report: report.Projection,
		ResumeMode:         request.ResumeMode,
		ResumeWorkspaceRef: request.ResumeWorkspace, RetryBriefRef: request.RetryBrief,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func feedbackJobID(scopeID, dispatchID string) string {
	sum := sha256.Sum256([]byte("feedback-job/v1\x00" + scopeID + "\x00" + dispatchID))
	return "feedback-" + hex.EncodeToString(sum[:12])
}

func feedbackIdempotencyKey(scopeID, dispatchID string) string {
	sum := sha256.Sum256([]byte("feedback-action/v1\x00" + scopeID + "\x00" + dispatchID))
	return "feedback-action-" + hex.EncodeToString(sum[:16])
}

func validateDispatchReceipts(
	receipts []appplatform.Receipt,
	jobID, handler, actor, idempotencyKey string,
) error {
	if len(receipts) != 1 {
		return fmt.Errorf("feedback dispatcher must return exactly one canonical application receipt")
	}
	receipt := receipts[0]
	finalized, err := appplatform.FinalizeReceipt(receipt)
	if err != nil || finalized.ID != receipt.ID ||
		receipt.Schema != appplatform.ReceiptSchema ||
		receipt.HandlerID != handler || receipt.Actor != actor ||
		receipt.IdempotencyKey != idempotencyKey ||
		receipt.SessionID != jobID ||
		receipt.Transport != appplatform.TransportEvent ||
		(receipt.Effect != appplatform.EffectWrite &&
			receipt.Effect != appplatform.EffectExternal) ||
		receipt.SemanticRef == "" || receipt.InputDigest == "" ||
		receipt.OutputDigest == "" || receipt.Outcome == "" {
		return fmt.Errorf("feedback dispatcher returned a non-canonical application receipt")
	}
	return nil
}
