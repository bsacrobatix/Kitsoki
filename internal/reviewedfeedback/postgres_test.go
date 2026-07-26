package reviewedfeedback

import (
	"context"
	"testing"

	"kitsoki/internal/dbruntime/pgtest"
	"kitsoki/internal/host"
)

func TestPostgresFeedbackStoresRetainDispatchAndReconcileReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := pgtest.Open(t)

	dispatches, err := NewPostgresDispatchStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &backendTestDispatcher{}
	backend := newBackendTestBackend(dispatches, dispatcher)
	request := backendTestRequest()
	firstDispatch, err := backend.Dispatch(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	restartedDispatches, err := NewPostgresDispatchStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	restartedBackend := newBackendTestBackend(restartedDispatches, dispatcher)
	replayedDispatch, err := restartedBackend.Dispatch(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if replayedDispatch.JobID != firstDispatch.JobID ||
		len(replayedDispatch.Receipts) != 1 || dispatcher.count() != 1 {
		t.Fatalf(
			"dispatch replay = %#v, calls=%d",
			replayedDispatch, dispatcher.count(),
		)
	}

	reconciles, err := NewPostgresReconcileStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	scope := reconcileTestScope()
	reconcileBackend := &reconcileTestBackend{
		reports: []host.ReviewedFeedbackReport{
			reconcileTestProjection(scope, "feedback/postgres"),
		},
	}
	service := DispatchReconciler{
		Operation: OperationFederation, ConfigurationID: "target-v1",
		Backend: reconcileBackend, Store: reconciles, Scope: scope, Limit: 5,
	}
	firstResult, err := service.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restartedReconciles, err := NewPostgresReconcileStore(db, nil)
	if err != nil {
		t.Fatal(err)
	}
	service.Store = restartedReconciles
	replayedResult, err := service.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !replayedResult.Replayed ||
		replayedResult.JobID != firstResult.JobID ||
		reconcileBackend.count() != 1 {
		t.Fatalf(
			"reconcile replay = %#v, calls=%d",
			replayedResult, reconcileBackend.count(),
		)
	}
}
