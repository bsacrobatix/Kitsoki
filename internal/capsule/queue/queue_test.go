package queue

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

func TestAtomicStateRewritePreservesDurableStoreIdentity(t *testing.T) {
	root := t.TempDir()
	queueDir := filepath.Join(root, ".capsules", "queue")
	if err := os.MkdirAll(queueDir, 0o750); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(queueDir)
	if err != nil {
		t.Fatal(err)
	}
	uid, primaryGID, ok := fileOwner(info)
	if !ok {
		t.Skip("platform does not expose Unix file ownership")
	}
	alternateGID := -1
	groups, err := os.Getgroups()
	if err != nil {
		t.Fatal(err)
	}
	for _, gid := range groups {
		if gid != primaryGID {
			alternateGID = gid
			break
		}
	}
	if alternateGID < 0 && os.Geteuid() == 0 {
		alternateGID = primaryGID + 1
	}
	if alternateGID < 0 {
		t.Skip("no alternate writable group available")
	}
	if err := os.Chown(queueDir, uid, alternateGID); err != nil {
		t.Skipf("cannot assign alternate queue group: %v", err)
	}

	path := filepath.Join(queueDir, "state.json")
	if err := write(path, State{Schema: Schema}); err != nil {
		t.Fatal(err)
	}
	assertIdentity := func(stage string) {
		t.Helper()
		stateInfo, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		gotUID, gotGID, ok := fileOwner(stateInfo)
		if !ok {
			t.Fatal("state ownership unavailable")
		}
		if gotUID != uid || gotGID != alternateGID {
			t.Fatalf("%s owner=%d:%d, want %d:%d", stage, gotUID, gotGID, uid, alternateGID)
		}
		if got := stateInfo.Mode().Perm(); got != 0o600 {
			t.Fatalf("%s mode=%#o, want 0600", stage, got)
		}
	}
	assertIdentity("first write")

	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	// Simulate the historical failure: a privileged writer left state.json
	// with the writer's primary group instead of the durable directory group.
	// The next atomic rewrite must self-heal ownership from the directory.
	if err := os.Chown(path, uid, primaryGID); err != nil {
		t.Fatal(err)
	}
	if err := write(path, State{Schema: Schema, Candidates: []Candidate{{ID: "second-write"}}}); err != nil {
		t.Fatal(err)
	}
	stateInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	gotUID, gotGID, _ := fileOwner(stateInfo)
	if gotUID != uid || gotGID != alternateGID {
		t.Fatalf("rewrite owner=%d:%d, want %d:%d", gotUID, gotGID, uid, alternateGID)
	}
	if got := stateInfo.Mode().Perm(); got != 0o640 {
		t.Fatalf("rewrite mode=%#o, want preserved 0640", got)
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

// TestSubmitReturnsTypedBusyWithinBoundOnLockContention guards the
// receipt-bound promotion path: a caller with a zero LockWait (the default
// used by `capsule promote`) must get a fast, errors.Is-detectable ErrBusy
// rather than hanging or surfacing an opaque os.IsExist error when the
// state lock is already held.
func TestSubmitReturnsTypedBusyWithinBoundOnLockContention(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".capsules", "queue")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lockFile := filepath.Join(dir, "state.lock")
	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := tryExclusiveFileLock(f)
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("test failed to acquire serializer lock")
	}
	defer func() {
		_ = releaseExclusiveFileLock(f)
		_ = f.Close()
	}()

	sha := strings.Repeat("a", 40)
	in := Submit{Branch: "agent/a", SHA: sha, Receipt: testReceipt(t, sha)}
	store := Store{ProjectRoot: root}
	done := make(chan error, 1)
	go func() {
		_, err := store.Submit(in)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("want ErrBusy, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Submit did not return within bound while the lock was held")
	}
}

func TestSubmitReclaimsPersistentLockFileWithoutLiveOwner(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".capsules", "queue")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lockFile := filepath.Join(dir, "state.lock")
	if err := os.WriteFile(lockFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	sha := strings.Repeat("b", 40)
	store := Store{ProjectRoot: root}
	if _, err := store.Submit(Submit{
		Branch:  "agent/restart",
		SHA:     sha,
		Receipt: testReceipt(t, sha),
	}); err != nil {
		t.Fatalf("persistent lock file without a live owner blocked submit: %v", err)
	}
	if _, err := os.Stat(lockFile); err != nil {
		t.Fatalf("serializer lock file should remain as a stable inode: %v", err)
	}
}

func TestSubmitRecoversSerializerAfterHolderProcessDies(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".capsules", "queue")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lockFile := filepath.Join(dir, "state.lock")

	cmd := exec.Command(os.Args[0], "-test.run=TestQueueLockHolderProcess")
	cmd.Env = append(os.Environ(), "KITSOKI_QUEUE_LOCK_HOLDER="+lockFile)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "locked" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("lock holder did not become ready: %q (%v)", scanner.Text(), scanner.Err())
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	sha := strings.Repeat("c", 40)
	store := Store{ProjectRoot: root, LockWait: time.Second}
	if _, err := store.Submit(Submit{
		Branch:  "agent/crash-restart",
		SHA:     sha,
		Receipt: testReceipt(t, sha),
	}); err != nil {
		t.Fatalf("dead holder's serializer lock was not released by the OS: %v", err)
	}
}

func TestQueueLockHolderProcess(t *testing.T) {
	path := os.Getenv("KITSOKI_QUEUE_LOCK_HOLDER")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := tryExclusiveFileLock(f)
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("helper could not acquire serializer lock")
	}
	fmt.Println("locked")
	select {}
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

func TestTargetWorkersPartitionClaimsAndFinalizeOnlyTheirTarget(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir(), LockWait: time.Second}
	shaA, shaB := strings.Repeat("a", 40), strings.Repeat("b", 40)
	a, err := store.Submit(Submit{Branch: "agent/a", SHA: shaA, Receipt: testReceipt(t, shaA), TargetRef: "wave/alpha", TargetBaseSHAAtAdmission: "base-alpha"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.Submit(Submit{Branch: "agent/b", SHA: shaB, Receipt: testReceipt(t, shaB), TargetRef: "wave/beta", TargetBaseSHAAtAdmission: "base-beta"})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var finalized []Candidate
	deps := func(target string) ProcessDeps {
		return ProcessDeps{TargetRef: target, GateVersion: "test", Integration: &fakeIntegration{speculate: func(_ context.Context, c Candidate, _ []Candidate) (Speculation, error) {
			return Speculation{SHA: "tree-" + c.SHA, BaseSHA: c.TargetBaseSHAAtAdmission}, nil
		}}, Gate: passingGate{}, Finalizer: finalizerFunc(func(_ context.Context, c Candidate) (FinalizeResult, error) {
			mu.Lock()
			defer mu.Unlock()
			finalized = append(finalized, c)
			return FinalizeResult{OldMainSHA: c.BaseSHA, NewMainSHA: c.TreeSHA}, nil
		})}
	}
	workers := []Worker{{Store: store, Deps: deps("wave/alpha")}, {Store: store, Deps: deps("wave/beta")}}
	var wg sync.WaitGroup
	for _, worker := range workers {
		wg.Add(1)
		go func(w Worker) {
			defer wg.Done()
			_, err := w.RunOnce(context.Background())
			if err != nil {
				t.Errorf("prepare: %v", err)
			}
		}(worker)
	}
	wg.Wait()
	for _, worker := range workers {
		if _, err := worker.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(finalized) != 2 {
		t.Fatalf("finalized=%#v", finalized)
	}
	seen := map[string]string{}
	for _, candidate := range finalized {
		seen[candidate.ID] = candidate.TargetRef
	}
	if seen[a.ID] != "wave/alpha" || seen[b.ID] != "wave/beta" {
		t.Fatalf("finalized targets=%v", seen)
	}
}

func TestWrongTargetWorkerLeavesCandidateUntouched(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	sha := strings.Repeat("c", 40)
	candidate, err := store.Submit(Submit{Branch: "agent/c", SHA: sha, Receipt: testReceipt(t, sha), TargetRef: "wave/alpha"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	progressed, err := (Worker{Store: store, Deps: ProcessDeps{TargetRef: "wave/beta", Integration: specIntegration(), Gate: passingGate{}}}).RunOnce(context.Background())
	if err != nil || progressed {
		t.Fatalf("progressed=%v err=%v", progressed, err)
	}
	after, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || after.Candidates[0].ID != candidate.ID {
		t.Fatalf("candidate mutated: before=%#v after=%#v", before, after)
	}
}

func TestLegacyMigrationRequiresExplicitTargetAndPreservesApprovalState(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".capsules", "queue")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := State{Schema: legacySchema, Candidates: []Candidate{{ID: "legacy", SHA: strings.Repeat("d", 40), Branch: "agent/d", ProjectID: "project", Status: AwaitingApproval, FinalizationPolicy: StewardReviewFinalization, ManifestDigest: "manifest", Evidence: []string{"receipt:path", "operator:note"}}}}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Store{ProjectRoot: root}).List(); err == nil || !strings.Contains(err.Error(), "explicit migration target_ref") {
		t.Fatalf("err=%v", err)
	}
	store := Store{ProjectRoot: root, LegacyTargetRef: "wave/alpha", LegacyTargetBaseSHAAtAdmission: "base", LegacyTargetPolicy: StewardApprovedPolicy}
	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	c := state.Candidates[0]
	if state.Schema != Schema || c.TargetRef != "wave/alpha" || c.TargetBaseSHAAtAdmission != "base" || c.TargetPolicy != StewardApprovedPolicy || c.Phase != AwaitingApproval || !strings.Contains(strings.Join(c.Evidence, " "), "receipt:path") {
		t.Fatalf("migration=%#v", state)
	}
	again, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state, again) {
		t.Fatalf("migration not idempotent: first=%#v second=%#v", state, again)
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

// A failing head requeues to the back of the line in retry_wait; it delays
// only itself, and the candidate behind it lands. The protected base CAS at
// finalization remains the correctness guard.
func TestRetryWaitHeadRequeuesToBackAndTrainContinues(t *testing.T) {
	store, first, _ := queuedPair(t)
	integration := &fakeIntegration{speculate: func(_ context.Context, c Candidate, _ []Candidate) (Speculation, error) {
		return Speculation{SHA: "spec-" + c.SHA}, nil
	}}
	gate := gateFunc(func(_ context.Context, spec Speculation) (GateResult, error) {
		return GateResult{Passed: spec.SHA != "spec-"+first.SHA, Evidence: []string{"gate:red"}}, nil
	})
	state, err := store.Process(context.Background(), ProcessDeps{Integration: integration, Gate: gate})
	if err != nil {
		t.Fatal(err)
	}
	if state.Candidates[0].Status != RetryWait || state.Candidates[1].Status != Landed {
		t.Fatalf("state=%#v", state.Candidates)
	}
	if state.Candidates[0].Position <= state.Candidates[1].Position {
		t.Fatalf("failed head did not requeue to the back: %#v", state.Candidates)
	}
	if state.Candidates[0].RetryAt.IsZero() {
		t.Fatalf("retry_wait candidate has no durable retry_at: %#v", state.Candidates[0])
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

func TestSubmitEmergencySkipTestsIsExplicitAndDoesNotForgeAReceipt(t *testing.T) {
	sha := strings.Repeat("e", 40)
	store := Store{ProjectRoot: t.TempDir()}
	candidate, err := store.Submit(Submit{Branch: "agent/emergency", SHA: sha, Admission: EmergencySkipTestsAdmission})
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Admission != EmergencySkipTestsAdmission || candidate.ReceiptID != "" || candidate.ReceiptDigest != string(EmergencySkipTestsAdmission) {
		t.Fatalf("candidate=%#v", candidate)
	}
	if _, err := store.Submit(Submit{Branch: "agent/emergency", SHA: sha, Admission: EmergencySkipTestsAdmission, Receipt: testReceipt(t, sha)}); err == nil || !strings.Contains(err.Error(), "cannot carry") {
		t.Fatalf("emergency submission accepted a receipt: %v", err)
	}
}

// TestSubmitRefusesCandidateNotResolvableInGitBackedProject guards the
// inverse of the no-fetch bug: a worker retrying a candidate whose commit
// was never published into the project used to hang in speculation_failed
// until an operator hand-published refs/kitsoki/queue-candidates/<id>.
// Submit must now refuse admission outright when the project root is a real
// git repository and the SHA is not resolvable there.
func TestSubmitRefusesCandidateNotResolvableInGitBackedProject(t *testing.T) {
	root := protectedQueueRepo(t)
	sha := strings.Repeat("b", 40)
	store := Store{ProjectRoot: root}
	if _, err := store.Submit(Submit{Branch: "agent/ghost", SHA: sha, Receipt: persistedReceipt(t, root, sha)}); err == nil {
		t.Fatal("expected Submit to refuse a candidate whose commit is not resolvable in the project")
	}
	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Candidates) != 0 {
		t.Fatalf("candidate should not have been admitted: %#v", state.Candidates)
	}
}

// TestSubmitAnchorsCandidateRefInGitBackedProject guards the ref-anchoring
// half of the same fix: an admitted candidate's commit must be reachable
// from a durable refs/kitsoki/queue-candidates/<id> ref independent of
// whatever workspace or branch originally produced it, so it survives GC
// and stays fetchable across worker retries.
func TestSubmitAnchorsCandidateRefInGitBackedProject(t *testing.T) {
	root := protectedQueueRepo(t)
	sha := git(t, root, "rev-parse", "HEAD")
	store := Store{ProjectRoot: root}
	candidate, err := store.Submit(Submit{Branch: "agent/anchor", SHA: sha, Receipt: persistedReceipt(t, root, sha)})
	if err != nil {
		t.Fatal(err)
	}
	ref := candidateRefName(candidate.ID)
	if got := git(t, root, "rev-parse", ref); got != sha {
		t.Fatalf("candidate ref %s = %s, want %s", ref, got, sha)
	}
}

func TestWorkersPrepareConcurrentlyAndFinalizeInFIFOOrder(t *testing.T) {
	store, first, second := queuedPair(t)
	store.LockWait = time.Second
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
	errs := make(chan error, len(workers))
	for _, worker := range workers {
		wg.Add(1)
		go func(worker Worker) {
			defer wg.Done()
			_, err := worker.RunOnce(context.Background())
			errs <- err
		}(worker)
	}
	got := map[string]bool{<-entered: true, <-entered: true}
	if !got[first.ID] || !got[second.ID] {
		t.Fatalf("prepared=%v", got)
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
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

// TestHeartbeatKeepsLeaseAliveDuringLongRunningSpeculation guards the fix
// for the fact that a single 30s lease used to be set once at claim time and
// never renewed while the actual Speculate/Gate.Run/Finalize call was still
// in flight — a deterministic gate that legitimately runs longer than one
// lease window (the doc's own "full CI" case) would have its lease reclaimed
// by a second worker mid-operation, forcing an unnecessary reprepare. A
// short real lease here stands in for that. The test observes an actual
// renewal, then crosses the original expiry instead of sleeping for an
// arbitrary multiple of the lease.
func TestHeartbeatKeepsLeaseAliveDuringLongRunningSpeculation(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir(), LockWait: time.Second}
	sha := strings.Repeat("7", 40)
	candidate, err := store.Submit(Submit{Branch: "agent/heartbeat", SHA: sha, Receipt: testReceipt(t, sha)})
	if err != nil {
		t.Fatal(err)
	}
	block := make(chan struct{})
	entered := make(chan struct{})
	integration := &fakeIntegration{speculate: func(_ context.Context, c Candidate, _ []Candidate) (Speculation, error) {
		close(entered)
		<-block
		return Speculation{SHA: "tree-" + c.SHA}, nil
	}}
	deps := ProcessDeps{Integration: integration, Gate: passingGate{}, GateVersion: "test", Lease: 100 * time.Millisecond}
	worker := Worker{Store: store, Deps: deps}

	done := make(chan error, 1)
	go func() { _, err := worker.RunOnce(context.Background()); done <- err }()
	<-entered

	initial, err := store.Get(candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	renewalDeadline := time.NewTimer(2 * time.Second)
	defer renewalDeadline.Stop()
	poll := time.NewTicker(2 * time.Millisecond)
	defer poll.Stop()
	for {
		current, getErr := store.Get(initial.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if current.LeaseExpiresAt.After(initial.LeaseExpiresAt) {
			break
		}
		select {
		case <-poll.C:
		case <-renewalDeadline.C:
			t.Fatal("heartbeat did not renew the blocked speculation lease")
		}
	}
	if wait := time.Until(initial.LeaseExpiresAt) + time.Millisecond; wait > 0 {
		expiry := time.NewTimer(wait)
		<-expiry.C
	}

	// The original lease is now expired while speculation remains blocked.
	// Without the observed heartbeat renewal, this claim would steal it.
	second := Worker{Store: store, Deps: deps}
	if _, ok, err := second.claimPreparation(); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("a second worker claimed a candidate whose lease the heartbeat should still be renewing")
	}

	close(block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if state.Candidates[0].Phase != ReadyToFinalize || state.Candidates[0].Attempt != 1 {
		t.Fatalf("candidate=%#v (want a single attempt, no stolen-lease reprepare)", state.Candidates[0])
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
