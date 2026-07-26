package reviewedfeedback

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	appplatform "kitsoki/internal/application"
	"kitsoki/internal/host"
	"kitsoki/internal/store"
)

type backendTestLedger struct{}

func (backendTestLedger) ListReviewed(
	context.Context,
	host.FeedbackScope,
	int,
) ([]host.ReviewedFeedbackReport, error) {
	return nil, nil
}

func (backendTestLedger) ResolveReviewed(
	_ context.Context,
	scope host.FeedbackScope,
	ref string,
) (ResolvedReport, error) {
	return ResolvedReport{
		Projection: host.ReviewedFeedbackReport{
			Ref: ref, Kind: "bug", Title: "Reviewed title",
			Summary: "Reviewed summary", Producer: "feedback-intake",
			Reviewed: true, AppID: scope.ApplicationID, Owner: scope.Owner,
			Revision: "7", ReceiptRef: ref,
			ReviewedAt: time.Date(2026, 7, 26, 3, 4, 5, 0, time.UTC),
		},
		Frame: 7,
	}, nil
}

type backendTestLocators struct{}

func (backendTestLocators) Resolve(string, string, string) (ResolvedLocators, error) {
	return ResolvedLocators{}, nil
}

type backendTestDispatcher struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (d *backendTestDispatcher) Dispatch(
	_ context.Context,
	plan DispatchPlan,
) (DispatchResult, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	if d.err != nil {
		return DispatchResult{}, d.err
	}
	return DispatchResult{
		JobID: plan.JobID,
		Receipts: []appplatform.Receipt{
			backendTestReceipt(plan),
		},
	}, nil
}

func (d *backendTestDispatcher) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func TestBackendConcurrentDispatchIsDurablyDeduplicated(t *testing.T) {
	dispatchStore, closeStore := newBackendTestStore(t)
	defer closeStore()
	dispatcher := &backendTestDispatcher{}
	backend := newBackendTestBackend(dispatchStore, dispatcher)
	request := backendTestRequest()

	const workers = 12
	results := make(chan host.FeedbackDispatchReceipt, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			result, err := backend.Dispatch(context.Background(), request)
			results <- result
			errs <- err
		}()
	}
	group.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
	}
	var jobID string
	for result := range results {
		if jobID == "" {
			jobID = result.JobID
		}
		if result.JobID != jobID || len(result.Receipts) != 1 {
			t.Fatalf("result = %#v", result)
		}
	}
	if dispatcher.count() != 1 {
		t.Fatalf("dispatcher calls = %d, want 1", dispatcher.count())
	}
}

func TestBackendRejectsDispatchIDReuseForDifferentReport(t *testing.T) {
	dispatchStore, closeStore := newBackendTestStore(t)
	defer closeStore()
	backend := newBackendTestBackend(dispatchStore, &backendTestDispatcher{})
	request := backendTestRequest()
	if _, err := backend.Dispatch(context.Background(), request); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	request.ReportRef = "feedback/different"
	if _, err := backend.Dispatch(context.Background(), request); err == nil ||
		!strings.Contains(err.Error(), "different reviewed request") {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestSQLiteDispatchStoreRestartRequiresExplicitRetry(t *testing.T) {
	dispatchStore, closeStore := newBackendTestStore(t)
	defer closeStore()
	request := DispatchState{
		ScopeID: "source.app", DispatchID: "dispatch-1",
		RequestDigest: "digest", JobID: "job",
	}
	state, err := dispatchStore.Claim(context.Background(), request)
	if err != nil || state.Status != DispatchPending || state.Attempt != 1 {
		t.Fatalf("initial claim = %#v, %v", state, err)
	}
	count, err := dispatchStore.InterruptPending(context.Background(), "daemon_restarted")
	if err != nil || count != 1 {
		t.Fatalf("interrupt pending = %d, %v", count, err)
	}
	state, err = dispatchStore.get(context.Background(), request.ScopeID, request.DispatchID)
	if err != nil || state.Status != DispatchInterrupted ||
		state.InterruptedReason != "daemon_restarted" {
		t.Fatalf("interrupted state = %#v, %v", state, err)
	}
	state, err = dispatchStore.Claim(context.Background(), request)
	if err != nil || state.Status != DispatchPending || state.Attempt != 2 {
		t.Fatalf("explicit retry = %#v, %v", state, err)
	}
}

func TestSQLiteDispatchStoreHasOneConcurrentClaimOwner(t *testing.T) {
	dispatchStore, closeStore := newBackendTestStore(t)
	defer closeStore()
	request := DispatchState{
		ScopeID: "source.app", DispatchID: "dispatch-concurrent",
		RequestDigest: "digest", JobID: "job",
	}
	const workers = 12
	states := make(chan DispatchState, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			state, err := dispatchStore.Claim(context.Background(), request)
			states <- state
			errs <- err
		}()
	}
	group.Wait()
	close(states)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
	}
	owners := 0
	for state := range states {
		if state.Claimed {
			owners++
		}
	}
	if owners != 1 {
		t.Fatalf("claim owners = %d, want 1", owners)
	}
}

func TestBackendScrubsDispatchFailureBeforePersistence(t *testing.T) {
	dispatchStore, closeStore := newBackendTestStore(t)
	defer closeStore()
	dispatcher := &backendTestDispatcher{
		err: errors.New("provider=claude open /private/story/app.yaml: authorization: Bearer secret-token-value"),
	}
	backend := newBackendTestBackend(dispatchStore, dispatcher)
	request := backendTestRequest()
	if _, err := backend.Dispatch(context.Background(), request); err == nil ||
		strings.Contains(err.Error(), "/private") ||
		strings.Contains(err.Error(), "provider=claude") ||
		strings.Contains(err.Error(), "secret-token-value") {
		t.Fatalf("public dispatch error = %v", err)
	}
	state, err := dispatchStore.get(
		context.Background(), request.Scope.ApplicationID, request.DispatchID,
	)
	if err != nil {
		t.Fatalf("read dispatch state: %v", err)
	}
	if state.Status != DispatchInterrupted ||
		strings.Contains(state.InterruptedReason, "secret-token-value") ||
		strings.Contains(state.InterruptedReason, "/private") ||
		strings.Contains(state.InterruptedReason, "provider=claude") ||
		state.InterruptedReason != "target_application_rejected" {
		t.Fatalf("interrupted state = %#v", state)
	}
}

func newBackendTestBackend(
	dispatchStore DispatchStore,
	dispatcher Dispatcher,
) *Backend {
	return &Backend{
		Binding: Binding{
			SourceApplication: "source.app",
			TargetApplication: "target.app",
			TargetHandler:     "feedback.apply",
			TargetAction:      "feedback.apply.action",
		},
		Ledger: backendTestLedger{}, Locators: backendTestLocators{},
		Store: dispatchStore, Dispatcher: dispatcher,
	}
}

func backendTestRequest() host.FeedbackDispatchRequest {
	return host.FeedbackDispatchRequest{
		Scope: host.FeedbackScope{
			ApplicationID: "source.app", Owner: "owner", Revision: "1",
		},
		Actor: "operator", ReportRef: "feedback/ref",
		DispatchID: "dispatch-1", ResumeMode: "fresh",
	}
}

func backendTestReceipt(plan DispatchPlan) appplatform.Receipt {
	receipt, _ := appplatform.FinalizeReceipt(appplatform.Receipt{
		HandlerID: plan.TargetHandler, SemanticRef: "feedback.apply",
		SessionID: plan.JobID, Actor: plan.ServerActor,
		Effect: appplatform.EffectWrite,
		Routing: appplatform.RoutingReceipt{
			Requested: appplatform.RoutingExact,
			Resolved:  appplatform.RoutingExact,
		},
		Budget: appplatform.BudgetDecision{
			Allowed: true, Code: "not_applicable",
		},
		IdempotencyKey: plan.IdempotencyKey,
		InputDigest:    "sha256:input",
		OutputDigest:   "sha256:output",
		Transport:      appplatform.TransportEvent,
		Outcome:        "ok",
	})
	return receipt
}

func newBackendTestStore(t *testing.T) (*SQLiteDispatchStore, func()) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	dispatchStore, err := NewSQLiteDispatchStore(st.DB(), nil)
	if err != nil {
		_ = st.Close()
		t.Fatalf("open dispatch store: %v", err)
	}
	return dispatchStore, func() { _ = st.Close() }
}
