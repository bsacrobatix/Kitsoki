package testrunner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/host"
)

// TestRunFlows_ReplayRejectsUncoveredResolvedBindingBeforeRealExec proves the
// boundary that protects replay jobs from an accidental provider drift.  The
// flow would invoke iface.workspace.create, and host_bindings maps it to the
// real shell-backed host.git_worktree handler.  Its cassette deliberately
// records only the Capsule provider.  Rig construction must reject that
// mismatch before a session/effect can invoke the poisoned command runner.
func TestRunFlows_ReplayRejectsUncoveredResolvedBindingBeforeRealExec(t *testing.T) {
	dir := t.TempDir()
	appPath := filepath.Join(dir, "app.yaml")
	flowPath := filepath.Join(dir, "flow.yaml")
	cassettePath := filepath.Join(dir, "replay.yaml")

	const appYAML = `app:
  id: replay-binding-safety
  version: 0.1.0
root: idle
host_interfaces:
  workspace:
    operations:
      create:
        input: { id: string }
        output: { ok: bool }
    default: host.capsule_workspace
states:
  idle:
    on_enter:
      - invoke: iface.workspace.create
        with: { id: would-run-real-workspace }
`
	const flowYAML = `test_kind: flow
initial_state: idle
host_bindings:
  workspace: host.git_worktree
host_cassette: replay.yaml
turns: []
`
	const cassetteYAML = `kind: host_cassette
app_id: replay-binding-safety
episodes:
  - id: capsule-workspace
    match: { handler: host.capsule_workspace }
    response:
      data: { ok: true }
`
	for path, body := range map[string]string{appPath: appYAML, flowPath: flowYAML, cassettePath: cassetteYAML} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	execCalls := 0
	restore := host.SetExecRunnerForTest(func(context.Context, string, string, ...string) (string, string, int, error) {
		execCalls++
		return "", "poisoned real command runner invoked", 1, nil
	})
	defer restore()

	_, err := RunFlows(context.Background(), appPath, flowPath, FlowOptions{})
	if err == nil || !strings.Contains(err.Error(), "cassette replay coverage missing resolved host binding(s): workspace -> host.git_worktree") {
		t.Fatalf("RunFlows error = %v, want fail-closed uncovered-binding error", err)
	}
	if execCalls != 0 {
		t.Fatalf("real command runner called %d time(s), want 0", execCalls)
	}
}
