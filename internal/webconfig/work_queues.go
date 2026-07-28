package webconfig

import (
	"fmt"
	"path/filepath"
	"strings"

	"kitsoki/internal/workqueue"
)

const maxWorkQueueApplications = 64

// resolveWorkQueues validates and normalizes server-owned story queue policy.
// Applications and queue names are opaque identifiers; workers and story input
// cannot supply endpoints, credentials, retry policy, or capability policy.
func (cfg *WebConfig) resolveWorkQueues() error {
	if len(cfg.WorkQueues) > maxWorkQueueApplications {
		return fmt.Errorf("work_queues has %d applications, exceeds %d", len(cfg.WorkQueues), maxWorkQueueApplications)
	}
	cfg.WorkQueueBundleRoot = strings.TrimSpace(cfg.WorkQueueBundleRoot)
	if cfg.WorkQueueBundleRoot != "" {
		cfg.WorkQueueBundleRoot = filepath.Clean(cfg.WorkQueueBundleRoot)
	}
	producesCode := false
	for applicationID, queues := range cfg.WorkQueues {
		if err := configIdentity("application id", applicationID); err != nil {
			return fmt.Errorf("work_queues: %w", err)
		}
		if len(queues) == 0 || len(queues) > 64 {
			return fmt.Errorf("work_queues.%s queue count must be within 1..64", applicationID)
		}
		for name, queue := range queues {
			if err := configIdentity("queue name", name); err != nil {
				return fmt.Errorf("work_queues.%s: %w", applicationID, err)
			}
			resolved, err := workqueue.ResolveQueueConfig(queue)
			if err != nil {
				return fmt.Errorf("work_queues.%s.%s is invalid or exceeds a safe bound", applicationID, name)
			}
			queues[name] = resolved
			producesCode = producesCode || resolved.ProducesCode
		}
	}
	if producesCode && !filepath.IsAbs(cfg.WorkQueueBundleRoot) {
		return fmt.Errorf("work_queue_bundle_root must be absolute when a queue produces code")
	}
	return nil
}
