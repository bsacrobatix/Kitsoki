package studio

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"kitsoki/internal/capsule/delivery"
	"kitsoki/internal/capsule/queue"
)

func TestQueueToolsHonorExactExternalQueueRoot(t *testing.T) {
	project := t.TempDir()
	queueRoot := filepath.Join(t.TempDir(), "shared-queue")
	store := queue.Store{ProjectRoot: project, QueueRoot: queueRoot}
	seeded, err := store.Submit(queue.Submit{
		Branch: "external", SHA: strings.Repeat("d", 40),
		Admission: queue.EmergencySkipTestsAdmission,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := queueToolHandlers{store: queue.Store{ProjectRoot: project}}
	toolResult, out, err := h.status(context.Background(), nil, QueueStatusInput{ID: seeded.ID, QueueRoot: queueRoot})
	if err != nil || toolResult != nil {
		t.Fatalf("status err=%v tool=%+v", err, toolResult)
	}
	result, ok := out.(delivery.Result)
	if !ok || result.Candidate == nil || result.Candidate.ID != seeded.ID {
		t.Fatalf("external queue result=%#v", out)
	}
	toolResult, out, err = h.cancel(context.Background(), nil, QueueOpInput{ID: seeded.ID, QueueRoot: queueRoot, Actor: "test"})
	if err != nil || toolResult != nil {
		t.Fatalf("cancel err=%v tool=%+v", err, toolResult)
	}
	cancelled, ok := out.(delivery.Result)
	if !ok || cancelled.Candidate == nil || cancelled.Candidate.Phase != queue.NeedsInput {
		t.Fatalf("external queue cancel=%#v", out)
	}
	local, err := (queue.Store{ProjectRoot: project}).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(local.Candidates) != 0 {
		t.Fatalf("MCP created a second project-local ledger: %+v", local.Candidates)
	}
}

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
	result, ok := out.(delivery.Result)
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
	statusResult, ok := statusOut.(delivery.Result)
	if !ok || statusResult.Candidate == nil {
		t.Fatalf("id status must return the candidate: %+v", statusOut)
	}
	if !statusResult.Candidate.OverrideGate || statusResult.Candidate.Phase != queue.Queued {
		t.Fatalf("durable state must reflect the override: %+v", statusResult.Candidate)
	}
}

func TestQueueDeliveryOperationsShareServiceResult(t *testing.T) {
	h, store := queueTestHandlers(t)
	seeded, err := store.Submit(queue.Submit{
		Branch: "delivery", SHA: strings.Repeat("b", 40),
		Admission: queue.EmergencySkipTestsAdmission,
	})
	if err != nil {
		t.Fatal(err)
	}
	call := func(
		run func(context.Context, *mcpsdk.CallToolRequest, QueueOpInput) (*mcpsdk.CallToolResult, any, error),
		wantPhase queue.Status,
		wantAction delivery.Action,
	) {
		t.Helper()
		toolResult, out, err := run(context.Background(), nil, QueueOpInput{ID: seeded.ID, Actor: "test"})
		if err != nil {
			t.Fatal(err)
		}
		if toolResult != nil {
			t.Fatalf("tool error: %+v", toolResult)
		}
		result, ok := out.(delivery.Result)
		if !ok || result.Candidate == nil {
			t.Fatalf("result=%#v (%T)", out, out)
		}
		if result.Candidate.Phase != wantPhase || result.Action != wantAction {
			t.Fatalf("result=%+v want phase=%s action=%s", result, wantPhase, wantAction)
		}
	}
	call(h.cancel, queue.NeedsInput, delivery.ActionRepair)
	call(h.retry, queue.Queued, delivery.ActionProcessing)
	call(h.reject, queue.Rejected, delivery.ActionTerminal)
}
