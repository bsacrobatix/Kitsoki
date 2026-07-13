package queue

import (
	"strings"
	"testing"
	"time"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/receipt"
)

func TestSubmitPersistsReceiptBoundCandidateAndIsIdempotent(t *testing.T) {
	sha := strings.Repeat("a", 40)
	store := Store{ProjectRoot: t.TempDir()}
	in := Submit{Branch: "agent-a", SHA: sha, Receipt: testReceipt(t, sha), Paths: []string{"b", "a"}, Now: time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)}
	first, err := store.Submit(in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Submit(in)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("not idempotent: %#v %#v", first, second)
	}
	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Candidates) != 1 || state.Candidates[0].Paths[0] != "a" {
		t.Fatalf("state=%#v", state)
	}
}
func TestSubmitRejectsReceiptForAnotherCandidate(t *testing.T) {
	_, err := (Store{ProjectRoot: t.TempDir()}).Submit(Submit{Branch: "agent-a", SHA: strings.Repeat("a", 40), Receipt: testReceipt(t, strings.Repeat("b", 40))})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err=%v", err)
	}
}
func testReceipt(t *testing.T, sha string) receipt.Receipt {
	t.Helper()
	lock, err := environment.SealLock(environment.Lock{Schema: environment.LockSchema, ID: "ci", DefinitionDigest: "definition", Toolchains: map[string]string{}, Network: "none", Sandbox: "process"})
	if err != nil {
		t.Fatal(err)
	}
	env, err := executor.Seal(executor.Envelope{JobID: "job", ProjectID: "project", DefinitionDigest: "definition", Instance: control.Handle{ID: "workspace", Generation: 1}, SourceDigest: sha, StoryPath: "story", StoryDigest: "story-digest", Environment: lock})
	if err != nil {
		t.Fatal(err)
	}
	verdict := ci.Verdict{Schema: ci.VerdictSchema, Pipeline: "test", Outcome: "passed", SourceDigest: sha, StoryDigest: env.StoryDigest, EnvironmentDigest: env.Environment.Digest, EnvelopeDigest: env.Digest}
	verdict = ci.NormalizeVerdict(verdict)
	r, v, err := receipt.Build(receipt.BuildInput{Job: artifactjob.Job{ID: "job"}, Envelope: env, Verdict: verdict, TraceDigest: "trace"})
	if err != nil || v.Status != "valid" {
		t.Fatalf("receipt %v %#v", err, v)
	}
	return r
}
