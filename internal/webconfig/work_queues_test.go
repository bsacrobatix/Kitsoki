package webconfig

import (
	"testing"

	"kitsoki/internal/workqueue"
)

func TestWorkQueuesApplyDefaultsAndMergeByApplication(t *testing.T) {
	base, err := loadConfigText(t, `work_queues:
  pog-ops:
    triage: {}
`)
	if err != nil {
		t.Fatal(err)
	}
	triage := base.WorkQueues["pog-ops"]["triage"]
	if triage.MaxInputBytes != workqueue.DefaultMaxInputBytes || triage.MaxAttempts != workqueue.DefaultMaxAttempts {
		t.Fatalf("defaults = %#v", triage)
	}
	merged := mergeConfig(base, WebConfig{WorkQueues: map[string]map[string]workqueue.QueueConfig{
		"other": {"review": {Priority: 3}},
	}})
	if len(merged.WorkQueues) != 2 || merged.WorkQueues["pog-ops"]["triage"].MaxAttempts != workqueue.DefaultMaxAttempts || merged.WorkQueues["other"]["review"].Priority != 3 {
		t.Fatalf("merge = %#v", merged.WorkQueues)
	}
}

func TestWorkQueuesAcceptCodeQueueWithAbsoluteBundleRoot(t *testing.T) {
	cfg, err := loadConfigText(t, `work_queue_bundle_root: /var/lib/kitsoki/bundles
work_queues:
  app:
    code:
      produces_code: true
`)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.WorkQueues["app"]["code"].ProducesCode ||
		cfg.WorkQueueBundleRoot != "/var/lib/kitsoki/bundles" {
		t.Fatalf("code queue = %#v", cfg)
	}
}

func TestWorkQueuesRejectUnsafeConfiguration(t *testing.T) {
	for _, body := range []string{
		"work_queues:\n  /tmp/app:\n    q: {}\n",
		"work_queues:\n  app:\n    ../q: {}\n",
		"work_queues:\n  app:\n    q:\n      max_input_bytes: 9999999\n",
		"work_queues:\n  app:\n    q:\n      max_attempts: 21\n",
		"work_queues:\n  app:\n    q:\n      priority: 101\n",
		"work_queues:\n  app:\n    q:\n      required_capabilities: ['../escape']\n",
		"work_queues:\n  app:\n    q:\n      produces_code: true\n",
	} {
		if _, err := loadConfigText(t, body); err == nil {
			t.Fatalf("unsafe config accepted: %s", body)
		}
	}
}
