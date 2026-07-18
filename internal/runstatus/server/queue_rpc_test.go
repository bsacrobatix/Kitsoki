package server

import (
	"context"
	"strings"
	"testing"

	"kitsoki/internal/app"
	"kitsoki/internal/capsule/queue"
)

func newQueueServer(root string) *Server {
	return New("", &app.AppDef{}, WithMaterializeRoot(root))
}

// queue.status with no state file yet returns an empty state, not an error.
func TestQueueRPC_StatusEmpty(t *testing.T) {
	s := newQueueServer(t.TempDir())
	out, rerr := s.dispatch(context.Background(), "queue.status", map[string]any{})
	if rerr != nil {
		t.Fatalf("queue.status error: %+v", rerr)
	}
	state, ok := out.(queue.State)
	if !ok {
		t.Fatalf("queue.status returned %T, want queue.State", out)
	}
	if len(state.Candidates) != 0 {
		t.Fatalf("expected empty queue, got %d candidates", len(state.Candidates))
	}
}

// An operator verb on an unknown candidate is a method error, not a crash.
func TestQueueRPC_UnknownCandidate(t *testing.T) {
	s := newQueueServer(t.TempDir())
	out, rerr := s.dispatch(context.Background(), "queue.kick", map[string]any{"id": "nope", "actor": "brad"})
	if rerr == nil {
		t.Fatalf("expected error, got result %+v", out)
	}
	if rerr.Code != codeServerError {
		t.Fatalf("expected codeServerError, got %d: %s", rerr.Code, rerr.Message)
	}
	if !strings.Contains(rerr.Message, "queue.kick") || !strings.Contains(rerr.Message, "nope") {
		t.Fatalf("unexpected message: %s", rerr.Message)
	}
}

// An unknown queue.* method still falls through to method-missing.
func TestQueueRPC_UnknownMethodFallsThrough(t *testing.T) {
	s := newQueueServer(t.TempDir())
	_, rerr := s.dispatch(context.Background(), "queue.nonsense", map[string]any{})
	if rerr == nil || rerr.Code != codeMethodMissing {
		t.Fatalf("expected codeMethodMissing, got %+v", rerr)
	}
}

// Full happy path: seed a candidate via Store.Submit (emergency admission
// needs no receipt), then drive park/resume/emergency/override/reject through
// the RPC surface and assert the phase after each verb.
func TestQueueRPC_OperatorVerbs(t *testing.T) {
	root := t.TempDir()
	store := queue.Store{ProjectRoot: root}
	sha := strings.Repeat("ab", 20) // 40-hex
	seeded, err := store.Submit(queue.Submit{Branch: "agent/x", SHA: sha, Admission: queue.EmergencySkipTestsAdmission})
	if err != nil {
		t.Fatalf("seed submit: %v", err)
	}
	s := newQueueServer(root)
	op := func(method string) (queue.Candidate, *rpcError) {
		out, rerr := s.dispatch(context.Background(), method,
			map[string]any{"id": seeded.ID, "actor": "brad", "reason": "test"})
		if rerr != nil {
			return queue.Candidate{}, rerr
		}
		return out.(queue.Candidate), nil
	}

	// queue.status with id returns the single candidate.
	out, rerr := s.dispatch(context.Background(), "queue.status", map[string]any{"id": seeded.ID})
	if rerr != nil {
		t.Fatalf("queue.status(id) error: %+v", rerr)
	}
	if c := out.(queue.Candidate); c.ID != seeded.ID || c.Phase != queue.Queued {
		t.Fatalf("unexpected candidate: id=%s phase=%s", c.ID, c.Phase)
	}

	// kick requires retry_wait; on a queued candidate it is a clean error.
	if _, rerr := op("queue.kick"); rerr == nil || rerr.Code != codeServerError {
		t.Fatalf("kick on queued candidate: expected codeServerError, got %+v", rerr)
	}

	if c, rerr := op("queue.park"); rerr != nil || c.Phase != queue.NeedsInput {
		t.Fatalf("park: rerr=%+v phase=%v", rerr, c.Phase)
	}
	if c, rerr := op("queue.resume"); rerr != nil || c.Phase != queue.Queued {
		t.Fatalf("resume: rerr=%+v phase=%v", rerr, c.Phase)
	}
	if c, rerr := op("queue.emergency"); rerr != nil || c.EmergencySequence == 0 {
		t.Fatalf("emergency: rerr=%+v seq=%d", rerr, c.EmergencySequence)
	}
	c, rerr2 := op("queue.override")
	if rerr2 != nil || !c.OverrideGate || c.OverrideBy != "brad" {
		t.Fatalf("override: rerr=%+v gate=%v by=%q", rerr2, c.OverrideGate, c.OverrideBy)
	}
	if c, rerr := op("queue.reject"); rerr != nil || c.Phase != queue.Rejected {
		t.Fatalf("reject: rerr=%+v phase=%v", rerr, c.Phase)
	}

	// Terminal candidate: further verbs error, data is retained.
	if _, rerr := op("queue.park"); rerr == nil {
		t.Fatalf("park after reject should fail")
	}
	out, rerr = s.dispatch(context.Background(), "queue.status", map[string]any{})
	if rerr != nil {
		t.Fatalf("final queue.status error: %+v", rerr)
	}
	state := out.(queue.State)
	if len(state.Candidates) != 1 || state.Candidates[0].Phase != queue.Rejected {
		t.Fatalf("expected 1 retained rejected candidate, got %+v", state.Candidates)
	}
}

// The "project" param overrides the server's configured root.
func TestQueueRPC_ProjectParam(t *testing.T) {
	other := t.TempDir()
	store := queue.Store{ProjectRoot: other}
	sha := strings.Repeat("cd", 20)
	if _, err := store.Submit(queue.Submit{Branch: "agent/y", SHA: sha, Admission: queue.EmergencySkipTestsAdmission}); err != nil {
		t.Fatalf("seed submit: %v", err)
	}
	s := newQueueServer(t.TempDir()) // server root has no queue
	out, rerr := s.dispatch(context.Background(), "queue.status", map[string]any{"project": other})
	if rerr != nil {
		t.Fatalf("queue.status(project) error: %+v", rerr)
	}
	if state := out.(queue.State); len(state.Candidates) != 1 {
		t.Fatalf("expected 1 candidate via project param, got %d", len(state.Candidates))
	}
}
