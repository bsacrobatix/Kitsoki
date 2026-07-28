package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"kitsoki/internal/effect"
	"kitsoki/internal/host/opschema"
)

func TestWorkQueueWorkerHandlerFailsClosedAndFixesWorkerAuthority(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry)
	unavailable, err := registry.Invoke(context.Background(), "host.work_queue_worker.claim", map[string]any{"queue": "q"})
	if err != nil || !strings.Contains(unavailable.Error, "unavailable outside configured daemon mode") {
		t.Fatalf("unavailable = %#v, %v", unavailable, err)
	}
	capture := &workQueueWorkerCapture{}
	registry.Replace("host.work_queue_worker", NewWorkQueueWorkerHandler(capture))
	got, err := registry.Invoke(context.Background(), "host.work_queue_worker.complete", map[string]any{"work_ref": "wq_1", "fence": 2, "receipt": map[string]any{"outcome": "done"}, "worker_id": "forged"})
	if err != nil || got.Data["work_ref"] != "wq_1" {
		t.Fatalf("complete = %#v, %v", got, err)
	}
	if capture.ref != "wq_1" || capture.fence != 2 || !strings.Contains(string(capture.receipt), "outcome") {
		t.Fatalf("capture = %#v", capture)
	}
}

func TestWorkQueueWorkerRegistrationSchemaAndEffect(t *testing.T) {
	class, deterministic := ClassifyDispatchedCall("host.work_queue_worker", map[string]any{"op": "claim"})
	if class != effect.Write || deterministic {
		t.Fatalf("claim classification = (%q, %v)", class, deterministic)
	}
	claim, ok := opschema.Builtins().Lookup("host.work_queue_worker", "claim")
	if !ok || claim.Input["queue"].Type != "string" || claim.Output["payload"].Type != "object" {
		t.Fatalf("claim schema = %#v, %v", claim, ok)
	}
}

type workQueueWorkerCapture struct {
	ref     string
	fence   int64
	receipt json.RawMessage
}

func (c *workQueueWorkerCapture) Claim(context.Context, string) (map[string]any, error) {
	return map[string]any{"claimed": false}, nil
}
func (c *workQueueWorkerCapture) Heartbeat(_ context.Context, ref string, fence int64) (map[string]any, error) {
	c.ref, c.fence = ref, fence
	return map[string]any{"work_ref": ref}, nil
}
func (c *workQueueWorkerCapture) Complete(_ context.Context, ref string, fence int64, receipt json.RawMessage) (map[string]any, error) {
	c.ref, c.fence, c.receipt = ref, fence, receipt
	return map[string]any{"work_ref": ref}, nil
}
func (c *workQueueWorkerCapture) Fail(_ context.Context, ref string, fence int64, _ bool, _ string, receipt json.RawMessage) (map[string]any, error) {
	c.ref, c.fence, c.receipt = ref, fence, receipt
	return map[string]any{"work_ref": ref}, nil
}
