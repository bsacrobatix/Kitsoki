package main

import "testing"

func TestGitopsGHAgentGateRequiresIndependentVerify(t *testing.T) {
	result := map[string]any{
		"gh_agent_enqueue_status":         "queued",
		"gh_agent_enqueued_count":         1,
		"gh_agent_drain_status":           "drained",
		"gh_agent_failed_count":           0,
		"gh_agent_active_count":           0,
		"gh_agent_done_count":             1,
		"gh_agent_missing_evidence_count": 0,
		"gh_agent_missing_verify_count":   0,
		"gh_agent_missing_run_url_count":  0,
	}
	if !gitopsGHAgentGateOK(result) {
		t.Fatalf("complete gh-agent result should pass")
	}
	result["gh_agent_missing_verify_count"] = 1
	if gitopsGHAgentGateOK(result) {
		t.Fatalf("missing independent verification must fail the gh-agent gate")
	}
}
