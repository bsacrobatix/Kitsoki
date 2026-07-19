package queue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// This file pins Worker lifecycle edges that queue_test.go, ops_test.go, and
// protected_e2e_test.go do not directly exercise: lease claim/renew races,
// gate failure classification (including the gate's own harness-failure
// branch, which was previously reachable but never asserted), the backoff
// schedule, the prepared-tuple validator's incomplete-tuple branch, the
// firstGateError fallback, and finalize's error/FIFO/emergency edges. Every
// top-level identifier here is prefixed wlt to avoid collisions with other
// agents' concurrently-added test files in this package.

// --- fixtures -------------------------------------------------------------

// wltSHA derives a deterministic, unique-per-test-and-seed 40 char lowercase
// hex SHA so multiple candidates can be submitted into the same store
// without tripping Submit's same-SHA resubmission/supersede logic.
func wltSHA(t *testing.T, seeds ...string) string {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name() + "\x00" + strings.Join(seeds, "\x00")))
	return hex.EncodeToString(sum[:])[:40]
}

func wltSubmit(t *testing.T, store Store, name string) Candidate {
	t.Helper()
	sha := wltSHA(t, name)
	c, err := store.Submit(Submit{Branch: "agent/" + name, SHA: sha, Receipt: testReceipt(t, sha)})
	if err != nil {
		t.Fatalf("submit %s: %v", name, err)
	}
	return c
}

// wltMutate directly rewrites the durable state for candidate id (the same
// technique queue_test.go's TestExpiredLeaseIsReclaimedWithAttemptEvidence
// uses) and returns the mutated candidate as re-read from the store.
func wltMutate(t *testing.T, store Store, id string, mutate func(*Candidate)) Candidate {
	t.Helper()
	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i := range state.Candidates {
		if state.Candidates[i].ID == id {
			mutate(&state.Candidates[i])
			found = true
		}
	}
	if !found {
		t.Fatalf("candidate %s not found for mutation", id)
	}
	_, path, err := store.paths()
	if err != nil {
		t.Fatal(err)
	}
	if err := write(path, state); err != nil {
		t.Fatal(err)
	}
	return wltGet(t, store, id)
}

func wltGet(t *testing.T, store Store, id string) Candidate {
	t.Helper()
	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range state.Candidates {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("candidate %s not found", id)
	return Candidate{}
}

// wltReadyCandidate submits a candidate and fast-forwards it directly to
// ready_to_finalize with a valid prepared tree/base, bypassing prepare(): the
// default AutonomousFinalization policy's finalizationAuthorized short
// circuits to true without needing a full DependencyFingerprint/Approval
// tuple, so this is sufficient to exercise finalize() in isolation.
func wltReadyCandidate(t *testing.T, store Store, name string) Candidate {
	t.Helper()
	c := wltSubmit(t, store, name)
	return wltMutate(t, store, c.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = ReadyToFinalize, ReadyToFinalize
		cand.TreeSHA = "tree-" + cand.SHA
		cand.BaseSHA = "base-" + cand.SHA
		cand.GateVersion = "test/v1"
	})
}

// --- RunOnce ---------------------------------------------------------------

func TestWltRunOnceRequiresIntegrationAndGate(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	cases := []ProcessDeps{
		{Gate: passingGate{}},            // missing Integration
		{Integration: specIntegration()}, // missing Gate
		{},                               // missing both
	}
	for i, deps := range cases {
		worker := Worker{Store: store, Deps: deps}
		progressed, err := worker.RunOnce(context.Background())
		if progressed || err == nil || !strings.Contains(err.Error(), "integration and gate are required") {
			t.Fatalf("case %d: progressed=%v err=%v", i, progressed, err)
		}
	}
}

func TestWltRunOnceEmptyQueueIsCleanNoop(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	worker := Worker{Store: store, Deps: ProcessDeps{Integration: specIntegration(), Gate: passingGate{}}}
	progressed, err := worker.RunOnce(context.Background())
	if err != nil || progressed {
		t.Fatalf("progressed=%v err=%v", progressed, err)
	}
	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Candidates) != 0 {
		t.Fatalf("state=%#v", state)
	}
}

// TestWltRunOnceWithOnlyTerminalOrParkedOrFutureRetryCandidatesIsNoop covers
// (e): a queue where nothing is currently eligible for claim or finalize
// (landed, rejected, parked, and a retry_wait candidate whose timer has not
// elapsed) must leave every candidate's state completely untouched.
func TestWltRunOnceWithOnlyTerminalOrParkedOrFutureRetryCandidatesIsNoop(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	landed := wltSubmit(t, store, "landed")
	wltMutate(t, store, landed.ID, func(c *Candidate) { c.Phase, c.Status = Landed, Landed })
	rejected := wltSubmit(t, store, "rejected")
	wltMutate(t, store, rejected.ID, func(c *Candidate) { c.Phase, c.Status = Rejected, Rejected })
	parked := wltSubmit(t, store, "parked")
	wltMutate(t, store, parked.ID, func(c *Candidate) { c.Phase, c.Status = NeedsInput, NeedsInput })
	future := wltSubmit(t, store, "future-retry")
	wltMutate(t, store, future.ID, func(c *Candidate) {
		c.Phase, c.Status = RetryWait, RetryWait
		c.RetryAt = time.Now().Add(time.Hour)
	})

	before, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	worker := Worker{Store: store, Deps: ProcessDeps{Integration: specIntegration(), Gate: passingGate{}}}
	progressed, err := worker.RunOnce(context.Background())
	if err != nil || progressed {
		t.Fatalf("progressed=%v err=%v", progressed, err)
	}
	after, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("no-op RunOnce mutated state: before=%#v after=%#v", before, after)
	}
}

// TestWltRunOnceClaimOrderIsLowestEligiblePositionFirst covers (e): three
// queued candidates must be claimed for preparation strictly in ascending
// position/sequence order, one per RunOnce call.
func TestWltRunOnceClaimOrderIsLowestEligiblePositionFirst(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	var ids []string
	for _, name := range []string{"c1", "c2", "c3"} {
		ids = append(ids, wltSubmit(t, store, name).ID)
	}
	var order []string
	integration := &fakeIntegration{speculate: func(_ context.Context, c Candidate, _ []Candidate) (Speculation, error) {
		order = append(order, c.ID)
		return Speculation{SHA: "spec-" + c.SHA}, nil
	}}
	worker := Worker{Store: store, Deps: ProcessDeps{Integration: integration, Gate: passingGate{}}}
	for range ids {
		if _, err := worker.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(order, ids) {
		t.Fatalf("claim order=%v want=%v", order, ids)
	}
}

// --- lease lifecycle --------------------------------------------------------

func TestWltLiveLeaseBlocksConcurrentClaimByAnotherWorker(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	c := wltSubmit(t, store, "leased")
	wltMutate(t, store, c.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = Preparing, Preparing
		cand.WorkerID = "worker-a"
		cand.LeaseExpiresAt = time.Now().Add(time.Minute)
	})
	other := Worker{Store: store, Deps: ProcessDeps{WorkerID: "worker-b"}}
	_, ok, err := other.claimPreparation()
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("a second worker claimed a candidate whose lease is still live")
	}
	got := wltGet(t, store, c.ID)
	if got.WorkerID != "worker-a" || got.Phase != Preparing {
		t.Fatalf("candidate mutated by a blocked claim attempt: %#v", got)
	}
}

// TestWltExpiredLeaseReclaimedByDifferentWorkerAttemptContinuity covers (a):
// worker A's lease expires mid in-flight phase; worker B must be able to
// reclaim it into a fresh Preparing attempt, preserving attempt continuity
// (the durable retry-ledger property Submit's resubmission path also relies
// on) and clearing the transient reprepare failure note recorded at reclaim.
func TestWltExpiredLeaseReclaimedByDifferentWorkerAttemptContinuity(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	c := wltSubmit(t, store, "expired")
	wltMutate(t, store, c.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = Gating, Gating
		cand.WorkerID = "worker-a"
		cand.LeaseExpiresAt = time.Now().Add(-time.Minute)
		cand.Attempt = 1
	})
	other := Worker{Store: store, Deps: ProcessDeps{WorkerID: "worker-b"}}
	claimed, ok, err := other.claimPreparation()
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expired lease was not reclaimed by the second worker")
	}
	if claimed.WorkerID != "worker-b" || claimed.Attempt != 2 || claimed.Phase != Preparing {
		t.Fatalf("claimed=%#v", claimed)
	}
	if claimed.Failure != "" {
		t.Fatalf("claim did not clear the transient reprepare failure note: %#v", claimed)
	}
}

func TestWltRenewLeaseExtendsLeaseStillHeldByThisWorker(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	c := wltSubmit(t, store, "renew")
	old := time.Now().Add(5 * time.Second).UTC()
	wltMutate(t, store, c.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = Gating, Gating
		cand.WorkerID = "queue-worker" // matches the default fallback identity
		cand.LeaseExpiresAt = old
	})
	w := Worker{Store: store, Deps: ProcessDeps{Lease: time.Minute}}
	if err := w.renewLease(c.ID); err != nil {
		t.Fatal(err)
	}
	got := wltGet(t, store, c.ID)
	if !got.LeaseExpiresAt.After(old) {
		t.Fatalf("lease not extended: old=%v new=%v", old, got.LeaseExpiresAt)
	}
	if got.Phase != Gating {
		t.Fatalf("renewLease changed phase: %#v", got)
	}
}

// TestWltRenewLeaseIsNoopWhenLeaseNoLongerHeldByThisWorker covers the
// concurrent-mutation half of (a): a heartbeat ticking for worker A must not
// touch a lease a different worker (B) has since claimed — renewLease's
// WorkerID guard, not just claimPreparation's expiry check, must hold.
func TestWltRenewLeaseIsNoopWhenLeaseNoLongerHeldByThisWorker(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	c := wltSubmit(t, store, "stolen")
	fixed := time.Now().Add(5 * time.Second).UTC()
	wltMutate(t, store, c.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = Preparing, Preparing
		cand.WorkerID = "worker-b" // reclaimed by a different worker
		cand.LeaseExpiresAt = fixed
	})
	w := Worker{Store: store, Deps: ProcessDeps{WorkerID: "worker-a"}}
	if err := w.renewLease(c.ID); err != nil {
		t.Fatal(err)
	}
	got := wltGet(t, store, c.ID)
	if diff := got.LeaseExpiresAt.Sub(fixed); diff < -time.Millisecond || diff > time.Millisecond {
		t.Fatalf("renewLease from a worker that no longer holds the lease touched it: old=%v new=%v", fixed, got.LeaseExpiresAt)
	}
}

// TestWltRenewLeaseIsNoopWhenCandidateNoLongerInFlight exercises renewLease's
// phase switch directly (independent of the WorkerID guard above): even if
// the WorkerID field still matched, a candidate no longer in an in-flight
// phase must never have its lease resurrected.
func TestWltRenewLeaseIsNoopWhenCandidateNoLongerInFlight(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	c := wltSubmit(t, store, "parked-midflight")
	wltMutate(t, store, c.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = NeedsInput, NeedsInput
		cand.WorkerID = "queue-worker"
		cand.LeaseExpiresAt = time.Time{}
	})
	w := Worker{Store: store, Deps: ProcessDeps{}}
	if err := w.renewLease(c.ID); err != nil {
		t.Fatal(err)
	}
	got := wltGet(t, store, c.ID)
	if !got.LeaseExpiresAt.IsZero() || got.Phase != NeedsInput {
		t.Fatalf("renewLease resurrected a non-in-flight candidate's lease: %#v", got)
	}
}

// --- gate failure classification (failGate) --------------------------------

func TestWltFailGateHarnessErrorParksImmediately(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	wltSubmit(t, store, "gate-harness-direct")
	w := Worker{Store: store, Deps: ProcessDeps{MaxAttempts: 5}}
	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	cand := &state.Candidates[0]
	w.failGate(&state, cand, Harness(fmt.Errorf("gate harness could not launch")))
	if cand.Phase != NeedsInput || cand.Status != NeedsInput || cand.RetryReason != "gate_harness_failure" {
		t.Fatalf("cand=%#v", cand)
	}
	if !cand.LeaseExpiresAt.IsZero() || cand.WorkerID != "" || !cand.RetryAt.IsZero() {
		t.Fatalf("harness park left lease/worker/retry_at set: %#v", cand)
	}
}

func TestWltFailGateRetryableFailureSetsBackoffAndRetryReason(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	wltSubmit(t, store, "gate-retry")
	clock := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	w := Worker{Store: store, Deps: ProcessDeps{MaxAttempts: 5, RetryDelay: time.Minute, MaxRetryDelay: 10 * time.Minute, Now: func() time.Time { return clock }}}
	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	cand := &state.Candidates[0]
	cand.Attempt = 1
	w.failGate(&state, cand, fmt.Errorf("deterministic gate failed"))
	if cand.Phase != RetryWait || cand.RetryReason != "gate_failed" {
		t.Fatalf("cand=%#v", cand)
	}
	if !cand.RetryAt.Equal(clock.Add(time.Minute)) {
		t.Fatalf("retry_at=%v want %v", cand.RetryAt, clock.Add(time.Minute))
	}
}

func TestWltFailGateExhaustedAttemptsParksAsNeedsInput(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	wltSubmit(t, store, "gate-exhausted")
	w := Worker{Store: store, Deps: ProcessDeps{MaxAttempts: 2}}
	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	cand := &state.Candidates[0]
	cand.Attempt = 2
	w.failGate(&state, cand, fmt.Errorf("deterministic gate failed"))
	if cand.Phase != NeedsInput || cand.RetryReason != "max_attempts_exhausted" {
		t.Fatalf("cand=%#v", cand)
	}
	if !strings.Contains(strings.Join(cand.Evidence, " "), "max attempts (2) exhausted") {
		t.Fatalf("evidence=%v", cand.Evidence)
	}
}

// TestWltGateHarnessErrorParksThroughFullPrepareFlow closes a real coverage
// gap: "gate_harness_failure" (worker.go's failGate Harness branch reached
// from Gate.Run itself, as opposed to resolver/speculation harness failures)
// was never exercised end to end through RunOnce before this test.
func TestWltGateHarnessErrorParksThroughFullPrepareFlow(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	sha := wltSHA(t)
	if _, err := store.Submit(Submit{Branch: "agent/gate-harness-flow", SHA: sha, Receipt: testReceipt(t, sha)}); err != nil {
		t.Fatal(err)
	}
	integration := &fakeIntegration{speculate: func(context.Context, Candidate, []Candidate) (Speculation, error) {
		return Speculation{SHA: "spec-tree"}, nil
	}}
	gate := gateFunc(func(context.Context, Speculation) (GateResult, error) {
		return GateResult{}, Harness(fmt.Errorf("gate harness could not launch"))
	})
	worker := Worker{Store: store, Deps: ProcessDeps{Integration: integration, Gate: gate}}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if state.Candidates[0].Phase != NeedsInput || state.Candidates[0].RetryReason != "gate_harness_failure" {
		t.Fatalf("candidate=%#v", state.Candidates[0])
	}
}

// TestWltRepairerReceivesActualGateErrorNotGenericFallback pins
// firstGateError's non-nil branch: when Gate.Run itself errors (as opposed
// to merely returning Passed:false), the Repairer must see that real error,
// not the "deterministic gate failed" fallback reserved for a red-but-clean
// gate run.
func TestWltRepairerReceivesActualGateErrorNotGenericFallback(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	sha := wltSHA(t)
	if _, err := store.Submit(Submit{Branch: "agent/gate-err", SHA: sha, Receipt: testReceipt(t, sha)}); err != nil {
		t.Fatal(err)
	}
	integration := &fakeIntegration{speculate: func(context.Context, Candidate, []Candidate) (Speculation, error) {
		return Speculation{SHA: "spec-tree"}, nil
	}}
	wantErr := fmt.Errorf("gate harness crashed mid-run")
	var gotReason error
	gate := gateFunc(func(context.Context, Speculation) (GateResult, error) {
		return GateResult{}, wantErr
	})
	repairer := repairFunc(func(_ context.Context, _ Speculation, reason error) ([]string, error) {
		gotReason = reason
		return nil, fmt.Errorf("repair also failed")
	})
	worker := Worker{Store: store, Deps: ProcessDeps{Integration: integration, Gate: gate, Repairer: repairer}}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if gotReason == nil || gotReason.Error() != wantErr.Error() {
		t.Fatalf("repairer reason=%v want=%v", gotReason, wantErr)
	}
}

// --- backoff -----------------------------------------------------------------

func TestWltBackoffSchedule(t *testing.T) {
	base, max := time.Minute, 5*time.Minute
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{attempt: -1, want: base}, // clamped up to 1
		{attempt: 0, want: base},  // clamped up to 1
		{attempt: 1, want: base},  // no doubling yet
		{attempt: 2, want: 2 * time.Minute},
		{attempt: 3, want: 4 * time.Minute},
		{attempt: 4, want: max}, // would be 8m uncapped; capped mid-loop
		{attempt: 10, want: max},
	}
	for _, tc := range cases {
		if got := backoff(base, max, tc.attempt); got != tc.want {
			t.Fatalf("backoff(attempt=%d)=%v want %v", tc.attempt, got, tc.want)
		}
	}
}

func TestWltBackoffClampsBaseAlreadyAboveMax(t *testing.T) {
	if got := backoff(10*time.Minute, 5*time.Minute, 1); got != 5*time.Minute {
		t.Fatalf("got=%v want=%v", got, 5*time.Minute)
	}
}

// --- validatePreparedTuple / firstGateError -----------------------------------

func TestWltValidatePreparedTupleRejectsIncompleteTuple(t *testing.T) {
	c := Candidate{SHA: strings.Repeat("2", 40), BaseSHA: "base", TreeSHA: "tree"} // GateVersion and DependencyFingerprint missing
	if err := validatePreparedTuple(c); err == nil || !strings.Contains(err.Error(), "complete prepared receipt tuple") {
		t.Fatalf("err=%v", err)
	}
}

func TestWltFirstGateErrorPassesThroughNonNilError(t *testing.T) {
	err := fmt.Errorf("boom")
	if got := firstGateError(err); got != err {
		t.Fatalf("got=%v want=%v", got, err)
	}
}

func TestWltFirstGateErrorFallsBackToGenericMessageWhenNil(t *testing.T) {
	got := firstGateError(nil)
	if got == nil || got.Error() != "deterministic gate failed" {
		t.Fatalf("got=%v", got)
	}
}

// --- finalize ------------------------------------------------------------------

func TestWltFinalizeGenericFinalizerErrorRetriesWithBackoffAndRequeues(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	c := wltReadyCandidate(t, store, "finalize-generic-error")
	clock := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	finalizer := finalizerFunc(func(context.Context, Candidate) (FinalizeResult, error) {
		return FinalizeResult{}, fmt.Errorf("protected CAS rejected: stale ref")
	})
	worker := Worker{Store: store, Deps: ProcessDeps{Integration: specIntegration(), Gate: passingGate{}, Finalizer: finalizer, RetryDelay: time.Minute, MaxAttempts: 5, Now: func() time.Time { return clock }}}
	progressed, err := worker.RunOnce(context.Background())
	if err != nil || !progressed {
		t.Fatalf("progressed=%v err=%v", progressed, err)
	}
	got := wltGet(t, store, c.ID)
	if got.Phase != RetryWait || got.RetryReason != "finalization_failed" {
		t.Fatalf("candidate=%#v", got)
	}
	if !got.RetryAt.Equal(clock.Add(time.Minute)) {
		t.Fatalf("retry_at=%v want %v", got.RetryAt, clock.Add(time.Minute))
	}
	if !strings.Contains(got.Failure, "stale ref") {
		t.Fatalf("failure=%q", got.Failure)
	}
}

func TestWltFinalizeHarnessErrorParksImmediately(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	c := wltReadyCandidate(t, store, "finalize-harness")
	finalizer := finalizerFunc(func(context.Context, Candidate) (FinalizeResult, error) {
		return FinalizeResult{}, Harness(fmt.Errorf("finalizer harness missing binary"))
	})
	worker := Worker{Store: store, Deps: ProcessDeps{Integration: specIntegration(), Gate: passingGate{}, Finalizer: finalizer}}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := wltGet(t, store, c.ID)
	if got.Phase != NeedsInput || got.RetryReason != "finalization_harness_failure" {
		t.Fatalf("candidate=%#v", got)
	}
}

func TestWltFinalizeEnvErrorRetriesShortWithoutBurningAttemptBudget(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	c := wltReadyCandidate(t, store, "finalize-env")
	wltMutate(t, store, c.ID, func(cand *Candidate) { cand.Attempt = 3 })
	clock := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	finalizer := finalizerFunc(func(context.Context, Candidate) (FinalizeResult, error) {
		return FinalizeResult{}, Environmental(fmt.Errorf("protected checkout fetch: lock held"))
	})
	worker := Worker{Store: store, Deps: ProcessDeps{Integration: specIntegration(), Gate: passingGate{}, Finalizer: finalizer, EnvRetryDelay: 30 * time.Second, MaxEnvDuration: time.Hour, Now: func() time.Time { return clock }}}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := wltGet(t, store, c.ID)
	if got.Phase != RetryWait || got.RetryReason != "finalization_failed" {
		t.Fatalf("candidate=%#v", got)
	}
	if got.Attempt != 2 {
		t.Fatalf("environmental finalization failure should roll back the claim-time attempt increment: attempt=%d", got.Attempt)
	}
	if !got.RetryAt.Equal(clock.Add(30 * time.Second)) {
		t.Fatalf("retry_at=%v want %v", got.RetryAt, clock.Add(30*time.Second))
	}
	if got.EnvRetries != 1 || got.FirstEnvFailureAt.IsZero() {
		t.Fatalf("environmental retry bookkeeping missing: %#v", got)
	}
}

// TestWltFinalizeOnlyAdvancesFIFOHeadLeavingLaterCandidateUntouched covers
// (d): with two candidates ready to finalize, only the FIFO head must be
// finalized on a single RunOnce pass; the later candidate stays completely
// untouched (still ready_to_finalize, no lease taken) until its turn.
func TestWltFinalizeOnlyAdvancesFIFOHeadLeavingLaterCandidateUntouched(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	head := wltReadyCandidate(t, store, "fifo-head")
	tail := wltReadyCandidate(t, store, "fifo-tail")
	var finalized []string
	finalizer := finalizerFunc(func(_ context.Context, c Candidate) (FinalizeResult, error) {
		finalized = append(finalized, c.ID)
		return FinalizeResult{NewMainSHA: c.TreeSHA}, nil
	})
	worker := Worker{Store: store, Deps: ProcessDeps{Integration: specIntegration(), Gate: passingGate{}, Finalizer: finalizer}}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{head.ID}; !reflect.DeepEqual(finalized, want) {
		t.Fatalf("finalized=%v want=%v", finalized, want)
	}
	gotHead := wltGet(t, store, head.ID)
	gotTail := wltGet(t, store, tail.ID)
	if gotHead.Phase != Landed {
		t.Fatalf("head did not land: %#v", gotHead)
	}
	if gotTail.Phase != ReadyToFinalize || gotTail.WorkerID != "" {
		t.Fatalf("tail candidate was touched before its turn: %#v", gotTail)
	}
}

// TestWltFinalizeEmergencyCandidateBypassesNormalFIFOOrder covers (d)'s
// emergency-lane variant: a later-submitted candidate marked with a
// non-zero EmergencySequence must finalize ahead of an earlier, non-emergency
// candidate — before()'s emergency-lane-first ordering, exercised through
// the real finalize() head-selection path.
func TestWltFinalizeEmergencyCandidateBypassesNormalFIFOOrder(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	_ = wltReadyCandidate(t, store, "fifo-normal")
	emergency := wltReadyCandidate(t, store, "fifo-emergency")
	wltMutate(t, store, emergency.ID, func(cand *Candidate) { cand.EmergencySequence = 1 })
	var finalized []string
	finalizer := finalizerFunc(func(_ context.Context, c Candidate) (FinalizeResult, error) {
		finalized = append(finalized, c.ID)
		return FinalizeResult{NewMainSHA: c.TreeSHA}, nil
	})
	worker := Worker{Store: store, Deps: ProcessDeps{Integration: specIntegration(), Gate: passingGate{}, Finalizer: finalizer}}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if want := []string{emergency.ID}; !reflect.DeepEqual(finalized, want) {
		t.Fatalf("emergency candidate did not bypass FIFO order: finalized=%v want=%v", finalized, want)
	}
}

// --- update --------------------------------------------------------------------

func TestWltUpdateRejectsWrongTargetCandidate(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	sha := wltSHA(t)
	c, err := store.Submit(Submit{Branch: "agent/target", SHA: sha, Receipt: testReceipt(t, sha), TargetRef: "wave/alpha"})
	if err != nil {
		t.Fatal(err)
	}
	w := Worker{Store: store, Deps: ProcessDeps{TargetRef: "wave/beta"}}
	if err := w.update(c.ID, func(*State, *Candidate) {}); err == nil || !strings.Contains(err.Error(), "refuses candidate") {
		t.Fatalf("err=%v", err)
	}
}

func TestWltUpdateErrorsWhenCandidateDisappeared(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	w := Worker{Store: store}
	if err := w.update("missing-candidate", func(*State, *Candidate) {}); err == nil || !strings.Contains(err.Error(), "disappeared") {
		t.Fatalf("err=%v", err)
	}
}
