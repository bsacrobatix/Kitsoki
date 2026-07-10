package reconcile

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	var events []Event
	r := Reconciler{VCS: Git{}, Events: EventSinkFunc(func(_ context.Context, e Event) error {
		events = append(events, e)
		return nil
	})}
	p, err := r.Plan(context.Background(), PlanRequest{Workspace: dir, TargetRef: "origin/main", Operation: Refresh, Generation: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != "capsule.sync.planned" || events[0].PlanDigest != p.Digest {
		t.Fatalf("planned events %#v", events)
	}
	if p.Class != Diverged || p.Digest == "" {
		t.Fatalf("plan %#v", p)
	}
	if p.Continuation == nil {
		t.Fatal("diverged plan did not include continuation")
	}
	if p.Continuation.Schema != "capsule-sync-continuation/v1" || p.Continuation.Token == "" {
		t.Fatalf("continuation %#v", p.Continuation)
	}
	if got := strings.Join(p.Continuation.RequiredInputs, ","); got != "resolver_decision,independent_lost_work_review,validation_receipt" {
		t.Fatalf("required inputs %s", got)
	}
	if _, err := r.Apply(context.Background(), p, ""); err == nil || !strings.Contains(err.Error(), p.Continuation.Token) {
		t.Fatal("diverged apply succeeded")
	}
	if len(events) != 2 || events[1].Kind != "capsule.sync.conflicted" || events[1].ContinuationToken != p.Continuation.Token {
		t.Fatalf("conflicted events %#v", events)
	}
}

func TestMaterializeConflictArtifactForDivergedPlan(t *testing.T) {
	dir := capsuletest.Open(t, "diverged-remote")
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte(".kitsoki-capsule\ncapsule-manifest.json\npeers/\n.capsules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "update-index", "--assume-unchanged", "peers/origin.git/refs/heads/main")
	r := Reconciler{VCS: Git{}}
	p, err := r.Plan(context.Background(), PlanRequest{Workspace: dir, TargetRef: "origin/main", Operation: Refresh, Generation: 7})
	if err != nil {
		t.Fatal(err)
	}
	artifact, path, err := r.MaterializeConflictArtifact(context.Background(), p, dir)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Schema != ConflictArtifactSchema || artifact.PlanDigest != p.Digest || artifact.ContinuationToken != p.Continuation.Token {
		t.Fatalf("artifact %#v", artifact)
	}
	if artifact.MergeBase == "" || len(artifact.CandidatePaths) == 0 || len(artifact.TargetPaths) == 0 {
		t.Fatalf("missing conflict facts %#v", artifact)
	}
	if len(artifact.RequiredInputs) != len(p.Continuation.RequiredInputs) {
		t.Fatalf("required inputs %#v", artifact.RequiredInputs)
	}
	if filepath.Base(path) != p.Continuation.Token+".conflict.json" {
		t.Fatalf("artifact path %s", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
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
	var events []Event
	r := Reconciler{VCS: Git{}, Events: EventSinkFunc(func(_ context.Context, e Event) error {
		events = append(events, e)
		return nil
	}), Gates: GateVerifierFunc(func(_ context.Context, receipt string, _ Plan) error {
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
	if !sawEvent(events, "capsule.sync.applied") {
		t.Fatalf("missing applied event %#v", events)
	}
	if _, err := r.Apply(context.Background(), p, "ok"); err == nil {
		t.Fatal("stale plan accepted")
	}
	if !sawEvent(events, "capsule.sync.stale") {
		t.Fatalf("missing stale event %#v", events)
	}
}
func TestPublishRequiresExplicitProvider(t *testing.T) {
	dir := capsuletest.Open(t, "clean-repo")
	runGit(t, dir, "checkout", "-b", "candidate")
	if err := os.WriteFile(filepath.Join(dir, "publish.txt"), []byte("publish"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "publish.txt")
	runGit(t, dir, "commit", "-m", "publish")
	r := Reconciler{VCS: Git{}}
	p, err := r.Plan(context.Background(), PlanRequest{Workspace: dir, TargetRef: "main", Operation: Publish, Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	if p.RequiredEffect != "remote_publish" {
		t.Fatalf("effect %s", p.RequiredEffect)
	}
	if _, err := r.Apply(context.Background(), p, ""); err == nil {
		t.Fatal("publish without provider applied")
	}
	publisher := recordingPublisher{}
	result, err := (Reconciler{VCS: Git{}, Publisher: &publisher}).Apply(context.Background(), p, "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || publisher.plan.Digest != p.Digest || publisher.refs.WorkspaceHead != p.Candidate {
		t.Fatalf("publish result=%#v publisher=%#v", result, publisher)
	}
	if got := strings.TrimSpace(runGitOutput(t, dir, "rev-parse", "main")); got != p.Expected.Target {
		t.Fatalf("publish mutated local main: %s", got)
	}
}
func TestLocalBareRemotePublisherPublishesWithRemoteLease(t *testing.T) {
	dir := capsuletest.Open(t, "clean-repo")
	remote := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, dir, "push", remote, "main:refs/heads/main")
	runGit(t, dir, "checkout", "-b", "candidate")
	if err := os.WriteFile(filepath.Join(dir, "publish-local.txt"), []byte("publish"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "publish-local.txt")
	runGit(t, dir, "commit", "-m", "publish local")

	r := Reconciler{VCS: Git{}, Publisher: LocalBareRemotePublisher{Remote: remote}}
	p, err := r.Plan(context.Background(), PlanRequest{Workspace: dir, TargetRef: "main", Operation: Publish, Generation: 1})
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Apply(context.Background(), p, "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.NewTarget != p.Candidate {
		t.Fatalf("publish result %#v", result)
	}
	if got := strings.TrimSpace(runGitOutput(t, remote, "rev-parse", "refs/heads/main")); got != p.Candidate {
		t.Fatalf("remote main = %s, want %s", got, p.Candidate)
	}
	if got := strings.TrimSpace(runGitOutput(t, dir, "rev-parse", "main")); got != p.Expected.Target {
		t.Fatalf("local main mutated: %s", got)
	}
}
func TestLocalBareRemotePublisherRejectsStaleRemote(t *testing.T) {
	dir := capsuletest.Open(t, "clean-repo")
	remote := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, dir, "push", remote, "main:refs/heads/main")
	runGit(t, dir, "checkout", "-b", "candidate")
	if err := os.WriteFile(filepath.Join(dir, "candidate.txt"), []byte("candidate"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "candidate.txt")
	runGit(t, dir, "commit", "-m", "candidate")

	r := Reconciler{VCS: Git{}, Publisher: LocalBareRemotePublisher{Remote: remote}}
	p, err := r.Plan(context.Background(), PlanRequest{Workspace: dir, TargetRef: "main", Operation: Publish, Generation: 1})
	if err != nil {
		t.Fatal(err)
	}

	other := filepath.Join(t.TempDir(), "other")
	runGit(t, "", "clone", remote, other)
	if err := os.WriteFile(filepath.Join(other, "remote.txt"), []byte("remote"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "add", "remote.txt")
	runGit(t, other, "commit", "-m", "remote advance")
	runGit(t, other, "push", "origin", "main:refs/heads/main")

	if _, err := r.Apply(context.Background(), p, ""); err == nil || !strings.Contains(err.Error(), "stale remote ref") {
		t.Fatalf("expected stale remote ref, got %v", err)
	}
	if got := strings.TrimSpace(runGitOutput(t, remote, "rev-parse", "refs/heads/main")); got == p.Candidate {
		t.Fatalf("stale publish updated remote to candidate")
	}
}
func TestPlanRejectsUnknownOperation(t *testing.T) {
	dir := capsuletest.Open(t, "clean-repo")
	if _, err := (Reconciler{VCS: Git{}}).Plan(context.Background(), PlanRequest{Workspace: dir, TargetRef: "main", Operation: Operation("invent"), Generation: 1}); err == nil {
		t.Fatal("unknown operation was accepted")
	}
}

type recordingPublisher struct {
	plan Plan
	refs ObservedRefs
}

func (p *recordingPublisher) Publish(_ context.Context, plan Plan, refs ObservedRefs) (ApplyResult, error) {
	p.plan = plan
	p.refs = refs
	return ApplyResult{PlanDigest: plan.Digest, OldTarget: refs.Target, NewTarget: plan.Candidate, Applied: true}, nil
}

func sawEvent(events []Event, kind string) bool {
	for _, event := range events {
		if event.Kind == kind {
			return true
		}
	}
	return false
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}
