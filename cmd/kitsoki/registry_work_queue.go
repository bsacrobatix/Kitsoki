package main

import (
	"context"
	"encoding/json"

	"kitsoki/internal/host"
	"kitsoki/internal/workqueue"
)

func (r *SessionRegistry) wireWorkQueue(rt *sessionRuntime, applicationID string) {
	if rt == nil || rt.HostRegistry == nil || applicationID == "" {
		return
	}
	r.mu.Lock()
	service := r.workQueueServices[applicationID]
	r.mu.Unlock()
	if service == nil {
		return
	}
	rt.HostRegistry.Replace(
		"host.work_queue",
		host.NewWorkQueueHandler(
			workQueueServiceAdapter{service: service},
			applicationID,
		),
	)
}

type workQueueServiceAdapter struct {
	service *workqueue.Service
}

func (a workQueueServiceAdapter) Enqueue(
	ctx context.Context,
	applicationID, queue, idempotencyKey string,
	input json.RawMessage,
) (map[string]any, error) {
	receipt, err := a.service.Enqueue(
		ctx, applicationID, queue, idempotencyKey, input,
	)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"work_ref": receipt.Ref,
		"status":   string(receipt.Status),
		"replayed": receipt.Replayed,
		"receipt":  receipt,
	}, nil
}

func (a workQueueServiceAdapter) Get(
	ctx context.Context,
	applicationID, workRef string,
) (map[string]any, error) {
	projection, err := a.service.Get(ctx, applicationID, workRef)
	if err != nil {
		return nil, err
	}
	countable := projection.Receipt != nil && projection.Receipt.Countable
	return map[string]any{
		"work_ref":     projection.Ref,
		"queue":        projection.Queue,
		"status":       string(projection.Status),
		"attempt":      projection.Attempts,
		"max_attempts": projection.MaxAttempts,
		"countable":    countable,
		"receipt":      projection.Receipt,
	}, nil
}

func (a workQueueServiceAdapter) Snapshot(
	ctx context.Context,
	applicationID string,
	maxItems, maxBytes int,
) (map[string]any, error) {
	items, err := a.service.Snapshot(ctx, applicationID, maxItems, maxBytes)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"schema": "kitsoki/work-queue-snapshot/v1",
		"items":  items,
		"counts": map[string]any{"items": len(items)},
	}, nil
}
