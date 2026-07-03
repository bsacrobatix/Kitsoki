package testrunner_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/host"
	"kitsoki/internal/testrunner"
)

func TestBug46ReviewTaskFetchesGitHubIssueFromTicketRepo(t *testing.T) {
	if testing.Short() {
		t.Skip("TestBug46ReviewTaskFetchesGitHubIssueFromTicketRepo: skipped under -short (full story run)")
	}

	restore := host.SetExecRunnerForTest(func(_ context.Context, _ string, name string, args ...string) (string, string, int, error) {
		require.Equal(t, "gh", name)
		repo := ""
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "--repo" {
				repo = args[i+1]
				break
			}
		}

		title := "WRONG-REPO-ISSUE-46"
		body := "Issue #46 from the cwd fallback repository."
		url := "https://github.com/wrong/repo/issues/46"
		if repo == "constructorfabric/Kitsoki" {
			title = "Correct Kitsoki feature issue"
			body = "Issue #46 from constructorfabric/Kitsoki."
			url = "https://github.com/constructorfabric/Kitsoki/issues/46"
		}

		payload, err := json.Marshal(map[string]any{
			"number":    float64(46),
			"title":     title,
			"body":      body,
			"state":     "OPEN",
			"labels":    []any{},
			"assignees": []any{},
			"url":       url,
			"comments":  []any{},
		})
		require.NoError(t, err)
		return string(payload), "", 0, nil
	})
	defer restore()

	dir := t.TempDir()
	flowPath := filepath.Join(dir, "bug46_review_task_repo.yaml")
	fixture := `test_kind: flow
app: ` + repoStoriesImplementationAppPath(t) + `
initial_state: idle
initial_world:
  ticket_id:     "46"
  ticket_repo:   "constructorfabric/Kitsoki"
  ticket_title:  "Inbox title before fetch"
  ticket_url:    "https://github.com/constructorfabric/Kitsoki/issues/46"
  thread:        "46"
  workdir:       ".worktrees/bug46"
  judge_mode:    "human"

host_bindings:
  ticket: host.gh.ticket

host_handlers:
  host.agent.decide:
    data:
      ok: true
      submitted:
        summary_title:    "Stub task summary"
        summary_markdown: "Stub summary."
        scope:            "Stub scope."
        acceptance_criteria: ["stub criterion"]

turns:
  - intent: { name: start, slots: {} }
    expect_state: review_task_executing
    expect_world:
      ticket_title: "Correct Kitsoki feature issue"

expect_no_errors: true
`
	require.NoError(t, os.WriteFile(flowPath, []byte(fixture), 0o644))

	report, err := testrunner.RunFlows(t.Context(), repoStoriesImplementationAppPath(t), flowPath, testrunner.FlowOptions{})
	require.NoError(t, err)
	require.Equal(t, 0, report.Failed, "turn failures: %+v", report.Results[0].Turns)
}

func repoStoriesImplementationAppPath(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("../../stories/implementation/app.yaml")
	require.NoError(t, err)
	return abs
}
