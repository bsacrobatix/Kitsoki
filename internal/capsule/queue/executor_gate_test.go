package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/executor"
)

// xgWriteFixtures writes and commits a minimal but real capsule CI pipeline
// (a "change" pipeline with a hermetic no-host-probe environment and a
// trivial story) into root, so ci.Load/storydigest.Compute exercise the real
// production code paths ExecutorGate drives through, exactly as
// internal/capsule/ci's own tests do (see ci_test.go's requireFiles).
func xgWriteFixtures(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		".kitsoki/ci.yaml": "schema: capsule-ci/v1\n" +
			"default_environment: xg\n" +
			"pipelines:\n" +
			"  change:\n" +
			"    story: .kitsoki/stories/xg/app.yaml\n" +
			"    triggers: [local]\n" +
			"    result:\n" +
			"      schema: capsule-ci-verdict/v1\n",
		// No source/toolchains: environment.Resolver.Resolve never invokes a
		// host probe for this definition, so the fixture is fully hermetic.
		".kitsoki/environments/xg.yaml": "schema: capsule-environment/v1\nid: xg\nsandbox: supervised\n",
		".kitsoki/stories/xg/app.yaml": "app:\n  id: xg\n  version: 0.1.0\n  title: XG\n  author: test\n  license: CC0\n" +
			"intents:\n  look:\n    description: Look\n    examples: [look]\n" +
			"root: idle\nstates:\n  idle:\n    view:\n      - prose: ok\n    on:\n      look:\n        - target: idle\n",
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
	git(t, root, "add",
		".kitsoki/ci.yaml",
		".kitsoki/environments/xg.yaml",
		".kitsoki/stories/xg/app.yaml",
	)
	git(t, root, "commit", "-m", "xg fixtures")
}

// xgSetupRepo builds a plain git repo (no queue speculative-workspace
// lifecycle) carrying the xg fixtures, for tests that call ExecutorGate.Run
// directly against a hand-built Speculation.
func xgSetupRepo(t *testing.T) (root, head string) {
	t.Helper()
	parallelQueueIntegrationTest(t)
	root = t.TempDir()
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.name", "Queue Test")
	git(t, root, "config", "user.email", "queue@example.invalid")
	commit(t, root, "base.txt", "base\n", "base")
	xgWriteFixtures(t, root)
	head = git(t, root, "rev-parse", "HEAD")
	return root, head
}

func xgCloneAt(t *testing.T, root, sha string) string {
	t.Helper()
	clone := filepath.Join(t.TempDir(), "speculative")
	run(t, root, "git", "clone", "--shared", "--no-checkout", root, clone)
	git(t, clone, "checkout", "--detach", sha)
	return clone
}

func xgSetStagingTarget(t *testing.T, root, sha string) {
	t.Helper()
	old := git(t, root, "rev-parse", "staging/local")
	git(t, root, "update-ref", "refs/heads/staging/local", sha, old)
	staging := filepath.Join(root, ".capsules", "staging", "local")
	git(t, staging, "fetch", "source", "staging/local")
	git(t, staging, "reset", "--hard", "FETCH_HEAD")
}

func xgCapabilities() executor.Capabilities {
	return executor.Capabilities{ID: "xg-stub", Placements: []string{"xg-stub"}, Isolation: "supervised", Networks: []string{"none"}, Cancellable: false}
}

// xgProvider is a hermetic executor.Provider double standing in for a real
// remote worker: like the production HTTPRemoteWorker, it ignores the task
// callback entirely and returns its own scripted Result (a real remote
// worker owns story execution itself; the queue's local task callback is
// never invoked for a genuinely remote dispatch).
type xgProvider struct {
	cap     executor.Capabilities
	verdict *ci.Verdict
	runErr  error
	onRun   func(executor.Prepared)
}

func (p xgProvider) Describe(context.Context) (executor.Capabilities, error) { return p.cap, nil }

func (p xgProvider) Prepare(_ context.Context, e executor.Envelope) (executor.Prepared, error) {
	sealed, err := executor.Seal(e)
	if err != nil {
		return executor.Prepared{}, err
	}
	if err := executor.ValidateCapabilities(p.cap, sealed.Policy); err != nil {
		return executor.Prepared{}, err
	}
	return executor.Prepared{ID: "xg-prepared-" + sealed.Digest[len(sealed.Digest)-8:], Envelope: sealed, Placement: "xg-stub", Applied: sealed.Policy}, nil
}

func (p xgProvider) Run(_ context.Context, prepared executor.Prepared, _ executor.Task, _ executor.EventSink) (executor.Result, error) {
	if p.onRun != nil {
		p.onRun(prepared)
	}
	if p.runErr != nil {
		return executor.Result{}, p.runErr
	}
	v := *p.verdict
	v.SourceDigest = prepared.Envelope.SourceDigest
	v.StoryDigest = prepared.Envelope.StoryDigest
	v.EnvironmentDigest = prepared.Envelope.Environment.Digest
	v.EnvelopeDigest = prepared.Envelope.Digest
	v = ci.NormalizeVerdict(v)
	raw, err := json.Marshal(v)
	if err != nil {
		return executor.Result{}, err
	}
	return executor.Result{VerdictArtifact: "verdict:xg", VerdictJSON: raw}, nil
}

func (p xgProvider) Cancel(context.Context, string) error { return nil }

var _ executor.Provider = xgProvider{}

// xgSelector is a stub ci.ExecutorSelector: it resolves exactly one executor
// name to provider, or returns err unconditionally (simulating an
// unconfigured/misnamed executor).
func xgSelector(name string, provider executor.Provider, err error) ci.ExecutorSelector {
	return ci.ExecutorSelectorFunc(func(_ context.Context, got string) (executor.Provider, error) {
		if err != nil {
			return nil, err
		}
		if got != name {
			return nil, fmt.Errorf("xg selector: executor %q is not configured", got)
		}
		return provider, nil
	})
}

func xgPassingVerdict() ci.Verdict {
	return ci.Verdict{Schema: ci.VerdictSchema, Pipeline: "change", Outcome: "passed", Summary: "all green", Checks: []ci.Check{{ID: "xg-check", Kind: "deterministic", Outcome: "passed", Evidence: []string{"artifact:xg"}}}}
}

func TestXgExecutorGatePassesOnGreenVerdict(t *testing.T) {
	root, head := xgSetupRepo(t)
	verdict := xgPassingVerdict()
	provider := xgProvider{cap: xgCapabilities(), verdict: &verdict}
	gate := ExecutorGate{ProjectRoot: root, TargetRef: "main", Executor: "xg-remote", Pipeline: "change", Selector: xgSelector("xg-remote", provider, nil)}

	result, err := gate.Run(context.Background(), Speculation{WorkspacePath: root, SHA: head, BaseSHA: head})
	if err != nil {
		t.Fatalf("err=%v result=%#v", err, result)
	}
	if !result.Passed {
		t.Fatalf("result=%#v", result)
	}
	if want := "executor-gate/v2:xg-remote:change:" + head; result.GateVersion != want {
		t.Fatalf("gate version=%q, want %q", result.GateVersion, want)
	}
	if len(result.Evidence) == 0 {
		t.Fatal("expected non-empty evidence")
	}
	found := false
	for _, line := range result.Evidence {
		if strings.Contains(line, "pipeline=change") && strings.Contains(line, "executor=xg-remote") && strings.Contains(line, "outcome=passed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("evidence missing dispatch summary: %#v", result.Evidence)
	}
}

func TestXgExecutorGateRequiresPolicyBase(t *testing.T) {
	root, head := xgSetupRepo(t)
	gate := ExecutorGate{ProjectRoot: root, TargetRef: "main", Executor: "xg-remote", Pipeline: "change"}
	if _, err := gate.Run(context.Background(), Speculation{WorkspacePath: root, SHA: head}); err == nil ||
		!strings.Contains(err.Error(), "requires a speculative policy base") {
		t.Fatalf("err=%v", err)
	}
}

func TestXgExecutorGateIgnoresDirtyPrimaryPolicyFiles(t *testing.T) {
	root, head := xgSetupRepo(t)
	specWorkspace := xgCloneAt(t, root, head)
	if err := os.WriteFile(filepath.Join(root, ".kitsoki", "ci.yaml"), []byte("not: [valid\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".kitsoki", "environments", "xg.yaml"), []byte("also: [invalid\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	verdict := xgPassingVerdict()
	provider := xgProvider{cap: xgCapabilities(), verdict: &verdict}
	gate := ExecutorGate{ProjectRoot: root, TargetRef: "main", Executor: "xg-remote", Pipeline: "change", Selector: xgSelector("xg-remote", provider, nil)}
	result, err := gate.Run(context.Background(), Speculation{WorkspacePath: specWorkspace, SHA: head, BaseSHA: head})
	if err != nil || !result.Passed {
		t.Fatalf("dirty primary policy leaked into executor gate: result=%#v err=%v", result, err)
	}
}

func TestXgExecutorGateCandidatePolicyActivatesOnlyAfterBaseAdvances(t *testing.T) {
	root, base := xgSetupRepo(t)
	ciPath := filepath.Join(root, ".kitsoki", "ci.yaml")
	raw, err := os.ReadFile(ciPath)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte(
		"  future:\n"+
			"    story: .kitsoki/stories/xg/app.yaml\n"+
			"    triggers: [local]\n"+
			"    result:\n"+
			"      schema: capsule-ci-verdict/v1\n")...)
	if err := os.WriteFile(ciPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".kitsoki/ci.yaml")
	git(t, root, "commit", "-m", "candidate adds future pipeline")
	candidate := git(t, root, "rev-parse", "HEAD")

	verdict := xgPassingVerdict()
	verdict.Pipeline = "future"
	provider := xgProvider{cap: xgCapabilities(), verdict: &verdict}
	gate := ExecutorGate{ProjectRoot: root, TargetRef: "main", Executor: "xg-remote", Pipeline: "future", Selector: xgSelector("xg-remote", provider, nil)}
	spec := Speculation{WorkspacePath: root, SHA: candidate, BaseSHA: base}
	if _, err := gate.Run(context.Background(), spec); err == nil || !strings.Contains(err.Error(), `pipeline "future" is not declared`) {
		t.Fatalf("candidate policy activated before protected base advanced: %v", err)
	}
	spec.BaseSHA = candidate
	result, err := gate.Run(context.Background(), spec)
	if err != nil || !result.Passed {
		t.Fatalf("candidate policy did not activate at new base: result=%#v err=%v", result, err)
	}
	if want := "executor-gate/v2:xg-remote:future:" + candidate; result.GateVersion != want {
		t.Fatalf("gate version=%q want=%q", result.GateVersion, want)
	}
}

func TestXgExecutorGateFailsOnRedVerdict(t *testing.T) {
	root, head := xgSetupRepo(t)
	verdict := ci.Verdict{Schema: ci.VerdictSchema, Pipeline: "change", Outcome: "failed", Summary: "tests failed", Checks: []ci.Check{{ID: "xg-check", Kind: "deterministic", Outcome: "failed"}}}
	provider := xgProvider{cap: xgCapabilities(), verdict: &verdict}
	gate := ExecutorGate{ProjectRoot: root, TargetRef: "main", Executor: "xg-remote", Pipeline: "change", Selector: xgSelector("xg-remote", provider, nil)}

	result, err := gate.Run(context.Background(), Speculation{WorkspacePath: root, SHA: head, BaseSHA: head})
	if err == nil {
		t.Fatal("expected error for a red verdict")
	}
	if result.Passed {
		t.Fatalf("result=%#v", result)
	}
	var harness HarnessError
	var envErr EnvError
	if errors.As(err, &harness) {
		t.Fatalf("a genuinely-ran red verdict must not be a harness failure: %v", err)
	}
	if errors.As(err, &envErr) {
		t.Fatalf("a genuinely-ran red verdict must not burn the environmental budget: %v", err)
	}
}

func TestXgExecutorGateTransportErrorIsEnvironmental(t *testing.T) {
	root, head := xgSetupRepo(t)
	provider := xgProvider{cap: xgCapabilities(), runErr: fmt.Errorf("capsule executor: remote POST /v1/capsules/run failed kind=transport cause=dial tcp: connection refused")}
	gate := ExecutorGate{ProjectRoot: root, TargetRef: "main", Executor: "xg-remote", Pipeline: "change", Selector: xgSelector("xg-remote", provider, nil)}

	result, err := gate.Run(context.Background(), Speculation{WorkspacePath: root, SHA: head, BaseSHA: head})
	if err == nil {
		t.Fatal("expected error")
	}
	if result.Passed {
		t.Fatalf("result=%#v", result)
	}
	var envErr EnvError
	if !errors.As(err, &envErr) {
		t.Fatalf("expected EnvError classification, got %v", err)
	}
	var harness HarnessError
	if errors.As(err, &harness) {
		t.Fatalf("transport failure must not be classified as a harness failure: %v", err)
	}
}

func TestXgExecutorGateInfraFailedOutcomeIsEnvironmental(t *testing.T) {
	root, head := xgSetupRepo(t)
	verdict := ci.Verdict{Schema: ci.VerdictSchema, Pipeline: "change", Outcome: "infra_failed", Summary: "worker crashed mid-run"}
	provider := xgProvider{cap: xgCapabilities(), verdict: &verdict}
	gate := ExecutorGate{ProjectRoot: root, TargetRef: "main", Executor: "xg-remote", Pipeline: "change", Selector: xgSelector("xg-remote", provider, nil)}

	result, err := gate.Run(context.Background(), Speculation{WorkspacePath: root, SHA: head, BaseSHA: head})
	if err == nil {
		t.Fatal("expected error")
	}
	if result.Passed {
		t.Fatalf("result=%#v", result)
	}
	var envErr EnvError
	if !errors.As(err, &envErr) {
		t.Fatalf("expected EnvError classification for infra_failed outcome, got %v", err)
	}
}

func TestXgExecutorGateUnknownExecutorIsHarness(t *testing.T) {
	root, head := xgSetupRepo(t)
	gate := ExecutorGate{ProjectRoot: root, TargetRef: "main", Executor: "does-not-exist", Pipeline: "change", Selector: xgSelector("xg-remote", xgProvider{}, nil)}

	_, err := gate.Run(context.Background(), Speculation{WorkspacePath: root, SHA: head, BaseSHA: head})
	if err == nil {
		t.Fatal("expected error")
	}
	var harness HarnessError
	if !errors.As(err, &harness) {
		t.Fatalf("expected HarnessError classification, got %v", err)
	}
}

func TestXgExecutorGateUnknownPipelineIsHarness(t *testing.T) {
	root, head := xgSetupRepo(t)
	gate := ExecutorGate{ProjectRoot: root, TargetRef: "main", Executor: "xg-remote", Pipeline: "does-not-exist", Selector: xgSelector("xg-remote", xgProvider{}, nil)}

	_, err := gate.Run(context.Background(), Speculation{WorkspacePath: root, SHA: head, BaseSHA: head})
	if err == nil {
		t.Fatal("expected error")
	}
	var harness HarnessError
	if !errors.As(err, &harness) {
		t.Fatalf("expected HarnessError classification, got %v", err)
	}
}

func TestXgExecutorGateRejectsDirtySpeculativeWorkspace(t *testing.T) {
	root, head := xgSetupRepo(t)
	if err := os.WriteFile(filepath.Join(root, "leaked.txt"), []byte("leak\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gate := ExecutorGate{ProjectRoot: root, TargetRef: "main", Executor: "xg-remote", Pipeline: "change", Selector: xgSelector("xg-remote", xgProvider{}, nil)}

	_, err := gate.Run(context.Background(), Speculation{WorkspacePath: root, SHA: head, BaseSHA: head})
	if err == nil || !strings.Contains(err.Error(), "clean speculative workspace") {
		t.Fatalf("err=%v", err)
	}
	var harness HarnessError
	var envErr EnvError
	if errors.As(err, &harness) || errors.As(err, &envErr) {
		t.Fatalf("dirty-workspace refusal should be a plain error like ShellGate's, got %v", err)
	}
}

func TestXgExecutorGateDetectsHeadMoved(t *testing.T) {
	root, head := xgSetupRepo(t)
	verdict := xgPassingVerdict()
	provider := xgProvider{cap: xgCapabilities(), verdict: &verdict, onRun: func(executor.Prepared) {
		commit(t, root, "moved.txt", "moved\n", "moved head during dispatch")
	}}
	gate := ExecutorGate{ProjectRoot: root, TargetRef: "main", Executor: "xg-remote", Pipeline: "change", Selector: xgSelector("xg-remote", provider, nil)}

	_, err := gate.Run(context.Background(), Speculation{WorkspacePath: root, SHA: head, BaseSHA: head})
	if err == nil || !strings.Contains(err.Error(), "moved speculative HEAD") {
		t.Fatalf("err=%v", err)
	}
}

// TestXgExecutorGateWiringLandsCandidateOnPassingRemoteVerdict proves
// ExecutorGate works as queue.Gate through the real Store.Process/Worker
// machinery (claim, speculate, gate, finalize), not just in isolation: a
// receipt-admitted candidate lands on staging/local once the stubbed remote
// executor reports a passing verdict for the exact speculative tree.
func TestXgExecutorGateWiringLandsCandidateOnPassingRemoteVerdict(t *testing.T) {
	root := protectedQueueRepo(t)
	xgWriteFixtures(t, root)
	base := git(t, root, "rev-parse", "HEAD")
	xgSetStagingTarget(t, root, base)
	commit(t, root, "candidate.txt", "candidate\n", "candidate")
	sha := git(t, root, "rev-parse", "HEAD")
	git(t, root, "branch", "agent/candidate", sha)
	git(t, root, "reset", "--hard", base)

	store := Store{ProjectRoot: root}
	if _, err := store.Submit(Submit{Branch: "agent/candidate", SHA: sha, Receipt: testReceipt(t, sha), TargetRef: "staging/local"}); err != nil {
		t.Fatal(err)
	}

	verdict := xgPassingVerdict()
	provider := xgProvider{cap: xgCapabilities(), verdict: &verdict}
	gate := ExecutorGate{ProjectRoot: root, TargetRef: "staging/local", Executor: "xg-remote", Pipeline: "change", Selector: xgSelector("xg-remote", provider, nil)}

	state, err := store.Process(context.Background(), ProcessDeps{
		Integration: StagingIntegration{ProjectRoot: root, GateCommand: "git diff --check"},
		Gate:        gate,
		TargetRef:   "staging/local",
		GateTier:    "change",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Candidates) != 1 || state.Candidates[0].Status != Landed {
		t.Fatalf("queue state=%#v", state.Candidates)
	}
	if got := git(t, root, "rev-parse", "staging/local"); got != sha {
		t.Fatalf("staging=%s, want candidate %s", got, sha)
	}
	if !strings.Contains(state.Candidates[0].GateVersion, "executor-gate/v2") {
		t.Fatalf("gate version=%q", state.Candidates[0].GateVersion)
	}
}

type xgCountingMemo struct {
	lookups int
	stores  int
}

func (m *xgCountingMemo) Lookup(string, string, string) (GateResult, bool) {
	m.lookups++
	return GateResult{Passed: true}, true
}

func (m *xgCountingMemo) Store(string, string, string, GateResult) error {
	m.stores++
	return nil
}

func TestXgExecutorGateTargetMoveForcesRegateAndNeverUsesMemo(t *testing.T) {
	root := protectedQueueRepo(t)
	xgWriteFixtures(t, root)
	base := git(t, root, "rev-parse", "HEAD")
	xgSetStagingTarget(t, root, base)
	commit(t, root, "candidate-regate.txt", "candidate\n", "candidate")
	sha := git(t, root, "rev-parse", "HEAD")
	git(t, root, "branch", "agent/regate", sha)
	git(t, root, "reset", "--hard", base)

	store := Store{ProjectRoot: root}
	submitted, err := store.Submit(Submit{
		Branch: "agent/regate", SHA: sha, Receipt: testReceipt(t, sha), TargetRef: "staging/local",
	})
	if err != nil {
		t.Fatal(err)
	}
	runs := 0
	verdict := xgPassingVerdict()
	provider := xgProvider{cap: xgCapabilities(), verdict: &verdict, onRun: func(executor.Prepared) { runs++ }}
	memo := &xgCountingMemo{}
	deps := ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: root, TargetRef: "staging/local"},
		Gate: ExecutorGate{
			ProjectRoot: root, TargetRef: "staging/local", Executor: "xg-remote", Pipeline: "change",
			Selector: xgSelector("xg-remote", provider, nil),
		},
		Finalizer:   ProtectedFinalizer{ProjectRoot: root, TargetRef: "staging/local"},
		TargetRef:   "staging/local",
		GateTier:    "change",
		GateVersion: "executor:xg-remote:change",
		GateMemo:    memo,
	}
	worker := Worker{Store: store, Deps: deps}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	green := mustGet(t, store, submitted.ID)
	if green.phase() != ReadyToFinalize || runs != 1 {
		t.Fatalf("first green=%#v runs=%d", green, runs)
	}
	if baseInVersion, ok := executorGatePolicyBase(green.GateVersion); !ok || baseInVersion != base {
		t.Fatalf("first gate version=%q does not bind base %s", green.GateVersion, base)
	}

	// Another authority advances the protected target after green. Even
	// though the selected candidate is now already at the target, the old
	// policy snapshot cannot authorize CAS completion.
	git(t, root, "update-ref", "refs/heads/staging/local", sha, base)
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	stale := mustGet(t, store, submitted.ID)
	if stale.phase() != Reprepare || runs != 1 {
		t.Fatalf("target move did not force reprepare: candidate=%#v runs=%d", stale, runs)
	}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	regated := mustGet(t, store, submitted.ID)
	if regated.phase() != ReadyToFinalize || runs != 2 {
		t.Fatalf("candidate was not regated on new policy base: %#v runs=%d", regated, runs)
	}
	if baseInVersion, ok := executorGatePolicyBase(regated.GateVersion); !ok || baseInVersion != sha {
		t.Fatalf("regate version=%q does not bind live base %s", regated.GateVersion, sha)
	}
	if memo.lookups != 0 || memo.stores != 0 {
		t.Fatalf("executor gate touched generic memo across policy bases: lookups=%d stores=%d", memo.lookups, memo.stores)
	}
}
