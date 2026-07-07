package testrunner_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/capsuletest"
	"kitsoki/internal/testrunner"
)

func TestAutonomousBugfixOperationDemoCorpus(t *testing.T) {
	appPath := repoStoriesBugfixAppPath(t)
	cassettePath, err := filepath.Abs("../../stories/bugfix/flows/codeact_live_proof_triage.cassette.yaml")
	require.NoError(t, err)

	workspace := capsuletest.Open(t, "clean-repo")
	capsuletest.Verify(t, workspace)

	flowPath := filepath.Join(t.TempDir(), "capsule_triage_operation.yaml")
	require.NoError(t, os.WriteFile(flowPath, []byte(fmt.Sprintf(`test_kind: flow
app: %q
host_cassette: %q
initial_state: idle
initial_world:
  workdir: %q
  bugfix_mode: "triage"
  judge_mode: "llm"
  ticket_id: "capsule-codeact-live-proof-demo"
  ticket_title: "Codeact schema handling should remain wired"
  ticket_body: >
    Confirm whether host.agent.codeact still honors its schema argument and
    cite concrete evidence from the current tree. This corpus entry runs
    against a hermetic clean-repo capsule and replays the recorded no-LLM
    codeact response.
turns:
  - intent: { name: triage, slots: {} }

expect_terminal: true
expect_operation_policy: bugfix_triage
expect_operation_status: completed
expect_terminal_artifact: triage_verdict
expect_no_errors: true
`, appPath, cassettePath, workspace)), 0o644))

	report, err := testrunner.RunFlows(t.Context(), appPath, flowPath, testrunner.FlowOptions{})
	require.NoError(t, err)
	requireFlowReportPassed(t, report)
	capsuletest.Verify(t, workspace)
}

func repoStoriesBugfixAppPath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("../../stories/bugfix/app.yaml")
	require.NoError(t, err)
	return abs
}

func requireFlowReportPassed(t *testing.T, report *testrunner.FlowReport) {
	t.Helper()
	require.NotNil(t, report)
	for _, r := range report.Results {
		if r.Passed {
			continue
		}
		for _, turn := range r.Turns {
			for _, failure := range turn.Failures {
				t.Logf("flow=%s turn=%d failure: %s", filepath.Base(r.File), turn.TurnIndex+1, failure)
			}
		}
	}
	require.Equal(t, 0, report.Failed)
}
