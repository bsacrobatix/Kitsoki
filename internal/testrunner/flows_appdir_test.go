package testrunner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"kitsoki/internal/host"
	"kitsoki/internal/machine"
)

// TestRunFlowFileHostBindingsRepublishAppDir pins the second load that a
// fixture with host_bindings performs. It must publish its own app directory
// before LoadWithResolver expands ${KITSOKI_APP_DIR}; otherwise a prior,
// concurrent story's directory can select that story's host bindings and
// workspace base.
func TestRunFlowFileHostBindingsRepublishAppDir(t *testing.T) {
	appDir := t.TempDir()
	otherDir := t.TempDir()
	appPath := filepath.Join(appDir, "app.yaml")
	flowPath := filepath.Join(appDir, "flow.yaml")
	const appYAML = `app:
  id: fixture
  version: 0.1.0
root: foyer
states:
  foyer:
    description: "Foyer"
    view: "Foyer"
agents:
  engineer:
    system_prompt: "fixture"
meta_modes:
  self:
    trigger: meta-fixture
    agent: engineer
    cwd: "${KITSOKI_APP_DIR}"
`
	if err := os.WriteFile(appPath, []byte(appYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(flowPath, []byte(`test_kind: flow
initial_state: foyer
host_bindings:
  unused: host.run
turns: []
`), 0o600); err != nil {
		t.Fatal(err)
	}
	def, err := loadAppForRun(appPath, nil)
	if err != nil {
		t.Fatalf("initial app load: %v", err)
	}
	m, err := machine.New(def)
	if err != nil {
		t.Fatalf("initial machine: %v", err)
	}
	t.Setenv(host.AppDirEnv, otherDir)

	// The non-empty override deliberately drives runFlowFile's per-fixture
	// reload path. Unknown interfaces are ignored by the override seam, so this
	// fixture stays minimal while still exercising LoadWithResolver rather than
	// app.Load.
	_, err = runFlowFile(context.Background(), def, m, appPath, flowPath, FlowOptions{})
	if err != nil {
		t.Fatalf("run flow with bindings: %v", err)
	}
	if got := os.Getenv(host.AppDirEnv); got != appDir {
		t.Fatalf("%s = %q, want %q; binding reload must republish its own app dir", host.AppDirEnv, got, appDir)
	}
}
