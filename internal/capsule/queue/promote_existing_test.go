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
	"kitsoki/internal/capsule/record"
)

func TestPromoteExistingCarriesExactStagingLandingToMainAndRestartsIdempotently(t *testing.T) {
	root, staged, source := landedStagingCandidate(t)
	mainBefore := git(t, root, "rev-parse", "main")
	certifications := 0
	certifier := ExistingSHACertifierFunc(func(_ context.Context, in ExistingSHACertification) (record.Stored, error) {
		certifications++
		if in.Request.LandedSHA != staged || in.SourceLandingReceiptID != source.ReceiptID ||
			in.Request.Pipeline != "change" {
			t.Fatalf("certification input = %#v", in)
		}
		return persistedPromotionReceipt(t, root, in.JobID, staged), nil
	})
	request := PromoteExistingRequest{
		SourceTarget: "staging/local", LandedSHA: staged,
		DestinationTarget: "main", Pipeline: "change", GateCommand: "git diff --check",
	}
	first, err := (PromoteExistingAuthority{ProjectRoot: root, Certifier: certifier}).Promote(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != PromoteExistingStatusQueued || first.Candidate.SHA != staged || first.Candidate.TargetRef != "main" {
		t.Fatalf("first result = %#v", first)
	}
	if first.Candidate.RequiredGateTier != "full" {
		t.Fatalf("change-tier source certification weakened main landing tier: candidate=%#v", first.Candidate)
	}
	if got := git(t, root, "rev-parse", "main"); got != mainBefore {
		t.Fatalf("promote-existing performed protected CAS: main=%s want=%s", got, mainBefore)
	}
	if !sameStrings(first.Candidate.RequiredReceiptIDs, []string{first.Record.CIReceiptID, source.ReceiptID}) {
		t.Fatalf("required receipts = %v, want new=%s source=%s", first.Candidate.RequiredReceiptIDs, first.Record.CIReceiptID, source.ReceiptID)
	}

	// A new authority instance models controller restart. It must recover the
	// same durable receipt/candidate without invoking CI again.
	restarted, err := (PromoteExistingAuthority{
		ProjectRoot: root,
		Certifier: ExistingSHACertifierFunc(func(context.Context, ExistingSHACertification) (record.Stored, error) {
			t.Fatal("restart attempted duplicate certification")
			return record.Stored{}, nil
		}),
	}).Promote(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Record.CIReceiptID != first.Record.CIReceiptID || restarted.Candidate.ID != first.Candidate.ID {
		t.Fatalf("restart identities changed: first=%#v restarted=%#v", first, restarted)
	}
	if certifications != 1 {
		t.Fatalf("certifications=%d want 1", certifications)
	}
	state, err := (Store{ProjectRoot: root}).List()
	if err != nil {
		t.Fatal(err)
	}
	mainCandidates := 0
	for _, candidate := range state.Candidates {
		if candidate.TargetRef == "main" && candidate.SHA == staged {
			mainCandidates++
		}
	}
	if mainCandidates != 1 {
		t.Fatalf("main candidates=%d state=%#v", mainCandidates, state)
	}
	receipts, err := filepath.Glob(filepath.Join(root, ".capsules", "ci", first.Record.CIJobID+".receipt.json"))
	if err != nil || len(receipts) != 1 {
		t.Fatalf("exact CI receipts=%v err=%v", receipts, err)
	}

	// Only the ordinary queue worker may perform the destination CAS.
	fullGateRuns := 0
	fullGateSHA := ""
	state, err = (Store{ProjectRoot: root}).Process(context.Background(), ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: root, TargetRef: "main"},
		Gate: gateFunc(func(_ context.Context, spec Speculation) (GateResult, error) {
			fullGateRuns++
			fullGateSHA = spec.SHA
			return GateResult{Passed: true, Evidence: []string{"gate:full-final-tree=" + spec.SHA}}, nil
		}),
		Finalizer:   ProtectedFinalizer{ProjectRoot: root, TargetRef: "main"},
		TargetRef:   "main",
		GateVersion: "full:" + request.GateCommand,
	})
	if err != nil {
		t.Fatal(err)
	}
	mainAfter := git(t, root, "rev-parse", "main")
	mainCandidate := candidateByID(t, state, first.Candidate.ID)
	if mainCandidate.phase() != Landed || mainCandidate.ResultMainSHA != mainAfter || mainCandidate.SHA != staged {
		t.Fatalf("main candidate = %#v", mainCandidate)
	}
	if certifications != 1 || fullGateRuns != 1 {
		t.Fatalf("source certifications=%d full final-tree gates=%d, want exactly one of each", certifications, fullGateRuns)
	}
	if fullGateSHA == "" || fullGateSHA != mainCandidate.TreeSHA ||
		fullGateSHA != mainCandidate.ValidatedSHA ||
		fullGateSHA != mainCandidate.ResultMainSHA ||
		fullGateSHA != mainAfter {
		t.Fatalf("full gate did not bind the exact landed final tree: gate=%s tree=%s validated=%s result=%s main=%s",
			fullGateSHA, mainCandidate.TreeSHA, mainCandidate.ValidatedSHA, mainCandidate.ResultMainSHA, mainAfter)
	}
	git(t, root, "merge-base", "--is-ancestor", staged, mainAfter)

	afterLanding, err := (PromoteExistingAuthority{
		ProjectRoot: root,
		Certifier: ExistingSHACertifierFunc(func(context.Context, ExistingSHACertification) (record.Stored, error) {
			t.Fatal("post-landing restart attempted duplicate certification")
			return record.Stored{}, nil
		}),
	}).Promote(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if afterLanding.Candidate.ID != first.Candidate.ID || afterLanding.Candidate.phase() != Landed {
		t.Fatalf("post-landing replay changed authority: %#v", afterLanding)
	}
}

func TestPromoteExistingUsesOneExternalQueueAuthorityAcrossLandingAndRestart(t *testing.T) {
	queueRoot := filepath.Join(t.TempDir(), "shared-queue")
	root, staged, source := landedStagingCandidateAt(t, queueRoot)
	request := PromoteExistingRequest{
		SourceTarget: "staging/local", LandedSHA: staged,
		DestinationTarget: "main", Pipeline: "change", GateCommand: "git diff --check",
	}
	certifications := 0
	authority := PromoteExistingAuthority{
		ProjectRoot: root, QueueRoot: queueRoot,
		Certifier: ExistingSHACertifierFunc(func(_ context.Context, in ExistingSHACertification) (record.Stored, error) {
			certifications++
			return persistedPromotionReceipt(t, root, in.JobID, staged), nil
		}),
	}
	first, err := authority.Promote(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Candidate.RequiredGateTier != "full" {
		t.Fatalf("external authority weakened main landing tier: %#v", first.Candidate)
	}
	fullGateRuns := 0
	fullGateSHA := ""
	state, err := (Store{ProjectRoot: root, QueueRoot: queueRoot}).Process(context.Background(), ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: root, QueueRoot: queueRoot, TargetRef: "main"},
		Gate: gateFunc(func(_ context.Context, spec Speculation) (GateResult, error) {
			fullGateRuns++
			fullGateSHA = spec.SHA
			return GateResult{Passed: true, Evidence: []string{"gate:full-final-tree=" + spec.SHA}}, nil
		}),
		Finalizer:   ProtectedFinalizer{ProjectRoot: root, QueueRoot: queueRoot, TargetRef: "main"},
		TargetRef:   "main",
		GateVersion: "full:" + request.GateCommand,
	})
	if err != nil {
		t.Fatal(err)
	}
	landed := candidateByID(t, state, first.Candidate.ID)
	if landed.phase() != Landed || landed.ResultMainSHA == "" {
		t.Fatalf("destination candidate=%#v", landed)
	}
	if fullGateSHA == "" || fullGateSHA != landed.TreeSHA || fullGateSHA != landed.ValidatedSHA ||
		fullGateSHA != landed.ResultMainSHA || fullGateSHA != git(t, root, "rev-parse", "main") {
		t.Fatalf("external full gate did not bind exact landed tree: gate=%s candidate=%#v", fullGateSHA, landed)
	}
	restarted, err := authority.Promote(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.Candidate.ID != first.Candidate.ID || certifications != 1 || fullGateRuns != 1 {
		t.Fatalf("restart=%#v certifications=%d full-gates=%d", restarted, certifications, fullGateRuns)
	}
	if _, err := os.Stat(filepath.Join(root, ".capsules", "queue", "state.json")); !os.IsNotExist(err) {
		t.Fatalf("project-local queue fork exists: %v", err)
	}
	raw, err := filepath.Glob(filepath.Join(queueRoot, "promote-existing", "*.json"))
	if err != nil || len(raw) != 1 {
		t.Fatalf("promotion records=%v err=%v", raw, err)
	}
	if !sameStrings(first.Candidate.RequiredReceiptIDs, []string{first.Record.CIReceiptID, source.ReceiptID}) {
		t.Fatalf("required receipts=%v", first.Candidate.RequiredReceiptIDs)
	}
}

func TestPromoteExistingRelativeQueueRootUsesStoreCWDNotProjectRoot(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	authority := PromoteExistingAuthority{ProjectRoot: t.TempDir(), QueueRoot: "relative-authority"}
	got := authority.queueRoot()
	want := filepath.Join(cwd, "relative-authority")
	if got != want {
		t.Fatalf("queue root=%s want Store-compatible %s", got, want)
	}
}

func TestPromoteExistingRejectsReachableSHAWithoutLandedSourceReceipt(t *testing.T) {
	root := protectedQueueRepo(t)
	base := git(t, root, "rev-parse", "main")
	commit(t, root, "reachable.txt", "reachable\n", "reachable only")
	reachable := git(t, root, "rev-parse", "HEAD")
	git(t, root, "reset", "--hard", base)
	git(t, root, "update-ref", "refs/heads/staging/local", reachable)

	result, err := (PromoteExistingAuthority{
		ProjectRoot: root,
		Certifier: ExistingSHACertifierFunc(func(context.Context, ExistingSHACertification) (record.Stored, error) {
			t.Fatal("unproven source reached certifier")
			return record.Stored{}, nil
		}),
	}).Promote(context.Background(), PromoteExistingRequest{
		SourceTarget: "staging/local", LandedSHA: reachable,
		DestinationTarget: "main", Pipeline: "change", GateCommand: "git diff --check",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PromoteExistingStatusNeedsInput || result.Record.FailureClass != "source_landing_unproven" {
		t.Fatalf("result = %#v", result)
	}
}

func TestPromoteExistingRejectsMissingSourceLandingReceipt(t *testing.T) {
	root, staged, source := landedStagingCandidate(t)
	if err := os.Remove(source.ReceiptRef); err != nil {
		t.Fatal(err)
	}
	result, err := (PromoteExistingAuthority{
		ProjectRoot: root,
		Certifier: ExistingSHACertifierFunc(func(context.Context, ExistingSHACertification) (record.Stored, error) {
			t.Fatal("missing source receipt reached certifier")
			return record.Stored{}, nil
		}),
	}).Promote(context.Background(), PromoteExistingRequest{
		SourceTarget: "staging/local", LandedSHA: staged,
		DestinationTarget: "main", Pipeline: "change", GateCommand: "git diff --check",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PromoteExistingStatusNeedsInput || result.Record.FailureClass != "source_landing_unproven" ||
		!strings.Contains(result.Record.Failure, "receipt") {
		t.Fatalf("result = %#v", result)
	}
}

func TestPromoteExistingFailsClosedWhenSourceMovesDuringCertification(t *testing.T) {
	root, staged, _ := landedStagingCandidate(t)
	git(t, root, "checkout", "-b", "test/staging-advance", "main")
	commit(t, root, "advance.txt", "advance\n", "advance staging")
	advanced := git(t, root, "rev-parse", "HEAD")
	git(t, root, "checkout", "main")

	request := PromoteExistingRequest{
		SourceTarget: "staging/local", LandedSHA: staged,
		DestinationTarget: "main", Pipeline: "change", GateCommand: "git diff --check",
	}
	result, err := (PromoteExistingAuthority{
		ProjectRoot: root,
		Certifier: ExistingSHACertifierFunc(func(_ context.Context, in ExistingSHACertification) (record.Stored, error) {
			stored := persistedPromotionReceipt(t, root, in.JobID, staged)
			git(t, root, "update-ref", "refs/heads/staging/local", advanced, staged)
			return stored, nil
		}),
	}).Promote(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PromoteExistingStatusNeedsInput || result.Record.FailureClass != "target_moved" {
		t.Fatalf("result = %#v", result)
	}
	state, err := (Store{ProjectRoot: root}).List()
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range state.Candidates {
		if candidate.TargetRef == "main" && candidate.SHA == staged {
			t.Fatalf("target movement admitted main candidate: %#v", candidate)
		}
	}
}

func landedStagingCandidate(t *testing.T) (string, string, Candidate) {
	return landedStagingCandidateAt(t, "")
}

func landedStagingCandidateAt(t *testing.T, queueRoot string) (string, string, Candidate) {
	t.Helper()
	root := protectedQueueRepo(t)
	// The shared queue fixture leaves lifecycle scripts untracked so WIP tests
	// can exercise their preservation. A two-target train needs those scripts
	// to remain available after staging finalization, as they are in a real
	// installed project, so commit them into this fixture's base.
	oldBase := git(t, root, "rev-parse", "main")
	git(t, root, "add", "scripts")
	git(t, root, "commit", "-m", "install queue lifecycle")
	installedBase := git(t, root, "rev-parse", "main")
	git(t, root, "update-ref", "refs/heads/staging/local", installedBase, oldBase)
	stagingClone := filepath.Join(root, ".capsules", "staging", "local")
	git(t, stagingClone, "fetch", "source", "staging/local")
	git(t, stagingClone, "reset", "--hard", "FETCH_HEAD")

	base := git(t, root, "rev-parse", "main")
	commit(t, root, "worker-fix.txt", "fixed\n", "worker fix")
	workerSHA := git(t, root, "rev-parse", "HEAD")
	git(t, root, "branch", "worker/fix", workerSHA)
	git(t, root, "reset", "--hard", base)
	sourceReceipt := persistedReceipt(t, root, workerSHA)
	store := Store{ProjectRoot: root, QueueRoot: queueRoot}
	submitted, err := store.Submit(Submit{
		Branch: "worker/fix", SHA: workerSHA, Receipt: sourceReceipt,
		ReceiptRef: filepath.Join(root, ".capsules", "ci", "job-"+workerSHA[:8]+".receipt.json"),
		TargetRef:  "staging/local",
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Process(context.Background(), ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: root, QueueRoot: queueRoot, TargetRef: "staging/local"},
		Gate:        ShellGate{Command: "git diff --check"},
		Finalizer:   ProtectedFinalizer{ProjectRoot: root, QueueRoot: queueRoot, TargetRef: "staging/local"},
		TargetRef:   "staging/local",
		GateVersion: "change:git diff --check",
	})
	if err != nil {
		t.Fatal(err)
	}
	source := candidateByID(t, state, submitted.ID)
	if source.phase() != Landed || source.ResultMainSHA == "" {
		t.Fatalf("source candidate = %#v", source)
	}
	if got := git(t, root, "rev-parse", "staging/local"); got != source.ResultMainSHA {
		t.Fatalf("staging=%s result=%s", got, source.ResultMainSHA)
	}
	return root, source.ResultMainSHA, source
}

func persistedPromotionReceipt(t *testing.T, root, jobID, sha string) record.Stored {
	t.Helper()
	lock, err := environment.SealLock(environment.Lock{
		Schema: environment.LockSchema, ID: "ci", DefinitionDigest: "sha256:env-def",
		Network: "none", Sandbox: "supervised",
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := executor.Seal(executor.Envelope{
		JobID: jobID, ProjectID: "p", DefinitionDigest: "sha256:def",
		Instance:     control.Handle{ID: "promote-existing", Generation: 1},
		SourceDigest: sha, StoryPath: "stories/ci/app.yaml",
		StoryDigest: "sha256:promote-existing-story", Environment: lock,
		Policy: executor.Policy{Network: "none"},
	})
	if err != nil {
		t.Fatal(err)
	}
	verdict := ci.Verdict{
		Schema: ci.VerdictSchema, Pipeline: "change", Outcome: "passed",
		Checks:            []ci.Check{{ID: "test", Kind: "deterministic", Outcome: "passed", Evidence: []string{"artifact:test"}}},
		PromotionEligible: true, SourceDigest: envelope.SourceDigest,
		StoryDigest: envelope.StoryDigest, EnvironmentDigest: envelope.Environment.Digest,
		EnvelopeDigest: envelope.Digest,
	}
	result := ci.RunResult{
		Job: artifactjob.Job{ID: artifactjob.JobID(jobID)}, Envelope: envelope,
		Verdict: verdict, Execution: executor.Result{VerdictArtifact: "artifact:verdict"},
		Terminal: true,
	}
	stored, err := record.Persist(root, result)
	if err != nil {
		t.Fatal(err)
	}
	if err := (ci.FileRunStore{ProjectRoot: root}).Write(ci.RunRecord{
		JobID: jobID, Result: result, ReceiptID: stored.Receipt.ReceiptID,
		ReceiptVerification: stored.Verification.Status,
	}); err != nil {
		t.Fatal(err)
	}
	return stored
}

func candidateByID(t *testing.T, state State, id string) Candidate {
	t.Helper()
	for _, candidate := range state.Candidates {
		if candidate.ID == id {
			return candidate
		}
	}
	t.Fatalf("candidate %s not found in %#v", id, state)
	return Candidate{}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	left := append([]string(nil), a...)
	right := append([]string(nil), b...)
	sortStrings(left)
	sortStrings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
