package host

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// WorkQueueWorkerController is the daemon-owned worker lease boundary. Each
// implementation is constructed with a fixed target application, worker, and
// queue policy; story calls supply only work identifiers and receipts.
type WorkQueueWorkerController interface {
	Claim(context.Context, string) (map[string]any, error)
	Heartbeat(context.Context, string, int64) (map[string]any, error)
	Complete(context.Context, string, int64, json.RawMessage) (map[string]any, error)
	Fail(context.Context, string, int64, bool, string, json.RawMessage) (map[string]any, error)
}

// NewWorkQueueWorkerHandler exposes only the worker operations to a configured
// worker application. Its nil form is the fail-closed builtin sentinel.
func NewWorkQueueWorkerHandler(controller WorkQueueWorkerController) Handler {
	return func(ctx context.Context, args map[string]any) (Result, error) {
		if controller == nil {
			return Result{Error: "host.work_queue_worker: durable work queue worker is unavailable outside configured daemon mode"}, nil
		}
		op, _ := args["op"].(string)
		switch op {
		case "claim":
			queue, _ := args["queue"].(string)
			data, err := controller.Claim(ctx, queue)
			if err != nil {
				return Result{}, fmt.Errorf("host.work_queue_worker.claim: %w", err)
			}
			return Result{Data: data}, nil
		case "heartbeat":
			workRef, fence, err := workQueueWorkerLeaseArgs(args)
			if err != nil {
				return Result{}, fmt.Errorf("host.work_queue_worker.heartbeat: %w", err)
			}
			data, err := controller.Heartbeat(ctx, workRef, fence)
			if err != nil {
				return Result{}, fmt.Errorf("host.work_queue_worker.heartbeat: %w", err)
			}
			return Result{Data: data}, nil
		case "complete":
			workRef, fence, err := workQueueWorkerLeaseArgs(args)
			if err != nil {
				return Result{}, fmt.Errorf("host.work_queue_worker.complete: %w", err)
			}
			receipt, err := workQueueWorkerReceipt(args["receipt"])
			if err != nil {
				return Result{}, fmt.Errorf("host.work_queue_worker.complete: %w", err)
			}
			data, err := controller.Complete(ctx, workRef, fence, receipt)
			if err != nil {
				return Result{}, fmt.Errorf("host.work_queue_worker.complete: %w", err)
			}
			return Result{Data: data}, nil
		case "fail":
			workRef, fence, err := workQueueWorkerLeaseArgs(args)
			if err != nil {
				return Result{}, fmt.Errorf("host.work_queue_worker.fail: %w", err)
			}
			retryable, _ := args["retryable"].(bool)
			reason, _ := args["reason"].(string)
			receipt, err := workQueueWorkerReceipt(args["receipt"])
			if err != nil {
				return Result{}, fmt.Errorf("host.work_queue_worker.fail: %w", err)
			}
			data, err := controller.Fail(ctx, workRef, fence, retryable, reason, receipt)
			if err != nil {
				return Result{}, fmt.Errorf("host.work_queue_worker.fail: %w", err)
			}
			return Result{Data: data}, nil
		default:
			return Result{}, fmt.Errorf("host.work_queue_worker: unknown op %q (want claim, heartbeat, complete, or fail)", op)
		}
	}
}

func workQueueWorkerLeaseArgs(args map[string]any) (string, int64, error) {
	workRef, _ := args["work_ref"].(string)
	if strings.TrimSpace(workRef) == "" {
		return "", 0, fmt.Errorf("work_ref is required")
	}
	fence, ok := workQueueWorkerFence(args["fence"])
	if !ok || fence < 1 {
		return "", 0, fmt.Errorf("fence must be a positive integer")
	}
	return workRef, fence, nil
}

func workQueueWorkerFence(value any) (int64, bool) {
	switch value := value.(type) {
	case int:
		return int64(value), true
	case int64:
		return value, true
	case float64:
		if math.Trunc(value) == value && value >= math.MinInt64 && value <= math.MaxInt64 {
			return int64(value), true
		}
	}
	return 0, false
}

func workQueueWorkerReceipt(value any) (json.RawMessage, error) {
	if value == nil {
		return nil, fmt.Errorf("receipt is required")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("receipt must be JSON serializable: %w", err)
	}
	return raw, nil
}

// WorkQueueWorkerHandler is replaced only for configured worker applications.
var WorkQueueWorkerHandler = NewWorkQueueWorkerHandler(nil)
