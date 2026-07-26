package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/queue"
	"kitsoki/internal/capsule/record"
)

func TestCapsulePromoteExposesExplicitEmergencySkipTestsFlag(t *testing.T) {
	flag := capsulePromoteCmd().Flags().Lookup("skip-tests")
	if flag == nil {
		t.Fatal("capsule promote is missing --skip-tests")
	}
	if flag.DefValue != "false" {
		t.Fatalf("skip-tests default = %q", flag.DefValue)
	}
}

// TestCapsulePromoteDoctorCheckReturnsTypedNotReadyForMissingConfig guards
// the receipt-bound promotion path: `capsule promote` must run the same
// bounded, no-spend readiness preflight as `capsule ci doctor` and surface a
// typed not-ready report instead of failing with an opaque error (or, prior
// to this preflight existing, proceeding into a run that could hang).
func TestCapsulePromoteDoctorCheckReturnsTypedNotReadyForMissingConfig(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, ".capsules", "workspaces", "w")
	instance := control.Instance{ID: "w", State: control.StateReady, Generation: 1, DefinitionDigest: "sha256:def", Head: "sha256:source"}

	report, err := capsulePromoteDoctorCheck(context.Background(), root, "change", instance, workspacePath)
	if err != nil {
		t.Fatalf("doctor check returned an error instead of a typed report: %v", err)
	}
	if report.Ready {
		t.Fatalf("expected a not-ready report for a workspace missing .kitsoki/ci.yaml, got %#v", report)
	}
	if len(report.Checks) == 0 {
		t.Fatal("expected at least one typed check explaining why promote is blocked")
	}
}

func TestCapsulePromoteRetryAfterBusyReusesExactReceiptAndQueueCandidate(t *testing.T) {
	root := t.TempDir()
	sha := strings.Repeat("a", 40)
	instance := control.Instance{ID: "w", Generation: 7, Head: sha}
	opts := capsulePromoteOptions{Pipeline: "change", TargetRef: "main", GateCommand: "node scripts/kitsoki-ci.mjs"}
	stored := promoteReuseStored(t, root, instance, opts.Pipeline, "busy-receipt-run")
	if err := persistPromoteReceiptReuse(root, instance, "agent/retry", opts, stored); err != nil {
		t.Fatal(err)
	}
	runs := 0
	reused, wasReused, err := promoteReceiptForAttempt(context.Background(), root, instance, "agent/retry", opts, func(context.Context) (record.Stored, error) {
		runs++
		return record.Stored{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !wasReused || runs != 0 || reused.Receipt.ReceiptID != stored.Receipt.ReceiptID {
		t.Fatalf("retry did not reuse exact receipt: reused=%t runs=%d got=%#v want=%#v", wasReused, runs, reused.Receipt, stored.Receipt)
	}
	q := queue.Store{ProjectRoot: root}
	first, err := q.Submit(queue.Submit{Branch: "agent/retry", SHA: sha, Receipt: reused.Receipt, ReceiptRef: reused.ReceiptPath, Backend: "local", TargetRef: opts.TargetRef})
	if err != nil {
		t.Fatal(err)
	}
	second, err := q.Submit(queue.Submit{Branch: "agent/retry", SHA: sha, Receipt: reused.Receipt, ReceiptRef: reused.ReceiptPath, Backend: "local", TargetRef: opts.TargetRef})
	if err != nil {
		t.Fatal(err)
	}
	state, err := q.List()
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || len(state.Candidates) != 1 {
		t.Fatalf("retry duplicated candidate: first=%s second=%s candidates=%#v", first.ID, second.ID, state.Candidates)
	}
}

func TestCapsulePromoteReceiptReuseFailsClosedOnRunSubstitution(t *testing.T) {
	root := t.TempDir()
	sha := strings.Repeat("b", 40)
	instance := control.Instance{ID: "w", Generation: 7, Head: sha}
	opts := capsulePromoteOptions{Pipeline: "change", TargetRef: "main", GateCommand: "node scripts/kitsoki-ci.mjs"}
	stored := promoteReuseStored(t, root, instance, opts.Pipeline, "substituted-run")
	if err := persistPromoteReceiptReuse(root, instance, "agent/retry", opts, stored); err != nil {
		t.Fatal(err)
	}
	run, err := (ci.FileRunStore{ProjectRoot: root}).Get(stored.Receipt.JobID)
	if err != nil {
		t.Fatal(err)
	}
	run.Result.Verdict.StoryDigest = "sha256:substituted"
	if err := (ci.FileRunStore{ProjectRoot: root}).Write(run); err != nil {
		t.Fatal(err)
	}
	runs := 0
	_, _, err = promoteReceiptForAttempt(context.Background(), root, instance, "agent/retry", opts, func(context.Context) (record.Stored, error) {
		runs++
		return record.Stored{}, nil
	})
	if err == nil || runs != 0 || !strings.Contains(err.Error(), "reusable receipt provenance") {
		t.Fatalf("substituted run was not fail-closed: err=%v runs=%d", err, runs)
	}
}

func TestCapsulePromoteReceiptReuseDoesNotCrossQueueGate(t *testing.T) {
	root := t.TempDir()
	sha := strings.Repeat("c", 40)
	instance := control.Instance{ID: "w", Generation: 7, Head: sha}
	old := capsulePromoteOptions{Pipeline: "change", TargetRef: "main", GateCommand: "node scripts/old-gate.mjs"}
	stored := promoteReuseStored(t, root, instance, old.Pipeline, "old-gate-run")
	if err := persistPromoteReceiptReuse(root, instance, "agent/retry", old, stored); err != nil {
		t.Fatal(err)
	}
	newOpts := old
	newOpts.GateCommand = "node scripts/new-gate.mjs"
	runs := 0
	got, wasReused, err := promoteReceiptForAttempt(context.Background(), root, instance, "agent/retry", newOpts, func(context.Context) (record.Stored, error) {
		runs++
		return stored, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if wasReused || runs != 1 || got.Receipt.ReceiptID != stored.Receipt.ReceiptID {
		t.Fatalf("receipt crossed a changed queue gate: reused=%t runs=%d got=%#v", wasReused, runs, got.Receipt)
	}
}

func promoteReuseStored(t *testing.T, root string, instance control.Instance, pipeline, jobID string) record.Stored {
	t.Helper()
	lock, err := environment.SealLock(environment.Lock{Schema: environment.LockSchema, ID: "ci", DefinitionDigest: "sha256:env-def", Network: "none", Sandbox: "supervised"})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := executor.Seal(executor.Envelope{JobID: jobID, ProjectID: "test", DefinitionDigest: "sha256:def", Instance: control.Handle{ID: instance.ID, Generation: instance.Generation}, SourceDigest: instance.Head, StoryPath: "stories/ci/app.yaml", StoryDigest: "sha256:story", Environment: lock, Policy: executor.Policy{Network: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	verdict := ci.Verdict{Schema: ci.VerdictSchema, Pipeline: pipeline, Outcome: "passed", Checks: []ci.Check{{ID: "test", Kind: "deterministic", Outcome: "passed", Evidence: []string{"artifact:test"}}}, PromotionEligible: true, SourceDigest: envelope.SourceDigest, StoryDigest: envelope.StoryDigest, EnvironmentDigest: envelope.Environment.Digest, EnvelopeDigest: envelope.Digest}
	run := ci.RunResult{Job: artifactjob.Job{ID: artifactjob.JobID(jobID)}, Envelope: envelope, Verdict: verdict, Execution: executor.Result{VerdictArtifact: "artifact:verdict"}}
	stored, err := record.Persist(root, run)
	if err != nil {
		t.Fatal(err)
	}
	if err := (ci.FileRunStore{ProjectRoot: root}).Write(ci.RunRecord{JobID: jobID, Result: run, ReceiptID: stored.Receipt.ReceiptID, ReceiptVerification: stored.Verification.Status}); err != nil {
		t.Fatal(err)
	}
	return stored
}
