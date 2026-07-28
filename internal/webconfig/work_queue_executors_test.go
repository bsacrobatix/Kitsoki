package webconfig

import "testing"

func TestWorkQueueExecutorRequiresFixedConfiguredJobAndBundleForCode(t *testing.T) {
	base := `
workers:
  - id: runner
    label: Runner
    placement: workstation
    enabled: true
work_queue_bundle_root: /tmp/bundles
work_queues:
  pog:
    bugfix: {produces_code: true}
story_application_jobs:
  adapter:
    bugfix:
      application_id: pog-bugfix
      event: execute
      artifact_outputs: [artifact_ref]
      primary_output: artifact_ref
      bundle_ref_output: bundle_ref
      bundle_digest_output: bundle_digest
      bounds: {max_input_bytes: 1024, max_runtime_seconds: 60}
work_queue_executors:
  pog-bugfix:
    target_application: pog
    queue: bugfix
    worker_id: runner
    caller_application: adapter
    template: bugfix
    max_concurrent: 1
    lease_seconds: 30
    poll_seconds: 2
`
	if _, err := loadConfigText(t, base); err != nil {
		t.Fatal(err)
	}
	withoutBundle := `
workers: [{id: runner, label: Runner, placement: workstation, enabled: true}]
work_queue_bundle_root: /tmp/bundles
work_queues: {pog: {bugfix: {produces_code: true}}}
story_application_jobs:
  adapter:
    bugfix:
      application_id: pog-bugfix
      event: execute
      artifact_outputs: [artifact_ref]
      primary_output: artifact_ref
      bounds: {max_input_bytes: 1024, max_runtime_seconds: 60}
work_queue_executors: {pog-bugfix: {target_application: pog, queue: bugfix, worker_id: runner, caller_application: adapter, template: bugfix, max_concurrent: 1, lease_seconds: 30, poll_seconds: 2}}
`
	if _, err := loadConfigText(t, withoutBundle); err == nil {
		t.Fatal("code queue accepted executor without bundle outputs")
	}
}
