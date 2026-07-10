package reconcile

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"kitsoki/internal/capsuletest"
)

func TestPlanClassifiesDivergedCapsule(t *testing.T) {
	dir := capsuletest.Open(t, "diverged-remote")
	// The reusable capsule retains its bare remote under peers/ for inspection;
	// it is fixture infrastructure, not a candidate workspace change.
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte(".kitsoki-capsule\ncapsule-manifest.json\npeers/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "update-index", "--assume-unchanged", "peers/origin.git/refs/heads/main")
	r := Reconciler{VCS: Git{}}
	p, err := r.Plan(context.Background(), PlanRequest{Workspace: dir, TargetRef: "origin/main", Operation: Refresh, Generation: 7})
	if err != nil {
		t.Fatal(err)
	}
	if p.Class != Diverged || p.Digest == "" {
		t.Fatalf("plan %#v", p)
	}
	if _, err := r.Apply(context.Background(), p, ""); err == nil {
		t.Fatal("diverged apply succeeded")
	}
}
func TestApplyIsFastForwardOnlyAndStaleSafe(t *testing.T) {
	dir := capsuletest.Open(t, "clean-repo")
	runGit(t, dir, "checkout", "-b", "candidate")
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "new.txt")
	runGit(t, dir, "commit", "-m", "candidate")
	r := Reconciler{VCS: Git{}, Gates: GateVerifierFunc(func(_ context.Context, receipt string, _ Plan) error {
		if receipt != "ok" {
			return os.ErrPermission
		}
		return nil
	})}
	p, err := r.Plan(context.Background(), PlanRequest{Workspace: dir, TargetRef: "main", Operation: Promote, Generation: 1, RequiredGate: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Class != LocalAhead {
		t.Fatalf("class %s", p.Class)
	}
	if _, err := r.Apply(context.Background(), p, "no"); err == nil {
		t.Fatal("gate missing accepted")
	}
	if _, err := r.Apply(context.Background(), p, "ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Apply(context.Background(), p, "ok"); err == nil {
		t.Fatal("stale plan accepted")
	}
}
func TestPlanRejectsUnknownOperation(t *testing.T) {
	dir := capsuletest.Open(t, "clean-repo")
	if _, err := (Reconciler{VCS: Git{}}).Plan(context.Background(), PlanRequest{Workspace: dir, TargetRef: "main", Operation: Operation("invent"), Generation: 1}); err == nil {
		t.Fatal("unknown operation was accepted")
	}
}
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
