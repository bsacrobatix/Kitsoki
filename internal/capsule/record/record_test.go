package record

import (
	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/receipt"
	"kitsoki/internal/capsule/reconcile"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersistBuildsReceiptAndTrace(t *testing.T) {
	out, err := Persist(t.TempDir(), validRunResult("job", "sha256:source"))
	if err != nil {
		t.Fatal(err)
	}
	if out.Verification.Status != "valid" || out.Receipt.ReceiptID == "" {
		t.Fatalf("stored %#v", out)
	}
}

func TestPersistSignsWhenProjectPolicyRequiresIt(t *testing.T) {
	root := t.TempDir()
	writeReceiptPolicyProject(t, root, true)
	out, err := PersistWithOptions(root, validRunResult("job", "sha256:source"), PersistOptions{Signer: receipt.FakeSigner{ID: "test-signer"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Receipt.Integrity.Signer != "test-signer" || out.Receipt.Integrity.Signature == "" {
		t.Fatalf("receipt was not signed %#v", out.Receipt.Integrity)
	}
	if out.Verification.Status != "valid" || !out.Verification.PromotionEligible {
		t.Fatalf("verification %#v", out.Verification)
	}
}

func TestPersistRejectsMissingRequiredSigner(t *testing.T) {
	root := t.TempDir()
	writeReceiptPolicyProject(t, root, true)
	if _, err := Persist(root, validRunResult("job", "sha256:source")); err == nil || !strings.Contains(err.Error(), "signature required") {
		t.Fatalf("expected signature-required error, got %v", err)
	}
}

func TestPromotionGateEnforcesReceiptSignaturePolicy(t *testing.T) {
	root := t.TempDir()
	writeReceiptPolicyProject(t, root, true)
	run := validRunResult("job", "sha256:candidate")
	stored, err := PersistWithOptions(root, run, PersistOptions{Signer: receipt.FakeSigner{ID: "test-signer"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := (ci.FileRunStore{ProjectRoot: root}).Write(ci.RunRecord{JobID: "job", Result: run, ReceiptID: stored.Receipt.ReceiptID, ReceiptVerification: stored.Verification.Status}); err != nil {
		t.Fatal(err)
	}
	plan := reconcile.Plan{Candidate: "sha256:candidate"}
	if err := (PromotionGate{ProjectRoot: root}).Verify(nil, stored.Receipt.ReceiptID, plan); err == nil {
		t.Fatal("unsigned verifier accepted signed-required policy without signer")
	}
	if err := (PromotionGate{ProjectRoot: root, Signer: receipt.FakeSigner{ID: "test-signer"}}).Verify(nil, stored.Receipt.ReceiptID, plan); err != nil {
		t.Fatal(err)
	}
	if err := (PromotionGate{ProjectRoot: root, Signer: receipt.FakeSigner{ID: "wrong"}}).Verify(nil, stored.Receipt.ReceiptID, plan); err == nil {
		t.Fatal("wrong signer accepted")
	}
}

func validRunResult(jobID, sourceDigest string) ci.RunResult {
	e, _ := executor.Seal(executor.Envelope{JobID: jobID, ProjectID: "p", DefinitionDigest: "sha256:def", Instance: control.Handle{ID: "w", Generation: 1}, SourceDigest: sourceDigest, StoryDigest: "sha256:story", Environment: environment.Lock{Schema: environment.LockSchema, ID: "ci", Digest: "sha256:env"}, Policy: executor.Policy{Network: "none"}})
	v := ci.Verdict{Schema: ci.VerdictSchema, Pipeline: "change", Outcome: "passed", Checks: []ci.Check{{ID: "test", Kind: "deterministic", Outcome: "passed", Evidence: []string{"artifact:test"}}}, PromotionEligible: true, SourceDigest: e.SourceDigest, StoryDigest: e.StoryDigest, EnvironmentDigest: e.Environment.Digest, EnvelopeDigest: e.Digest}
	return ci.RunResult{Job: artifactjob.Job{ID: artifactjob.JobID(jobID)}, Envelope: e, Verdict: v, Execution: executor.Result{VerdictArtifact: "artifact:verdict"}}
}

func writeReceiptPolicyProject(t *testing.T, root string, require bool) {
	t.Helper()
	files := map[string]string{
		".kitsoki/environments/ci.yaml": "schema: capsule-environment/v1\nid: ci\nsource:\n  host_probe: true\n",
		".kitsoki/stories/ci/app.yaml":  "app:\n  id: ci\nrooms:\n  idle:\n    view: ok\n",
		".kitsoki/ci.yaml":              "schema: capsule-ci/v1\ndefault_environment: ci\npipelines:\n  change:\n    story: .kitsoki/stories/ci/app.yaml\n    triggers: [local]\n    result:\n      schema: capsule-ci-verdict/v1\n",
	}
	if require {
		files[".kitsoki/ci.yaml"] += "receipt:\n  require_signature: true\n  signer: test-signer\n"
	}
	for path, raw := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
