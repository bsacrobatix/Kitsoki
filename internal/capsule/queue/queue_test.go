package queue

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
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

func TestConcurrentSubmitSerializesOneCandidate(t *testing.T) {
	sha := strings.Repeat("a", 40)
	store := Store{ProjectRoot: t.TempDir(), LockWait: time.Second}
	in := Submit{Branch: "agent/a", SHA: sha, Receipt: testReceipt(t, sha)}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := store.Submit(in); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Candidates) != 1 {
		t.Fatalf("candidates=%#v", state.Candidates)
	}
}

func TestProcessParksSpeculativeConflictAndKeepsEvidence(t *testing.T) {
	store, first, second := queuedPair(t)
	integration := &fakeIntegration{speculate: func(_ context.Context, c Candidate, ahead []Candidate) (Speculation, error) {
		if c.ID == second.ID {
			return Speculation{}, fmt.Errorf("merge conflict with %s", first.ID)
		}
		return Speculation{SHA: "spec-" + c.SHA, Evidence: []string{"speculation:first"}}, nil
	}}
	state, err := store.Process(context.Background(), ProcessDeps{Integration: integration, Gate: passingGate{}})
	if err != nil {
		t.Fatal(err)
	}
	if state.Candidates[0].Status != Landed || state.Candidates[1].Status != RetryWait {
		t.Fatalf("state=%#v", state.Candidates)
	}
	if state.Candidates[1].RetryReason != "speculation_failed" || !strings.Contains(strings.Join(state.Candidates[1].Evidence, " "), "merge conflict") {
		t.Fatalf("retry=%#v", state.Candidates[1])
	}
}

func TestConcurrentConflictingCandidatesParkSecond(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir(), LockWait: time.Second}
	shas := []string{strings.Repeat("d", 40), strings.Repeat("e", 40)}
	var wg sync.WaitGroup
	for _, sha := range shas {
		sha := sha
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.Submit(Submit{Branch: "agent/" + sha[:1], SHA: sha, Receipt: testReceipt(t, sha)}); err != nil {
				t.Errorf("submit: %v", err)
			}
		}()
	}
	wg.Wait()
	first := ""
	integration := &fakeIntegration{speculate: func(_ context.Context, c Candidate, ahead []Candidate) (Speculation, error) {
		if first == "" {
			first = c.ID
			return Speculation{SHA: "spec-" + c.SHA}, nil
		}
		return Speculation{}, fmt.Errorf("conflict with %s", first)
	}}
	state, err := store.Process(context.Background(), ProcessDeps{Integration: integration, Gate: passingGate{}})
	if err != nil {
		t.Fatal(err)
	}
	if state.Candidates[0].Status != Landed || state.Candidates[1].Status != RetryWait || integration.landed != 1 {
		t.Fatalf("state=%#v landed=%d", state.Candidates, integration.landed)
	}
}

func TestProcessRunsGateAgainstSpeculativeTreeAndDoesNotLandOnFailure(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	sha := strings.Repeat("c", 40)
	if _, err := store.Submit(Submit{Branch: "agent/c", SHA: sha, Receipt: testReceipt(t, sha)}); err != nil {
		t.Fatal(err)
	}
	integration := &fakeIntegration{speculate: func(context.Context, Candidate, []Candidate) (Speculation, error) {
		return Speculation{SHA: "speculative-merge-sha"}, nil
	}}
	gate := gateFunc(func(_ context.Context, s Speculation) (GateResult, error) {
		if s.SHA != "speculative-merge-sha" {
			t.Fatalf("gate ran on %q", s.SHA)
		}
		return GateResult{Passed: false, Evidence: []string{"gate:red"}}, nil
	})
	state, err := store.Process(context.Background(), ProcessDeps{Integration: integration, Gate: gate})
	if err != nil {
		t.Fatal(err)
	}
	if integration.landed != 0 || state.Candidates[0].Status != RetryWait || state.Candidates[0].RetryReason != "gate_failed" {
		t.Fatalf("landed=%d state=%#v", integration.landed, state.Candidates[0])
	}
}

func TestProcessRunsOneBoundedRepairBeforeParkingRedGate(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	sha := strings.Repeat("f", 40)
	if _, err := store.Submit(Submit{Branch: "agent/f", SHA: sha, Receipt: testReceipt(t, sha)}); err != nil {
		t.Fatal(err)
	}
	integration := &fakeIntegration{speculate: func(context.Context, Candidate, []Candidate) (Speculation, error) {
		return Speculation{SHA: "speculative", WorkspacePath: t.TempDir()}, nil
	}}
	runs := 0
	gate := gateFunc(func(context.Context, Speculation) (GateResult, error) {
		runs++
		return GateResult{Passed: runs == 2}, nil
	})
	repairs := 0
	state, err := store.Process(context.Background(), ProcessDeps{
		Integration: integration,
		Gate:        gate,
		Repairer: repairFunc(func(context.Context, Speculation, error) ([]string, error) {
			repairs++
			return []string{"repair:attempted"}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Candidates[0].Status != Landed || repairs != 1 || runs != 2 || integration.landed != 1 {
		t.Fatalf("state=%#v repairs=%d runs=%d landed=%d", state.Candidates, repairs, runs, integration.landed)
	}
}

func TestRetryWaitHeadAllowsLaterPreparationButBlocksFinalization(t *testing.T) {
	store, first, _ := queuedPair(t)
	integration := &fakeIntegration{speculate: func(_ context.Context, c Candidate, _ []Candidate) (Speculation, error) {
		if c.ID == first.ID {
			return Speculation{SHA: "spec-" + c.SHA}, nil
		}
		return Speculation{SHA: "spec-" + c.SHA}, nil
	}}
	gate := gateFunc(func(_ context.Context, spec Speculation) (GateResult, error) {
		return GateResult{Passed: spec.SHA != "spec-"+first.SHA, Evidence: []string{"gate:red"}}, nil
	})
	state, err := store.Process(context.Background(), ProcessDeps{Integration: integration, Gate: gate})
	if err != nil {
		t.Fatal(err)
	}
	if state.Candidates[0].Status != RetryWait || state.Candidates[1].Status != ReadyToFinalize {
		t.Fatalf("state=%#v", state.Candidates)
	}
}

func TestProcessRecoversRunningStateAndKeepsOrder(t *testing.T) {
	store, first, _ := queuedPair(t)
	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	state.Candidates[0].Status = Running
	_, path, err := store.paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := write(path, state); err != nil {
		t.Fatal(err)
	}
	integration := &fakeIntegration{speculate: func(_ context.Context, c Candidate, ahead []Candidate) (Speculation, error) {
		if c.ID == first.ID && len(ahead) != 0 {
			t.Fatalf("first candidate had ahead=%#v", ahead)
		}
		return Speculation{SHA: "spec-" + c.SHA}, nil
	}}
	state, err = store.Process(context.Background(), ProcessDeps{Integration: integration, Gate: passingGate{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Candidates) != 2 || state.Candidates[0].Status != Landed || state.Candidates[1].Status != Landed || integration.landed != 2 {
		t.Fatalf("state=%#v landed=%d", state, integration.landed)
	}
}

func queuedPair(t *testing.T) (Store, Candidate, Candidate) {
	t.Helper()
	store := Store{ProjectRoot: t.TempDir()}
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	first, err := store.Submit(Submit{Branch: "agent/a", SHA: a, Receipt: testReceipt(t, a)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Submit(Submit{Branch: "agent/b", SHA: b, Receipt: testReceipt(t, b)})
	if err != nil {
		t.Fatal(err)
	}
	return store, first, second
}

type fakeIntegration struct {
	speculate func(context.Context, Candidate, []Candidate) (Speculation, error)
	landed    int
}

func (f *fakeIntegration) Speculate(ctx context.Context, c Candidate, ahead []Candidate) (Speculation, error) {
	spec, err := f.speculate(ctx, c, ahead)
	if spec.BaseSHA == "" {
		spec.BaseSHA = "base-" + c.SHA
	}
	return spec, err
}
func (f *fakeIntegration) Land(context.Context, Speculation) error { f.landed++; return nil }

type passingGate struct{}

func (passingGate) Run(context.Context, Speculation) (GateResult, error) {
	return GateResult{Passed: true, Evidence: []string{"gate:green"}}, nil
}

type gateFunc func(context.Context, Speculation) (GateResult, error)

func (f gateFunc) Run(ctx context.Context, s Speculation) (GateResult, error) { return f(ctx, s) }

type repairFunc func(context.Context, Speculation, error) ([]string, error)

func (f repairFunc) Repair(ctx context.Context, s Speculation, err error) ([]string, error) {
	return f(ctx, s, err)
}

type finalizerFunc func(context.Context, Candidate) (FinalizeResult, error)

func (f finalizerFunc) Finalize(ctx context.Context, c Candidate) (FinalizeResult, error) {
	return f(ctx, c)
}

func TestSubmitRejectsReceiptForAnotherCandidate(t *testing.T) {
	_, err := (Store{ProjectRoot: t.TempDir()}).Submit(Submit{Branch: "agent-a", SHA: strings.Repeat("a", 40), Receipt: testReceipt(t, strings.Repeat("b", 40))})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err=%v", err)
	}
}

func TestWorkersPrepareConcurrentlyAndFinalizeInFIFOOrder(t *testing.T) {
	store, first, second := queuedPair(t)
	entered := make(chan string, 2)
	release := make(chan struct{})
	integration := &fakeIntegration{speculate: func(_ context.Context, c Candidate, _ []Candidate) (Speculation, error) {
		entered <- c.ID
		<-release
		return Speculation{SHA: "tree-" + c.SHA}, nil
	}}
	var mu sync.Mutex
	var finalized []string
	deps := ProcessDeps{Integration: integration, Gate: passingGate{}, WorkerID: "worker", GateVersion: "test", Finalizer: finalizerFunc(func(_ context.Context, c Candidate) (FinalizeResult, error) {
		mu.Lock()
		defer mu.Unlock()
		finalized = append(finalized, c.ID)
		return FinalizeResult{OldMainSHA: c.BaseSHA, NewMainSHA: c.TreeSHA}, nil
	})}
	workers := []Worker{{Store: store, Deps: deps}, {Store: store, Deps: deps}}
	var wg sync.WaitGroup
	for _, worker := range workers {
		wg.Add(1)
		go func(worker Worker) { defer wg.Done(); _, _ = worker.RunOnce(context.Background()) }(worker)
	}
	got := map[string]bool{<-entered: true, <-entered: true}
	if !got[first.ID] || !got[second.ID] {
		t.Fatalf("prepared=%v", got)
	}
	close(release)
	wg.Wait()
	if _, err := workers[0].RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := workers[1].RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{first.ID, second.ID}; !reflect.DeepEqual(finalized, want) {
		t.Fatalf("finalization order=%v want=%v", finalized, want)
	}
}

func TestFinalizerStaleBaseForcesReprepareWithoutLanding(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	sha := strings.Repeat("9", 40)
	if _, err := store.Submit(Submit{Branch: "agent/stale", SHA: sha, Receipt: testReceipt(t, sha)}); err != nil {
		t.Fatal(err)
	}
	worker := Worker{Store: store, Deps: ProcessDeps{Integration: &fakeIntegration{speculate: func(_ context.Context, c Candidate, _ []Candidate) (Speculation, error) {
		return Speculation{SHA: "tree-" + c.SHA}, nil
	}}, Gate: passingGate{}, GateVersion: "test", Finalizer: finalizerFunc(func(context.Context, Candidate) (FinalizeResult, error) { return FinalizeResult{Stale: true}, nil })}}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if state.Candidates[0].Status != Reprepare || state.Candidates[0].ResultMainSHA != "" {
		t.Fatalf("candidate=%#v", state.Candidates[0])
	}
}

func TestExpiredLeaseIsReclaimedWithAttemptEvidence(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	sha := strings.Repeat("8", 40)
	c, err := store.Submit(Submit{Branch: "agent/lease", SHA: sha, Receipt: testReceipt(t, sha)})
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	state.Candidates[0].Phase, state.Candidates[0].Status = Preparing, Preparing
	state.Candidates[0].WorkerID = "crashed"
	state.Candidates[0].LeaseExpiresAt = time.Now().Add(-time.Second)
	_, path, err := store.paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := write(path, state); err != nil {
		t.Fatal(err)
	}
	worker := Worker{Store: store, Deps: ProcessDeps{Integration: &fakeIntegration{speculate: func(_ context.Context, c Candidate, _ []Candidate) (Speculation, error) {
		return Speculation{SHA: "tree-" + c.SHA}, nil
	}}, Gate: passingGate{}, GateVersion: "test"}}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err = store.List()
	if err != nil {
		t.Fatal(err)
	}
	if state.Candidates[0].ID != c.ID || state.Candidates[0].Attempt != 1 || state.Candidates[0].Status != ReadyToFinalize {
		t.Fatalf("candidate=%#v", state.Candidates[0])
	}
}

func TestPreparedTupleRejectsIdentityMismatch(t *testing.T) {
	c := Candidate{SHA: strings.Repeat("1", 40), ReceiptDigest: "receipt", BaseSHA: "base", TreeSHA: "tree", GateVersion: "gate"}
	c.DependencyFingerprint = preparedFingerprint(c)
	if err := validatePreparedTuple(c); err != nil {
		t.Fatal(err)
	}
	c.TreeSHA = "other-tree"
	if err := validatePreparedTuple(c); err == nil || !strings.Contains(err.Error(), "does not match") {
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
