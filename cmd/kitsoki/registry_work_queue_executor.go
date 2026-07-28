package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"kitsoki/internal/applicationjob"
	"kitsoki/internal/workerregistry"
	"kitsoki/internal/workqueue"
)

// StartWorkQueueExecutors starts only server-configured adapters. It returns a
// shutdown function so daemon termination stops heartbeats and leaves leases
// to their durable expiry/recovery path.
func (r *SessionRegistry) StartWorkQueueExecutors(ctx context.Context) (func(), error) {
	r.mu.Lock()
	store, jobs, bindings, workers := r.workQueueStore, r.applicationJobs, r.cfg.WorkQueueExecutors, append([]workerregistry.Entry(nil), r.cfg.Workers...)
	r.mu.Unlock()
	if len(bindings) == 0 {
		return func() {}, nil
	}
	if store == nil || jobs == nil {
		return nil, fmt.Errorf("work queue application executors require daemon queue and application-job services")
	}
	runCtx, cancel := context.WithCancel(ctx)
	for _, binding := range bindings {
		worker, ok := configuredQueueWorker(workers, binding.WorkerID)
		if !ok {
			cancel()
			return nil, fmt.Errorf("work queue executor worker %q is unavailable", binding.WorkerID)
		}
		executor := workqueue.ApplicationExecutor{Store: store, Jobs: applicationJobQueueClient{service: jobs}, Config: workqueue.ApplicationExecutorConfig{
			ApplicationID: binding.TargetApplication, Queue: binding.Queue, WorkerID: binding.WorkerID,
			CallerApplication: binding.CallerApplication, Template: binding.Template,
			Capabilities: workerregistry.CapabilityLabels(worker), MaxConcurrent: binding.MaxConcurrent,
			LeaseDuration: time.Duration(binding.LeaseSeconds) * time.Second, PollInterval: time.Duration(binding.PollSeconds) * time.Second,
		}}
		go func() { _ = executor.Run(runCtx) }()
	}
	return cancel, nil
}

type applicationJobQueueClient struct{ service *applicationjob.Service }

func (c applicationJobQueueClient) Submit(ctx context.Context, caller, template string, input json.RawMessage) (workqueue.ApplicationJobResult, error) {
	result, err := c.service.Submit(ctx, caller, template, input)
	return queueApplicationJobResult(result), err
}

func (c applicationJobQueueClient) Status(ctx context.Context, caller, ref string) (workqueue.ApplicationJobResult, error) {
	result, err := c.service.Status(ctx, caller, ref)
	return queueApplicationJobResult(result), err
}

func queueApplicationJobResult(result applicationjob.Result) workqueue.ApplicationJobResult {
	return workqueue.ApplicationJobResult{Ref: result.JobRef, Status: result.Status, Reason: result.Reason,
		Artifacts: append([]string(nil), result.Artifacts...), BundleRef: result.BundleRef,
		BundleDigest: result.BundleDigest, BundleKind: result.BundleKind}
}
