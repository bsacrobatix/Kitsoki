package reviewedfeedback

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appplatform "kitsoki/internal/application"
	"kitsoki/internal/applicationfeedback"
	"kitsoki/internal/clock"
	"kitsoki/internal/host"
	"kitsoki/internal/store"
)

type reconcileTestBackend struct {
	mu      sync.Mutex
	reports []host.ReviewedFeedbackReport
	calls   int
	fail    bool
	entered chan struct{}
	release chan struct{}
}

type reconcileTestCaptureSource struct {
	candidates  []CapturedFeedback
	acks        []CaptureAcknowledgement
	ackFailures int
}

func (s *reconcileTestCaptureSource) ListFeedback(
	context.Context,
	CaptureRequest,
) ([]CapturedFeedback, error) {
	return append([]CapturedFeedback(nil), s.candidates...), nil
}

func (s *reconcileTestCaptureSource) AcknowledgeFeedback(
	_ context.Context,
	ack CaptureAcknowledgement,
) error {
	s.acks = append(s.acks, ack)
	if s.ackFailures > 0 {
		s.ackFailures--
		return errors.New("transport secret /private/source")
	}
	return nil
}

func (b *reconcileTestBackend) ListReviewed(
	_ context.Context,
	_ host.FeedbackListRequest,
) ([]host.ReviewedFeedbackReport, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]host.ReviewedFeedbackReport(nil), b.reports...), nil
}

func (b *reconcileTestBackend) Dispatch(
	_ context.Context,
	request host.FeedbackDispatchRequest,
) (host.FeedbackDispatchReceipt, error) {
	b.mu.Lock()
	b.calls++
	fail := b.fail
	entered := b.entered
	release := b.release
	b.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if release != nil {
		<-release
	}
	if fail {
		return host.FeedbackDispatchReceipt{}, errors.New(
			"provider=live token=secret /private/target",
		)
	}
	jobID := "feedback-job"
	receipt, _ := appplatform.FinalizeReceipt(appplatform.Receipt{
		HandlerID: "target.feedback.apply", SemanticRef: "feedback/ref",
		SessionID: jobID, Actor: ServerActor, Effect: appplatform.EffectWrite,
		Routing: appplatform.RoutingReceipt{
			Requested: appplatform.RoutingExact, Resolved: appplatform.RoutingExact,
		},
		Budget:         appplatform.BudgetDecision{Allowed: true, Code: "not_applicable"},
		IdempotencyKey: request.DispatchID, InputDigest: "sha256:input",
		OutputDigest: "sha256:output", Transport: appplatform.TransportEvent,
		Outcome: "ok",
	})
	return host.FeedbackDispatchReceipt{
		JobID: jobID, Receipts: []appplatform.Receipt{receipt},
	}, nil
}

func (b *reconcileTestBackend) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

func TestDispatchReconcilerSuccessEmptyReplayAndFailure(t *testing.T) {
	store := newReconcileTestStore(t)
	scope := reconcileTestScope()
	backend := &reconcileTestBackend{reports: []host.ReviewedFeedbackReport{
		reconcileTestProjection(scope, "feedback/one"),
	}}
	service := DispatchReconciler{
		Operation: OperationCampaign, ConfigurationID: "target-v1",
		Backend: backend, Store: store,
		Scope: scope, Limit: 5,
	}
	result, err := service.Reconcile(context.Background())
	if err != nil || result.Status != "dispatched" ||
		result.ReportRef != "feedback/one" || len(result.Receipts) != 1 {
		t.Fatalf("success = %#v, %v", result, err)
	}
	replay, err := service.Reconcile(context.Background())
	if err != nil || !replay.Replayed || replay.JobID != result.JobID ||
		backend.count() != 1 {
		t.Fatalf("replay = %#v, calls=%d, err=%v", replay, backend.count(), err)
	}
	if _, err := (DispatchReconciler{
		Operation: OperationCampaign, ConfigurationID: "target-v2",
		Backend: backend, Store: store, Scope: scope, Limit: 5,
	}).Reconcile(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "durable state is unavailable") ||
		backend.count() != 1 {
		t.Fatalf("binding drift error = %v, calls=%d", err, backend.count())
	}

	empty, err := (DispatchReconciler{
		Operation: OperationFederation, ConfigurationID: "target-v1",
		Backend: &reconcileTestBackend{},
		Store:   store, Scope: scope, Limit: 5,
	}).Reconcile(context.Background())
	if err != nil || empty.Status != "empty" {
		t.Fatalf("empty = %#v, %v", empty, err)
	}

	failing := &reconcileTestBackend{
		reports: []host.ReviewedFeedbackReport{reconcileTestProjection(scope, "feedback/fail")},
		fail:    true,
	}
	_, err = (DispatchReconciler{
		Operation: OperationFederation, ConfigurationID: "target-v1",
		Backend: failing,
		Store:   store, Scope: scope, Limit: 5,
	}).Reconcile(context.Background())
	if err == nil || strings.Contains(err.Error(), "secret") ||
		strings.Contains(err.Error(), "/private") || strings.Contains(err.Error(), "provider") {
		t.Fatalf("privacy-safe failure = %v", err)
	}
}

func TestDispatchReconcilerConcurrentClaimDispatchesAtMostOne(t *testing.T) {
	store := newReconcileTestStore(t)
	scope := reconcileTestScope()
	backend := &reconcileTestBackend{
		reports: []host.ReviewedFeedbackReport{reconcileTestProjection(scope, "feedback/one")},
		entered: make(chan struct{}, 1), release: make(chan struct{}),
	}
	service := DispatchReconciler{
		Operation: OperationCampaign, ConfigurationID: "target-v1",
		Backend: backend, Store: store,
		Scope: scope, Limit: 5,
	}
	first := make(chan error, 1)
	go func() {
		_, err := service.Reconcile(context.Background())
		first <- err
	}()
	<-backend.entered
	_, secondErr := service.Reconcile(context.Background())
	if secondErr == nil || !strings.Contains(secondErr.Error(), "in progress") {
		t.Fatalf("concurrent reconcile error = %v", secondErr)
	}
	close(backend.release)
	if err := <-first; err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if backend.count() != 1 {
		t.Fatalf("dispatch calls = %d, want 1", backend.count())
	}
}

func TestSQLiteReconcileStoreRestartRequiresExplicitRetry(t *testing.T) {
	store := newReconcileTestStore(t)
	item := ReconcileItem{ID: "feedback/one", Digest: "digest"}
	claim, err := store.ClaimNext(
		context.Background(), OperationIntake, "source.app", []ReconcileItem{item},
	)
	if err != nil || !claim.Claimed || claim.Attempt != 1 {
		t.Fatalf("initial claim = %#v, %v", claim, err)
	}
	count, err := store.InterruptPending(context.Background(), "daemon_restarted")
	if err != nil || count != 1 {
		t.Fatalf("interrupt pending = %d, %v", count, err)
	}
	retry, err := store.ClaimNext(
		context.Background(), OperationIntake, "source.app", []ReconcileItem{item},
	)
	if err != nil || !retry.Claimed || retry.Attempt != 2 {
		t.Fatalf("explicit retry = %#v, %v", retry, err)
	}
}

func TestReconcileResultRefusesOversizedCanonicalReceipt(t *testing.T) {
	receipt, err := appplatform.FinalizeReceipt(appplatform.Receipt{
		HandlerID: "target.feedback.apply", SemanticRef: "feedback/ref",
		SessionID: "job", Actor: ServerActor, Effect: appplatform.EffectWrite,
		Routing: appplatform.RoutingReceipt{
			Requested: appplatform.RoutingExact, Resolved: appplatform.RoutingExact,
		},
		Budget:         appplatform.BudgetDecision{Allowed: true, Code: "not_applicable"},
		IdempotencyKey: "dispatch", InputDigest: "sha256:input",
		OutputDigest: "sha256:output", Transport: appplatform.TransportEvent,
		Outcome: "ok", SelectedImplementor: strings.Repeat("x", MaxReconcileBytes),
	})
	if err != nil {
		t.Fatal(err)
	}
	claim := ReconcileClaim{
		Operation: OperationCampaign, AppID: "source.app",
		ItemID: "feedback/one", Digest: "digest",
	}
	result := ReconcileResult{
		Schema: ReconcileSchema, Operation: OperationCampaign,
		Status: "dispatched", ApplicationID: "source.app",
		ReportRef: "feedback/one", JobID: "job",
		Receipts: []appplatform.Receipt{receipt},
	}
	if err := validateReconcileResult(claim, result); err == nil ||
		!strings.Contains(err.Error(), "byte bound") {
		t.Fatalf("oversized result error = %v", err)
	}
}

func TestApplicationFeedbackLedgerSourceIntakeDoesNotDuplicateCanonicalRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "feedback.jsonl")
	ledger := JSONLLedger{Path: path, Home: dir}
	scope := reconcileTestScope()
	report := reconcileTestReport(scope.ApplicationID, "feedback/api-one", "review this")
	if _, _, err := ledger.AppendReviewed(
		context.Background(), scope, report, time.Date(2026, 7, 26, 1, 2, 3, 0, time.UTC),
	); err != nil {
		t.Fatalf("seed API-style canonical record: %v", err)
	}
	before := feedbackLineCount(t, path)
	service := IntakeReconciler{
		SourceID: ApplicationFeedbackSourceID,
		Source:   LedgerCaptureSource{Ledger: ledger},
		Ledger:   ledger, Store: newReconcileTestStore(t), Scope: scope,
		Clock: clock.NewFake(time.Date(2026, 7, 26, 2, 3, 4, 0, time.UTC)),
		Limit: 5,
	}
	result, err := service.Reconcile(context.Background())
	if err != nil || result.Status != "persisted" ||
		result.IntakeReceipt == nil ||
		result.IntakeReceipt.Schema != IntakeReceiptSchema {
		t.Fatalf("intake = %#v, %v", result, err)
	}
	if after := feedbackLineCount(t, path); after != before {
		t.Fatalf("ledger lines = %d, want unchanged %d", after, before)
	}
	replay, err := service.Reconcile(context.Background())
	if err != nil || !replay.Replayed ||
		replay.IntakeReceipt == nil ||
		replay.IntakeReceipt.ID != result.IntakeReceipt.ID {
		t.Fatalf("durable replay = %#v, %v", replay, err)
	}
}

func TestIntakeReconcilerScrubsDifferentTypedSourceBeforeCanonicalPersistence(t *testing.T) {
	home := t.TempDir()
	ledger := JSONLLedger{
		Path: filepath.Join(t.TempDir(), "feedback.jsonl"),
		Home: home,
	}
	scope := reconcileTestScope()
	report := reconcileTestReport(
		scope.ApplicationID, "feedback/typed-one",
		"failure under "+filepath.Join(home, "private", "workspace"),
	)
	source := &reconcileTestCaptureSource{candidates: []CapturedFeedback{{
		SourceRef: report.IdempotencyKey,
		Report:    report,
	}}}
	service := IntakeReconciler{
		SourceID: "typed-capture", Source: source, Ledger: ledger,
		Store: newReconcileTestStore(t), Scope: scope,
		Clock: clock.NewFake(time.Date(2026, 7, 26, 2, 3, 4, 0, time.UTC)),
		Limit: 5,
	}
	result, err := service.Reconcile(context.Background())
	if err != nil || result.Status != "persisted" || len(source.acks) != 1 {
		t.Fatalf("typed intake = %#v, acks=%d, err=%v", result, len(source.acks), err)
	}
	raw, err := os.ReadFile(ledger.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), home) {
		t.Fatalf("canonical ledger leaked home path: %s", raw)
	}
	reports, err := ledger.ListReviewed(context.Background(), scope, 5)
	if err != nil || len(reports) != 1 ||
		strings.Contains(reports[0].Summary, home) {
		t.Fatalf("reviewed projection = %#v, %v", reports, err)
	}
	before := feedbackLineCount(t, ledger.Path)
	replay, err := service.Reconcile(context.Background())
	if err != nil || !replay.Replayed ||
		feedbackLineCount(t, ledger.Path) != before || len(source.acks) != 1 {
		t.Fatalf("typed replay = %#v, acks=%d, err=%v", replay, len(source.acks), err)
	}
}

func TestIntakeRetryKeepsStableReceiptAfterAppendBeforeAcknowledgement(t *testing.T) {
	ledger := JSONLLedger{Path: filepath.Join(t.TempDir(), "feedback.jsonl")}
	scope := reconcileTestScope()
	report := reconcileTestReport(
		scope.ApplicationID, "feedback/retry-one", "reviewed retry",
	)
	source := &reconcileTestCaptureSource{
		candidates: []CapturedFeedback{{
			SourceRef: report.IdempotencyKey,
			Report:    report,
		}},
		ackFailures: 1,
	}
	fakeClock := clock.NewFake(time.Date(2026, 7, 26, 2, 3, 4, 0, time.UTC))
	service := IntakeReconciler{
		SourceID: "typed-capture", Source: source, Ledger: ledger,
		Store: newReconcileTestStore(t), Scope: scope, Clock: fakeClock, Limit: 5,
	}
	if _, err := service.Reconcile(context.Background()); err == nil ||
		strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "/private") {
		t.Fatalf("privacy-safe acknowledgement failure = %v", err)
	}
	if len(source.acks) != 1 || feedbackLineCount(t, ledger.Path) != 1 {
		t.Fatalf("first attempt acks=%d ledger=%d", len(source.acks), feedbackLineCount(t, ledger.Path))
	}
	fakeClock.Advance(time.Hour)
	result, err := service.Reconcile(context.Background())
	if err != nil || result.Status != "persisted" || len(source.acks) != 2 {
		t.Fatalf("retry = %#v, acks=%d, err=%v", result, len(source.acks), err)
	}
	if source.acks[0].Receipt.ID != source.acks[1].Receipt.ID ||
		result.IntakeReceipt == nil ||
		result.IntakeReceipt.ID != source.acks[0].Receipt.ID ||
		feedbackLineCount(t, ledger.Path) != 1 {
		t.Fatalf("unstable retry receipts = %#v", source.acks)
	}
}

func TestIntakeReconcilerConfiguredUnavailableAndEmptyAreDistinct(t *testing.T) {
	scope := reconcileTestScope()
	store := newReconcileTestStore(t)
	ledger := JSONLLedger{Path: filepath.Join(t.TempDir(), "feedback.jsonl")}
	unavailable := IntakeReconciler{
		SourceID: "registered-but-missing", Ledger: ledger,
		Store: store, Scope: scope, Clock: clock.Real(),
	}
	if _, err := unavailable.Reconcile(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "configured source is unavailable") {
		t.Fatalf("unavailable error = %v", err)
	}
	empty, err := (IntakeReconciler{
		SourceID: ApplicationFeedbackSourceID,
		Source:   LedgerCaptureSource{Ledger: ledger}, Ledger: ledger,
		Store: store, Scope: scope, Clock: clock.Real(),
	}).Reconcile(context.Background())
	if err != nil || empty.Status != "empty" {
		t.Fatalf("configured empty = %#v, %v", empty, err)
	}
}

func newReconcileTestStore(t *testing.T) *SQLiteReconcileStore {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	reconciles, err := NewSQLiteReconcileStore(st.DB(), clock.Real())
	if err != nil {
		t.Fatalf("open reconcile store: %v", err)
	}
	return reconciles
}

func reconcileTestScope() host.FeedbackScope {
	return host.FeedbackScope{
		ApplicationID: "source.app", Owner: "owner", Revision: "1",
	}
}

func reconcileTestProjection(
	scope host.FeedbackScope,
	ref string,
) host.ReviewedFeedbackReport {
	return host.ReviewedFeedbackReport{
		Ref: ref, Kind: "bug", Title: "Reviewed title", Summary: "Reviewed summary",
		Producer: "application-feedback", Reviewed: true,
		AppID: scope.ApplicationID, Owner: scope.Owner, Revision: scope.Revision,
		ReceiptRef: ref, ReviewedAt: time.Date(2026, 7, 26, 1, 2, 3, 0, time.UTC),
	}
}

func reconcileTestReport(appID, ref, text string) applicationfeedback.Report {
	return applicationfeedback.Report{
		Schema: applicationfeedback.ReportSchema, IdempotencyKey: ref,
		App: appID, Producer: "application-feedback", Kind: "bug",
		UserText: text, Reviewed: true,
		Attachment: applicationfeedback.Attachment{
			Schema:        applicationfeedback.AttachmentSchema,
			ApplicationID: appID, FrameRevision: 7,
		},
	}
}

func feedbackLineCount(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return 0
	}
	return len(strings.Split(trimmed, "\n"))
}
