package webconfig

import "testing"

func TestWorkQueueWorkersValidateConfiguredEnabledWorkerAndMerge(t *testing.T) {
	base, err := loadConfigText(t, `workers:
  - id: runner
    label: Runner
    placement: workstation
    enabled: true
work_queues:
  target:
    triage: {}
work_queue_workers:
  worker-app:
    target_application: target
    story_path: /trusted/worker/app.yaml
    allowed_queues: [triage]
    worker_id: runner
    max_concurrent: 2
    lease_seconds: 30
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := base.WorkQueueWorkers["worker-app"]; got.TargetApplication != "target" || got.MaxConcurrent != 2 {
		t.Fatalf("binding = %#v", got)
	}
	merged := mergeConfig(base, WebConfig{WorkQueueWorkers: map[string]WorkQueueWorkerBinding{
		"other-worker": {TargetApplication: "other", StoryPath: "/trusted/other/app.yaml", WorkerID: "runner", AllowedQueues: []string{"q"}, MaxConcurrent: 1, LeaseSeconds: 10},
	}})
	if len(merged.WorkQueueWorkers) != 2 {
		t.Fatalf("merged = %#v", merged.WorkQueueWorkers)
	}
}

func TestWorkQueueWorkersRejectUnknownQueueOrDisabledWorker(t *testing.T) {
	for _, body := range []string{
		`workers:
  - id: runner
    label: Runner
    placement: thin
    enabled: false
work_queues: {target: {q: {}}}
work_queue_workers: {worker: {target_application: target, story_path: /trusted/worker/app.yaml, allowed_queues: [q], worker_id: runner, max_concurrent: 1, lease_seconds: 10}}
`,
		`workers:
  - id: runner
    label: Runner
    placement: thin
    enabled: true
work_queues: {target: {q: {}}}
work_queue_workers: {worker: {target_application: target, story_path: /trusted/worker/app.yaml, allowed_queues: [missing], worker_id: runner, max_concurrent: 1, lease_seconds: 10}}
`,
	} {
		if _, err := loadConfigText(t, body); err == nil {
			t.Fatalf("unsafe binding accepted: %s", body)
		}
	}
}
