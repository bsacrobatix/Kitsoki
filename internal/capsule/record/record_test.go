package record

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/receipt"
	"kitsoki/internal/capsule/reconcile"
	capsuletrace "kitsoki/internal/capsule/trace"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testLauncher func(context.Context, executor.Prepared) (ci.Verdict, error)

func (f testLauncher) Launch(ctx context.Context, prepared executor.Prepared) (ci.Verdict, error) {
	return f(ctx, prepared)
}

func TestPersistBuildsReceiptAndTrace(t *testing.T) {
	out, err := Persist(t.TempDir(), validRunResult("job", "sha256:source"))
	if err != nil {
		t.Fatal(err)
	}
	if out.Verification.Status != "valid" || out.Receipt.ReceiptID == "" {
		t.Fatalf("stored %#v", out)
	}
}

func TestPersistTraceIncludesLifecycleEnvironmentAndPolicyFacts(t *testing.T) {
	run := validRunResult("job", "sha256:source")
	run.Execution.ExecutionID = "exec-1"
	run.Events = []executor.Event{
		{Kind: capsuletrace.KindExecutorStarted, EnvelopeDigest: run.Envelope.Digest, ExecutionID: "exec-1", Fields: map[string]any{"transport": "https", "remote_host": "worker.example"}},
		{Kind: capsuletrace.KindExecutorFinished, EnvelopeDigest: run.Envelope.Digest, ExecutionID: "exec-1", Fields: map[string]any{"transport": "https", "remote_host": "worker.example"}},
	}
	run.Envelope.Environment.CacheKeys = []string{"project:runstatus"}
	run.Envelope.Environment.SecretRequired = true
	run.Envelope.Policy = executor.Policy{Network: "replay", MinimumSandbox: "workspace", ExternalWrite: "deny"}
	sealed, err := executor.Seal(run.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	run.Envelope = sealed
	run.Verdict.SourceDigest = sealed.SourceDigest
	run.Verdict.StoryDigest = sealed.StoryDigest
	run.Verdict.EnvironmentDigest = sealed.Environment.Digest
	run.Verdict.EnvelopeDigest = sealed.Digest
	for i := range run.Events {
		run.Events[i].EnvelopeDigest = sealed.Digest
	}

	out, err := Persist(t.TempDir(), run)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out.TracePath)
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimSpace(string(raw))
	sum := sha256.Sum256([]byte(trimmed))
	if out.Receipt.TraceDigest != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("trace digest mismatch receipt=%s raw=%s", out.Receipt.TraceDigest, trimmed)
	}
	var doc capsuletrace.Document
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		t.Fatal(err)
	}
	if err := capsuletrace.ValidateDocument(doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Events) != 7 {
		t.Fatalf("events %#v", doc.Events)
	}
	wantKinds := []string{
		capsuletrace.KindWorkspaceReady,
		capsuletrace.KindEnvironmentResolved,
		capsuletrace.KindExecutorPrepared,
		capsuletrace.KindCIStarted,
		capsuletrace.KindExecutorStarted,
		capsuletrace.KindExecutorFinished,
		capsuletrace.KindCIVerdict,
	}
	for i, want := range wantKinds {
		if doc.Events[i].Kind != want {
			t.Fatalf("event %d kind=%s want=%s events=%#v", i, doc.Events[i].Kind, want, doc.Events)
		}
	}
	env := doc.Events[1].Fields
	if env["environment_digest"] != sealed.Environment.Digest || env["environment_private_inputs"] != true {
		t.Fatalf("environment fields %#v", env)
	}
	policy := doc.Events[2].Fields
	if policy["network"] != "replay" || policy["minimum_sandbox"] != "workspace" || policy["external_write"] != "deny" {
		t.Fatalf("policy fields %#v", policy)
	}
	started := doc.Events[4].Fields
	if started["execution_id"] != "exec-1" || started["remote_host"] != "worker.example" {
		t.Fatalf("executor event fields %#v", started)
	}
	if strings.Contains(trimmed, "/Users/") || strings.Contains(strings.ToLower(trimmed), "secret_required") {
		t.Fatalf("trace leaked unsafe content: %s", trimmed)
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

func TestPromotionGateRejectsAcceptedAttemptSubstitution(t *testing.T) {
	root := t.TempDir()
	run := validRunResult("job", "sha256:candidate")
	stored, err := Persist(root, run)
	if err != nil {
		t.Fatal(err)
	}
	substituted := validRunResult("job", "sha256:candidate")
	substituted.Envelope.StoryDigest = "sha256:other-story"
	substituted.Verdict.StoryDigest = "sha256:other-story"
	if err := (ci.FileRunStore{ProjectRoot: root}).Write(ci.RunRecord{JobID: "job", Result: substituted, ReceiptID: stored.Receipt.ReceiptID, ReceiptVerification: "valid"}); err != nil {
		t.Fatal(err)
	}
	plan := reconcile.Plan{Candidate: "sha256:candidate"}
	if err := (PromotionGate{ProjectRoot: root}).Verify(nil, stored.Receipt.ReceiptID, plan); err == nil || !strings.Contains(err.Error(), "run record does not match receipt") {
		t.Fatalf("accepted substituted run record: %v", err)
	}
}

func TestLocalAndFakeRemoteReceiptsAuthorizePromotionPlan(t *testing.T) {
	for _, tc := range []struct {
		name     string
		executor string
	}{
		{name: "host"},
		{name: "fake-remote", executor: "remote-fake"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeReceiptPolicyProjectWithExecutor(t, root, false, tc.executor)
			service := ci.Service{
				ProjectRoot: root,
				Jobs:        artifactjob.NewMemoryStore(),
				Env:         environment.Resolver{Probe: environment.ToolProbeFunc(func(context.Context, string) (string, error) { return "go1.25", nil })},
				Executors:   ci.NewBuiltinExecutors(),
				Launcher: testLauncher(func(_ context.Context, prepared executor.Prepared) (ci.Verdict, error) {
					return ci.Verdict{Schema: ci.VerdictSchema, Pipeline: "change", Outcome: "passed", Summary: "promotion fixture", Checks: []ci.Check{{ID: "tests", Kind: "deterministic", Outcome: "passed", Evidence: []string{"artifact:tests"}}}, PromotionEligible: true, SourceDigest: prepared.Envelope.SourceDigest, StoryDigest: prepared.Envelope.StoryDigest, EnvironmentDigest: prepared.Envelope.Environment.Digest, EnvelopeDigest: prepared.Envelope.Digest}, nil
				}),
			}
			run, err := service.Run(context.Background(), ci.RunRequest{Pipeline: "change", Workspace: control.Handle{ID: "w", Generation: 1}, DefinitionDigest: "sha256:def", SourceDigest: "sha256:candidate", StoryDigest: "sha256:story", Trigger: ci.Trigger{Kind: "local"}})
			if err != nil {
				t.Fatal(err)
			}
			stored, err := Persist(root, run)
			if err != nil {
				t.Fatal(err)
			}
			if err := (ci.FileRunStore{ProjectRoot: root}).Write(ci.RunRecord{JobID: string(run.Job.ID), Result: run, ReceiptID: stored.Receipt.ReceiptID, ReceiptVerification: stored.Verification.Status}); err != nil {
				t.Fatal(err)
			}
			plan := reconcile.Plan{Candidate: "sha256:candidate"}
			if err := (PromotionGate{ProjectRoot: root}).Verify(context.Background(), stored.Receipt.ReceiptID, plan); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func validRunResult(jobID, sourceDigest string) ci.RunResult {
	e, _ := executor.Seal(executor.Envelope{JobID: jobID, ProjectID: "p", DefinitionDigest: "sha256:def", Instance: control.Handle{ID: "w", Generation: 1}, SourceDigest: sourceDigest, StoryDigest: "sha256:story", Environment: environment.Lock{Schema: environment.LockSchema, ID: "ci", Digest: "sha256:env"}, Policy: executor.Policy{Network: "none"}})
	v := ci.Verdict{Schema: ci.VerdictSchema, Pipeline: "change", Outcome: "passed", Checks: []ci.Check{{ID: "test", Kind: "deterministic", Outcome: "passed", Evidence: []string{"artifact:test"}}}, PromotionEligible: true, SourceDigest: e.SourceDigest, StoryDigest: e.StoryDigest, EnvironmentDigest: e.Environment.Digest, EnvelopeDigest: e.Digest}
	return ci.RunResult{Job: artifactjob.Job{ID: artifactjob.JobID(jobID)}, Envelope: e, Verdict: v, Execution: executor.Result{VerdictArtifact: "artifact:verdict"}}
}

func writeReceiptPolicyProject(t *testing.T, root string, require bool) {
	t.Helper()
	writeReceiptPolicyProjectWithExecutor(t, root, require, "")
}

func writeReceiptPolicyProjectWithExecutor(t *testing.T, root string, require bool, executor string) {
	t.Helper()
	executorLine := ""
	if executor != "" {
		executorLine = "    executor: " + executor + "\n"
	}
	files := map[string]string{
		".kitsoki/environments/ci.yaml": "schema: capsule-environment/v1\nid: ci\nsource:\n  host_probe: true\n",
		".kitsoki/stories/ci/app.yaml":  "app:\n  id: ci\nrooms:\n  idle:\n    view: ok\n",
		".kitsoki/ci.yaml":              "schema: capsule-ci/v1\ndefault_environment: ci\npipelines:\n  change:\n    story: .kitsoki/stories/ci/app.yaml\n    triggers: [local]\n" + executorLine + "    result:\n      schema: capsule-ci-verdict/v1\n",
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
