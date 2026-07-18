package queue

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/receipt"
	"kitsoki/internal/capsule/record"
)

// persistedReceipt builds a promotion-eligible receipt for sha AND persists it
// with its run record under root/.capsules/ci, so the finalizer's
// record.PromotionGate can verify it end to end.
func persistedReceipt(t *testing.T, root, sha string) receipt.Receipt {
	t.Helper()
	jobID := "job-" + sha[:8]
	lock, err := environment.SealLock(environment.Lock{Schema: environment.LockSchema, ID: "ci", DefinitionDigest: "sha256:env-def", Network: "none", Sandbox: "supervised"})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := executor.Seal(executor.Envelope{JobID: jobID, ProjectID: "p", DefinitionDigest: "sha256:def", Instance: control.Handle{ID: "w", Generation: 1}, SourceDigest: sha, StoryPath: "stories/ci/app.yaml", StoryDigest: "sha256:story", Environment: lock, Policy: executor.Policy{Network: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	verdict := ci.Verdict{Schema: ci.VerdictSchema, Pipeline: "change", Outcome: "passed", Checks: []ci.Check{{ID: "test", Kind: "deterministic", Outcome: "passed", Evidence: []string{"artifact:test"}}}, PromotionEligible: true, SourceDigest: envelope.SourceDigest, StoryDigest: envelope.StoryDigest, EnvironmentDigest: envelope.Environment.Digest, EnvelopeDigest: envelope.Digest}
	result := ci.RunResult{Job: artifactjob.Job{ID: artifactjob.JobID(jobID)}, Envelope: envelope, Verdict: verdict, Execution: executor.Result{VerdictArtifact: "artifact:verdict"}}
	stored, err := record.Persist(root, result)
	if err != nil {
		t.Fatal(err)
	}
	if err := (ci.FileRunStore{ProjectRoot: root}).Write(ci.RunRecord{JobID: jobID, Result: result, ReceiptID: stored.Receipt.ReceiptID, ReceiptVerification: stored.Verification.Status}); err != nil {
		t.Fatal(err)
	}
	return stored.Receipt
}

// End-to-end scenarios against real git repositories, pinning the productized
// POG landing contract: disjoint divergence continues automatically, dirty
// protected checkouts are preserved (never lost, never blocking), and the
// operator override lands through the full protected CAS.

func TestDisjointDivergentCandidateLandsAutomatically(t *testing.T) {
	root := protectedQueueRepo(t)
	// Candidate edits candidate.txt off the original base…
	git(t, root, "checkout", "-b", "agent/disjoint")
	commit(t, root, "candidate.txt", "candidate\n", "candidate work")
	candidateSHA := git(t, root, "rev-parse", "HEAD")
	git(t, root, "checkout", "main")
	// …while main advances with a disjoint file: divergent but conflict-free.
	commit(t, root, "mainline.txt", "mainline\n", "mainline work")
	mainBefore := git(t, root, "rev-parse", "main")

	store := Store{ProjectRoot: root}
	if _, err := store.Submit(Submit{Branch: "agent/disjoint", SHA: candidateSHA, Receipt: persistedReceipt(t, root, candidateSHA)}); err != nil {
		t.Fatal(err)
	}
	state, err := store.Process(context.Background(), ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: root, TargetRef: "main"},
		Gate:        ShellGate{Command: "git diff --check"},
		Finalizer:   ProtectedFinalizer{ProjectRoot: root, TargetRef: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := state.Candidates[0]
	if c.phase() != Landed {
		t.Fatalf("disjoint divergent candidate did not land: %#v", c)
	}
	if !hasEvidence(c, "queue:disjoint-histories-merged-automatically") {
		t.Fatalf("auto-merge not evidenced: %v", c.Evidence)
	}
	main := git(t, root, "rev-parse", "main")
	if main == mainBefore {
		t.Fatal("main did not advance")
	}
	// The merge preserves both lines of history and both files.
	git(t, root, "merge-base", "--is-ancestor", candidateSHA, "main")
	git(t, root, "merge-base", "--is-ancestor", mainBefore, "main")
	if got := strings.TrimSpace(git(t, root, "show", "main:candidate.txt")); got != "candidate" {
		t.Fatalf("candidate content=%q", got)
	}
	if got := strings.TrimSpace(git(t, root, "show", "main:mainline.txt")); got != "mainline" {
		t.Fatalf("mainline content=%q", got)
	}
}

func TestDirtyProtectedCheckoutIsPreservedNotLostAndTrainContinues(t *testing.T) {
	root := protectedQueueRepo(t)
	base := git(t, root, "rev-parse", "HEAD")
	commit(t, root, "candidate.txt", "candidate\n", "candidate")
	sha := git(t, root, "rev-parse", "HEAD")
	git(t, root, "branch", "agent/candidate", sha)
	git(t, root, "reset", "--hard", base)

	// A human (or stray agent) left uncommitted + untracked work in the
	// protected checkout.
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("uncommitted local edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "stray-notes.txt"), []byte("do not lose me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	store := Store{ProjectRoot: root}
	if _, err := store.Submit(Submit{Branch: "agent/candidate", SHA: sha, Receipt: persistedReceipt(t, root, sha)}); err != nil {
		t.Fatal(err)
	}
	state, err := store.Process(context.Background(), ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: root, TargetRef: "main"},
		Gate:        ShellGate{Command: "git diff --check"},
		Finalizer:   ProtectedFinalizer{ProjectRoot: root, TargetRef: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := state.Candidates[0]
	if c.phase() != Landed {
		t.Fatalf("candidate blocked by dirty checkout: %#v", c)
	}
	if got := git(t, root, "rev-parse", "main"); got != sha {
		t.Fatalf("main=%s want %s", got, sha)
	}
	// The WIP is on a preserved branch, byte for byte.
	branches := strings.Fields(git(t, root, "for-each-ref", "--format=%(refname:short)", "refs/heads/queue/preserved-wip"))
	if len(branches) != 1 {
		t.Fatalf("preserved branches=%v", branches)
	}
	if got := strings.TrimSpace(git(t, root, "show", branches[0]+":base.txt")); got != "uncommitted local edit" {
		t.Fatalf("preserved edit=%q", got)
	}
	if got := strings.TrimSpace(git(t, root, "show", branches[0]+":stray-notes.txt")); got != "do not lose me" {
		t.Fatalf("preserved untracked=%q", got)
	}
	if !strings.Contains(c.FinalizationLog, "preserved protected-checkout WIP on "+branches[0]) {
		t.Fatalf("finalization log missing preservation evidence: %q", c.FinalizationLog)
	}
	// The protected checkout is clean and synced to the new main.
	if status := strings.TrimSpace(git(t, root, "status", "--porcelain", "--untracked-files=all", "--", ".", ":(exclude).capsules")); status != "" {
		t.Fatalf("protected checkout dirty after landing: %q", status)
	}
	if head := git(t, root, "rev-parse", "HEAD"); head != sha {
		t.Fatalf("checkout HEAD=%s not synced to new main %s", head, sha)
	}
}

func TestOverrideLandsThroughProtectedCASOnRealRepo(t *testing.T) {
	root := protectedQueueRepo(t)
	base := git(t, root, "rev-parse", "HEAD")
	commit(t, root, "hotfix.txt", "hotfix\n", "hotfix")
	sha := git(t, root, "rev-parse", "HEAD")
	git(t, root, "branch", "agent/hotfix", sha)
	git(t, root, "reset", "--hard", base)

	store := Store{ProjectRoot: root}
	submitted, err := store.Submit(Submit{Branch: "agent/hotfix", SHA: sha, Receipt: persistedReceipt(t, root, sha)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Override(Op{ID: submitted.ID, Actor: "brad", Reason: "prod outage"}); err != nil {
		t.Fatal(err)
	}
	// The deterministic gate is broken (always red); override lands anyway.
	state, err := store.Process(context.Background(), ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: root, TargetRef: "main"},
		Gate:        ShellGate{Command: "false"},
		Finalizer:   ProtectedFinalizer{ProjectRoot: root, TargetRef: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := state.Candidates[0]
	if c.phase() != Landed || c.GateVersion != "operator-override/v1" {
		t.Fatalf("override landing: %#v", c)
	}
	if got := git(t, root, "rev-parse", "main"); got != sha {
		t.Fatalf("main=%s want %s", got, sha)
	}
}
