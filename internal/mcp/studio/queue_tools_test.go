package studio

import (
	"context"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"kitsoki/internal/capsule/queue"
)

func queueTestHandlers(t *testing.T) (queueToolHandlers, queue.Store) {
	t.Helper()
	store := queue.Store{ProjectRoot: t.TempDir()}
	return queueToolHandlers{store: store}, store
}

func TestQueueStatusOnEmptyQueue(t *testing.T) {
	h, _ := queueTestHandlers(t)

	res, out, err := h.status(context.Background(), nil, QueueStatusInput{})
	if err != nil {
		t.Fatal(err)
	}
	if res != nil {
		t.Fatalf("empty queue status must not be a tool error: %+v", res)
	}
	result, ok := out.(QueueStatusResult)
	if !ok {
		t.Fatalf("unexpected status result type %T", out)
	}
	if result.State == nil || result.Candidate != nil {
		t.Fatalf("empty-id status must return state, not a candidate: %+v", result)
	}
	if len(result.State.Candidates) != 0 {
		t.Fatalf("expected an empty queue, got %d candidates", len(result.State.Candidates))
	}
}

func TestQueueOpOnUnknownCandidateReturnsToolError(t *testing.T) {
	h, _ := queueTestHandlers(t)

	res, out, err := h.park(context.Background(), nil, QueueOpInput{ID: "no-such-candidate", Actor: "brad"})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("park of an unknown candidate must be a tool error, got result=%+v out=%+v", res, out)
	}

	res, _, err = h.reject(context.Background(), nil, QueueOpInput{})
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.IsError {
		t.Fatal("an op without a candidate id must be a tool error")
	}
}

func TestQueueParkResumeOverrideHappyPath(t *testing.T) {
	h, store := queueTestHandlers(t)
	seeded, err := store.Submit(queue.Submit{Branch: "b", SHA: strings.Repeat("a", 40), Admission: queue.EmergencySkipTestsAdmission})
	if err != nil {
		t.Fatal(err)
	}

	opCandidate := func(res *mcpsdk.CallToolResult, out any) queue.Candidate {
		t.Helper()
		if res != nil {
			t.Fatalf("unexpected tool error: %+v", res)
		}
		result, ok := out.(QueueOpResult)
		if !ok {
			t.Fatalf("unexpected op result type %T", out)
		}
		return result.Candidate
	}

	toolRes, out, err := h.park(context.Background(), nil, QueueOpInput{ID: seeded.ID, Actor: "brad", Reason: "waiting on review"})
	if err != nil {
		t.Fatal(err)
	}
	parked := opCandidate(toolRes, out)
	if parked.Phase != queue.NeedsInput {
		t.Fatalf("park must move the candidate to needs_input, got %s", parked.Phase)
	}

	toolRes, out, err = h.resume(context.Background(), nil, QueueOpInput{ID: seeded.ID, Actor: "brad", Reason: "review done"})
	if err != nil {
		t.Fatal(err)
	}
	resumed := opCandidate(toolRes, out)
	if resumed.Phase != queue.Queued || resumed.Attempt != 0 {
		t.Fatalf("resume must re-queue with a fresh attempt budget, got phase=%s attempt=%d", resumed.Phase, resumed.Attempt)
	}

	toolRes, out, err = h.override(context.Background(), nil, QueueOpInput{ID: seeded.ID, Actor: "brad", Reason: "hotfix"})
	if err != nil {
		t.Fatal(err)
	}
	overridden := opCandidate(toolRes, out)
	if !overridden.OverrideGate || overridden.OverrideBy != "brad" || overridden.EmergencySequence == 0 {
		t.Fatalf("override must record a durable attributed gate waiver in the emergency lane: %+v", overridden)
	}

	// The status tool observes the same durable state the ops mutated.
	statusRes, statusOut, err := h.status(context.Background(), nil, QueueStatusInput{ID: seeded.ID})
	if err != nil {
		t.Fatal(err)
	}
	if statusRes != nil {
		t.Fatalf("status of a known candidate must not be a tool error: %+v", statusRes)
	}
	statusResult, ok := statusOut.(QueueStatusResult)
	if !ok || statusResult.Candidate == nil {
		t.Fatalf("id status must return the candidate: %+v", statusOut)
	}
	if !statusResult.Candidate.OverrideGate || statusResult.Candidate.Phase != queue.Queued {
		t.Fatalf("durable state must reflect the override: %+v", statusResult.Candidate)
	}
}
