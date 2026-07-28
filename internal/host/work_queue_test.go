package host

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"kitsoki/internal/effect"
	"kitsoki/internal/host/opschema"
)

func TestWorkQueueHandlerFailsClosedOutsideDaemon(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltins(registry)
	result, err := registry.Invoke(
		context.Background(),
		"host.work_queue.snapshot",
		map[string]any{"max_items": 10, "max_bytes": 1024},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Error, "unavailable outside daemon mode") {
		t.Fatalf("error = %q", result.Error)
	}
}

func TestWorkQueueHandlerFixesApplicationScope(t *testing.T) {
	controller := &workQueueControllerCapture{}
	handler := NewWorkQueueHandler(controller, "operations")
	result, err := handler(context.Background(), map[string]any{
		"op": "enqueue", "queue": "feedback",
		"idempotency_key": "feedback:1",
		"input":           map[string]any{"feedback_ref": "one"},
		"application_id":  "other",
	})
	if err != nil {
		t.Fatal(err)
	}
	if controller.applicationID != "operations" {
		t.Fatalf("application id = %q", controller.applicationID)
	}
	if result.Data["work_ref"] != "wq_1" {
		t.Fatalf("result = %#v", result.Data)
	}
	var input map[string]any
	if err := json.Unmarshal(controller.input, &input); err != nil {
		t.Fatal(err)
	}
	if input["feedback_ref"] != "one" {
		t.Fatalf("input = %#v", input)
	}
}

func TestWorkQueueRegistrationSchemaAndEffect(t *testing.T) {
	class, deterministic := ClassifyDispatchedCall(
		"host.work_queue",
		map[string]any{"op": "enqueue"},
	)
	if class != effect.Write || !deterministic {
		t.Fatalf("enqueue classification = (%q, %v)", class, deterministic)
	}
	class, deterministic = ClassifyDispatchedCall(
		"host.work_queue.snapshot", nil,
	)
	if class != effect.Read || !deterministic {
		t.Fatalf("snapshot classification = (%q, %v)", class, deterministic)
	}
	enqueue, ok := opschema.Builtins().Lookup("host.work_queue", "enqueue")
	if !ok || enqueue.Input["queue"].Type != "string" ||
		enqueue.Output["work_ref"].Type != "string" {
		t.Fatalf("enqueue opschema = %#v, %v", enqueue, ok)
	}
}

type workQueueControllerCapture struct {
	applicationID string
	input         json.RawMessage
}

func (c *workQueueControllerCapture) Enqueue(
	_ context.Context,
	applicationID, _, _ string,
	input json.RawMessage,
) (map[string]any, error) {
	c.applicationID = applicationID
	c.input = input
	return map[string]any{"work_ref": "wq_1"}, nil
}

func (c *workQueueControllerCapture) Get(
	_ context.Context,
	applicationID, _ string,
) (map[string]any, error) {
	c.applicationID = applicationID
	return map[string]any{"work_ref": "wq_1"}, nil
}

func (c *workQueueControllerCapture) Snapshot(
	_ context.Context,
	applicationID string,
	_, _ int,
) (map[string]any, error) {
	c.applicationID = applicationID
	return map[string]any{"application_id": applicationID}, nil
}
