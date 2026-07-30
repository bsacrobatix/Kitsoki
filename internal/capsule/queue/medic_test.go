package queue

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// This file pins P1.7 part 2: the merge-queue medic. Every top-level
// identifier here is prefixed Md to avoid collisions with other agents'
// concurrently-added test files in this package.

// mdClock returns a MedicDeps.Now func fixed at t, for deterministic
// deadline arithmetic.
func mdClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// mdOnlyCandidate returns the sole candidate in store, failing the test if
// there is not exactly one.
func mdOnlyCandidate(t *testing.T, store Store) Candidate {
	t.Helper()
	state, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Candidates) != 1 {
		t.Fatalf("want exactly one candidate, got %d: %#v", len(state.Candidates), state.Candidates)
	}
	return state.Candidates[0]
}

// TestMdConflictCandidateGetsExactlyOneResolverDispatch is the real
// end-to-end case: a genuinely conflicting candidate parks at
// needs_conflict_input (the configured resolver runs but leaves the
// conflict markers in place — a no-op resolver policy, not a launch
// failure). One MedicRunOnce dispatches it back to queued exactly once. A
// second, immediate MedicRunOnce pass — simulating the very next worker
// cycle before anything has reprocessed the candidate — must not dispatch
// again: the candidate is no longer needs_conflict_input.
func TestMdConflictCandidateGetsExactlyOneResolverDispatch(t *testing.T) {
	store, root, _ := conflictingCandidate(t)
	deps := ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: root, TargetRef: "main", ResolverCommand: "true"},
		Gate:        ShellGate{Command: "git diff --check"},
		Finalizer:   ProtectedFinalizer{ProjectRoot: root, TargetRef: "main"},
	}
	drain(t, store, deps)
	c := mdOnlyCandidate(t, store)
	if c.phase() != NeedsConflictInput {
		t.Fatalf("precondition: phase=%s evidence=%v", c.phase(), c.Evidence)
	}

	clock := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	medicDeps := MedicDeps{Now: mdClock(clock), MedicID: "medic-1", MaxDispatches: 3, Deadline: 2 * time.Hour}
	result, err := store.MedicRunOnce(medicDeps)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Verb != "dispatch_resolver" || result.Actions[0].CandidateID != c.ID {
		t.Fatalf("actions=%#v", result.Actions)
	}
	c = mustGet(t, store, c.ID)
	if c.phase() != Queued || c.MedicDispatches != 1 || c.MedicFirstDispatchAt.IsZero() || c.MedicLastAction != "dispatch_resolver" || c.MedicLastBy != "medic-1" {
		t.Fatalf("after dispatch: %#v", c)
	}
	if !hasEvidence(c, "queue:medic dispatch_resolver by medic-1") {
		t.Fatalf("evidence=%v", c.Evidence)
	}

	// Second immediate pass: the candidate is queued now, not
	// needs_conflict_input, so nothing should happen.
	result2, err := store.MedicRunOnce(medicDeps)
	if err != nil {
		t.Fatal(err)
	}
	if len(result2.Actions) != 0 {
		t.Fatalf("second immediate pass must not double-dispatch: actions=%#v", result2.Actions)
	}
	c2 := mustGet(t, store, c.ID)
	if c2.MedicDispatches != 1 {
		t.Fatalf("dispatch count changed on a no-op pass: %d", c2.MedicDispatches)
	}
}

// TestMdRestartMidCycleDoesNotDoubleDispatch simulates the worker process
// dying immediately after a medic dispatch lands durably, then restarting:
// a brand-new Store value against the same directory (no shared in-memory
// state at all) must not re-dispatch the candidate its predecessor already
// moved out of needs_conflict_input. Store itself carries no in-process
// cache (just ProjectRoot/QueueRoot/LockWait), so this mainly pins that the
// durable state.json — not anything held in memory — is the single source
// of truth a restarted process reads; see
// TestMdConcurrentMedicRunOnceSingleFlightsDispatch below for the sharper
// property (true concurrent access serialized through the same file lock,
// racing for the actual crash window this test cannot construct because
// MedicRunOnce's write is a single atomic operation).
func TestMdRestartMidCycleDoesNotDoubleDispatch(t *testing.T) {
	store, root, _ := conflictingCandidate(t)
	deps := ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: root, TargetRef: "main", ResolverCommand: "true"},
		Gate:        ShellGate{Command: "git diff --check"},
		Finalizer:   ProtectedFinalizer{ProjectRoot: root, TargetRef: "main"},
	}
	drain(t, store, deps)
	c := mdOnlyCandidate(t, store)
	if c.phase() != NeedsConflictInput {
		t.Fatalf("precondition: phase=%s", c.phase())
	}

	medicDeps := MedicDeps{Now: mdClock(time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC))}

	firstProcess := Store{ProjectRoot: root}
	if _, err := firstProcess.MedicRunOnce(medicDeps); err != nil {
		t.Fatal(err)
	}

	restarted := Store{ProjectRoot: root}
	result, err := restarted.MedicRunOnce(medicDeps)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) != 0 {
		t.Fatalf("restarted process re-dispatched an already-dispatched candidate: %#v", result.Actions)
	}
	got := mustGet(t, restarted, c.ID)
	if got.MedicDispatches != 1 {
		t.Fatalf("dispatch count = %d, want exactly 1 across both processes", got.MedicDispatches)
	}
}

// TestMdResolverSucceedsCandidateProceedsAfterDispatch proves the medic's
// dispatch is genuinely productive, not just a phase shuffle: a resolver
// that fails the first time (leaving the conflict marker in place) but
// succeeds on the next run — simulated with a marker file the fake resolver
// command checks, exactly the shape resolver_test.go's fakes use, no LLM —
// lets the candidate actually land once the medic gives the worker another
// shot at it.
func TestMdResolverSucceedsCandidateProceedsAfterDispatch(t *testing.T) {
	store, root, candidateSHA := conflictingCandidate(t)
	marker := filepath.Join(t.TempDir(), "resolved-after-medic-dispatch")
	resolverCmd := fmt.Sprintf(`if [ -f %q ]; then printf 'resolved\n' > base.txt; else touch %q; exit 1; fi`, marker, marker)
	deps := ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: root, TargetRef: "main", ResolverCommand: resolverCmd},
		Gate:        ShellGate{Command: "git diff --check"},
		Finalizer:   ProtectedFinalizer{ProjectRoot: root, TargetRef: "main"},
	}
	drain(t, store, deps)
	c := mdOnlyCandidate(t, store)
	if c.phase() != NeedsConflictInput {
		t.Fatalf("precondition: phase=%s evidence=%v", c.phase(), c.Evidence)
	}

	if _, err := store.MedicRunOnce(MedicDeps{Now: mdClock(time.Now().UTC())}); err != nil {
		t.Fatal(err)
	}
	c = mustGet(t, store, c.ID)
	if c.phase() != Queued {
		t.Fatalf("after dispatch: phase=%s", c.phase())
	}

	// The next worker pass re-runs Speculate -> resolveConflicts; the
	// marker now exists, so the resolver actually fixes base.txt this time.
	drain(t, store, deps)
	c = mustGet(t, store, c.ID)
	if c.phase() != Landed {
		t.Fatalf("candidate did not proceed to landed after medic dispatch + successful resolver retry: %#v", c)
	}
	if got := strings.TrimSpace(git(t, root, "show", "main:base.txt")); got != "resolved" {
		t.Fatalf("main content=%q", got)
	}
	git(t, root, "merge-base", "--is-ancestor", candidateSHA, "main")
}

// TestMdDeadlineExceededEscalatesBudgetExhausted pins the wall-clock ceiling:
// even with dispatches well under MaxDispatches, exceeding Deadline since
// the first dispatch escalates straight to needs_human with the generic
// ReasonBudgetExhausted — never the more specific resolver/repairer code,
// because "ran out of time" and "kept failing" are different claims.
func TestMdDeadlineExceededEscalatesBudgetExhausted(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	c := wltSubmit(t, store, "conflict-deadline")
	firstDispatchAt := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	wltMutate(t, store, c.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = NeedsConflictInput, NeedsConflictInput
		cand.ConflictContinuation = "cont-1"
		cand.MedicDispatches = 1
		cand.MedicFirstDispatchAt = firstDispatchAt
	})

	now := firstDispatchAt.Add(3 * time.Hour) // past the 2h deadline below
	result, err := store.MedicRunOnce(MedicDeps{Now: mdClock(now), MedicID: "medic-1", MaxDispatches: 3, Deadline: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Verb != "escalate" {
		t.Fatalf("actions=%#v", result.Actions)
	}
	got := mustGet(t, store, c.ID)
	if got.phase() != NeedsHuman || got.ReasonCode != ReasonBudgetExhausted || got.RetryReason != "medic_deadline_exceeded" {
		t.Fatalf("escalated candidate: phase=%s code=%s reason=%s", got.phase(), got.ReasonCode, got.RetryReason)
	}
	if got.ParkedBy != "medic-1" || got.ParkedAt.IsZero() {
		t.Fatalf("parked attribution: by=%q at=%v", got.ParkedBy, got.ParkedAt)
	}
	if got.NeedsHumanEvidenceRef == "" {
		t.Fatalf("expected a needs_human evidence pointer")
	}
	if got.MedicDispatches != 1 {
		t.Fatalf("escalation must not clear dispatch history: dispatches=%d", got.MedicDispatches)
	}
	if !hasEvidence(got, "queue:medic escalate by medic-1") {
		t.Fatalf("evidence=%v", got.Evidence)
	}
}

// TestMdDispatchCountExhaustionEscalatesResolverExhausted pins the
// dispatch-count ceiling (distinct from the deadline above): well within
// the wall-clock deadline, reaching MaxDispatches escalates with the
// specific ReasonResolverExhausted.
func TestMdDispatchCountExhaustionEscalatesResolverExhausted(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	c := wltSubmit(t, store, "conflict-exhaust")
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	wltMutate(t, store, c.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = NeedsConflictInput, NeedsConflictInput
		cand.ConflictContinuation = "cont-1"
		cand.MedicDispatches = 3
		cand.MedicFirstDispatchAt = start
	})

	now := start.Add(5 * time.Minute)
	result, err := store.MedicRunOnce(MedicDeps{Now: mdClock(now), MaxDispatches: 3, Deadline: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Verb != "escalate" {
		t.Fatalf("actions=%#v", result.Actions)
	}
	got := mustGet(t, store, c.ID)
	if got.phase() != NeedsHuman || got.ReasonCode != ReasonResolverExhausted || got.RetryReason != "medic_resolver_exhausted" {
		t.Fatalf("phase=%s code=%s reason=%s", got.phase(), got.ReasonCode, got.RetryReason)
	}
}

// TestMdNeverTouchesNeedsHumanCandidates is the required guardrail test: a
// candidate automation already escalated must never be re-examined by the
// medic, regardless of configuration.
func TestMdNeverTouchesNeedsHumanCandidates(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	c := wltSubmit(t, store, "already-needs-human")
	before := wltMutate(t, store, c.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = NeedsHuman, NeedsHuman
		cand.ReasonCode = ReasonRepairerExhausted
		cand.RetryReason = "max_attempts_exhausted"
		cand.ParkedAt, cand.ParkedBy = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "queue-worker"
		cand.NeedsHumanEvidenceRef = cand.ID
	})

	result, err := store.MedicRunOnce(MedicDeps{Now: mdClock(time.Now().UTC()), RepairerConfigured: true, GateFailureThreshold: 1, MaxDispatches: 100, Deadline: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) != 0 {
		t.Fatalf("medic touched a needs_human candidate: actions=%#v", result.Actions)
	}
	after := mustGet(t, store, c.ID)
	if after.phase() != NeedsHuman || after.ParkedAt != before.ParkedAt || after.ParkedBy != before.ParkedBy ||
		after.ReasonCode != before.ReasonCode || after.RetryReason != before.RetryReason || after.MedicDispatches != 0 {
		t.Fatalf("needs_human candidate mutated: before=%#v after=%#v", before, after)
	}
}

// TestMdNeverTouchesOperatorParkedNeedsInput pins the deliberate scope
// decision documented on MedicDeps: needs_input is exactly what a human's
// own `queue park` verb produces, and the medic must never re-litigate that
// decision the way overriding it would.
func TestMdNeverTouchesOperatorParkedNeedsInput(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	c := wltSubmit(t, store, "operator-parked")
	if _, err := store.Park(Op{ID: c.ID, Actor: "operator", Reason: "investigating"}); err != nil {
		t.Fatal(err)
	}
	before := mustGet(t, store, c.ID)

	result, err := store.MedicRunOnce(MedicDeps{Now: mdClock(time.Now().UTC()), RepairerConfigured: true, GateFailureThreshold: 1, MaxDispatches: 100, Deadline: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) != 0 {
		t.Fatalf("medic touched an operator-parked needs_input candidate: actions=%#v", result.Actions)
	}
	after := mustGet(t, store, c.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("needs_input candidate mutated:\nbefore=%#v\nafter=%#v", before, after)
	}
}

// TestMdRepeatedGateFailureKicksBackoffWhenRepairerConfigured pins the
// second medic-actionable state: a retry_wait candidate whose gate keeps
// failing gets its backoff cleared early — exactly what `kick` does — once
// its Attempt streak reaches GateFailureThreshold, but only once per
// attempt (MedicKickedAttempt fences a second pass at the same attempt).
func TestMdRepeatedGateFailureKicksBackoffWhenRepairerConfigured(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	c := wltSubmit(t, store, "gate-repeat")
	wltMutate(t, store, c.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = RetryWait, RetryWait
		cand.Attempt = 2
		cand.ReasonCode = ReasonGateFailed
		cand.RetryReason = "gate_failed"
		cand.RetryAt = time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC)
	})

	now := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	deps := MedicDeps{Now: mdClock(now), RepairerConfigured: true, GateFailureThreshold: 2, MaxDispatches: 3, Deadline: 2 * time.Hour}
	result, err := store.MedicRunOnce(deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Verb != "kick_gate_retry" {
		t.Fatalf("actions=%#v", result.Actions)
	}
	got := mustGet(t, store, c.ID)
	if !got.RetryAt.IsZero() {
		t.Fatalf("backoff not cleared: retry_at=%v", got.RetryAt)
	}
	if got.phase() != RetryWait || got.MedicDispatches != 1 || got.MedicKickedAttempt != 2 || got.MedicLastAction != "kick_gate_retry" {
		t.Fatalf("candidate=%#v", got)
	}

	// A second pass at the same attempt (nothing has reclaimed/retried it
	// yet) must not kick again.
	result2, err := store.MedicRunOnce(deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(result2.Actions) != 0 {
		t.Fatalf("medic kicked the same attempt twice: actions=%#v", result2.Actions)
	}
}

// TestMdRepeatedGateFailureIgnoredWithoutRepairerConfigured pins the
// "productive, not pointless" guardrail: with no repairer configured for
// this worker, kicking a repeated gate failure early would only burn the
// ordinary attempt budget faster for no benefit, so the medic leaves it
// alone entirely.
func TestMdRepeatedGateFailureIgnoredWithoutRepairerConfigured(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	c := wltSubmit(t, store, "gate-repeat-no-repairer")
	wltMutate(t, store, c.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = RetryWait, RetryWait
		cand.Attempt = 5
		cand.ReasonCode = ReasonGateFailed
		cand.RetryAt = time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC)
	})
	result, err := store.MedicRunOnce(MedicDeps{Now: mdClock(time.Now().UTC()), RepairerConfigured: false, GateFailureThreshold: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) != 0 {
		t.Fatalf("medic acted without a configured repairer: actions=%#v", result.Actions)
	}
	after := mustGet(t, store, c.ID)
	if after.MedicDispatches != 0 || !after.RetryAt.Equal(time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC)) {
		t.Fatalf("candidate mutated: %#v", after)
	}
}

// TestMdRepeatedGateFailureExhaustionEscalatesRepairerExhausted pins the
// gate-retry path's own exhaustion code, distinct from the conflict path's
// ReasonResolverExhausted.
func TestMdRepeatedGateFailureExhaustionEscalatesRepairerExhausted(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	c := wltSubmit(t, store, "gate-repeat-exhaust")
	start := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	wltMutate(t, store, c.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = RetryWait, RetryWait
		cand.Attempt = 4
		cand.ReasonCode = ReasonGateFailed
		cand.RetryAt = time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC)
		cand.MedicDispatches = 3
		cand.MedicFirstDispatchAt = start
	})
	now := start.Add(5 * time.Minute)
	deps := MedicDeps{Now: mdClock(now), RepairerConfigured: true, GateFailureThreshold: 2, MaxDispatches: 3, Deadline: 2 * time.Hour}
	result, err := store.MedicRunOnce(deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Verb != "escalate" {
		t.Fatalf("actions=%#v", result.Actions)
	}
	got := mustGet(t, store, c.ID)
	if got.phase() != NeedsHuman || got.ReasonCode != ReasonRepairerExhausted || got.RetryReason != "medic_repairer_exhausted" {
		t.Fatalf("phase=%s code=%s reason=%s", got.phase(), got.ReasonCode, got.RetryReason)
	}
}

// TestMdResumeResetsMedicBudget pins that Resume (a human declaring the
// underlying cause fixed) gives the medic a fresh budget, exactly like it
// already resets the ordinary attempt budget.
func TestMdResumeResetsMedicBudget(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	c := wltSubmit(t, store, "resume-resets-medic")
	wltMutate(t, store, c.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = NeedsHuman, NeedsHuman
		cand.ReasonCode = ReasonResolverExhausted
		cand.MedicDispatches = 3
		cand.MedicFirstDispatchAt = time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
		cand.MedicKickedAttempt = 2
		cand.MedicLastAction = "escalate:medic_resolver_exhausted"
		cand.MedicLastAt = time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
		cand.MedicLastBy = "medic-1"
	})
	got, err := store.Resume(Op{ID: c.ID, Actor: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if got.MedicDispatches != 0 || !got.MedicFirstDispatchAt.IsZero() || got.MedicKickedAttempt != 0 ||
		got.MedicLastAction != "" || !got.MedicLastAt.IsZero() || got.MedicLastBy != "" {
		t.Fatalf("resume did not reset medic budget: %#v", got)
	}
}

// TestMdTargetScopingIgnoresCandidatesForOtherTargets pins the target-scoping
// fix: MedicDeps.TargetRef must gate every branch of the scan exactly like
// Worker.matchesTarget gates claimPreparation/finalize/update (worker.go's
// "worker target %q refuses candidate" guard), so `queue worker --target X
// --medic` (or `queue medic --target X`) never un-parks, kicks the backoff
// on, or escalates a candidate bound to a different protected target — a
// target this medic instance has no authority over and no worker in this
// process could ever claim, matchesTarget, or finalize.
func TestMdTargetScopingIgnoresCandidatesForOtherTargets(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}

	conflict := wltSubmit(t, store, "target-scope-conflict")
	beforeConflict := wltMutate(t, store, conflict.ID, func(cand *Candidate) {
		cand.TargetRef = "other-target"
		cand.Phase, cand.Status = NeedsConflictInput, NeedsConflictInput
		cand.ConflictContinuation = "cont-1"
	})

	gateRetry := wltSubmit(t, store, "target-scope-gate-retry")
	beforeGateRetry := wltMutate(t, store, gateRetry.ID, func(cand *Candidate) {
		cand.TargetRef = "other-target"
		cand.Phase, cand.Status = RetryWait, RetryWait
		cand.Attempt = 5
		cand.ReasonCode = ReasonGateFailed
		cand.RetryAt = time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC)
	})

	stranded := wltSubmit(t, store, "target-scope-stranded")
	beforeStranded := wltMutate(t, store, stranded.ID, func(cand *Candidate) {
		cand.TargetRef = "other-target"
		cand.Phase, cand.Status = Queued, Queued
		cand.MedicDispatches = 1
		cand.MedicFirstDispatchAt = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	})

	deps := MedicDeps{
		Now: mdClock(time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)), TargetRef: "main",
		RepairerConfigured: true, GateFailureThreshold: 1, MaxDispatches: 100, Deadline: time.Hour,
	}
	result, err := store.MedicRunOnce(deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) != 0 {
		t.Fatalf("medic acted on mismatched-target candidates: actions=%#v", result.Actions)
	}
	if after := mustGet(t, store, conflict.ID); !reflect.DeepEqual(beforeConflict, after) {
		t.Fatalf("conflict candidate for other-target mutated:\nbefore=%#v\nafter=%#v", beforeConflict, after)
	}
	if after := mustGet(t, store, gateRetry.ID); !reflect.DeepEqual(beforeGateRetry, after) {
		t.Fatalf("gate-retry candidate for other-target mutated:\nbefore=%#v\nafter=%#v", beforeGateRetry, after)
	}
	if after := mustGet(t, store, stranded.ID); !reflect.DeepEqual(beforeStranded, after) {
		t.Fatalf("stranded candidate for other-target mutated:\nbefore=%#v\nafter=%#v", beforeStranded, after)
	}

	// Control: the same three candidates, unscoped (empty TargetRef matches
	// every target), all get touched in one pass — proving the zero-actions
	// result above is genuinely the target filter at work, not a broken
	// fixture that would never match anything regardless.
	deps.TargetRef = ""
	result2, err := store.MedicRunOnce(deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(result2.Actions) != 3 {
		t.Fatalf("unscoped medic pass should have acted on all three: actions=%#v", result2.Actions)
	}
}

// TestMdConflictDispatchResetsPositionToBackOfLine pins that dispatching a
// stalled needs_conflict_input candidate moves it to the back of the line
// (nextPosition, exactly what Worker.retryOrPark does for an ordinary
// failure) rather than leaving it at its old, often earliest, Position —
// otherwise an un-parked known-stalled candidate would re-occupy the
// finalization head (before() orders by Position) and block every healthy
// candidate behind it.
func TestMdConflictDispatchResetsPositionToBackOfLine(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	a := wltSubmit(t, store, "position-a")
	b := wltSubmit(t, store, "position-b")
	c := wltSubmit(t, store, "position-c")

	// a is the one that stalled: park it as needs_conflict_input without
	// touching its (lowest, head-of-line) Position, exactly like
	// failPreparation actually leaves it.
	wltMutate(t, store, a.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = NeedsConflictInput, NeedsConflictInput
		cand.ConflictContinuation = "cont-1"
	})

	result, err := store.MedicRunOnce(MedicDeps{Now: mdClock(time.Now().UTC()), MaxDispatches: 3, Deadline: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Verb != "dispatch_resolver" {
		t.Fatalf("actions=%#v", result.Actions)
	}
	got := mustGet(t, store, a.ID)
	bAfter := mustGet(t, store, b.ID)
	cAfter := mustGet(t, store, c.ID)
	if got.Position <= bAfter.Position || got.Position <= cAfter.Position {
		t.Fatalf("dispatched candidate did not move to the back of the line: dispatched.Position=%d b.Position=%d c.Position=%d", got.Position, bAfter.Position, cAfter.Position)
	}
}

// TestMdStrandedDispatchPastDeadlineEscalates pins the reaper arm added
// alongside target scoping: a candidate the medic already dispatched back to
// Queued that nothing has re-driven (no worker claimed it before the
// deadline — worker down, target mismatch, or a standalone `queue medic`
// with no worker running at all) is escalated rather than left to sit
// forever carrying an already-spent budget with nothing left in the switch
// able to ever revisit it again.
func TestMdStrandedDispatchPastDeadlineEscalates(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	c := wltSubmit(t, store, "stranded-past-deadline")
	dispatchedAt := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	wltMutate(t, store, c.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = Queued, Queued
		cand.MedicDispatches = 1
		cand.MedicFirstDispatchAt = dispatchedAt
		cand.MedicLastAction, cand.MedicLastBy = "dispatch_resolver", "medic-1"
	})

	now := dispatchedAt.Add(3 * time.Hour)
	result, err := store.MedicRunOnce(MedicDeps{Now: mdClock(now), MedicID: "medic-1", MaxDispatches: 3, Deadline: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Verb != "escalate" {
		t.Fatalf("actions=%#v", result.Actions)
	}
	got := mustGet(t, store, c.ID)
	if got.phase() != NeedsHuman || got.ReasonCode != ReasonBudgetExhausted || got.RetryReason != "medic_deadline_exceeded" {
		t.Fatalf("phase=%s code=%s reason=%s", got.phase(), got.ReasonCode, got.RetryReason)
	}
	if got.ParkedBy != "medic-1" || got.ParkedAt.IsZero() {
		t.Fatalf("parked attribution: by=%q at=%v", got.ParkedBy, got.ParkedAt)
	}
	if got.NeedsHumanEvidenceRef == "" {
		t.Fatalf("expected a needs_human evidence pointer")
	}
}

// TestMdStrandedDispatchWithinDeadlineLeftAlone is the control: a
// dispatched-but-not-yet-claimed candidate still within its budget is left
// completely untouched, since it may still be claimed and land normally at
// any moment — the reaper only ever fires once the deadline has actually
// passed.
func TestMdStrandedDispatchWithinDeadlineLeftAlone(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	c := wltSubmit(t, store, "stranded-within-deadline")
	dispatchedAt := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	before := wltMutate(t, store, c.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = Queued, Queued
		cand.MedicDispatches = 1
		cand.MedicFirstDispatchAt = dispatchedAt
	})

	now := dispatchedAt.Add(5 * time.Minute)
	result, err := store.MedicRunOnce(MedicDeps{Now: mdClock(now), MaxDispatches: 3, Deadline: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) != 0 {
		t.Fatalf("medic acted on a dispatched candidate still within its deadline: actions=%#v", result.Actions)
	}
	after := mustGet(t, store, c.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("candidate mutated while still within deadline:\nbefore=%#v\nafter=%#v", before, after)
	}
}

// TestMdStrandedDispatchInReprepareEscalatesPastDeadline pins that the
// reaper arm also covers Reprepare, the other claimable non-terminal phase a
// dispatched candidate can land in (see worker.go's claim check), not just
// Queued. The fixture keeps the reaper's other precondition satisfied —
// Attempt is still exactly the dispatch's MedicDispatchAtAttempt, i.e.
// nothing re-claimed it — so this isolates the phase arm itself. In
// production a Reprepare candidate usually HAS been re-claimed since the
// dispatch (that is how it got to Reprepare), which
// TestMdStrandedDispatchReclaimedSinceDispatchIsLeftAlone pins as
// deliberately left alone.
func TestMdStrandedDispatchInReprepareEscalatesPastDeadline(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	c := wltSubmit(t, store, "stranded-reprepare")
	dispatchedAt := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	wltMutate(t, store, c.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = Reprepare, Reprepare
		cand.Attempt = 2
		cand.MedicDispatches = 2
		cand.MedicDispatchAtAttempt = 2
		cand.MedicFirstDispatchAt = dispatchedAt
		cand.MedicLastAt, cand.MedicLastAction = dispatchedAt, "dispatch_resolver"
	})

	now := dispatchedAt.Add(3 * time.Hour)
	result, err := store.MedicRunOnce(MedicDeps{Now: mdClock(now), MaxDispatches: 3, Deadline: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Verb != "escalate" {
		t.Fatalf("actions=%#v", result.Actions)
	}
	got := mustGet(t, store, c.ID)
	if got.phase() != NeedsHuman || got.ReasonCode != ReasonBudgetExhausted {
		t.Fatalf("phase=%s code=%s", got.phase(), got.ReasonCode)
	}
}

// TestMdConcurrentMedicRunOnceSingleFlightsDispatch is the sharper
// single-flight property TestMdRestartMidCycleDoesNotDoubleDispatch cannot
// exercise on its own: several goroutines calling Store.MedicRunOnce
// concurrently against the same durable state, genuinely racing for the
// same state.lock file (not just two sequential Store values), must still
// land exactly one dispatch — proving the lock-protected
// read-mutate-write actually serializes concurrent medic passes rather than
// merely tolerating sequential restarts with nothing actually contending.
func TestMdConcurrentMedicRunOnceSingleFlightsDispatch(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir(), LockWait: 5 * time.Second}
	c := wltSubmit(t, store, "concurrent-single-flight")
	wltMutate(t, store, c.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = NeedsConflictInput, NeedsConflictInput
		cand.ConflictContinuation = "cont-1"
	})

	deps := MedicDeps{Now: mdClock(time.Now().UTC()), MedicID: "medic-race", MaxDispatches: 3, Deadline: time.Hour}
	const n = 8
	var wg sync.WaitGroup
	results := make([]MedicResult, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = store.MedicRunOnce(deps)
		}(i)
	}
	wg.Wait()

	totalActions := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
		totalActions += len(results[i].Actions)
	}
	if totalActions != 1 {
		t.Fatalf("want exactly one dispatch total across %d concurrent passes, got %d", n, totalActions)
	}
	got := mustGet(t, store, c.ID)
	if got.MedicDispatches != 1 || got.phase() != Queued {
		t.Fatalf("candidate double-dispatched or left in the wrong phase: %#v", got)
	}
}

// TestMdCleanPreparationEndsTheMedicStall pins that the medic's
// productive-retry budget is scoped to the stall it is treating, not to the
// candidate's lifetime. Without the reset in Worker.prepare,
// MedicFirstDispatchAt is a per-candidate stopwatch that only a human
// resume/override ever stops, so a long-lived candidate the medic helped once
// would be escalated straight to needs_human on its first medic-actionable
// event any time later — killing a candidate retryOrPark would have retried
// eight more times, under a reason code (budget-exhausted /
// medic_deadline_exceeded) that misdescribes what happened.
func TestMdCleanPreparationEndsTheMedicStall(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	c := wltSubmit(t, store, "stall-scoped-budget")
	dispatchedAt := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	wltMutate(t, store, c.ID, func(cand *Candidate) {
		cand.MedicDispatches = 1
		cand.MedicFirstDispatchAt = dispatchedAt
		cand.MedicDispatchAtAttempt = 0
		cand.MedicLastAction, cand.MedicLastAt, cand.MedicLastBy = "dispatch_resolver", dispatchedAt, "medic-1"
	})

	// The medic's dispatch worked: one ordinary worker cycle takes the
	// candidate all the way through speculation and a green gate.
	worker := Worker{Store: store, Deps: ProcessDeps{
		Integration: &fakeIntegration{speculate: func(_ context.Context, cand Candidate, _ []Candidate) (Speculation, error) {
			return Speculation{SHA: "spec-" + cand.SHA}, nil
		}},
		Gate: passingGate{},
	}}
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	prepared := mustGet(t, store, c.ID)
	if prepared.phase() != ReadyToFinalize {
		t.Fatalf("precondition: a clean preparation should be ready_to_finalize, got %s (%s)", prepared.phase(), prepared.Failure)
	}
	if prepared.MedicDispatches != 0 || !prepared.MedicFirstDispatchAt.IsZero() || prepared.MedicDispatchAtAttempt != 0 ||
		prepared.MedicKickedAttempt != 0 || prepared.MedicLastAction != "" || !prepared.MedicLastAt.IsZero() || prepared.MedicLastBy != "" {
		t.Fatalf("clean preparation did not end the medic stall: %#v", prepared)
	}

	// A month later the same candidate enters an ordinary, healthy
	// gate-failure retry streak at attempt 2 of 10, with a repairer
	// configured. The medic must productively kick it, not escalate it on an
	// inherited, long-spent wall-clock deadline.
	later := dispatchedAt.Add(30 * 24 * time.Hour)
	wltMutate(t, store, c.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = RetryWait, RetryWait
		cand.Attempt = 2
		cand.ReasonCode = ReasonGateFailed
		cand.RetryReason = "gate_failed"
		cand.RetryAt = later.Add(time.Hour)
	})
	result, err := store.MedicRunOnce(MedicDeps{
		Now: mdClock(later), MedicID: "medic-1", RepairerConfigured: true,
		GateFailureThreshold: 2, MaxDispatches: 3, Deadline: 2 * time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Verb != "kick_gate_retry" {
		t.Fatalf("healthy retry streak was not productively kicked: actions=%#v", result.Actions)
	}
	got := mustGet(t, store, c.ID)
	if got.phase() != RetryWait || !got.RetryAt.IsZero() || got.ReasonCode != ReasonGateFailed {
		t.Fatalf("after kick: phase=%s retry_at=%v code=%s", got.phase(), got.RetryAt, got.ReasonCode)
	}
	if !got.MedicFirstDispatchAt.Equal(later) {
		t.Fatalf("the kick should have opened a fresh budget at %v, got %v", later, got.MedicFirstDispatchAt)
	}
}

// TestMdStrandedDispatchReclaimedSinceDispatchIsLeftAlone pins the reaper
// arm's "has anything re-driven this dispatch" precondition: a worker claim
// increments Attempt, so an Attempt past MedicDispatchAtAttempt proves the
// dispatch was picked up. Such a candidate back in Reprepare is an ordinary
// lease-expiry reclaim, which the base machinery owns — escalating it to
// needs_human on the medic's wall clock would terminate a candidate nothing
// is actually stuck on.
func TestMdStrandedDispatchReclaimedSinceDispatchIsLeftAlone(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	c := wltSubmit(t, store, "stranded-but-reclaimed")
	dispatchedAt := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	before := wltMutate(t, store, c.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = Reprepare, Reprepare
		cand.Attempt = 3 // claimed once since the dispatch below
		cand.MedicDispatches = 1
		cand.MedicDispatchAtAttempt = 2
		cand.MedicFirstDispatchAt = dispatchedAt
		cand.MedicLastAction, cand.MedicLastAt, cand.MedicLastBy = "dispatch_resolver", dispatchedAt, "medic-1"
	})

	result, err := store.MedicRunOnce(MedicDeps{Now: mdClock(dispatchedAt.Add(3 * time.Hour)), MedicID: "medic-1", MaxDispatches: 3, Deadline: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) != 0 {
		t.Fatalf("reaper escalated a dispatch a worker demonstrably re-claimed: actions=%#v", result.Actions)
	}
	if after := mustGet(t, store, c.ID); !reflect.DeepEqual(before, after) {
		t.Fatalf("re-claimed candidate mutated:\nbefore=%#v\nafter=%#v", before, after)
	}
}

// TestMdStrandedDispatchBehindAMovingTrainIsLeftAlone pins the reaper arm's
// other precondition. medicHandleConflict dispatches to the BACK of the line,
// so on a deep train a freshly, correctly dispatched candidate can easily sit
// queued past the deadline having done nothing wrong except wait its FIFO
// turn. Escalating it would undo the medic's own progress and manufacture
// false operator work. The control half of this test — the same fixture with
// every trace of worker activity moved to before the dispatch — still
// escalates, proving the difference is genuinely the liveness signal and not
// a fixture that stopped matching.
func TestMdStrandedDispatchBehindAMovingTrainIsLeftAlone(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	stranded := wltSubmit(t, store, "dispatched-behind-a-moving-train")
	ahead := wltSubmit(t, store, "ahead-in-line")
	dispatchedAt := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	now := dispatchedAt.Add(3 * time.Hour)

	before := wltMutate(t, store, stranded.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = Queued, Queued
		cand.Attempt = 1
		cand.MedicDispatches = 1
		cand.MedicDispatchAtAttempt = 1
		cand.MedicFirstDispatchAt = dispatchedAt
		cand.MedicLastAction, cand.MedicLastAt, cand.MedicLastBy = "dispatch_resolver", dispatchedAt, "medic-1"
	})
	// A worker claimed the candidate ahead of it after the dispatch and is
	// still gating it, heartbeat-renewing its lease: a multi-hour CI run in
	// front of the dispatched candidate.
	wltMutate(t, store, ahead.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = Gating, Gating
		cand.Attempt = 1
		cand.WorkerID = "worker-a"
		cand.Started = dispatchedAt.Add(time.Minute)
		cand.PhaseStartedAt = dispatchedAt.Add(time.Minute)
		cand.LeaseExpiresAt = now.Add(30 * time.Second)
	})

	deps := MedicDeps{Now: mdClock(now), MedicID: "medic-1", MaxDispatches: 3, Deadline: 2 * time.Hour}
	result, err := store.MedicRunOnce(deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) != 0 {
		t.Fatalf("reaper escalated a candidate merely waiting behind a moving train: actions=%#v", result.Actions)
	}
	if after := mustGet(t, store, stranded.ID); !reflect.DeepEqual(before, after) {
		t.Fatalf("backlogged candidate mutated:\nbefore=%#v\nafter=%#v", before, after)
	}

	// Control: the train stopped — nothing has been claimed, leased, or
	// landed since the dispatch — so the same candidate is genuinely
	// stranded and must be escalated.
	wltMutate(t, store, ahead.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = Queued, Queued
		cand.WorkerID = ""
		cand.Started = dispatchedAt.Add(-time.Hour)
		cand.PhaseStartedAt = dispatchedAt.Add(-time.Hour)
		cand.LeaseExpiresAt = time.Time{}
	})
	result2, err := store.MedicRunOnce(deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(result2.Actions) != 1 || result2.Actions[0].Verb != "escalate" || result2.Actions[0].CandidateID != stranded.ID {
		t.Fatalf("a genuinely stranded dispatch was not escalated: actions=%#v", result2.Actions)
	}
	got := mustGet(t, store, stranded.ID)
	if got.phase() != NeedsHuman || got.ReasonCode != ReasonBudgetExhausted || got.RetryReason != "medic_deadline_exceeded" {
		t.Fatalf("phase=%s code=%s reason=%s", got.phase(), got.ReasonCode, got.RetryReason)
	}
}

func TestMdHistoricalCompletionAndExpiredLeaseDoNotSuppressStrandedEscalation(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	stranded := wltSubmit(t, store, "stranded-after-old-activity")
	historical := wltSubmit(t, store, "historical-worker-activity")
	dispatchedAt := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	now := dispatchedAt.Add(3 * time.Hour)

	wltMutate(t, store, stranded.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = Queued, Queued
		cand.Attempt = 1
		cand.MedicDispatches = 1
		cand.MedicDispatchAtAttempt = 1
		cand.MedicFirstDispatchAt = dispatchedAt
		cand.MedicLastAction, cand.MedicLastAt = "dispatch_resolver", dispatchedAt
	})
	wltMutate(t, store, historical.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = Landed, Landed
		cand.WorkerID = ""
		cand.Started = dispatchedAt.Add(10 * time.Minute)
		cand.PhaseStartedAt = dispatchedAt.Add(20 * time.Minute)
		cand.Completed = dispatchedAt.Add(time.Hour)
		cand.LeaseExpiresAt = dispatchedAt.Add(90 * time.Minute)
	})

	deps := MedicDeps{Now: mdClock(now), MedicID: "medic-1", MaxDispatches: 3, Deadline: 2 * time.Hour}
	first, err := store.MedicRunOnce(deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Actions) != 1 || first.Actions[0].CandidateID != stranded.ID || first.Actions[0].Verb != "escalate" {
		t.Fatalf("historical activity permanently suppressed escalation: actions=%#v", first.Actions)
	}
	second, err := store.MedicRunOnce(deps)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Actions) != 0 {
		t.Fatalf("stranded candidate escalated more than once: actions=%#v", second.Actions)
	}
}

func TestMdContinuouslyRenewedLiveLeaseSuppressesStrandedEscalation(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	stranded := wltSubmit(t, store, "stranded-behind-renewed-lease")
	active := wltSubmit(t, store, "active-renewed-worker")
	dispatchedAt := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	now := dispatchedAt.Add(3 * time.Hour)

	before := wltMutate(t, store, stranded.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = Queued, Queued
		cand.Attempt = 1
		cand.MedicDispatches = 1
		cand.MedicDispatchAtAttempt = 1
		cand.MedicFirstDispatchAt = dispatchedAt
		cand.MedicLastAction, cand.MedicLastAt = "dispatch_resolver", dispatchedAt
	})
	wltMutate(t, store, active.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = Gating, Gating
		cand.WorkerID = "worker-live"
		cand.LeaseExpiresAt = now.Add(30 * time.Second)
	})

	deps := MedicDeps{Now: mdClock(now), MedicID: "medic-1", MaxDispatches: 3, Deadline: 2 * time.Hour}
	if result, err := store.MedicRunOnce(deps); err != nil {
		t.Fatal(err)
	} else if len(result.Actions) != 0 {
		t.Fatalf("live lease did not suppress escalation: actions=%#v", result.Actions)
	}

	now = now.Add(20 * time.Second)
	wltMutate(t, store, active.ID, func(cand *Candidate) {
		cand.LeaseExpiresAt = now.Add(30 * time.Second)
	})
	deps.Now = mdClock(now)
	if result, err := store.MedicRunOnce(deps); err != nil {
		t.Fatal(err)
	} else if len(result.Actions) != 0 {
		t.Fatalf("renewed live lease did not suppress escalation: actions=%#v", result.Actions)
	}
	if after := mustGet(t, store, stranded.ID); !reflect.DeepEqual(before, after) {
		t.Fatalf("live worker caused stranded candidate mutation:\nbefore=%#v\nafter=%#v", before, after)
	}
}

// TestMdStrandedDispatchIgnoresOtherTargetsLiveness pins that the liveness
// signal is scoped to the stranded candidate's own protected target. Workers
// are target-scoped (Worker.matchesTarget), so a busy train on another target
// says nothing about whether anyone will ever claim this candidate — and an
// unscoped medic pass sees both.
func TestMdStrandedDispatchIgnoresOtherTargetsLiveness(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	stranded := wltSubmit(t, store, "stranded-on-its-own-target")
	elsewhere := wltSubmit(t, store, "busy-on-another-target")
	dispatchedAt := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	now := dispatchedAt.Add(3 * time.Hour)

	wltMutate(t, store, stranded.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = Queued, Queued
		cand.MedicDispatches = 1
		cand.MedicFirstDispatchAt = dispatchedAt
		cand.MedicLastAction, cand.MedicLastAt, cand.MedicLastBy = "dispatch_resolver", dispatchedAt, "medic-1"
	})
	wltMutate(t, store, elsewhere.ID, func(cand *Candidate) {
		cand.TargetRef = "release/2026.07"
		cand.Phase, cand.Status = Gating, Gating
		cand.WorkerID = "worker-release"
		cand.Started, cand.PhaseStartedAt = now.Add(-time.Minute), now.Add(-time.Minute)
		cand.LeaseExpiresAt = now.Add(30 * time.Second)
	})

	result, err := store.MedicRunOnce(MedicDeps{Now: mdClock(now), MedicID: "medic-1", MaxDispatches: 3, Deadline: 2 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Actions) != 1 || result.Actions[0].CandidateID != stranded.ID || result.Actions[0].Verb != "escalate" {
		t.Fatalf("another target's busy train was read as liveness: actions=%#v", result.Actions)
	}
}

// TestMdOverrideResetsMedicBudget mirrors the above for Override, the other
// verb that returns a parked/retry_wait candidate to queued.
func TestMdOverrideResetsMedicBudget(t *testing.T) {
	store := Store{ProjectRoot: t.TempDir()}
	c := wltSubmit(t, store, "override-resets-medic")
	wltMutate(t, store, c.ID, func(cand *Candidate) {
		cand.Phase, cand.Status = NeedsConflictInput, NeedsConflictInput
		cand.ConflictContinuation = "cont-1"
		cand.MedicDispatches = 2
		cand.MedicFirstDispatchAt = time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
		cand.MedicLastAction = "dispatch_resolver"
		cand.MedicLastAt = time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
		cand.MedicLastBy = "medic-1"
	})
	got, err := store.Override(Op{ID: c.ID, Actor: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if got.MedicDispatches != 0 || !got.MedicFirstDispatchAt.IsZero() || got.MedicLastAction != "" ||
		!got.MedicLastAt.IsZero() || got.MedicLastBy != "" {
		t.Fatalf("override did not reset medic budget: %#v", got)
	}
}
