package webconfig

import (
	"fmt"
	"path/filepath"
	"strings"

	"kitsoki/internal/workerregistry"
	"kitsoki/internal/workqueue"
)

// WorkQueueExecutorBinding pins one work queue to one registered Capsule CI
// workspace/pipeline and worker placement. No queue payload can choose a
// repository, source revision, workspace, story, executor, or output path.
type WorkQueueExecutorBinding struct {
	TargetApplication string                          `yaml:"target_application"`
	Queue             string                          `yaml:"queue"`
	ProjectRoot       string                          `yaml:"project_root"`
	WorkspaceID       string                          `yaml:"workspace_id"`
	Pipeline          string                          `yaml:"pipeline"`
	WorkerID          string                          `yaml:"worker_id"`
	WorkerPolicy      string                          `yaml:"worker_policy"`
	InputSchema       map[string]workqueue.InputField `yaml:"input_schema"`
	// Bundle*Output name the daemon-owned verdict projection written after it
	// reads the worker's retained WIP bundle. They never authorize Story
	// supplied bundle values.
	BundleRefOutput    string `yaml:"bundle_ref_output"`
	BundleDigestOutput string `yaml:"bundle_digest_output"`
	BundleKindOutput   string `yaml:"bundle_kind_output,omitempty"`
	MaxConcurrent      int    `yaml:"max_concurrent"`
	LeaseSeconds       int    `yaml:"lease_seconds"`
	PollSeconds        int    `yaml:"poll_seconds"`
}

func (cfg *WebConfig) resolveWorkQueueExecutors() error {
	if len(cfg.WorkQueueExecutors) > maxWorkQueueWorkers {
		return fmt.Errorf("work_queue_executors has %d bindings, exceeds %d", len(cfg.WorkQueueExecutors), maxWorkQueueWorkers)
	}
	workers := enabledQueueExecutorWorkers(cfg.Workers)
	for id, b := range cfg.WorkQueueExecutors {
		if err := configIdentity("executor id", id); err != nil {
			return fmt.Errorf("work_queue_executors: %w", err)
		}
		b.TargetApplication, b.Queue, b.WorkspaceID, b.Pipeline, b.WorkerID, b.WorkerPolicy = strings.TrimSpace(b.TargetApplication), strings.TrimSpace(b.Queue), strings.TrimSpace(b.WorkspaceID), strings.TrimSpace(b.Pipeline), strings.TrimSpace(b.WorkerID), strings.TrimSpace(b.WorkerPolicy)
		b.ProjectRoot = filepath.Clean(strings.TrimSpace(b.ProjectRoot))
		for label, value := range map[string]string{"target_application": b.TargetApplication, "queue": b.Queue, "workspace_id": b.WorkspaceID, "pipeline": b.Pipeline, "worker_id": b.WorkerID, "worker_policy": b.WorkerPolicy, "bundle_ref_output": b.BundleRefOutput, "bundle_digest_output": b.BundleDigestOutput} {
			if err := configIdentity(label, value); err != nil {
				return fmt.Errorf("work_queue_executors.%s: %w", id, err)
			}
		}
		if b.BundleKindOutput != "" {
			if err := configIdentity("bundle_kind_output", b.BundleKindOutput); err != nil {
				return fmt.Errorf("work_queue_executors.%s: %w", id, err)
			}
		}
		if !filepath.IsAbs(b.ProjectRoot) {
			return fmt.Errorf("work_queue_executors.%s.project_root must be an absolute allowlisted project root", id)
		}
		queues, ok := cfg.WorkQueues[b.TargetApplication]
		queue, queueOK := queues[b.Queue]
		if !ok || !queueOK {
			return fmt.Errorf("work_queue_executors.%s names an unknown queue", id)
		}
		if !workers[b.WorkerID] {
			return fmt.Errorf("work_queue_executors.%s.worker_id %q is not an enabled worker", id, b.WorkerID)
		}
		if err := validateWorkQueueExecutorSchema(b.InputSchema); err != nil {
			return fmt.Errorf("work_queue_executors.%s.input_schema: %w", id, err)
		}
		if queue.ProducesCode && (b.BundleRefOutput == "" || b.BundleDigestOutput == "") {
			return fmt.Errorf("work_queue_executors.%s code queue requires terminal bundle outputs", id)
		}
		if b.MaxConcurrent < 1 || b.MaxConcurrent > maxWorkQueueWorkerCapacity || b.LeaseSeconds < 1 || b.LeaseSeconds > maxWorkQueueWorkerLeaseSeconds || b.PollSeconds < 1 || b.PollSeconds > 3600 {
			return fmt.Errorf("work_queue_executors.%s has invalid concurrency, lease, or poll bounds", id)
		}
		cfg.WorkQueueExecutors[id] = b
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

func validateWorkQueueExecutorSchema(schema map[string]workqueue.InputField) error {
	// Reuse the executor's construction guard without making config depend on
	// an unexported validator.
	if len(schema) == 0 || len(schema) > 32 {
		return fmt.Errorf("must contain 1..32 fields")
	}
	for name, field := range schema {
		if err := configIdentity("field", name); err != nil {
			return err
		}
		switch field.Type {
		case "string", "integer", "number", "boolean", "object", "array":
		default:
			return fmt.Errorf("field %q has unsupported type %q", name, field.Type)
		}
	}
	return nil
}
