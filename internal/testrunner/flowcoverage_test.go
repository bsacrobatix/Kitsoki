package testrunner_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/testrunner"
)

func TestRunFlowCoverage_BranchesRequireExpectedStateForAmbiguousIntent(t *testing.T) {
	dir := t.TempDir()
	appYAML := `
app:
  id: coverage_branch_test
  version: 0.1.0
  title: "coverage branch test"
  author: a
  license: CC0
world:
  choice: { type: string, default: "" }
root: idle
intents:
  choose:
    slots:
      mode:
        type: enum
        required: true
        values: [fast, safe]
states:
  idle:
    on:
      choose:
        - when: "slots.mode == 'fast'"
          target: fast
        - when: "slots.mode == 'safe'"
          target: safe
  fast:
    terminal: true
  safe:
    terminal: true
`
	flowYAML := `
test_kind: flow
initial_state: idle
turns:
  - intent:
      name: choose
      slots: { mode: fast }
    expect_state: fast
---
test_kind: flow
initial_state: idle
turns:
  - intent:
      name: choose
      slots: { mode: safe }
`
	appPath, flowPath := writeFixture(t, dir, appYAML, flowYAML)

	report, err := testrunner.RunFlowCoverage(t.Context(), appPath, testrunner.FlowCoverageOptions{
		FlowsGlob:          flowPath,
		RequireAllBranches: true,
	})
	require.NoError(t, err)
	require.False(t, report.Passed)
	require.Equal(t, 1, report.BranchCoverage.Covered)
	require.Equal(t, 2, report.BranchCoverage.Total)

	var fast, safe testrunner.FlowBranchCoverage
	for _, branch := range report.Branches {
		switch branch.Target {
		case "fast":
			fast = branch
		case "safe":
			safe = branch
		}
	}
	require.True(t, fast.Covered)
	require.False(t, safe.Covered, "ambiguous guarded branch needs expect_state evidence")
}

func TestRunFlowCoverage_ReportsEnumParameterGaps(t *testing.T) {
	dir := t.TempDir()
	appYAML := `
app:
  id: coverage_params_test
  version: 0.1.0
  title: "coverage params test"
  author: a
  license: CC0
root: idle
intents:
  deploy:
    slots:
      env:
        type: enum
        required: true
        values: [staging, prod]
      mode:
        type: enum
        required: true
        values: [auto, manual]
states:
  idle:
    on:
      deploy:
        - target: done
  done:
    terminal: true
`
	flowYAML := `
test_kind: flow
initial_state: idle
turns:
  - intent:
      name: deploy
      slots: { env: staging, mode: auto }
    expect_state: done
`
	appPath, flowPath := writeFixture(t, dir, appYAML, flowYAML)

	report, err := testrunner.RunFlowCoverage(t.Context(), appPath, testrunner.FlowCoverageOptions{
		FlowsGlob:       flowPath,
		MaxCombinations: 8,
	})
	require.NoError(t, err)
	require.True(t, report.Passed, "branch threshold is independent from parameter gaps for now")
	require.Len(t, report.ParameterChecks, 1)

	check := report.ParameterChecks[0]
	require.False(t, check.Passed)
	require.Equal(t, []string{"prod"}, check.MissingValues["env"])
	require.Equal(t, []string{"manual"}, check.MissingValues["mode"])
	require.Equal(t, 4, check.RequiredCombinations)
	require.Len(t, check.MissingCombinations, 3)
}

func TestRunFlowCoverage_WritesJSONReport(t *testing.T) {
	dir := t.TempDir()
	appYAML := `
app:
  id: coverage_json_test
  version: 0.1.0
  title: "coverage json test"
  author: a
  license: CC0
root: idle
intents:
  finish: {}
states:
  idle:
    on:
      finish:
        - target: done
  done:
    terminal: true
`
	flowYAML := `
test_kind: flow
initial_state: idle
turns:
  - intent: { name: finish }
    expect_state: done
`
	appPath, flowPath := writeFixture(t, dir, appYAML, flowYAML)
	out := filepath.Join(dir, "coverage.json")

	report, err := testrunner.RunFlowCoverage(t.Context(), appPath, testrunner.FlowCoverageOptions{
		FlowsGlob:          flowPath,
		JSONOut:            out,
		RequireAllBranches: true,
	})
	require.NoError(t, err)
	require.True(t, report.Passed)
	require.FileExists(t, out)
}
