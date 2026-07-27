package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/capsule/headroom"
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

func TestContinuationMaterializationFailsClosedOnLowHeadroom(t *testing.T) {
	dir := capsuletest.Open(t, "diverged-remote")
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte(".kitsoki-capsule\ncapsule-manifest.json\npeers/\n.capsules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "update-index", "--assume-unchanged", "peers/origin.git/refs/heads/main")
	r := Reconciler{VCS: Git{}, Headroom: headroom.Guard{Enabled: true, FreeBytes: func(string) (int64, error) { return headroom.MinimumFloorBytes - 1, nil }}}
	p, err := r.Plan(context.Background(), PlanRequest{Workspace: dir, TargetRef: "origin/main", Operation: Refresh, Generation: 7})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = r.MaterializeConflictArtifact(context.Background(), p, dir)
	var refusal *headroom.Error
	if !errors.As(err, &refusal) {
		t.Fatalf("err=%v, want typed headroom refusal", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".capsules", "sync", p.Continuation.Token+".conflict.json")); !os.IsNotExist(statErr) {
		t.Fatalf("headroom refusal wrote continuation artifact: %v", statErr)
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

func TestMaterializeIntegrationInstanceForDivergedPlan(t *testing.T) {
	dir := capsuletest.Open(t, "rebase-conflict-ready")
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte(".kitsoki-capsule\ncapsule-manifest.json\n.capsules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Reconciler{VCS: Git{}}
	p, err := r.Plan(context.Background(), PlanRequest{Workspace: dir, TargetRef: "main", Operation: Refresh, Generation: 9})
	if err != nil {
		t.Fatal(err)
	}
	if p.Class != Diverged {
		t.Fatalf("class %s", p.Class)
	}
	instance, path, err := r.MaterializeIntegrationInstance(context.Background(), p, dir)
	if err != nil {
		t.Fatal(err)
	}
	if instance.Schema != IntegrationInstanceSchema || instance.PlanDigest != p.Digest || instance.ContinuationToken != p.Continuation.Token {
		t.Fatalf("instance %#v", instance)
	}
	if instance.State != "conflicted" || !contains(instance.ConflictPaths, "file.txt") {
		t.Fatalf("conflict instance %#v", instance)
	}
	if filepath.IsAbs(instance.InstancePath) || filepath.Base(path) != p.Continuation.Token+".integration.json" {
		t.Fatalf("paths instance=%s artifact=%s", instance.InstancePath, path)
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(instance.InstancePath), ".git")); err != nil {
		t.Fatal(err)
	}
	again, againPath, err := r.MaterializeIntegrationInstance(context.Background(), p, dir)
	if err != nil {
		t.Fatal(err)
	}
	if againPath != path || again.InstancePath != instance.InstancePath {
		t.Fatalf("non-idempotent rematerialize: %#v %s", again, againPath)
	}
}

func TestMaterializeIntegrationInstanceRejectsTamperedRetainedAuthority(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, artifactPath, instancePath string, instance IntegrationInstance)
	}{
		{
			name: "artifact branch",
			mutate: func(t *testing.T, artifactPath, _ string, instance IntegrationInstance) {
				instance.Branch = "attacker-controlled"
				writeIntegrationArtifact(t, artifactPath, instance)
			},
		},
		{
			name: "artifact schema",
			mutate: func(t *testing.T, artifactPath, _ string, instance IntegrationInstance) {
				instance.Schema = "attacker/v1"
				writeIntegrationArtifact(t, artifactPath, instance)
			},
		},
		{
			name: "artifact plan digest",
			mutate: func(t *testing.T, artifactPath, _ string, instance IntegrationInstance) {
				instance.PlanDigest = "sha256:attacker"
				writeIntegrationArtifact(t, artifactPath, instance)
			},
		},
		{
			name: "artifact continuation token",
			mutate: func(t *testing.T, artifactPath, _ string, instance IntegrationInstance) {
				instance.ContinuationToken = "cont-attacker"
				writeIntegrationArtifact(t, artifactPath, instance)
			},
		},
		{
			name: "artifact operation",
			mutate: func(t *testing.T, artifactPath, _ string, instance IntegrationInstance) {
				instance.Operation = Promote
				writeIntegrationArtifact(t, artifactPath, instance)
			},
		},
		{
			name: "artifact target ref",
			mutate: func(t *testing.T, artifactPath, _ string, instance IntegrationInstance) {
				instance.TargetRef = "attacker/main"
				writeIntegrationArtifact(t, artifactPath, instance)
			},
		},
		{
			name: "artifact candidate",
			mutate: func(t *testing.T, artifactPath, _ string, instance IntegrationInstance) {
				instance.Candidate = strings.Repeat("a", 40)
				writeIntegrationArtifact(t, artifactPath, instance)
			},
		},
		{
			name: "artifact target",
			mutate: func(t *testing.T, artifactPath, _ string, instance IntegrationInstance) {
				instance.Target = strings.Repeat("b", 40)
				writeIntegrationArtifact(t, artifactPath, instance)
			},
		},
		{
			name: "artifact instance path",
			mutate: func(t *testing.T, artifactPath, _ string, instance IntegrationInstance) {
				instance.InstancePath = ".capsules/sync/other.integration"
				writeIntegrationArtifact(t, artifactPath, instance)
			},
		},
		{
			name: "missing instance directory",
			mutate: func(t *testing.T, _ string, instancePath string, _ IntegrationInstance) {
				if err := os.RemoveAll(instancePath); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "missing artifact with retained directory",
			mutate: func(t *testing.T, artifactPath, _ string, _ IntegrationInstance) {
				if err := os.Remove(artifactPath); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink instance directory",
			mutate: func(t *testing.T, _ string, instancePath string, _ IntegrationInstance) {
				if err := os.RemoveAll(instancePath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), instancePath); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "non git instance directory",
			mutate: func(t *testing.T, _ string, instancePath string, _ IntegrationInstance) {
				if err := os.RemoveAll(filepath.Join(instancePath, ".git")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "artifact symlink",
			mutate: func(t *testing.T, artifactPath, _ string, instance IntegrationInstance) {
				outside := filepath.Join(t.TempDir(), "artifact.json")
				writeIntegrationArtifact(t, outside, instance)
				if err := os.Remove(artifactPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, artifactPath); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := capsuletest.Open(t, "rebase-conflict-ready")
			if err := os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte(".kitsoki-capsule\ncapsule-manifest.json\n.capsules/\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			r := Reconciler{VCS: Git{}}
			p, err := r.Plan(context.Background(), PlanRequest{Workspace: dir, TargetRef: "main", Operation: Refresh, Generation: 9})
			if err != nil {
				t.Fatal(err)
			}
			instance, artifactPath, err := r.MaterializeIntegrationInstance(context.Background(), p, dir)
			if err != nil {
				t.Fatal(err)
			}
			instancePath := filepath.Join(dir, filepath.FromSlash(instance.InstancePath))
			tt.mutate(t, artifactPath, instancePath, instance)
			if _, _, err := r.MaterializeIntegrationInstance(context.Background(), p, dir); err == nil {
				t.Fatal("tampered retained integration authority was accepted")
			}
		})
	}
}

func writeIntegrationArtifact(t *testing.T, path string, instance IntegrationInstance) {
	t.Helper()
	raw, err := json.MarshalIndent(instance, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestApplyContinuationUpdatesTargetFromResolvedIntegration(t *testing.T) {
	dir := capsuletest.Open(t, "rebase-conflict-ready")
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte(".kitsoki-capsule\ncapsule-manifest.json\n.capsules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := Reconciler{VCS: Git{}, Gates: GateVerifierFunc(func(_ context.Context, receipt string, _ Plan) error {
		if receipt != "validation-ok" {
			return os.ErrPermission
		}
		return nil
	})}
	p, err := r.Plan(context.Background(), PlanRequest{Workspace: dir, TargetRef: "main", Operation: Refresh, Generation: 11, RequiredGate: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	instance, _, err := r.MaterializeIntegrationInstance(context.Background(), p, dir)
	if err != nil {
		t.Fatal(err)
	}
	instancePath := filepath.Join(dir, filepath.FromSlash(instance.InstancePath))
	if err := os.WriteFile(filepath.Join(instancePath, "file.txt"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, instancePath, "add", "file.txt")
	runGit(t, instancePath, "commit", "-m", "resolved integration")
	result, err := r.ApplyContinuation(context.Background(), ContinuationApplyRequest{Plan: p, ProjectRoot: dir, ResolverDecision: "artifact:resolver", LostWorkReview: "artifact:review", ValidationReceipt: "validation-ok"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || result.NewTarget == p.Candidate || result.NewTarget == p.Expected.Target {
		t.Fatalf("result %#v", result)
	}
	got := strings.TrimSpace(runGitOutput(t, dir, "rev-parse", "main"))
	if got != result.NewTarget {
		t.Fatalf("main = %s, want %s", got, result.NewTarget)
	}
	if ok, err := r.VCS.IsAncestor(context.Background(), dir, p.Candidate, result.NewTarget); err != nil || !ok {
		t.Fatalf("candidate not preserved: %v", err)
	}
	if ok, err := r.VCS.IsAncestor(context.Background(), dir, p.Expected.Target, result.NewTarget); err != nil || !ok {
		t.Fatalf("target not preserved: %v", err)
	}
}

func TestAbortContinuationPreservesPatchAndRemovesIntegration(t *testing.T) {
	dir := capsuletest.Open(t, "rebase-conflict-ready")
	if err := os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte(".kitsoki-capsule\ncapsule-manifest.json\n.capsules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var events []Event
	r := Reconciler{VCS: Git{}, Events: EventSinkFunc(func(_ context.Context, e Event) error {
		events = append(events, e)
		return nil
	})}
	p, err := r.Plan(context.Background(), PlanRequest{Workspace: dir, TargetRef: "main", Operation: Refresh, Generation: 12})
	if err != nil {
		t.Fatal(err)
	}
	instance, _, err := r.MaterializeIntegrationInstance(context.Background(), p, dir)
	if err != nil {
		t.Fatal(err)
	}
	instancePath := filepath.Join(dir, filepath.FromSlash(instance.InstancePath))
	if err := os.WriteFile(filepath.Join(instancePath, "file.txt"), []byte("manual resolution draft\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := r.AbortContinuation(context.Background(), AbortContinuationRequest{Plan: p, ProjectRoot: dir, Preserve: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Aborted || result.PreservedPatch == "" {
		t.Fatalf("abort result %#v", result)
	}
	if _, err := os.Stat(instancePath); !os.IsNotExist(err) {
		t.Fatalf("integration instance still exists or stat failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".capsules", "sync", p.Continuation.Token+".integration.json")); !os.IsNotExist(err) {
		t.Fatalf("integration artifact still exists or stat failed: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(result.PreservedPatch)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "manual resolution draft") || !strings.Contains(string(raw), p.Digest) {
		t.Fatalf("preserved patch %s", raw)
	}
	if !sawEvent(events, "capsule.sync.aborted") {
		t.Fatalf("missing abort event %#v", events)
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

func TestProtectedPromotionMovesSourceMainNotWorkspaceMain(t *testing.T) {
	source := capsuletest.Open(t, "clean-repo")
	workspace := filepath.Join(t.TempDir(), "workspace")
	runGit(t, "", "clone", "--no-local", source, workspace)
	runGit(t, workspace, "config", "user.name", "test")
	runGit(t, workspace, "config", "user.email", "test@example.invalid")
	base := strings.TrimSpace(runGitOutput(t, source, "rev-parse", "main"))
	runGit(t, workspace, "checkout", "-b", "candidate")
	if err := os.WriteFile(filepath.Join(workspace, "protected.txt"), []byte("protected\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workspace, "add", "protected.txt")
	runGit(t, workspace, "commit", "-m", "candidate")
	candidate := strings.TrimSpace(runGitOutput(t, workspace, "rev-parse", "HEAD"))
	// Protected/candidate reconciliation never consumes FETCH_HEAD. Make that
	// path deliberately unwritable as a file so any fetch that forgets
	// --no-write-fetch-head fails deterministically instead of leaving
	// process-owned scratch state in the shared protected checkout.
	fetchHead := filepath.Join(source, ".git", "FETCH_HEAD")
	if err := os.Remove(fetchHead); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Mkdir(fetchHead, 0o755); err != nil {
		t.Fatal(err)
	}

	r := Reconciler{VCS: Git{}, Gates: GateVerifierFunc(func(_ context.Context, receipt string, p Plan) error {
		if receipt != "ok" || p.Protected.Root != source {
			return os.ErrPermission
		}
		return nil
	})}
	p, err := r.Plan(context.Background(), PlanRequest{Workspace: workspace, ProtectedProjectRoot: source, TargetRef: "main", Operation: Promote, Generation: 1, RequiredGate: "ci"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Class != LocalAhead || p.Candidate != candidate || p.Expected.Target != base {
		t.Fatalf("plan %#v", p)
	}
	result, err := r.Apply(context.Background(), p, "ok")
	if err != nil {
		t.Fatal(err)
	}
	if result.NewTarget != candidate {
		t.Fatalf("result %#v", result)
	}
	if got := strings.TrimSpace(runGitOutput(t, source, "rev-parse", "main")); got != candidate {
		t.Fatalf("source main = %s, want %s", got, candidate)
	}
	if got := strings.TrimSpace(runGitOutput(t, workspace, "rev-parse", "main")); got != base {
		t.Fatalf("workspace-local main moved to %s, want %s", got, base)
	}
	if info, err := os.Stat(fetchHead); err != nil || !info.IsDir() {
		t.Fatalf("FETCH_HEAD scratch path was mutated: info=%v err=%v", info, err)
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

func TestLocalBareRemoteFetcherUpdatesRemoteTrackingRef(t *testing.T) {
	dir := capsuletest.Open(t, "clean-repo")
	remote := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, "", "init", "--bare", remote)
	runGit(t, dir, "push", remote, "main:refs/heads/main")
	other := filepath.Join(t.TempDir(), "other")
	runGit(t, "", "clone", remote, other)
	if err := os.WriteFile(filepath.Join(other, "remote.txt"), []byte("remote"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, other, "add", "remote.txt")
	runGit(t, other, "commit", "-m", "remote advance")
	runGit(t, other, "push", "origin", "main:refs/heads/main")

	result, err := (LocalBareRemoteFetcher{Remote: remote, RemoteName: "origin"}).Fetch(context.Background(), dir, "main")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Fetched || result.TargetRef != "refs/remotes/origin/main" || result.NewTarget == "" || result.NewTarget == result.OldTarget {
		t.Fatalf("fetch result %#v", result)
	}
	if got := strings.TrimSpace(runGitOutput(t, dir, "rev-parse", "refs/remotes/origin/main")); got != result.NewTarget {
		t.Fatalf("remote-tracking ref = %s, want %s", got, result.NewTarget)
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

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
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
