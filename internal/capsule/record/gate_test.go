package record

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/reconcile"
)

func TestPromotionGateRequiresMatchingVerifiedCandidateReceipt(t *testing.T) {
	root := t.TempDir()
	lock, err := environment.SealLock(environment.Lock{Schema: environment.LockSchema, ID: "ci", DefinitionDigest: "sha256:env-def", Network: "none", Sandbox: "supervised"})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := executor.Seal(executor.Envelope{
		JobID: "job", ProjectID: "p", DefinitionDigest: "sha256:def",
		Instance: control.Handle{ID: "w", Generation: 1}, SourceDigest: "candidate",
		StoryPath: "stories/ci/app.yaml", StoryDigest: "sha256:story", Environment: lock,
		Policy: executor.Policy{Network: "none"},
	})
	if err != nil {
		t.Fatal(err)
	}
	verdict := ci.Verdict{Schema: ci.VerdictSchema, Pipeline: "change", Outcome: "passed", Checks: []ci.Check{{ID: "test", Outcome: "passed", Evidence: []string{"artifact:test"}}}, PromotionEligible: true, SourceDigest: envelope.SourceDigest, StoryDigest: envelope.StoryDigest, EnvironmentDigest: envelope.Environment.Digest, EnvelopeDigest: envelope.Digest}
	result := ci.RunResult{Job: artifactjob.Job{ID: "job"}, Envelope: envelope, Verdict: verdict, Execution: executor.Result{VerdictArtifact: "artifact:verdict"}}
	stored, err := Persist(root, result)
	if err != nil {
		t.Fatal(err)
	}
	if err := (ci.FileRunStore{ProjectRoot: root}).Write(ci.RunRecord{JobID: "job", Result: result, ReceiptID: stored.Receipt.ReceiptID, ReceiptVerification: stored.Verification.Status}); err != nil {
		t.Fatal(err)
	}
	gate := PromotionGate{ProjectRoot: root}
	plan := reconcile.Plan{Candidate: "candidate"}
	if err := gate.Verify(context.Background(), stored.Receipt.ReceiptID, plan); err != nil {
		t.Fatal(err)
	}
	plan.Candidate = "other"
	if err := gate.Verify(context.Background(), stored.Receipt.ReceiptID, plan); err == nil {
		t.Fatal("accepted receipt for another candidate")
	}
	plan = reconcile.Plan{Candidate: "resolved-integration", ReceiptCandidate: "candidate"}
	if err := gate.Verify(context.Background(), stored.Receipt.ReceiptID, plan); err != nil {
		t.Fatalf("accepted integration provenance: %v", err)
	}
}

func TestPromotionGateUsesExplicitExternalEvidenceWithoutProjectCheckoutState(t *testing.T) {
	root := t.TempDir()
	authority := t.TempDir()
	lock, err := environment.SealLock(environment.Lock{Schema: environment.LockSchema, ID: "ci", DefinitionDigest: "sha256:env-def", Network: "none", Sandbox: "supervised"})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := executor.Seal(executor.Envelope{
		JobID: "external-job", ProjectID: "p", DefinitionDigest: "sha256:def",
		Instance: control.Handle{ID: "worker", Generation: 1}, SourceDigest: "candidate",
		StoryPath: "stories/ci/app.yaml", StoryDigest: "sha256:story", Environment: lock,
		Policy: executor.Policy{Network: "none"},
	})
	if err != nil {
		t.Fatal(err)
	}
	verdict := ci.Verdict{Schema: ci.VerdictSchema, Pipeline: "change", Outcome: "passed", Checks: []ci.Check{{ID: "test", Outcome: "passed", Evidence: []string{"artifact:test"}}}, PromotionEligible: true, SourceDigest: envelope.SourceDigest, StoryDigest: envelope.StoryDigest, EnvironmentDigest: envelope.Environment.Digest, EnvelopeDigest: envelope.Digest}
	result := ci.RunResult{Job: artifactjob.Job{ID: "external-job"}, Envelope: envelope, Verdict: verdict}
	stored, err := Persist(authority, result)
	if err != nil {
		t.Fatal(err)
	}
	run := ci.RunRecord{JobID: "external-job", Result: result, ReceiptID: stored.Receipt.ReceiptID, ReceiptVerification: stored.Verification.Status}
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	runPath := filepath.Join(authority, "external-job.run.json")
	if err := os.WriteFile(runPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".capsules", "ci")); !os.IsNotExist(err) {
		t.Fatalf("project checkout unexpectedly has local CI evidence: %v", err)
	}

	gate := PromotionGate{ProjectRoot: root, ReceiptRef: stored.ReceiptPath, RunRecordRef: runPath}
	if err := gate.Verify(context.Background(), stored.Receipt.ReceiptID, reconcile.Plan{Candidate: "candidate"}); err != nil {
		t.Fatalf("external gate evidence: %v", err)
	}

	run.Result.Envelope.SourceDigest = "substituted"
	raw, err = json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := gate.Verify(context.Background(), stored.Receipt.ReceiptID, reconcile.Plan{Candidate: "candidate"}); err == nil {
		t.Fatal("accepted substituted external run record")
	}
}
