package queue

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/capsule/record"
)

func writeTrackedGateProfile(t *testing.T, root string) {
	t.Helper()
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.name", "Gate Policy Test")
	git(t, root, "config", "user.email", "gate-policy@example.invalid")
	if err := os.MkdirAll(filepath.Join(root, ".kitsoki"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".kitsoki", "project-profile.yaml"), []byte(
		"schema: project-profile/v1\ncommands:\n  change: make quick\n  full: make full\n  release: make release\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".kitsoki/project-profile.yaml")
	git(t, root, "commit", "-m", "tracked gate policy")
}

func TestTrackedGatePolicyRejectsRelabeledWeakMainGate(t *testing.T) {
	root := t.TempDir()
	writeTrackedGateProfile(t, root)
	if err := ValidateGateCommand(root, "main", "true"); err == nil || !strings.Contains(err.Error(), `requires tracked full gate "make full"`) {
		t.Fatalf("weak main gate error=%v", err)
	}
	if err := ValidateGateCommand(root, "main", "make full"); err != nil {
		t.Fatal(err)
	}
	worker := Worker{
		Store: Store{ProjectRoot: root},
		Deps: ProcessDeps{
			Integration: &fakeIntegration{speculate: func(context.Context, Candidate, []Candidate) (Speculation, error) {
				t.Fatal("weak worker reached integration")
				return Speculation{}, nil
			}},
			Gate: ShellGate{Command: "true"}, TargetRef: "main", GateTier: "full",
		},
	}
	if _, err := worker.RunOnce(context.Background()); err == nil || !strings.Contains(err.Error(), `requires tracked full gate "make full"`) {
		t.Fatalf("worker weak-gate error=%v", err)
	}
}

func TestMainGatePolicyFailsClosedWithoutTrackedProfile(t *testing.T) {
	if err := ValidateGateCommand(t.TempDir(), "main", "true"); err == nil || !strings.Contains(err.Error(), "requires tracked full gate policy") {
		t.Fatalf("missing-profile main error=%v", err)
	}
}

func TestStagingGatePolicyFailsClosedWithoutTrackedProfile(t *testing.T) {
	if err := ValidateGateCommand(t.TempDir(), "staging/local", "true"); err == nil || !strings.Contains(err.Error(), "requires tracked change gate policy") {
		t.Fatalf("missing-profile staging error=%v", err)
	}
}

func TestTrackedGatePolicyIgnoresDirtyWorktreeAndOtherCheckedOutBranch(t *testing.T) {
	root := t.TempDir()
	writeTrackedGateProfile(t, root)
	git(t, root, "checkout", "-b", "feature")
	if err := os.WriteFile(filepath.Join(root, ".kitsoki", "project-profile.yaml"), []byte(
		"schema: project-profile/v1\ncommands:\n  full: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateGateCommand(root, "main", "true"); err == nil {
		t.Fatal("dirty non-target worktree weakened tracked main gate")
	}
	if err := ValidateGateCommand(root, "main", "make full"); err != nil {
		t.Fatal(err)
	}
}

func TestTrackedTestCommandIsTransitionalFullFallback(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.name", "Gate Policy Test")
	git(t, root, "config", "user.email", "gate-policy@example.invalid")
	if err := os.MkdirAll(filepath.Join(root, ".kitsoki"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".kitsoki", "project-profile.yaml"), []byte(
		"schema: project-profile/v1\ncommands:\n  test: make existing-test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".kitsoki/project-profile.yaml")
	git(t, root, "commit", "-m", "test-only profile")
	required, configured, err := RequiredGateCommand(root, "main")
	if err != nil || !configured || required != "make existing-test" {
		t.Fatalf("required=%q configured=%v err=%v", required, configured, err)
	}
}

func TestPromoteExistingGateCommandHasDistinctDurableIdentity(t *testing.T) {
	base := PromoteExistingRequest{
		SourceTarget: "staging/local", LandedSHA: strings.Repeat("a", 40),
		DestinationTarget: "main", Pipeline: "change", GateCommand: "make full",
	}
	weak := base
	weak.GateCommand = "true"
	if promoteExistingKey(base) == promoteExistingKey(weak) {
		t.Fatal("gate command is absent from promote-existing durable request identity")
	}

	root := t.TempDir()
	writeTrackedGateProfile(t, root)
	_, err := (PromoteExistingAuthority{
		ProjectRoot: root,
		Certifier: ExistingSHACertifierFunc(func(context.Context, ExistingSHACertification) (record.Stored, error) {
			t.Fatal("weak request reached certifier")
			return record.Stored{}, nil
		}),
	}).Promote(context.Background(), weak)
	if err == nil || !strings.Contains(err.Error(), `requires tracked full gate "make full"`) {
		t.Fatalf("promote-existing weak-gate error=%v", err)
	}
}
