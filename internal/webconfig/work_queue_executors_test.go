package webconfig

import "testing"

func TestWorkQueueCapsuleExecutorAcceptsPinnedTypedBinding(t *testing.T) {
	cfg, err := loadConfigText(t, `workers:
  - id: vm-pool
    label: VM pool
    placement: thin
    enabled: true
work_queues:
  pog:
    bugfix: {produces_code: true}
work_queue_bundle_root: /var/lib/kitsoki/bundles
work_queue_executors:
  pog-bugfix:
    target_application: pog
    queue: bugfix
    project_root: /srv/pog
    workspace_id: pog-bugfix
    pipeline: bugfix
    worker_id: vm-pool
    worker_policy: bugfix
    input_schema: {issue: {type: string, required: true}}
    bundle_ref_output: bundle_ref
    bundle_digest_output: bundle_digest
    max_concurrent: 1
    lease_seconds: 30
    poll_seconds: 2
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.WorkQueueExecutors["pog-bugfix"]; got.Pipeline != "bugfix" || got.InputSchema["issue"].Type != "string" {
		t.Fatalf("binding = %#v", got)
	}
}

func TestWorkQueueCapsuleExecutorRejectsMissingBundleOrSchema(t *testing.T) {
	for _, body := range []string{
		`workers: [{id: vm, label: VM, placement: thin, enabled: true}]
work_queues: {pog: {bugfix: {produces_code: true}}}
work_queue_executors: {x: {target_application: pog, queue: bugfix, project_root: /srv/pog, workspace_id: ws, pipeline: bugfix, worker_id: vm, worker_policy: bugfix, input_schema: {issue: {type: string}}, max_concurrent: 1, lease_seconds: 1, poll_seconds: 1}}
`,
		`workers: [{id: vm, label: VM, placement: thin, enabled: true}]
work_queues: {pog: {bugfix: {}}}
work_queue_executors: {x: {target_application: pog, queue: bugfix, project_root: /srv/pog, workspace_id: ws, pipeline: bugfix, worker_id: vm, worker_policy: bugfix, input_schema: {}, bundle_ref_output: ref, bundle_digest_output: digest, max_concurrent: 1, lease_seconds: 1, poll_seconds: 1}}
`,
	} {
		if _, err := loadConfigText(t, body); err == nil {
			t.Fatalf("unsafe binding accepted: %s", body)
		}
	}
}
