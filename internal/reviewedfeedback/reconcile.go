package reviewedfeedback

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	appplatform "kitsoki/internal/application"
	"kitsoki/internal/applicationfeedback"
	"kitsoki/internal/clock"
	"kitsoki/internal/host"
)

const (
	ReconcileSchema     = "kitsoki/feedback-reconcile/v1"
	IntakeReceiptSchema = "kitsoki/feedback-intake-receipt/v1"
	DefaultDrainLimit   = 50
	MaxDrainLimit       = 200
	MaxReconcileBytes   = 256 << 10

	OperationCampaign   = "reviewed_feedback_campaign"
	OperationIntake     = "feedback_intake"
	OperationFederation = "feedback_federation"
)

type ReconcileItem struct {
	ID     string
	Digest string
}

type ReconcileStatus string

const (
	ReconcilePending     ReconcileStatus = "pending"
	ReconcileCompleted   ReconcileStatus = "completed"
	ReconcileInterrupted ReconcileStatus = "interrupted"
)

type ReconcileClaim struct {
	Operation string
	AppID     string
	ItemID    string
	Digest    string
	Status    ReconcileStatus
	Result    ReconcileResult
	Claimed   bool
	Replayed  bool
	Attempt   int
}

type ReconcileStore interface {
	ClaimNext(context.Context, string, string, []ReconcileItem) (ReconcileClaim, error)
	Complete(context.Context, ReconcileClaim, ReconcileResult) error
	Interrupt(context.Context, ReconcileClaim, string) error
	InterruptPending(context.Context, string) (int64, error)
}

type ReconcileResult struct {
	Schema        string                `json:"schema"`
	Operation     string                `json:"operation"`
	Status        string                `json:"status"`
	ApplicationID string                `json:"application_id"`
	ReportRef     string                `json:"report_ref,omitempty"`
	JobID         string                `json:"job_id,omitempty"`
	Receipts      []appplatform.Receipt `json:"receipts,omitempty"`
	IntakeReceipt *IntakeReceipt        `json:"intake_receipt,omitempty"`
	Replayed      bool                  `json:"replayed,omitempty"`
}

func (r ReconcileResult) HostResult() host.Result {
	data := map[string]any{
		"schema": r.Schema, "operation": r.Operation, "status": r.Status,
		"application_id": r.ApplicationID, "replayed": r.Replayed,
	}
	if r.ReportRef != "" {
		data["report_ref"] = r.ReportRef
	}
	if r.JobID != "" {
		data["job_id"] = r.JobID
	}
	if len(r.Receipts) > 0 {
		data["receipts"] = r.Receipts
	}
	if r.IntakeReceipt != nil {
		data["intake_receipt"] = *r.IntakeReceipt
	}
	return host.Result{Data: data}
}

type IntakeReceipt struct {
	Schema        string `json:"schema"`
	ID            string `json:"id"`
	ApplicationID string `json:"application_id"`
	SourceID      string `json:"source_id"`
	ReportRef     string `json:"report_ref"`
	ReportDigest  string `json:"report_digest"`
}

// CaptureSource is the platform-owned typed intake seam. Implementations are
// registered by the daemon under an opaque source ID; host callers never
// select a source, transport, path, provider, actor, or credential.
// AcknowledgeFeedback must be idempotent for the stable receipt ID because a
// daemon can retry it after a crash between acknowledgement and SQLite commit.
type CaptureSource interface {
	ListFeedback(context.Context, CaptureRequest) ([]CapturedFeedback, error)
	AcknowledgeFeedback(context.Context, CaptureAcknowledgement) error
}

type CaptureRequest struct {
	SourceID      string
	ApplicationID string
	Limit         int
}

type CapturedFeedback struct {
	SourceRef string
	Report    applicationfeedback.Report
}

type CaptureAcknowledgement struct {
	SourceID      string
	ApplicationID string
	SourceRef     string
	Receipt       IntakeReceipt
}

type DispatchReconciler struct {
	Operation       string
	ConfigurationID string
	Backend         host.FeedbackBackend
	Store           ReconcileStore
	Scope           host.FeedbackScope
	Limit           int
}

func (s DispatchReconciler) Reconcile(ctx context.Context) (ReconcileResult, error) {
	if s.Operation != OperationCampaign && s.Operation != OperationFederation {
		return ReconcileResult{}, fmt.Errorf("feedback reconciliation operation is invalid")
	}
	if err := validateReconcileScope(s.Scope); err != nil {
		return ReconcileResult{}, err
	}
	if !safeToken(s.ConfigurationID, 180) {
		return ReconcileResult{}, fmt.Errorf("%s configured target identity is invalid", s.Operation)
	}
	if s.Backend == nil || s.Store == nil {
		return ReconcileResult{}, fmt.Errorf("%s configured service is unavailable", s.Operation)
	}
	limit, err := boundedDrainLimit(s.Limit)
	if err != nil {
		return ReconcileResult{}, err
	}
	reports, err := s.Backend.ListReviewed(ctx, host.FeedbackListRequest{
		Scope: s.Scope, Actor: ServerActor, Selector: "reviewed", Limit: limit + 1,
	})
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("%s reviewed source is unavailable", s.Operation)
	}
	if len(reports) > limit {
		return ReconcileResult{}, fmt.Errorf("%s selected more than its configured bound", s.Operation)
	}
	if len(reports) == 0 {
		return emptyReconcileResult(s.Operation, s.Scope.ApplicationID), nil
	}

	items := make([]ReconcileItem, 0, len(reports))
	byRef := make(map[string]host.ReviewedFeedbackReport, len(reports))
	for _, report := range reports {
		if !report.Reviewed || report.AppID != s.Scope.ApplicationID ||
			!safeReference(report.Ref, 180) {
			return ReconcileResult{}, fmt.Errorf("%s source returned an invalid scoped report", s.Operation)
		}
		if _, exists := byRef[report.Ref]; exists {
			return ReconcileResult{}, fmt.Errorf("%s source returned a duplicate report identity", s.Operation)
		}
		raw, marshalErr := json.Marshal(struct {
			Operation       string
			ConfigurationID string
			Report          host.ReviewedFeedbackReport
		}{
			Operation: s.Operation, ConfigurationID: s.ConfigurationID, Report: report,
		})
		if marshalErr != nil {
			return ReconcileResult{}, fmt.Errorf("%s could not normalize a reviewed report", s.Operation)
		}
		sum := sha256.Sum256(raw)
		items = append(items, ReconcileItem{ID: report.Ref, Digest: hex.EncodeToString(sum[:])})
		byRef[report.Ref] = report
	}
	claim, err := s.Store.ClaimNext(ctx, s.Operation, s.Scope.ApplicationID, items)
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("%s durable state is unavailable", s.Operation)
	}
	if claim.Replayed {
		out := claim.Result
		if err := validateReconcileResult(claim, out); err != nil {
			return ReconcileResult{}, fmt.Errorf("%s durable replay is invalid", s.Operation)
		}
		out.Replayed = true
		return out, nil
	}
	if !claim.Claimed {
		return ReconcileResult{}, fmt.Errorf("%s reconciliation is already in progress", s.Operation)
	}
	report, ok := byRef[claim.ItemID]
	if !ok {
		_ = s.Store.Interrupt(context.WithoutCancel(ctx), claim, "selected_report_missing")
		return ReconcileResult{}, fmt.Errorf("%s selected report is unavailable", s.Operation)
	}
	dispatchID := stableReconcileID(
		s.Operation, s.Scope.ApplicationID, s.ConfigurationID, report.Ref,
	)
	dispatched, err := s.Backend.Dispatch(ctx, host.FeedbackDispatchRequest{
		Scope: s.Scope, Actor: ServerActor, ReportRef: report.Ref,
		DispatchID: dispatchID, ResumeMode: "fresh",
	})
	if err != nil {
		_ = s.Store.Interrupt(context.WithoutCancel(ctx), claim, "target_rejected")
		return ReconcileResult{}, fmt.Errorf("%s configured target rejected the report", s.Operation)
	}
	if dispatched.JobID == "" || len(dispatched.Receipts) != 1 {
		_ = s.Store.Interrupt(context.WithoutCancel(ctx), claim, "noncanonical_receipt")
		return ReconcileResult{}, fmt.Errorf("%s configured target returned no canonical application receipt", s.Operation)
	}
	if err := validateReconcileReceipt(dispatched.JobID, dispatched.Receipts[0]); err != nil {
		_ = s.Store.Interrupt(context.WithoutCancel(ctx), claim, "noncanonical_receipt")
		return ReconcileResult{}, fmt.Errorf("%s configured target returned a noncanonical application receipt", s.Operation)
	}
	result := ReconcileResult{
		Schema: ReconcileSchema, Operation: s.Operation, Status: "dispatched",
		ApplicationID: s.Scope.ApplicationID, ReportRef: report.Ref,
		JobID: dispatched.JobID, Receipts: dispatched.Receipts,
	}
	if err := validateReconcileResult(claim, result); err != nil {
		_ = s.Store.Interrupt(context.WithoutCancel(ctx), claim, "invalid_result")
		return ReconcileResult{}, fmt.Errorf("%s result exceeded its integrity boundary", s.Operation)
	}
	if err := s.Store.Complete(context.WithoutCancel(ctx), claim, result); err != nil {
		_ = s.Store.Interrupt(context.WithoutCancel(ctx), claim, "completion_failed")
		return ReconcileResult{}, fmt.Errorf("%s durable completion is unavailable", s.Operation)
	}
	return result, nil
}

type IntakeReconciler struct {
	SourceID string
	Source   CaptureSource
	Ledger   JSONLLedger
	Store    ReconcileStore
	Scope    host.FeedbackScope
	Clock    clock.Clock
	Limit    int
}

func (s IntakeReconciler) Reconcile(ctx context.Context) (ReconcileResult, error) {
	if err := validateReconcileScope(s.Scope); err != nil {
		return ReconcileResult{}, err
	}
	if !safeToken(s.SourceID, 180) {
		return ReconcileResult{}, fmt.Errorf("feedback_intake configured source identity is invalid")
	}
	if s.Source == nil || s.Store == nil || s.Clock == nil || strings.TrimSpace(s.Ledger.Path) == "" {
		return ReconcileResult{}, fmt.Errorf("feedback_intake configured source is unavailable")
	}
	limit, err := boundedDrainLimit(s.Limit)
	if err != nil {
		return ReconcileResult{}, err
	}
	candidates, err := s.Source.ListFeedback(ctx, CaptureRequest{
		SourceID: s.SourceID, ApplicationID: s.Scope.ApplicationID, Limit: limit + 1,
	})
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("feedback_intake configured source is unavailable")
	}
	if len(candidates) > limit {
		return ReconcileResult{}, fmt.Errorf("feedback_intake selected more than its configured bound")
	}
	if len(candidates) == 0 {
		return emptyReconcileResult(OperationIntake, s.Scope.ApplicationID), nil
	}

	normalized := make(map[string]CapturedFeedback, len(candidates))
	items := make([]ReconcileItem, 0, len(candidates))
	for _, candidate := range candidates {
		clean, normalizeErr := s.Ledger.NormalizeCaptured(s.Scope, candidate.Report)
		if normalizeErr != nil {
			return ReconcileResult{}, fmt.Errorf("feedback_intake source returned an invalid or unsafe report")
		}
		if !safeReference(candidate.SourceRef, 180) {
			return ReconcileResult{}, fmt.Errorf("feedback_intake source returned an invalid opaque reference")
		}
		raw, marshalErr := json.Marshal(struct {
			SourceID  string
			SourceRef string
			Report    applicationfeedback.Report
		}{
			SourceID: s.SourceID, SourceRef: candidate.SourceRef, Report: clean,
		})
		if marshalErr != nil {
			return ReconcileResult{}, fmt.Errorf("feedback_intake could not normalize a report")
		}
		sum := sha256.Sum256(raw)
		digest := hex.EncodeToString(sum[:])
		itemID := clean.IdempotencyKey
		if prior, exists := normalized[itemID]; exists && prior.SourceRef != candidate.SourceRef {
			return ReconcileResult{}, fmt.Errorf("feedback_intake source returned conflicting duplicate reports")
		}
		normalized[itemID] = CapturedFeedback{SourceRef: candidate.SourceRef, Report: clean}
		items = append(items, ReconcileItem{ID: itemID, Digest: digest})
	}
	claim, err := s.Store.ClaimNext(ctx, OperationIntake, s.Scope.ApplicationID, items)
	if err != nil {
		return ReconcileResult{}, fmt.Errorf("feedback_intake durable state is unavailable")
	}
	if claim.Replayed {
		out := claim.Result
		if err := validateReconcileResult(claim, out); err != nil {
			return ReconcileResult{}, fmt.Errorf("feedback_intake durable replay is invalid")
		}
		out.Replayed = true
		return out, nil
	}
	if !claim.Claimed {
		return ReconcileResult{}, fmt.Errorf("feedback_intake reconciliation is already in progress")
	}
	candidate, ok := normalized[claim.ItemID]
	if !ok {
		_ = s.Store.Interrupt(context.WithoutCancel(ctx), claim, "selected_report_missing")
		return ReconcileResult{}, fmt.Errorf("feedback_intake selected report is unavailable")
	}
	reportRef, _, err := s.Ledger.AppendReviewed(
		context.WithoutCancel(ctx), s.Scope, candidate.Report, s.Clock.Now().UTC(),
	)
	if err != nil {
		_ = s.Store.Interrupt(context.WithoutCancel(ctx), claim, "ledger_rejected")
		return ReconcileResult{}, fmt.Errorf("feedback_intake canonical reviewed store rejected the report")
	}
	receipt := finalizeIntakeReceipt(IntakeReceipt{
		ApplicationID: s.Scope.ApplicationID, SourceID: s.SourceID,
		ReportRef: reportRef, ReportDigest: claim.Digest,
	})
	if err := s.Source.AcknowledgeFeedback(context.WithoutCancel(ctx), CaptureAcknowledgement{
		SourceID: s.SourceID, ApplicationID: s.Scope.ApplicationID,
		SourceRef: candidate.SourceRef, Receipt: receipt,
	}); err != nil {
		_ = s.Store.Interrupt(context.WithoutCancel(ctx), claim, "source_acknowledgement_failed")
		return ReconcileResult{}, fmt.Errorf("feedback_intake configured source did not accept its durable receipt")
	}
	result := ReconcileResult{
		Schema: ReconcileSchema, Operation: OperationIntake, Status: "persisted",
		ApplicationID: s.Scope.ApplicationID, ReportRef: reportRef, IntakeReceipt: &receipt,
	}
	if err := validateReconcileResult(claim, result); err != nil {
		_ = s.Store.Interrupt(context.WithoutCancel(ctx), claim, "invalid_result")
		return ReconcileResult{}, fmt.Errorf("feedback_intake result exceeded its integrity boundary")
	}
	if err := s.Store.Complete(context.WithoutCancel(ctx), claim, result); err != nil {
		_ = s.Store.Interrupt(context.WithoutCancel(ctx), claim, "completion_failed")
		return ReconcileResult{}, fmt.Errorf("feedback_intake durable completion is unavailable")
	}
	return result, nil
}

func emptyReconcileResult(operation, appID string) ReconcileResult {
	return ReconcileResult{
		Schema: ReconcileSchema, Operation: operation, Status: "empty", ApplicationID: appID,
	}
}

func validateReconcileScope(scope host.FeedbackScope) error {
	if !safeToken(scope.ApplicationID, 180) || strings.TrimSpace(scope.Revision) == "" {
		return fmt.Errorf("feedback reconciliation application scope is unavailable")
	}
	return nil
}

func boundedDrainLimit(limit int) (int, error) {
	if limit == 0 {
		limit = DefaultDrainLimit
	}
	if limit < 1 || limit > MaxDrainLimit {
		return 0, fmt.Errorf("feedback reconciliation limit is outside 1..%d", MaxDrainLimit)
	}
	return limit, nil
}

func stableReconcileID(operation, appID, configurationID, reportRef string) string {
	sum := sha256.Sum256([]byte(
		"feedback-reconcile/v1\x00" + operation + "\x00" + appID +
			"\x00" + configurationID + "\x00" + reportRef,
	))
	return operation + "-" + hex.EncodeToString(sum[:12])
}

func BindingConfigurationID(targetApplication, targetHandler, targetAction string) string {
	sum := sha256.Sum256([]byte(
		"feedback-binding/v1\x00" + targetApplication + "\x00" +
			targetHandler + "\x00" + targetAction,
	))
	return "binding-" + hex.EncodeToString(sum[:16])
}

func finalizeIntakeReceipt(receipt IntakeReceipt) IntakeReceipt {
	receipt.Schema = IntakeReceiptSchema
	receipt.ID = ""
	raw, _ := json.Marshal(receipt)
	sum := sha256.Sum256(raw)
	receipt.ID = "fir_" + hex.EncodeToString(sum[:16])
	return receipt
}

func validateReconcileReceipt(jobID string, receipt appplatform.Receipt) error {
	finalized, err := appplatform.FinalizeReceipt(receipt)
	if err != nil || finalized.ID != receipt.ID ||
		receipt.Schema != appplatform.ReceiptSchema ||
		receipt.SessionID != jobID || receipt.Actor != ServerActor ||
		receipt.Transport != appplatform.TransportEvent ||
		(receipt.Effect != appplatform.EffectWrite &&
			receipt.Effect != appplatform.EffectExternal) ||
		receipt.HandlerID == "" || receipt.SemanticRef == "" ||
		receipt.IdempotencyKey == "" || receipt.InputDigest == "" ||
		receipt.OutputDigest == "" || receipt.Outcome == "" {
		return fmt.Errorf("noncanonical application receipt")
	}
	return nil
}

func validateReconcileResult(claim ReconcileClaim, result ReconcileResult) error {
	if result.Schema != ReconcileSchema ||
		result.Operation != claim.Operation ||
		result.ApplicationID != claim.AppID ||
		result.ReportRef != claim.ItemID {
		return fmt.Errorf("feedback reconciliation result identity is inconsistent")
	}
	switch claim.Operation {
	case OperationCampaign, OperationFederation:
		if result.Status != "dispatched" || result.JobID == "" ||
			len(result.Receipts) != 1 ||
			validateReconcileReceipt(result.JobID, result.Receipts[0]) != nil ||
			result.IntakeReceipt != nil {
			return fmt.Errorf("feedback reconciliation dispatch result is invalid")
		}
	case OperationIntake:
		if result.Status != "persisted" || result.JobID != "" ||
			len(result.Receipts) != 0 || result.IntakeReceipt == nil {
			return fmt.Errorf("feedback intake result is invalid")
		}
		receipt := *result.IntakeReceipt
		if receipt.Schema != IntakeReceiptSchema ||
			receipt.ApplicationID != claim.AppID ||
			receipt.ReportRef != claim.ItemID ||
			receipt.ReportDigest != claim.Digest ||
			finalizeIntakeReceipt(receipt).ID != receipt.ID {
			return fmt.Errorf("feedback intake receipt is invalid")
		}
	default:
		return fmt.Errorf("feedback reconciliation operation is invalid")
	}
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > MaxReconcileBytes {
		return fmt.Errorf("feedback reconciliation result exceeds its byte bound")
	}
	return nil
}
