package record

import (
	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"testing"
)

func TestPersistBuildsReceiptAndTrace(t *testing.T) {
	e, _ := executor.Seal(executor.Envelope{JobID: "job", ProjectID: "p", DefinitionDigest: "sha256:def", Instance: control.Handle{ID: "w", Generation: 1}, SourceDigest: "sha256:source", StoryDigest: "sha256:story", Environment: environment.Lock{Schema: environment.LockSchema, ID: "ci", Digest: "sha256:env"}, Policy: executor.Policy{Network: "none"}})
	v := ci.Verdict{Schema: ci.VerdictSchema, Pipeline: "change", Outcome: "passed", Checks: []ci.Check{{ID: "test", Kind: "deterministic", Outcome: "passed", Evidence: []string{"artifact:test"}}}, PromotionEligible: true, SourceDigest: e.SourceDigest, StoryDigest: e.StoryDigest, EnvironmentDigest: e.Environment.Digest, EnvelopeDigest: e.Digest}
	out, err := Persist(t.TempDir(), ci.RunResult{Job: artifactjob.Job{ID: "job"}, Envelope: e, Verdict: v, Execution: executor.Result{VerdictArtifact: "artifact:verdict"}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Verification.Status != "valid" || out.Receipt.ReceiptID == "" {
		t.Fatalf("stored %#v", out)
	}
}
