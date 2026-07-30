package delivery

import (
	"context"
	"strings"
	"testing"

	"kitsoki/internal/capsule/queue"
)

func TestNextAction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		phase queue.Status
		want  Action
	}{
		{queue.Queued, ActionProcessing},
		{queue.Gating, ActionProcessing},
		{queue.RetryWait, ActionRetry},
		{queue.NeedsInput, ActionRepair},
		{queue.NeedsConflictInput, ActionRepair},
		{queue.AwaitingApproval, ActionReview},
		{queue.Landed, ActionTerminal},
		{queue.Rejected, ActionTerminal},
	}
	for _, test := range tests {
		test := test
		t.Run(string(test.phase), func(t *testing.T) {
			t.Parallel()
			if got := NextAction(queue.Candidate{Phase: test.phase}); got != test.want {
				t.Fatalf("NextAction(%s)=%s want %s", test.phase, got, test.want)
			}
		})
	}
}

func TestRetryCancelRejectUseOneQueueLedger(t *testing.T) {
	t.Parallel()
	store := queue.Store{ProjectRoot: t.TempDir()}
	service := New(store)
	candidate, err := store.Submit(queue.Submit{
		Branch: "agent/delivery", SHA: strings.Repeat("a", 40),
		Admission: queue.EmergencySkipTestsAdmission,
	})
	if err != nil {
		t.Fatal(err)
	}

	cancelled, err := service.Cancel(queue.Op{ID: candidate.ID, Actor: "test", Reason: "pause"})
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Candidate.Phase != queue.NeedsInput || cancelled.Action != ActionRepair {
		t.Fatalf("cancel result=%+v", cancelled)
	}

	retried, err := service.Retry(queue.Op{ID: candidate.ID, Actor: "test", Reason: "fixed"})
	if err != nil {
		t.Fatal(err)
	}
	if retried.Candidate.Phase != queue.Queued || retried.Action != ActionProcessing {
		t.Fatalf("retry result=%+v", retried)
	}

	rejected, err := service.Reject(queue.Op{ID: candidate.ID, Actor: "test", Reason: "superseded"})
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Candidate.Phase != queue.Rejected || rejected.Action != ActionTerminal {
		t.Fatalf("reject result=%+v", rejected)
	}

	got, err := service.Get(candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Candidate.Phase != queue.Rejected {
		t.Fatalf("single ledger diverged: %+v", got)
	}
	status, err := service.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(status.State.Candidates) != 1 || status.State.Candidates[0].ID != candidate.ID {
		t.Fatalf("status=%+v", status)
	}
}

func TestSubmitForcesDurableBundleAdmission(t *testing.T) {
	t.Parallel()
	store := &captureStore{}
	service := Service{store: store}
	got, err := service.Submit(context.Background(), SubmitRequest{
		Result:     queue.ExternalWorkerResult{ExecutionID: "execution"},
		BundlePath: "/retained/result.bundle",
		Submit:     Submit{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.submission.Submit.Admission != queue.DurableBundleAdmission {
		t.Fatalf("admission=%q want %q", store.submission.Submit.Admission, queue.DurableBundleAdmission)
	}
	if got.Candidate == nil || got.Candidate.ID != "queue-captured" || got.Anchor == nil {
		t.Fatalf("result=%+v", got)
	}
}

type captureStore struct {
	submission queue.ExternalBundleSubmission
}

func (s *captureStore) AdmitExternalBundle(_ context.Context, in queue.ExternalBundleSubmission) (queue.Candidate, queue.ExternalBundleAnchor, error) {
	s.submission = in
	return queue.Candidate{ID: "queue-captured", Phase: queue.Queued}, queue.ExternalBundleAnchor{ID: "anchor"}, nil
}
func (*captureStore) Get(string) (queue.Candidate, error)      { panic("unexpected") }
func (*captureStore) List() (queue.State, error)               { panic("unexpected") }
func (*captureStore) Kick(queue.Op) (queue.Candidate, error)   { panic("unexpected") }
func (*captureStore) Park(queue.Op) (queue.Candidate, error)   { panic("unexpected") }
func (*captureStore) Resume(queue.Op) (queue.Candidate, error) { panic("unexpected") }
func (*captureStore) Reject(queue.Op) (queue.Candidate, error) { panic("unexpected") }
