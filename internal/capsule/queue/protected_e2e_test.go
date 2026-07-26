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
	commit(t, root, ".gitignore", ".kitsoki.local.yaml\n", "ignore machine-local config")
	localConfig := []byte("default_profile: queue-test\n")
	if err := os.WriteFile(filepath.Join(root, ".kitsoki.local.yaml"), localConfig, 0o440); err != nil {
		t.Fatal(err)
	}
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
		Gate:        ShellGate{Command: "grep -qx 'default_profile: queue-test' .kitsoki.local.yaml && git diff --check"},
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
	if !hasEvidence(c, "queue:local-config-propagated-to-continuation") {
		t.Fatalf("local config propagation not evidenced: %v", c.Evidence)
	}
	if got, err := os.ReadFile(filepath.Join(c.WorkspacePath, ".kitsoki.local.yaml")); err != nil || string(got) != string(localConfig) {
		t.Fatalf("continuation local config=%q err=%v, want exact protected config", got, err)
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

func TestProtectedIntegrationRefreshesReusedWorkspaceRuntimeConfig(t *testing.T) {
	root := protectedQueueRepo(t)
	commit(t, root, ".gitignore", ".kitsoki.local.yaml\n", "ignore machine-local config")
	git(t, root, "checkout", "-b", "agent/config-reuse")
	commit(t, root, "candidate.txt", "candidate\n", "candidate work")
	candidateSHA := git(t, root, "rev-parse", "HEAD")
	git(t, root, "checkout", "main")

	source := filepath.Join(root, ".kitsoki.local.yaml")
	if err := os.WriteFile(source, []byte("runtime: A\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	integration := ProtectedIntegration{ProjectRoot: root, TargetRef: "main"}
	candidate := Candidate{ID: "config-reuse", SHA: candidateSHA, TargetRef: "main"}
	first, err := integration.Speculate(context.Background(), candidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.RuntimeConfigDigest == "" {
		t.Fatal("first preparation omitted runtime config identity")
	}

	if err := os.WriteFile(source, []byte("runtime: B\n"), 0o440); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(source, 0o440); err != nil {
		t.Fatal(err)
	}
	second, err := integration.Speculate(context.Background(), candidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second.WorkspacePath != first.WorkspacePath {
		t.Fatalf("workspace was not reused: first=%s second=%s", first.WorkspacePath, second.WorkspacePath)
	}
	if second.RuntimeConfigDigest == first.RuntimeConfigDigest {
		t.Fatal("runtime config identity did not change after protected policy update")
	}
	got, err := os.ReadFile(filepath.Join(second.WorkspacePath, ".kitsoki.local.yaml"))
	if err != nil || string(got) != "runtime: B\n" {
		t.Fatalf("reused workspace config=%q err=%v, want refreshed B", got, err)
	}
	info, err := os.Stat(filepath.Join(second.WorkspacePath, ".kitsoki.local.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o440 {
		t.Fatalf("reused workspace mode=%#o, want %#o", info.Mode().Perm(), os.FileMode(0o440))
	}

	memo := FileGateMemo{ProjectRoot: root}
	if err := memo.Store(first.SHA, "test/v1", first.RuntimeConfigDigest, GateResult{Passed: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := memo.Lookup(second.SHA, "test/v1", second.RuntimeConfigDigest); ok {
		t.Fatal("runtime config B incorrectly reused config A gate memo")
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

// TestWorkerPathFinalizationCleansDirtyCheckoutWithUnreadableFile is A8: the
// full worker path (Store.Process → Worker.finalize → ProtectedFinalizer,
// not PreserveWIP called directly) landing a candidate against a protected
// checkout that is dirty with both ordinary content AND a permission-denied
// file. It must land clean — HEAD synced to the new tip, checkout clean —
// while leaving the unreadable file's content completely untouched, exactly
// the property A4's WIP-preservation hardening exists to guarantee end to
// end through the real finalization path, not just in PreserveWIP isolation.
func TestWorkerPathFinalizationCleansDirtyCheckoutWithUnreadableFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses file permissions; this test needs a non-root process")
	}
	root := protectedQueueRepo(t)
	base := git(t, root, "rev-parse", "HEAD")
	commit(t, root, "candidate.txt", "candidate\n", "candidate")
	sha := git(t, root, "rev-parse", "HEAD")
	git(t, root, "branch", "agent/candidate", sha)
	git(t, root, "reset", "--hard", base)

	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("uncommitted local edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(root, "locked.txt")
	if err := os.WriteFile(locked, []byte("original locked content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o644) })

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
		t.Fatalf("candidate did not land: %#v", c)
	}
	if got := git(t, root, "rev-parse", "main"); got != sha {
		t.Fatalf("main=%s want %s", got, sha)
	}
	if head := git(t, root, "rev-parse", "HEAD"); head != sha {
		t.Fatalf("checkout HEAD=%s not synced to new main %s", head, sha)
	}
	// Clean except for the one path that could never be backed up.
	status := strings.TrimSpace(git(t, root, "status", "--porcelain", "--untracked-files=all", "--", ".", ":(exclude).capsules"))
	if status != "?? locked.txt" {
		t.Fatalf("protected checkout not clean (modulo the unreadable file): %q", status)
	}
	if !strings.Contains(c.FinalizationLog, "could not be read and were left untouched") {
		t.Fatalf("finalization log missing the skipped-path evidence: %q", c.FinalizationLog)
	}
	info, err := os.Lstat(locked)
	if err != nil {
		t.Fatalf("locked.txt should still exist untouched: %v", err)
	}
	if info.Mode().Perm() != 0o000 {
		t.Fatalf("locked.txt permissions changed: %v", info.Mode().Perm())
	}
	if err := os.Chmod(locked, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(locked)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original locked content" {
		t.Fatalf("locked.txt content changed: %q", got)
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

// TestTrainStackingChainsSpeculationAndCascadesOnLanding is the core B3
// proof: two independent, non-conflicting candidates queued back to back.
// Before stacking, preparing the second candidate always speculated against
// the live target — identical work to the first — and landing the first
// always staled the second's base, forcing a full reprepare (re-speculate,
// re-gate) even though the second's own change never touched anything the
// first landed. With stacking, the second candidate's workspace chains onto
// the first's already-speculated tree, and landing the first must NOT stale
// the second: it lands on its first attempt, with the first's landed
// content already baked into what its gate validated.
func TestTrainStackingChainsSpeculationAndCascadesOnLanding(t *testing.T) {
	root := protectedQueueRepo(t)
	base := git(t, root, "rev-parse", "HEAD")

	commit(t, root, "first.txt", "first\n", "first")
	firstSHA := git(t, root, "rev-parse", "HEAD")
	git(t, root, "branch", "agent/first", firstSHA)
	git(t, root, "reset", "--hard", base)

	commit(t, root, "second.txt", "second\n", "second")
	secondSHA := git(t, root, "rev-parse", "HEAD")
	git(t, root, "branch", "agent/second", secondSHA)
	git(t, root, "reset", "--hard", base)

	store := Store{ProjectRoot: root}
	firstC, err := store.Submit(Submit{Branch: "agent/first", SHA: firstSHA, Receipt: persistedReceipt(t, root, firstSHA)})
	if err != nil {
		t.Fatal(err)
	}
	secondC, err := store.Submit(Submit{Branch: "agent/second", SHA: secondSHA, Receipt: persistedReceipt(t, root, secondSHA)})
	if err != nil {
		t.Fatal(err)
	}

	worker := Worker{Store: store, Deps: ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: root, TargetRef: "main"},
		Gate:        ShellGate{Command: "git diff --check"},
		Finalizer:   ProtectedFinalizer{ProjectRoot: root, TargetRef: "main"},
	}}

	// Prepare the first candidate: claim+speculate+gate.
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := mustGet(t, store, firstC.ID)
	if first.phase() != ReadyToFinalize {
		t.Fatalf("first candidate not ready: %#v", first)
	}

	// Prepare the second candidate: it must chain onto the first's tree.
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := mustGet(t, store, secondC.ID)
	if second.phase() != ReadyToFinalize {
		t.Fatalf("second candidate not ready: %#v", second)
	}
	stacked := false
	for _, e := range second.Evidence {
		if e == "queue:stacked-on="+first.ID {
			stacked = true
		}
	}
	if !stacked {
		t.Fatalf("second candidate did not record stacking onto the first: evidence=%v", second.Evidence)
	}
	// The second candidate's prepared tree must contain BOTH files: proof
	// it actually chained onto the first's speculative tree, not just its
	// own commit.
	if _, err := gitOutput(context.Background(), second.WorkspacePath, "cat-file", "-e", second.TreeSHA+":first.txt"); err != nil {
		t.Fatalf("second candidate's tree is missing first.txt (did not stack): %v", err)
	}
	if _, err := gitOutput(context.Background(), second.WorkspacePath, "cat-file", "-e", second.TreeSHA+":second.txt"); err != nil {
		t.Fatalf("second candidate's tree is missing second.txt: %v", err)
	}

	// Finalize the first candidate: it lands.
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	first = mustGet(t, store, firstC.ID)
	if first.phase() != Landed {
		t.Fatalf("first candidate did not land: %#v", first)
	}

	// Finalize the second candidate: since it was already stacked on the
	// first's landed tree, this must land on the very next pass — no
	// reprepare cycle, no second gate run.
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	second = mustGet(t, store, secondC.ID)
	if second.phase() != Landed {
		t.Fatalf("second candidate did not cascade-land after the first: %#v", second)
	}
	if second.Attempt != 1 {
		t.Fatalf("second candidate needed %d attempts, want exactly 1 (stacking should have avoided a reprepare)", second.Attempt)
	}
	if got := git(t, root, "show", "main:first.txt"); strings.TrimSpace(got) != "first" {
		t.Fatalf("main missing first.txt content: %q", got)
	}
	if got := git(t, root, "show", "main:second.txt"); strings.TrimSpace(got) != "second" {
		t.Fatalf("main missing second.txt content: %q", got)
	}
}

// TestTrainStackingFallsBackToUnstackedOnConflict guards the safety valve:
// two candidates that both touch the same file cannot both be included in
// one stacked tree. The second candidate must not fail outright over a
// conflict that is not its own fault — it falls back to speculating against
// the live target directly, exactly the pre-stacking behavior, and still
// prepares successfully (landing order/conflict resolution against the
// live target is unchanged, orthogonal machinery this test does not need
// to exercise).
func TestTrainStackingFallsBackToUnstackedOnConflict(t *testing.T) {
	root := protectedQueueRepo(t)
	base := git(t, root, "rev-parse", "HEAD")

	commit(t, root, "shared.txt", "from-first\n", "first touches shared.txt")
	firstSHA := git(t, root, "rev-parse", "HEAD")
	git(t, root, "branch", "agent/first", firstSHA)
	git(t, root, "reset", "--hard", base)

	commit(t, root, "shared.txt", "from-second\n", "second touches shared.txt too")
	secondSHA := git(t, root, "rev-parse", "HEAD")
	git(t, root, "branch", "agent/second", secondSHA)
	git(t, root, "reset", "--hard", base)

	store := Store{ProjectRoot: root}
	firstC, err := store.Submit(Submit{Branch: "agent/first", SHA: firstSHA, Receipt: persistedReceipt(t, root, firstSHA)})
	if err != nil {
		t.Fatal(err)
	}
	secondC, err := store.Submit(Submit{Branch: "agent/second", SHA: secondSHA, Receipt: persistedReceipt(t, root, secondSHA)})
	if err != nil {
		t.Fatal(err)
	}

	worker := Worker{Store: store, Deps: ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: root, TargetRef: "main"},
		Gate:        ShellGate{Command: "git diff --check"},
		Finalizer:   ProtectedFinalizer{ProjectRoot: root, TargetRef: "main"},
	}}

	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := mustGet(t, store, firstC.ID)
	if first.phase() != ReadyToFinalize {
		t.Fatalf("first candidate not ready: %#v", first)
	}

	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	second := mustGet(t, store, secondC.ID)
	if second.phase() != ReadyToFinalize {
		t.Fatalf("second candidate should still prepare after falling back: %#v", second)
	}
	fellBack := false
	for _, e := range second.Evidence {
		if e == "queue:stack-conflict-with="+first.ID+" fell-back-to-unstacked" {
			fellBack = true
		}
	}
	if !fellBack {
		t.Fatalf("second candidate did not record the stack-conflict fallback: evidence=%v", second.Evidence)
	}
	// The fallback tree must be the second candidate's own unstacked
	// content, not a half-merged state.
	if got := git(t, root, "show", second.TreeSHA+":shared.txt"); strings.TrimSpace(got) != "from-second" {
		t.Fatalf("fallback tree has wrong shared.txt content: %q", got)
	}
}
