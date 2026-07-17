package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCapsuleCIPlacementConfig(t *testing.T, dir, agentLaunchPolicyBody, workersBody string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".kitsoki.yaml"), []byte("story_dirs: [./stories]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	local := agentLaunchPolicyBody + workersBody
	if err := os.WriteFile(filepath.Join(dir, ".kitsoki.local.yaml"), []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCheckCapsuleCIPlacement_NoPolicyConfiguredAllowsAnyRegisteredEnabledWorker(t *testing.T) {
	dir := t.TempDir()
	writeCapsuleCIPlacementConfig(t, dir, "", "workers:\n  - id: vm-a\n    label: VM A\n    placement: thin\n    enabled: true\n")
	if err := checkCapsuleCIPlacement(dir, "build", "", "vm-a"); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestCheckCapsuleCIPlacement_UnregisteredWorkerDenied(t *testing.T) {
	dir := t.TempDir()
	writeCapsuleCIPlacementConfig(t, dir, "", "workers:\n  - id: vm-a\n    label: VM A\n    placement: thin\n    enabled: true\n")
	err := checkCapsuleCIPlacement(dir, "build", "", "vm-missing")
	if err == nil || !strings.Contains(err.Error(), "not a registered worker") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckCapsuleCIPlacement_DisabledWorkerDenied(t *testing.T) {
	dir := t.TempDir()
	writeCapsuleCIPlacementConfig(t, dir, "", "workers:\n  - id: vm-a\n    label: VM A\n    placement: thin\n    enabled: false\n")
	err := checkCapsuleCIPlacement(dir, "build", "", "vm-a")
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckCapsuleCIPlacement_LaneNotInPlacementMapDenied(t *testing.T) {
	dir := t.TempDir()
	writeCapsuleCIPlacementConfig(t, dir,
		"agent_launch_policy:\n  enabled: true\n  placement:\n    research:\n      worker_classes: [thin]\n",
		"workers:\n  - id: vm-a\n    label: VM A\n    placement: thin\n    enabled: true\n")
	err := checkCapsuleCIPlacement(dir, "delivery", "", "vm-a")
	if err == nil || !strings.Contains(err.Error(), "no placement policy entry") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckCapsuleCIPlacement_WorkerClassSatisfiesLaneAllows(t *testing.T) {
	dir := t.TempDir()
	writeCapsuleCIPlacementConfig(t, dir,
		"agent_launch_policy:\n  enabled: true\n  placement:\n    research:\n      worker_classes: [thin, workstation]\n",
		"workers:\n  - id: vm-a\n    label: VM A\n    placement: workstation\n    enabled: true\n")
	if err := checkCapsuleCIPlacement(dir, "research", "", "vm-a"); err != nil {
		t.Fatalf("expected allow, got %v", err)
	}
}

func TestCheckCapsuleCIPlacement_WorkerClassOutsideLaneDenied(t *testing.T) {
	dir := t.TempDir()
	writeCapsuleCIPlacementConfig(t, dir,
		"agent_launch_policy:\n  enabled: true\n  placement:\n    build:\n      worker_classes: [thin]\n",
		"workers:\n  - id: vm-a\n    label: VM A\n    placement: workstation\n    enabled: true\n")
	err := checkCapsuleCIPlacement(dir, "build", "", "vm-a")
	if err == nil || !strings.Contains(err.Error(), "does not satisfy placement policy") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckCapsuleCIPlacement_ExplicitLaneOverridesPipelineName(t *testing.T) {
	dir := t.TempDir()
	writeCapsuleCIPlacementConfig(t, dir,
		"agent_launch_policy:\n  enabled: true\n  placement:\n    research:\n      worker_classes: [thin]\n",
		"workers:\n  - id: vm-a\n    label: VM A\n    placement: thin\n    enabled: true\n")
	// pipeline name "build" has no entry, but --lane research does.
	if err := checkCapsuleCIPlacement(dir, "build", "research", "vm-a"); err != nil {
		t.Fatalf("expected allow via explicit lane, got %v", err)
	}
}
