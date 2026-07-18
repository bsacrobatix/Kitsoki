package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/capsule/queue"
)

func queueCall(t *testing.T, op, project string, extra map[string]any) Result {
	t.Helper()
	args := map[string]any{"op": op, "project": project}
	for k, v := range extra {
		args[k] = v
	}
	res, err := QueueHandler(context.Background(), args)
	if err != nil {
		t.Fatalf("QueueHandler(%s): %v", op, err)
	}
	return res
}

func queueCandidatePhase(t *testing.T, res Result) string {
	t.Helper()
	if res.Error != "" {
		t.Fatalf("Result.Error = %q, want empty", res.Error)
	}
	c, ok := res.Data["candidate"].(map[string]any)
	if !ok {
		t.Fatalf("Result.Data[candidate] = %#v, want a map", res.Data["candidate"])
	}
	phase, _ := c["phase"].(string)
	return phase
}

// TestQueueHandler_StatusEmptyProject: host.queue.status on a fresh tmp
// project returns the empty durable state, not an error.
func TestQueueHandler_StatusEmptyProject(t *testing.T) {
	res := queueCall(t, "status", t.TempDir(), nil)
	if res.Error != "" {
		t.Fatalf("Result.Error = %q, want empty", res.Error)
	}
	state, ok := res.Data["state"].(map[string]any)
	if !ok {
		t.Fatalf("Result.Data[state] = %#v, want a map", res.Data["state"])
	}
	if candidates, ok := state["candidates"].([]any); !ok || len(candidates) != 0 {
		t.Errorf("state[candidates] = %#v, want an empty list", state["candidates"])
	}
}

// TestQueueHandler_UnknownCandidate: an operator op (and a status get) on a
// candidate id that does not exist is a clean domain error — Result.Error set,
// no Go error.
func TestQueueHandler_UnknownCandidate(t *testing.T) {
	dir := t.TempDir()
	for _, op := range []string{"kick", "park", "resume", "emergency", "override", "reject"} {
		res := queueCall(t, op, dir, map[string]any{"id": "nope", "actor": "tester"})
		if !strings.Contains(res.Error, "unknown candidate") {
			t.Errorf("op %s: Result.Error = %q, want an unknown-candidate domain error", op, res.Error)
		}
	}
	res := queueCall(t, "status", dir, map[string]any{"id": "nope"})
	if !strings.Contains(res.Error, "unknown candidate") {
		t.Errorf("status with unknown id: Result.Error = %q, want an unknown-candidate domain error", res.Error)
	}
}

// TestQueueHandler_UnknownOp: an unrecognised op is an infra (Go) error.
func TestQueueHandler_UnknownOp(t *testing.T) {
	_, err := QueueHandler(context.Background(), map[string]any{"op": "explode", "project": t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "unknown op") {
		t.Fatalf("err = %v, want an unknown-op error", err)
	}
}

// TestQueueHandler_ParkResumeHappyPath seeds a real candidate through the
// queue package's own Submit, then drives park and resume purely through the
// host handler, asserting the returned phases and the id/status surfaced by
// host.queue.status.
func TestQueueHandler_ParkResumeHappyPath(t *testing.T) {
	dir := t.TempDir()
	seeded, err := queue.Store{ProjectRoot: dir}.Submit(queue.Submit{
		Branch:    "b",
		SHA:       strings.Repeat("a", 40),
		Admission: queue.EmergencySkipTestsAdmission,
	})
	if err != nil {
		t.Fatalf("seed Submit: %v", err)
	}

	res := queueCall(t, "park", dir, map[string]any{"id": seeded.ID, "actor": "tester", "reason": "hold for review"})
	if phase := queueCandidatePhase(t, res); phase != string(queue.NeedsInput) {
		t.Errorf("park phase = %q, want %q", phase, queue.NeedsInput)
	}

	res = queueCall(t, "resume", dir, map[string]any{"id": seeded.ID, "actor": "tester", "reason": "resolved"})
	if phase := queueCandidatePhase(t, res); phase != string(queue.Queued) {
		t.Errorf("resume phase = %q, want %q", phase, queue.Queued)
	}

	res = queueCall(t, "status", dir, map[string]any{"id": seeded.ID})
	if res.Error != "" {
		t.Fatalf("status Result.Error = %q, want empty", res.Error)
	}
	c, _ := res.Data["candidate"].(map[string]any)
	if c["id"] != seeded.ID {
		t.Errorf("status candidate id = %v, want %q", c["id"], seeded.ID)
	}
	if c["phase"] != string(queue.Queued) {
		t.Errorf("status candidate phase = %v, want %q", c["phase"], queue.Queued)
	}
}

// TestNewStarlarkRunHandler_CtxHostQueueVerbAllowListed proves the queue
// verbs are reachable from a starlark script through ctx.host.call when the
// run's capabilities allow-list them: host.queue.status resolves through the
// registry's longest-prefix fallback to the QueueHandler RegisterBuiltins
// registered, against a real (empty) tmp queue.
func TestNewStarlarkRunHandler_CtxHostQueueVerbAllowListed(t *testing.T) {
	dir := t.TempDir()
	project := filepath.Join(dir, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "glue.star")
	if err := os.WriteFile(script, []byte(
		"def main(ctx):\n"+
			"    out = ctx.host.call(\"host.queue.status\", {\"project\": ctx.inputs[\"project\"]})\n"+
			"    return {\"count\": len(out[\"state\"][\"candidates\"])}\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script+".yaml", []byte(
		"inputs:\n  project: { type: string }\noutputs:\n  count: { type: int }\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := NewRegistry()
	RegisterBuiltins(reg)
	handler := NewStarlarkRunHandler(reg)

	res, err := handler(context.Background(), map[string]any{
		"script":       script,
		"inputs":       map[string]any{"project": project},
		"capabilities": starlarkTestHostCapabilities("host.queue.status"),
	})
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("handler result.Error = %q, want empty", res.Error)
	}
	if count, ok := res.Data["count"].(int64); !ok || count != 0 {
		t.Errorf("result.Data[count] = %#v (%T), want 0", res.Data["count"], res.Data["count"])
	}
}
