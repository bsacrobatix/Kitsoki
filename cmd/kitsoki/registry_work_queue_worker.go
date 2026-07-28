package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"kitsoki/internal/host"
	"kitsoki/internal/webconfig"
	"kitsoki/internal/workerregistry"
	"kitsoki/internal/workqueue"
)

func (r *SessionRegistry) wireWorkQueueWorker(
	rt *sessionRuntime,
	applicationID, storyPath string,
) {
	if rt == nil || rt.HostRegistry == nil || applicationID == "" {
		return
	}
	binding, ok := r.cfg.WorkQueueWorkers[applicationID]
	if !ok {
		return
	}
	// Application IDs are story-authored metadata, not authentication. The
	// privileged worker host is installed only for the operator-allowlisted
	// absolute story path.
	if filepath.Clean(storyPath) != filepath.Clean(binding.StoryPath) {
		return
	}
	r.mu.Lock()
	store := r.workQueueStore
	r.mu.Unlock()
	if store == nil {
		return
	}
	worker, ok := configuredQueueWorker(r.cfg.Workers, binding.WorkerID)
	if !ok {
		return
	}
	rt.HostRegistry.Replace("host.work_queue_worker", host.NewWorkQueueWorkerHandler(workQueueWorkerAdapter{
		store: store, binding: binding, capabilities: workerregistry.CapabilityLabels(worker),
	}))
}

func configuredQueueWorker(entries []workerregistry.Entry, id string) (workerregistry.Entry, bool) {
	for _, entry := range entries {
		if entry.ID == id && entry.Enabled {
			return entry, true
		}
	}
	return workerregistry.Entry{}, false
}

type workQueueWorkerAdapter struct {
	store        workqueue.Store
	binding      webconfig.WorkQueueWorkerBinding
	capabilities []string
}

func (a workQueueWorkerAdapter) Claim(ctx context.Context, queue string) (map[string]any, error) {
	if !a.allowedQueue(queue) {
		return nil, workqueue.ErrNotFound
	}
	lease, err := a.store.ClaimNext(ctx, workqueue.ClaimRequest{
		ApplicationID: a.binding.TargetApplication, Queue: queue,
		WorkerID: a.binding.WorkerID, Capabilities: a.capabilities,
		AvailableCapacity: a.binding.MaxConcurrent,
		LeaseDuration:     time.Duration(a.binding.LeaseSeconds) * time.Second,
	})
	if err != nil {
		return nil, err
	}
	if lease == nil {
		return map[string]any{"claimed": false}, nil
	}
	var payload any
	if err := json.Unmarshal(lease.Job.Payload, &payload); err != nil {
		return nil, fmt.Errorf("decode leased payload: %w", err)
	}
	return map[string]any{
		"claimed": true, "work_ref": lease.Job.ID, "queue": lease.Job.Queue,
		"payload": payload, "fence": lease.Job.Fence, "attempt": lease.Job.Attempts,
		"lease_expires_at": lease.Job.LeaseExpiresAt,
	}, nil
}

func (a workQueueWorkerAdapter) Heartbeat(ctx context.Context, workRef string, fence int64) (map[string]any, error) {
	if err := a.authorizeWork(ctx, workRef); err != nil {
		return nil, err
	}
	job, err := a.store.Heartbeat(ctx, workRef, a.binding.WorkerID, fence, time.Duration(a.binding.LeaseSeconds)*time.Second)
	if err != nil {
		return nil, err
	}
	return workerJobProjection(job), nil
}

func (a workQueueWorkerAdapter) Complete(ctx context.Context, workRef string, fence int64, raw json.RawMessage) (map[string]any, error) {
	if err := a.authorizeWork(ctx, workRef); err != nil {
		return nil, err
	}
	var receipt workqueue.Receipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return nil, fmt.Errorf("decode receipt: %w", err)
	}
	job, err := a.store.Complete(ctx, workRef, a.binding.WorkerID, fence, receipt)
	if err != nil {
		return nil, err
	}
	return workerJobProjection(job), nil
}

func (a workQueueWorkerAdapter) Fail(ctx context.Context, workRef string, fence int64, retryable bool, reason string, raw json.RawMessage) (map[string]any, error) {
	if err := a.authorizeWork(ctx, workRef); err != nil {
		return nil, err
	}
	var receipt workqueue.Receipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		return nil, fmt.Errorf("decode receipt: %w", err)
	}
	job, err := a.store.Fail(ctx, workRef, a.binding.WorkerID, fence, workqueue.Failure{Retryable: retryable, Message: strings.TrimSpace(reason), Receipt: receipt})
	if err != nil {
		return nil, err
	}
	return workerJobProjection(job), nil
}

func (a workQueueWorkerAdapter) authorizeWork(ctx context.Context, workRef string) error {
	job, err := a.store.Get(ctx, strings.TrimSpace(workRef))
	if err != nil {
		return err
	}
	if job.ApplicationID != a.binding.TargetApplication || !a.allowedQueue(job.Queue) {
		return workqueue.ErrNotFound
	}
	return nil
}

func (a workQueueWorkerAdapter) allowedQueue(queue string) bool {
	for _, allowed := range a.binding.AllowedQueues {
		if queue == allowed {
			return true
		}
	}
	return false
}

func workerJobProjection(job workqueue.Job) map[string]any {
	return map[string]any{"work_ref": job.ID, "queue": job.Queue, "status": string(job.State), "fence": job.Fence, "attempt": job.Attempts, "receipt": job.Receipt}
}
