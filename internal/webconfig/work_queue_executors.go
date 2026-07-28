package webconfig

import (
	"fmt"
	"strings"

	"kitsoki/internal/workerregistry"
)

// WorkQueueExecutorBinding installs a daemon-owned adapter from one queue to
// one registered Application Job. No Story input can select its target.
type WorkQueueExecutorBinding struct {
	TargetApplication string `yaml:"target_application"`
	Queue             string `yaml:"queue"`
	WorkerID          string `yaml:"worker_id"`
	CallerApplication string `yaml:"caller_application"`
	Template          string `yaml:"template"`
	MaxConcurrent     int    `yaml:"max_concurrent"`
	LeaseSeconds      int    `yaml:"lease_seconds"`
	PollSeconds       int    `yaml:"poll_seconds"`
}

func (cfg *WebConfig) resolveWorkQueueExecutors() error {
	if len(cfg.WorkQueueExecutors) > maxWorkQueueWorkers {
		return fmt.Errorf("work_queue_executors has %d bindings, exceeds %d", len(cfg.WorkQueueExecutors), maxWorkQueueWorkers)
	}
	workers := enabledQueueExecutorWorkers(cfg.Workers)
	for id, binding := range cfg.WorkQueueExecutors {
		if err := configIdentity("executor id", id); err != nil {
			return fmt.Errorf("work_queue_executors: %w", err)
		}
		binding.TargetApplication, binding.Queue, binding.WorkerID = strings.TrimSpace(binding.TargetApplication), strings.TrimSpace(binding.Queue), strings.TrimSpace(binding.WorkerID)
		binding.CallerApplication, binding.Template = strings.TrimSpace(binding.CallerApplication), strings.TrimSpace(binding.Template)
		for label, value := range map[string]string{"target_application": binding.TargetApplication, "queue": binding.Queue, "worker_id": binding.WorkerID, "caller_application": binding.CallerApplication, "template": binding.Template} {
			if err := configIdentity(label, value); err != nil {
				return fmt.Errorf("work_queue_executors.%s: %w", id, err)
			}
		}
		queues, ok := cfg.WorkQueues[binding.TargetApplication]
		queue, queueOK := queues[binding.Queue]
		if !ok || !queueOK {
			return fmt.Errorf("work_queue_executors.%s names an unknown queue", id)
		}
		if !workers[binding.WorkerID] {
			return fmt.Errorf("work_queue_executors.%s.worker_id %q is not an enabled worker", id, binding.WorkerID)
		}
		template, ok := cfg.StoryApplicationJobs[binding.CallerApplication][binding.Template]
		if !ok {
			return fmt.Errorf("work_queue_executors.%s names an unconfigured application job", id)
		}
		if queue.ProducesCode && (template.BundleRefOutput == "" || template.BundleDigestOutput == "") {
			return fmt.Errorf("work_queue_executors.%s code queue requires application-job bundle outputs", id)
		}
		if binding.MaxConcurrent < 1 || binding.MaxConcurrent > maxWorkQueueWorkerCapacity || binding.LeaseSeconds < 1 || binding.LeaseSeconds > maxWorkQueueWorkerLeaseSeconds || binding.PollSeconds < 1 || binding.PollSeconds > 3600 {
			return fmt.Errorf("work_queue_executors.%s has invalid concurrency, lease, or poll bounds", id)
		}
		cfg.WorkQueueExecutors[id] = binding
	}
	return nil
}

func enabledQueueExecutorWorkers(workers []workerregistry.Entry) map[string]bool {
	out := make(map[string]bool, len(workers))
	for _, worker := range workers {
		if worker.Enabled {
			out[worker.ID] = true
		}
	}
	return out
}
