package queue

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests pin the productized POG merge-queue semantics: bounded retries
// with durable backoff, back-of-line requeue, the emergency lane, and the
// operator verbs that guarantee no state is ever human-inescapable.

func failingGate() Gate {
	return gateFunc(func(context.Context, Speculation) (GateResult, error) {
		return GateResult{Passed: false, Evidence: []string{"gate:red"}}, nil
	})
}

func specIntegration() *fakeIntegration {
	return &fakeIntegration{speculate: func(_ context.Context, c Candidate, _ []Candidate) (Speculation, error) {
		return Speculation{SHA: "spec-" + c.SHA}, nil
	}}
}

func TestFailedCandidateBacksOffExponentiallyAndParksAtMaxAttempts(t *testing.T) {
	store, first, _ := queuedPair(t)
	clock := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	deps := ProcessDeps{Integration: specIntegration(), Gate: failingGate(), MaxAttempts: 3, RetryDelay: time.Minute, MaxRetryDelay: 3 * time.Minute, Now: func() time.Time { return clock }}
	worker := Worker{Store: store, Deps: deps}

	expectDelay := []time.Duration{time.Minute, 2 * time.Minute}
	for attempt := 1; attempt <= 2; attempt++ {
		for {
			progressed, err := worker.RunOnce(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !progressed {
				break
			}
		}
		c := mustGet(t, store, first.ID)
		if c.phase() != RetryWait || c.Attempt != attempt {
			t.Fatalf("attempt %d: phase=%s attempt=%d", attempt, c.phase(), c.Attempt)
		}
		if got := c.RetryAt.Sub(clock); got != expectDelay[attempt-1] {
			t.Fatalf("attempt %d: backoff=%s want %s", attempt, got, expectDelay[attempt-1])
		}
		clock = c.RetryAt.Add(time.Second)
	}
	for {
		progressed, err := worker.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !progressed {
			break
		}
	}
	c := mustGet(t, store, first.ID)
	// No Repairer configured for a gate failure, so exhaustion stays
	// ReasonGateFailed rather than escalating to ReasonRepairerExhausted.
	if c.phase() != NeedsHuman || c.RetryReason != "max_attempts_exhausted" || c.ReasonCode != ReasonGateFailed {
		t.Fatalf("exhausted candidate: phase=%s reason=%s code=%s", c.phase(), c.RetryReason, c.ReasonCode)
	}
}

// TestEnvironmentalSpeculationFailureRetriesWithoutBurningAttempts pins the
// classification split a plain speculation_failed used to lack: a
// queue.Environmental-wrapped error (a missing object not yet fetched, a
// workspace-create race — never a red gate) gets a short fixed backoff and
// never burns the bounded product-failure attempt budget, unlike
// TestFailedCandidateBacksOffExponentiallyAndParksAtMaxAttempts above.
//
// Each attempt's fetch failure names a different missing object — real
// transient fetch races don't reproduce the identical message twice in a
// row — so retryOrParkEnv's repeat-streak bound (see
// TestEnvironmentalFailureParksAfterRepeatedIdenticalOutcome) never trips
// here; this test is only about the wall-clock/attempt-count split.
func TestEnvironmentalSpeculationFailureRetriesWithoutBurningAttempts(t *testing.T) {
	sha := strings.Repeat("a", 40)
	store := Store{ProjectRoot: t.TempDir()}
	candidate, err := store.Submit(Submit{Branch: "agent/a", SHA: sha, Receipt: testReceipt(t, sha)})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	fetchCall := 0
	integration := &fakeIntegration{speculate: func(context.Context, Candidate, []Candidate) (Speculation, error) {
		fetchCall++
		return Speculation{}, Environmental(fmt.Errorf("fetch failed: object %d not a valid object", fetchCall))
	}}
	deps := ProcessDeps{Integration: integration, Gate: passingGate{}, EnvRetryDelay: 30 * time.Second, MaxEnvDuration: time.Hour, Now: func() time.Time { return clock }}
	worker := Worker{Store: store, Deps: deps}

	for attempt := 1; attempt <= 3; attempt++ {
		progressed, err := worker.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !progressed {
			t.Fatalf("attempt %d: expected worker to make progress", attempt)
		}
		c := mustGet(t, store, candidate.ID)
		if c.phase() != RetryWait || c.Attempt != 0 {
			t.Fatalf("attempt %d: phase=%s attempt=%d (want product attempt budget untouched)", attempt, c.phase(), c.Attempt)
		}
		if c.EnvRetries != attempt {
			t.Fatalf("attempt %d: env_retries=%d, want %d", attempt, c.EnvRetries, attempt)
		}
		if c.EnvRepeatStreak != 1 {
			t.Fatalf("attempt %d: env_repeat_streak=%d, want 1 (each attempt's message differs, so the repeat-streak bound must never advance)", attempt, c.EnvRepeatStreak)
		}
		if got := c.RetryAt.Sub(clock); got != 30*time.Second {
			t.Fatalf("attempt %d: env backoff=%s, want fixed 30s (not exponential)", attempt, got)
		}
		clock = c.RetryAt.Add(time.Second)
	}
}

// TestEnvironmentalFailureParksAfterWallClockBoundNotAttemptCount pins the
// other half of the same policy: a candidate stuck in a persistently
// degraded environment still eventually parks, bounded by wall-clock time
// since the failure streak began rather than by an attempt count that
// environmental failures deliberately do not consume.
//
// This test's fixture happens to repeat the identical failure message on
// every attempt, which is exactly what retryOrParkEnv's separate
// repeat-streak bound is designed to catch (see
// TestEnvironmentalFailureParksAfterRepeatedIdenticalOutcome) — so MaxEnvRepeat
// is set generously high here to isolate the wall-clock axis under test from
// that other, faster-tripping axis.
func TestEnvironmentalFailureParksAfterWallClockBoundNotAttemptCount(t *testing.T) {
	sha := strings.Repeat("a", 40)
	store := Store{ProjectRoot: t.TempDir()}
	candidate, err := store.Submit(Submit{Branch: "agent/a", SHA: sha, Receipt: testReceipt(t, sha)})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	integration := &fakeIntegration{speculate: func(context.Context, Candidate, []Candidate) (Speculation, error) {
		return Speculation{}, Environmental(fmt.Errorf("workspace create: lock held"))
	}}
	deps := ProcessDeps{Integration: integration, Gate: passingGate{}, EnvRetryDelay: 30 * time.Second, MaxEnvDuration: 90 * time.Second, MaxEnvRepeat: 1000, Now: func() time.Time { return clock }}
	worker := Worker{Store: store, Deps: deps}

	var c Candidate
	for i := 0; i < 10; i++ {
		progressed, err := worker.RunOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !progressed {
			t.Fatalf("iteration %d: expected worker to make progress", i)
		}
		c = mustGet(t, store, candidate.ID)
		if c.phase() == NeedsHuman {
			break
		}
		if c.Attempt != 0 {
			t.Fatalf("iteration %d: environmental failure burned the attempt budget: %d", i, c.Attempt)
		}
		clock = c.RetryAt.Add(time.Second)
	}
	// Environmental exhaustion is automation giving up, not an operator's
	// own park: needs_human, not needs_input.
	if c.phase() != NeedsHuman {
		t.Fatalf("candidate never parked after the wall-clock bound: phase=%s", c.phase())
	}
	if !strings.Contains(c.RetryReason, "environment_degraded") || c.ReasonCode != ReasonEnvironmentDegraded {
		t.Fatalf("retry reason=%q code=%q, want it to identify environment degradation, not max attempts", c.RetryReason, c.ReasonCode)
	}
	if c.Attempt != 0 {
		t.Fatalf("parked candidate burned the product attempt budget: %d", c.Attempt)
	}
}

// TestEnvironmentalFailureStreakResetsOnSuccessfulPreparation guards against
// stale streak state: once a candidate clears an environmental blip and
// prepares cleanly, EnvRetries/FirstEnvFailureAt — and, since this fix,
// EnvFailureSignature/EnvRepeatStreak — must not linger to bias a later,
// unrelated environmental failure's wall-clock or repeat-streak bound.
func TestEnvironmentalFailureStreakResetsOnSuccessfulPreparation(t *testing.T) {
	sha := strings.Repeat("a", 40)
	store := Store{ProjectRoot: t.TempDir()}
	candidate, err := store.Submit(Submit{Branch: "agent/a", SHA: sha, Receipt: testReceipt(t, sha)})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	failed := false
	integration := &fakeIntegration{speculate: func(_ context.Context, c Candidate, _ []Candidate) (Speculation, error) {
		if !failed {
			failed = true
			return Speculation{}, Environmental(fmt.Errorf("fetch failed"))
		}
		return Speculation{SHA: "spec-" + c.SHA}, nil
	}}
	deps := ProcessDeps{Integration: integration, Gate: passingGate{}, EnvRetryDelay: 30 * time.Second, MaxEnvDuration: time.Hour, Now: func() time.Time { return clock }}
	worker := Worker{Store: store, Deps: deps}

	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := mustGet(t, store, candidate.ID)
	if c.EnvRetries != 1 || c.FirstEnvFailureAt.IsZero() {
		t.Fatalf("expected the first environmental failure to be recorded: %#v", c)
	}
	if c.EnvFailureSignature != "fetch failed" || c.EnvRepeatStreak != 1 {
		t.Fatalf("expected the failure signature/streak to be recorded: signature=%q streak=%d", c.EnvFailureSignature, c.EnvRepeatStreak)
	}
	clock = c.RetryAt.Add(time.Second)

	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	c = mustGet(t, store, candidate.ID)
	if c.phase() != ReadyToFinalize {
		t.Fatalf("expected candidate to reach ready_to_finalize, got %s", c.phase())
	}
	if c.EnvRetries != 0 || !c.FirstEnvFailureAt.IsZero() {
		t.Fatalf("expected the environmental streak to reset on success: env_retries=%d first_env_failure_at=%v", c.EnvRetries, c.FirstEnvFailureAt)
	}
	if c.EnvFailureSignature != "" || c.EnvRepeatStreak != 0 {
		t.Fatalf("expected the failure signature/streak to reset on success: signature=%q streak=%d", c.EnvFailureSignature, c.EnvRepeatStreak)
	}
}

func TestKickClearsRetryTimerWithoutTouchingAttempts(t *testing.T) {
	store, first, _ := queuedPair(t)
	drain(t, store, ProcessDeps{Integration: specIntegration(), Gate: failingGate()})
	c := mustGet(t, store, first.ID)
	if c.phase() != RetryWait || c.RetryAt.IsZero() {
		t.Fatalf("precondition: %s retry_at=%v", c.phase(), c.RetryAt)
	}
	kicked, err := store.Kick(Op{ID: first.ID, Actor: "brad"})
	if err != nil {
		t.Fatal(err)
	}
	if !kicked.RetryAt.IsZero() || kicked.Attempt != c.Attempt {
		t.Fatalf("kick mutated more than the timer: %#v", kicked)
	}
	if !hasEvidence(kicked, "queue:kick by brad") {
		t.Fatalf("kick not audited: %v", kicked.Evidence)
	}
	if _, err := store.Kick(Op{ID: first.ID, Actor: "brad"}); err != nil {
		// second kick on still-retry_wait candidate is fine
		t.Fatal(err)
	}
	if _, err := store.Kick(Op{ID: "queue-missing", Actor: "brad"}); err == nil {
		t.Fatal("kick on unknown candidate must error")
	}
}

func TestParkedHeadNeverBlocksTheTrain(t *testing.T) {
	store, first, second := queuedPair(t)
	if _, err := store.Park(Op{ID: first.ID, Actor: "brad", Reason: "known-bad"}); err != nil {
		t.Fatal(err)
	}
	integration := specIntegration()
	state, err := store.Process(context.Background(), ProcessDeps{Integration: integration, Gate: passingGate{}})
	if err != nil {
		t.Fatal(err)
	}
	byID := index(state)
	if byID[second.ID].phase() != Landed {
		t.Fatalf("second candidate did not land past parked head: %#v", state.Candidates)
	}
	if byID[first.ID].phase() != NeedsInput {
		t.Fatalf("parked head changed: %s", byID[first.ID].phase())
	}
}

func TestResumeRestoresParkedCandidateWithFreshAttemptBudget(t *testing.T) {
	store, first, _ := queuedPair(t)
	drain(t, store, ProcessDeps{Integration: specIntegration(), Gate: failingGate(), MaxAttempts: 1})
	c := mustGet(t, store, first.ID)
	// The attempt budget exhausted itself (no Repairer configured), so the
	// candidate parked as needs_human, not an operator's own needs_input.
	if c.phase() != NeedsHuman || c.ReasonCode != ReasonGateFailed {
		t.Fatalf("precondition: phase=%s code=%s", c.phase(), c.ReasonCode)
	}
	resumed, err := store.Resume(Op{ID: first.ID, Actor: "brad"})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.phase() != Queued || resumed.Attempt != 0 || !resumed.RetryAt.IsZero() {
		t.Fatalf("resume: %#v", resumed)
	}
	if resumed.ReasonCode != "" || resumed.NeedsHumanEvidenceRef != "" {
		t.Fatalf("resume did not clear the needs_human classification: %#v", resumed)
	}
	// After a repair, the candidate lands.
	state, err := store.Process(context.Background(), ProcessDeps{Integration: specIntegration(), Gate: passingGate{}})
	if err != nil {
		t.Fatal(err)
	}
	if index(state)[first.ID].phase() != Landed {
		t.Fatalf("resumed candidate did not land: %#v", state.Candidates)
	}
}

func TestEmergencyLaneJumpsTheQueueButIsFIFOWithinItself(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	var ids []string
	for _, ch := range []string{"a", "b", "c", "d"} {
		sha := strings.Repeat(ch, 40)
		c, err := store.Submit(Submit{Branch: "agent/" + ch, SHA: sha, Receipt: testReceipt(t, sha)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.ID)
	}
	// Mark d then c emergency: d outranks c, both outrank a and b.
	if _, err := store.MarkEmergency(Op{ID: ids[3], Actor: "brad"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkEmergency(Op{ID: ids[2], Actor: "brad"}); err != nil {
		t.Fatal(err)
	}
	var order []string
	integration := &fakeIntegration{speculate: func(_ context.Context, c Candidate, _ []Candidate) (Speculation, error) {
		order = append(order, c.ID)
		return Speculation{SHA: "spec-" + c.SHA}, nil
	}}
	if _, err := store.Process(context.Background(), ProcessDeps{Integration: integration, Gate: passingGate{}}); err != nil {
		t.Fatal(err)
	}
	want := []string{ids[3], ids[2], ids[0], ids[1]}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Fatalf("claim order=%v want %v", order, want)
	}
}

func TestOverrideLandsParkedCandidateWithoutGateAndIsAudited(t *testing.T) {
	store, first, _ := queuedPair(t)
	deps := ProcessDeps{Integration: specIntegration(), Gate: failingGate(), MaxAttempts: 1}
	drain(t, store, deps)
	if mustGet(t, store, first.ID).phase() != NeedsHuman {
		t.Fatal("precondition failed")
	}
	if _, err := store.Override(Op{ID: first.ID, Actor: "brad", Reason: "hotfix outage"}); err != nil {
		t.Fatal(err)
	}
	// Gate still red for everyone else; the override candidate lands anyway.
	state, err := store.Process(context.Background(), ProcessDeps{Integration: specIntegration(), Gate: failingGate()})
	if err != nil {
		t.Fatal(err)
	}
	c := index(state)[first.ID]
	if c.phase() != Landed {
		t.Fatalf("override candidate did not land: %#v", c)
	}
	if c.GateVersion != "operator-override/v1" || c.OverrideBy != "brad" {
		t.Fatalf("override not durably attributed: %#v", c)
	}
	if !hasEvidence(c, "queue:gate-overridden-by=brad") {
		t.Fatalf("waiver missing from evidence: %v", c.Evidence)
	}
}

// TestOverrideResetsEnvironmentalBookkeepingLikeResume closes an asymmetry
// found alongside the retryOrParkEnv repeat-streak fix: Resume already resets
// env_retries/first_env_failure_at (see
// TestEnvironmentalFailureStreakResetsOnSuccessfulPreparation) when it
// returns a parked candidate to queued, but Override — the other operator
// verb with the identical parked/retry_wait-to-queued transition — did not.
// Left stale, an overridden candidate would carry a nonzero
// env_repeat_streak/first_env_failure_at into whatever it does next, biasing
// retryOrParkEnv's wall-clock and repeat-streak bounds against an unrelated
// later failure.
func TestOverrideResetsEnvironmentalBookkeepingLikeResume(t *testing.T) {
	sha := strings.Repeat("a", 40)
	store := Store{ProjectRoot: t.TempDir()}
	candidate, err := store.Submit(Submit{Branch: "agent/a", SHA: sha, Receipt: testReceipt(t, sha)})
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	integration := &fakeIntegration{speculate: func(context.Context, Candidate, []Candidate) (Speculation, error) {
		return Speculation{}, Environmental(fmt.Errorf("workspace create: lock held"))
	}}
	deps := ProcessDeps{Integration: integration, Gate: passingGate{}, EnvRetryDelay: time.Second, MaxEnvDuration: time.Hour, Now: func() time.Time { return clock }}
	worker := Worker{Store: store, Deps: deps}

	// The identical message on two consecutive attempts trips the
	// repeat-streak bound (default MaxEnvRepeat=2) and parks — this is the
	// precondition under test, not the thing being asserted here.
	var parkedPhase Status
	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := worker.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		c := mustGet(t, store, candidate.ID)
		parkedPhase = c.phase()
		if parkedPhase == NeedsInput {
			break
		}
		clock = c.RetryAt.Add(time.Second)
	}
	parked := mustGet(t, store, candidate.ID)
	if parkedPhase != NeedsInput || parked.EnvRepeatStreak < 2 || parked.EnvFailureSignature == "" || parked.FirstEnvFailureAt.IsZero() {
		t.Fatalf("precondition: expected candidate parked with environmental bookkeeping recorded: %#v", parked)
	}

	overridden, err := store.Override(Op{ID: candidate.ID, Actor: "brad", Reason: "hotfix"})
	if err != nil {
		t.Fatal(err)
	}
	if overridden.phase() != Queued {
		t.Fatalf("override: phase=%s, want queued", overridden.phase())
	}
	if overridden.EnvRetries != 0 || !overridden.FirstEnvFailureAt.IsZero() {
		t.Fatalf("override left stale env_retries/first_env_failure_at: %#v", overridden)
	}
	if overridden.EnvFailureSignature != "" || overridden.EnvRepeatStreak != 0 {
		t.Fatalf("override left stale env_failure_signature/env_repeat_streak: %#v", overridden)
	}
}

func TestHarnessFailureParksImmediatelyWithoutBurningRetries(t *testing.T) {
	store, first, second := queuedPair(t)
	integration := &fakeIntegration{speculate: func(_ context.Context, c Candidate, _ []Candidate) (Speculation, error) {
		if c.ID == first.ID {
			return Speculation{}, Harness(fmt.Errorf("resolver launch path missing app.yaml"))
		}
		return Speculation{SHA: "spec-" + c.SHA}, nil
	}}
	state, err := store.Process(context.Background(), ProcessDeps{Integration: integration, Gate: passingGate{}})
	if err != nil {
		t.Fatal(err)
	}
	byID := index(state)
	if byID[first.ID].phase() != NeedsHuman || byID[first.ID].RetryReason != "resolver_harness_failure" || byID[first.ID].ReasonCode != ReasonHarnessFailure {
		t.Fatalf("harness failure handling: %#v", byID[first.ID])
	}
	if byID[second.ID].phase() != Landed {
		t.Fatalf("harness-parked head blocked the train: %#v", byID[second.ID])
	}
}

func TestRejectIsTerminalButPreservesEvidence(t *testing.T) {
	store, first, _ := queuedPair(t)
	rejected, err := store.Reject(Op{ID: first.ID, Actor: "brad", Reason: "abandoned"})
	if err != nil {
		t.Fatal(err)
	}
	if rejected.phase() != Rejected || rejected.EjectionReason != "abandoned" {
		t.Fatalf("reject: %#v", rejected)
	}
	if _, err := store.Reject(Op{ID: first.ID, Actor: "brad"}); err == nil {
		t.Fatal("double reject must error")
	}
	if _, err := store.Resume(Op{ID: first.ID, Actor: "brad"}); err == nil {
		t.Fatal("resume of rejected candidate must error")
	}
}

func TestOperatorParkWinsOverInFlightWorkerResult(t *testing.T) {
	store, first, _ := queuedPair(t)
	release := make(chan struct{})
	started := make(chan struct{})
	integration := &fakeIntegration{speculate: func(_ context.Context, c Candidate, _ []Candidate) (Speculation, error) {
		if c.ID == first.ID {
			close(started)
			<-release
		}
		return Speculation{SHA: "spec-" + c.SHA}, nil
	}}
	worker := Worker{Store: store, Deps: ProcessDeps{Integration: integration, Gate: passingGate{}, Lease: time.Minute}}
	done := make(chan error, 1)
	go func() {
		_, err := worker.RunOnce(context.Background())
		done <- err
	}()
	<-started
	if _, err := store.Park(Op{ID: first.ID, Actor: "brad", Reason: "pulling this one"}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	c := mustGet(t, store, first.ID)
	if c.phase() != NeedsInput {
		t.Fatalf("worker result clobbered operator park: %s", c.phase())
	}
	if !hasEvidence(c, "result discarded; operator moved candidate") {
		t.Fatalf("discard not audited: %v", c.Evidence)
	}
}

func TestStewardApprovalHoldsThenPermitsExactlyOneFinalization(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	sha := strings.Repeat("7", 40)
	candidate, err := store.Submit(Submit{Branch: "wave/review", SHA: sha, Receipt: testReceipt(t, sha), FinalizationPolicy: StewardReviewFinalization, ManifestDigest: "sha256:manifest"})
	if err != nil {
		t.Fatal(err)
	}
	var finalized int
	worker := Worker{Store: store, Deps: ProcessDeps{
		Integration: specIntegration(), Gate: passingGate{}, GateVersion: "test-gate",
		Finalizer: finalizerFunc(func(context.Context, Candidate) (FinalizeResult, error) {
			finalized++
			return FinalizeResult{NewMainSHA: "landed-tree"}, nil
		}),
	}}
	if progressed, err := worker.RunOnce(context.Background()); err != nil || !progressed {
		t.Fatalf("prepare progressed=%v err=%v", progressed, err)
	}
	prepared := mustGet(t, store, candidate.ID)
	if prepared.phase() != AwaitingApproval || prepared.Approval != nil {
		t.Fatalf("prepared candidate = %#v", prepared)
	}
	if progressed, err := worker.RunOnce(context.Background()); err != nil || progressed {
		t.Fatalf("unapproved finalization progressed=%v err=%v", progressed, err)
	}
	if finalized != 0 {
		t.Fatalf("finalized before approval: %d", finalized)
	}
	approved, err := store.Approve(ApprovalOp{ID: candidate.ID, Actor: "brad", Reason: "reviewed", ManifestDigest: "sha256:manifest", TreeSHA: prepared.TreeSHA, ReceiptDigest: prepared.ReceiptDigest})
	if err != nil {
		t.Fatal(err)
	}
	if approved.phase() != ReadyToFinalize || approved.Approval == nil || !hasEvidence(approved, "queue:approve by brad") {
		t.Fatalf("approval = %#v", approved)
	}
	if progressed, err := worker.RunOnce(context.Background()); err != nil || !progressed {
		t.Fatalf("finalize progressed=%v err=%v", progressed, err)
	}
	if progressed, err := worker.RunOnce(context.Background()); err != nil || progressed {
		t.Fatalf("second finalization progressed=%v err=%v", progressed, err)
	}
	if finalized != 1 || mustGet(t, store, candidate.ID).phase() != Landed {
		t.Fatalf("finalizations=%d candidate=%#v", finalized, mustGet(t, store, candidate.ID))
	}
}

func TestStewardApprovalRejectsStaleIdentityWithoutMutation(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	sha := strings.Repeat("6", 40)
	candidate, err := store.Submit(Submit{Branch: "wave/review", SHA: sha, Receipt: testReceipt(t, sha), FinalizationPolicy: StewardReviewFinalization, ManifestDigest: "sha256:manifest", RequiredReceiptIDs: []string{"other-receipt"}})
	if err != nil {
		t.Fatal(err)
	}
	worker := Worker{Store: store, Deps: ProcessDeps{Integration: specIntegration(), Gate: passingGate{}, GateVersion: "test-gate"}}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := mustGet(t, store, candidate.ID)
	for _, op := range []ApprovalOp{
		{ID: candidate.ID, ManifestDigest: "sha256:stale"},
		{ID: candidate.ID, ManifestDigest: before.ManifestDigest, TreeSHA: "stale-tree"},
		{ID: candidate.ID, ManifestDigest: before.ManifestDigest, ReceiptDigest: "stale-receipt"},
		{ID: candidate.ID, ManifestDigest: before.ManifestDigest},
	} {
		if _, err := store.Approve(op); err == nil {
			t.Fatalf("approval %+v unexpectedly succeeded", op)
		}
		after := mustGet(t, store, candidate.ID)
		if after.phase() != AwaitingApproval || after.Approval != nil || fmt.Sprint(after.Evidence) != fmt.Sprint(before.Evidence) {
			t.Fatalf("failed approval mutated candidate: %#v", after)
		}
	}
}

func TestUnapproveWinsOverInFlightFinalization(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	sha := strings.Repeat("5", 40)
	candidate, err := store.Submit(Submit{Branch: "wave/review", SHA: sha, Receipt: testReceipt(t, sha), FinalizationPolicy: StewardReviewFinalization, ManifestDigest: "sha256:manifest"})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	worker := Worker{Store: store, Deps: ProcessDeps{Integration: specIntegration(), Gate: passingGate{}, GateVersion: "test-gate", Finalizer: finalizerFunc(func(context.Context, Candidate) (FinalizeResult, error) {
		close(entered)
		<-release
		return FinalizeResult{NewMainSHA: "landed-tree"}, nil
	})}}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	prepared := mustGet(t, store, candidate.ID)
	if _, err := store.Approve(ApprovalOp{ID: candidate.ID, ManifestDigest: prepared.ManifestDigest}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := worker.RunOnce(context.Background()); done <- err }()
	<-entered
	if _, err := store.Unapprove(Op{ID: candidate.ID, Actor: "brad", Reason: "new evidence"}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := mustGet(t, store, candidate.ID)
	if got.phase() != AwaitingApproval || got.ResultMainSHA != "" || got.Approval != nil || !hasEvidence(got, "result discarded; operator moved candidate") {
		t.Fatalf("unapprove was overwritten: %#v", got)
	}
}

func TestStaleFinalizationInvalidatesStewardApproval(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	sha := strings.Repeat("4", 40)
	candidate, err := store.Submit(Submit{Branch: "wave/review", SHA: sha, Receipt: testReceipt(t, sha), FinalizationPolicy: StewardReviewFinalization, ManifestDigest: "sha256:manifest"})
	if err != nil {
		t.Fatal(err)
	}
	worker := Worker{Store: store, Deps: ProcessDeps{Integration: specIntegration(), Gate: passingGate{}, GateVersion: "test-gate", Finalizer: finalizerFunc(func(context.Context, Candidate) (FinalizeResult, error) {
		return FinalizeResult{Stale: true, Log: "base moved"}, nil
	})}}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	prepared := mustGet(t, store, candidate.ID)
	if _, err := store.Approve(ApprovalOp{ID: candidate.ID, ManifestDigest: prepared.ManifestDigest}); err != nil {
		t.Fatal(err)
	}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := mustGet(t, store, candidate.ID)
	if got.phase() != Reprepare || got.Approval != nil || !hasEvidence(got, "approval invalidated by repreparation") {
		t.Fatalf("stale finalization did not invalidate approval: %#v", got)
	}
}

func TestResubmissionSupersedesActiveCandidateAndInheritsAttempts(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	sha := strings.Repeat("a", 40)
	first, err := store.Submit(Submit{Branch: "agent/a", SHA: sha, Receipt: testReceipt(t, sha)})
	if err != nil {
		t.Fatal(err)
	}
	drain(t, store, ProcessDeps{Integration: specIntegration(), Gate: failingGate()})
	if mustGet(t, store, first.ID).Attempt != 1 {
		t.Fatal("precondition: attempt not recorded")
	}
	// Same SHA, emergency admission => different identity => supersede.
	second, err := store.Submit(Submit{Branch: "agent/a", SHA: sha, Admission: EmergencySkipTestsAdmission})
	if err != nil {
		t.Fatal(err)
	}
	if second.Attempt != 1 {
		t.Fatalf("attempts were reset on resubmission: %#v", second)
	}
	old := mustGet(t, store, first.ID)
	if old.phase() != Rejected || old.EjectionReason != "superseded_by_resubmission" {
		t.Fatalf("prior candidate not superseded: %#v", old)
	}
}

// Concurrent workers plus concurrent operator traffic must corrupt nothing:
// every candidate ends terminal-or-parked exactly once, state stays parseable,
// and the state lock serializes every mutation. Run with -race.
func TestConcurrentWorkersAndOperatorsNoRaces(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir(), LockWait: 10 * time.Second}
	var ids []string
	for i := 0; i < 8; i++ {
		sha := strings.Repeat(fmt.Sprintf("%x", i+1)[:1], 40)
		c, err := store.Submit(Submit{Branch: fmt.Sprintf("agent/%d", i), SHA: sha, Receipt: testReceipt(t, sha)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, c.ID)
	}
	integration := &fakeIntegration{speculate: func(_ context.Context, c Candidate, _ []Candidate) (Speculation, error) {
		return Speculation{SHA: "spec-" + c.SHA}, nil
	}}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			w := Worker{Store: store, Deps: ProcessDeps{Integration: integration, Gate: passingGate{}, WorkerID: fmt.Sprintf("w%d", n), Lease: 5 * time.Second}}
			for {
				progressed, err := w.RunOnce(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				if !progressed {
					state, err := store.List()
					if err != nil {
						t.Error(err)
						return
					}
					allDone := true
					for _, c := range state.Candidates {
						if !terminal(c.phase()) && !parked(c.phase()) {
							allDone = false
						}
					}
					if allDone {
						return
					}
				}
			}
		}(i)
	}
	// Operator traffic against the same store while workers run.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for _, id := range ids[:2] {
			_, _ = store.MarkEmergency(Op{ID: id, Actor: "brad"})
		}
		for _, id := range ids {
			_, _ = store.Get(id)
		}
	}()
	wg.Wait()
	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	landed := 0
	for _, c := range state.Candidates {
		switch c.phase() {
		case Landed:
			landed++
		case Rejected, NeedsInput:
		default:
			t.Fatalf("candidate %s stuck in %s", c.ID, c.phase())
		}
	}
	if landed != len(ids) {
		t.Fatalf("landed=%d want %d: %#v", landed, len(ids), state.Candidates)
	}
}

func drain(t *testing.T, store Store, deps ProcessDeps) {
	t.Helper()
	if _, err := store.Process(context.Background(), deps); err != nil {
		t.Fatal(err)
	}
}

func mustGet(t *testing.T, store Store, id string) Candidate {
	t.Helper()
	c, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func index(state State) map[string]Candidate {
	out := map[string]Candidate{}
	for _, c := range state.Candidates {
		out[c.ID] = c
	}
	return out
}

func hasEvidence(c Candidate, needle string) bool {
	for _, e := range c.Evidence {
		if strings.Contains(e, needle) {
			return true
		}
	}
	return false
}
