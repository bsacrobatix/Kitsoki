package testrunner_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/testrunner"
)

func TestRunFlowsEffectOutcomeBindsThenRoutes(t *testing.T) {
	dir := t.TempDir()
	appPath, flowPath := writeFixture(t, dir, `
app: {id: outcome-flow, version: 1.0.0}
hosts: [host.probe]
world:
  message: {type: string, default: ""}
intents:
  go: {title: Go}
root: idle
states:
  idle:
    on:
      go: [{target: deciding}]
  deciding:
    on_enter:
      - invoke: host.probe
        result:
          status: {type: string, required: true}
          message: {type: string, required: true}
        bind:
          message: message
        outcomes:
          accepted:
            when: result.status == "ok"
            target: accepted
          rejected:
            default: true
            target: rejected
  accepted: {terminal: true}
  rejected: {terminal: true}
`, `
test_kind: flow
use_orchestrator: true
initial_state: idle
host_handlers:
  host.probe:
    data:
      status: ok
      message: bound-before-route
turns:
  - intent: {name: go}
    expect_state: accepted
    expect_world:
      message: bound-before-route
expect_no_errors: true
`)

	report, err := testrunner.RunFlows(t.Context(), appPath, flowPath, testrunner.FlowOptions{})
	require.NoError(t, err)
	require.Equal(t, 0, report.Failed, "report: %+v", report.Results)
	require.Equal(t, 1, report.Passed)
}
