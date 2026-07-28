package host

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// WorkQueueController is the application-scoped story boundary for durable
// queued work. Worker claims and lease mutation deliberately stay off this
// interface and are available only to daemon-owned worker adapters.
type WorkQueueController interface {
	Enqueue(
		context.Context,
		string,
		string,
		string,
		json.RawMessage,
	) (map[string]any, error)
	Get(context.Context, string, string) (map[string]any, error)
	Snapshot(context.Context, string, int, int) (map[string]any, error)
}

// NewWorkQueueHandler fixes application scope at construction. Story input
// cannot select another application, worker, endpoint, or execution provider.
func NewWorkQueueHandler(controller WorkQueueController, applicationID string) Handler {
	return func(ctx context.Context, args map[string]any) (Result, error) {
		if controller == nil || strings.TrimSpace(applicationID) == "" {
			return Result{
				Error: "host.work_queue: durable work queue is unavailable outside daemon mode",
			}, nil
		}
		op, _ := args["op"].(string)
		switch op {
		case "enqueue":
			queue, _ := args["queue"].(string)
			idempotencyKey, _ := args["idempotency_key"].(string)
			input, err := workQueueInput(args["input"])
			if err != nil {
				return Result{}, fmt.Errorf("host.work_queue.enqueue: %w", err)
			}
			data, err := controller.Enqueue(
				ctx, applicationID, queue, idempotencyKey, input,
			)
			if err != nil {
				return Result{}, fmt.Errorf("host.work_queue.enqueue: %w", err)
			}
			return Result{Data: data}, nil
		case "get":
			workRef, _ := args["work_ref"].(string)
			data, err := controller.Get(ctx, applicationID, workRef)
			if err != nil {
				return Result{}, fmt.Errorf("host.work_queue.get: %w", err)
			}
			return Result{Data: data}, nil
		case "snapshot":
			maxItems, err := campaignIntArg(args, "max_items")
			if err != nil {
				return Result{}, fmt.Errorf("host.work_queue.snapshot: %w", err)
			}
			maxBytes, err := campaignIntArg(args, "max_bytes")
			if err != nil {
				return Result{}, fmt.Errorf("host.work_queue.snapshot: %w", err)
			}
			snapshot, err := controller.Snapshot(
				ctx, applicationID, maxItems, maxBytes,
			)
			if err != nil {
				return Result{}, fmt.Errorf("host.work_queue.snapshot: %w", err)
			}
			return Result{Data: map[string]any{"snapshot": snapshot}}, nil
		default:
			return Result{}, fmt.Errorf(
				"host.work_queue: unknown op %q (want enqueue, get, or snapshot)",
				op,
			)
		}
	}
}

func workQueueInput(value any) (json.RawMessage, error) {
	if value == nil {
		return json.RawMessage(`{}`), nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("input must be JSON serializable: %w", err)
	}
	return raw, nil
}

// WorkQueueHandler is the fail-closed builtin sentinel. Daemon-backed session
// construction replaces it only for applications with configured queues.
var WorkQueueHandler = NewWorkQueueHandler(nil, "")
