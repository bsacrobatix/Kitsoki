package webconfig

import (
	"fmt"
	"path/filepath"
	"strings"
)

const (
	maxWorkQueueWorkers            = 64
	maxWorkQueueWorkerQueues       = 64
	maxWorkQueueWorkerCapacity     = 64
	maxWorkQueueWorkerLeaseSeconds = 3600
)

// WorkQueueWorkerBinding grants one worker Story Application a fixed lease
// adapter over queues owned by another application. No story argument selects
// an application, worker, capacity, capability set, or lease duration.
type WorkQueueWorkerBinding struct {
	TargetApplication string   `yaml:"target_application"`
	StoryPath         string   `yaml:"story_path"`
	AllowedQueues     []string `yaml:"allowed_queues"`
	WorkerID          string   `yaml:"worker_id"`
	MaxConcurrent     int      `yaml:"max_concurrent"`
	LeaseSeconds      int      `yaml:"lease_seconds"`
}

func (cfg *WebConfig) resolveWorkQueueWorkers() error {
	if len(cfg.WorkQueueWorkers) > maxWorkQueueWorkers {
		return fmt.Errorf("work_queue_workers has %d applications, exceeds %d", len(cfg.WorkQueueWorkers), maxWorkQueueWorkers)
	}
	workers := make(map[string]bool, len(cfg.Workers))
	for _, worker := range cfg.Workers {
		if worker.Enabled {
			workers[worker.ID] = true
		}
	}
	for applicationID, binding := range cfg.WorkQueueWorkers {
		if err := configIdentity("application id", applicationID); err != nil {
			return fmt.Errorf("work_queue_workers: %w", err)
		}
		binding.TargetApplication = strings.TrimSpace(binding.TargetApplication)
		binding.StoryPath = filepath.Clean(strings.TrimSpace(binding.StoryPath))
		binding.WorkerID = strings.TrimSpace(binding.WorkerID)
		if err := configIdentity("target application", binding.TargetApplication); err != nil {
			return fmt.Errorf("work_queue_workers.%s: %w", applicationID, err)
		}
		if !filepath.IsAbs(binding.StoryPath) {
			return fmt.Errorf("work_queue_workers.%s.story_path must be an absolute allowlisted story path", applicationID)
		}
		queues, ok := cfg.WorkQueues[binding.TargetApplication]
		if !ok {
			return fmt.Errorf("work_queue_workers.%s.target_application %q has no work_queues", applicationID, binding.TargetApplication)
		}
		if !workers[binding.WorkerID] {
			return fmt.Errorf("work_queue_workers.%s.worker_id %q is not an enabled worker", applicationID, binding.WorkerID)
		}
		if len(binding.AllowedQueues) == 0 || len(binding.AllowedQueues) > maxWorkQueueWorkerQueues {
			return fmt.Errorf("work_queue_workers.%s.allowed_queues must contain 1..%d queues", applicationID, maxWorkQueueWorkerQueues)
		}
		seen := map[string]bool{}
		for _, queue := range binding.AllowedQueues {
			if err := configIdentity("queue name", queue); err != nil {
				return fmt.Errorf("work_queue_workers.%s.allowed_queues contains invalid queue %q", applicationID, queue)
			}
			if _, ok := queues[queue]; !ok || seen[queue] {
				return fmt.Errorf("work_queue_workers.%s.allowed_queues contains unknown or duplicate queue %q", applicationID, queue)
			}
			seen[queue] = true
		}
		if binding.MaxConcurrent < 1 || binding.MaxConcurrent > maxWorkQueueWorkerCapacity {
			return fmt.Errorf("work_queue_workers.%s.max_concurrent must be within 1..%d", applicationID, maxWorkQueueWorkerCapacity)
		}
		if binding.LeaseSeconds < 1 || binding.LeaseSeconds > maxWorkQueueWorkerLeaseSeconds {
			return fmt.Errorf("work_queue_workers.%s.lease_seconds must be within 1..%d", applicationID, maxWorkQueueWorkerLeaseSeconds)
		}
		cfg.WorkQueueWorkers[applicationID] = binding
	}
	return nil
}
