package queue

import (
	"fmt"
	"strings"
	"time"
)

// MedicDeps configures the merge-queue medic (P1.7 part 2): a bounded,
// self-terminating pass that retries productively on two specific stalls the
// ordinary worker loop cannot recover from on its own, using only existing
// queue mechanics — never `queue override`, never a gate waiver, never a
// skipped test:
//
//   - needs_conflict_input: parked by failPreparation with the conflict
//     continuation retained (see resolver.go's resolveConflicts). Nothing
//     re-drives it — Attempt is frozen at whatever it was on entry, and only
//     a human `resume` returns it to queued — so before this item candidates
//     could sit parked indefinitely even though the configured or embedded
//     git-ops resolver might well succeed on a second try (a flaky launch, a
//     transient story-harness hiccup, an operator having just fixed the
//     resolver configuration in between).
//   - retry_wait with a repeated gate-failed retry streak and a Repairer
//     actually configured for this worker: still bounded by the ordinary
//     MaxAttempts/exponential-backoff policy in retryOrPark, but the medic
//     clears the backoff timer early (exactly what the existing `kick` verb
//     does) so a configured repairer gets its shot sooner rather than
//     waiting out the full delay.
//
// Both cases get their own bounded productive-retry budget — a wall-clock
// deadline AND a dispatch-count ceiling, tracked durably on the candidate
// itself (MedicDispatches/MedicFirstDispatchAt in queue.go) — separate from
// the ordinary product-failure Attempt budget, because the parked state
// itself does not otherwise bound how long the medic could keep re-driving a
// candidate.
//
// That budget is scoped to the stall being treated, not to the candidate's
// lifetime. A clean preparation (speculation plus the deterministic gate,
// both green — see Worker.prepare) ends the stall and resets the budget,
// exactly like it already resets the environmental-failure streak. Without
// that reset MedicFirstDispatchAt would be a per-candidate-lifetime
// stopwatch: a long-lived candidate the medic legitimately helped once would
// carry a spent wall-clock deadline forever, and the medic would escalate it
// to needs_human on the very first medic-actionable event weeks later —
// terminating a candidate the ordinary retryOrPark policy would have retried
// many more times, under a reason code that misdescribes what happened.
//
// On exhaustion the candidate is escalated straight to
// needs_human — never spun forever, never silently dropped — with a typed
// ReasonCode (resolver-exhausted / repairer-exhausted for a dispatch-count
// ceiling, budget-exhausted for the wall-clock deadline, which fires
// regardless of the dispatch count so a candidate that simply ran out of
// allotted time is never mislabeled as "kept failing") and the same
// NeedsHumanEvidenceRef fallback chain parkHuman uses.
//
// needs_input and needs_human are never touched. needs_input is exactly what
// an operator's own `queue park` verb produces (see ops.go's Park); acting on
// it would mean silently overriding a human's explicit decision to leave a
// candidate alone, which is precisely what this item's "never override,
// never weaken a gate" guardrail forbids. needs_human is the terminal that
// says automation is already out of options; the medic is part of
// "automation" for this purpose and must not re-litigate its own prior
// verdict, or another dispatch's, outside the one exhaustion transition
// above.
//
// Operationally this means the medic only ever clears the needs_conflict_input
// and repeated-gate-failure retry_wait slice of a stalled backlog. If most of
// a deployment's stalled population is needs_input (an operator's own park,
// ReasonOperatorParked), enabling --medic will not touch it — check the
// actual phase distribution (`queue status`'s PhaseCounts) before assuming
// --medic alone clears a backlog.
type MedicDeps struct {
	// Now, when set, replaces time.Now for deterministic tests.
	Now func() time.Time
	// MedicID names this medic instance in evidence, MedicLastBy, and the
	// needs_human ParkedBy stamp on escalation — analogous to
	// ProcessDeps.WorkerID. Defaults to "queue-medic".
	MedicID string
	// MaxDispatches bounds how many times the medic will productively retry
	// a single candidate (dispatch the resolver, or kick a repeated gate
	// failure) before escalating it to needs_human. Zero takes
	// DefaultMedicMaxDispatches.
	MaxDispatches int
	// Deadline bounds wall-clock time since the medic's first touch of the
	// stall it is treating (MedicFirstDispatchAt, which a clean preparation
	// or a human resume/override clears — see resetMedicBudget); exceeding
	// it escalates to needs_human even when MaxDispatches has not been
	// reached — a resolver that never finishes in time is exhausted exactly
	// as much as one that returns and fails repeatedly. Zero takes
	// DefaultMedicDeadline.
	Deadline time.Duration
	// RepairerConfigured must mirror whether ProcessDeps.Repairer is
	// actually set for this worker. The medic only accelerates a repeated
	// gate-failed retry_wait candidate when a repairer is configured to
	// receive it: kicking early with nothing configured to use the extra
	// attempt would just burn the ordinary attempt budget faster for no
	// benefit, which is not "productive" retrying.
	RepairerConfigured bool
	// GateFailureThreshold: a retry_wait candidate whose current Attempt
	// streak has reached this count on a gate-failed reason becomes
	// medic-actionable. Zero takes DefaultMedicGateFailureThreshold.
	GateFailureThreshold int
	// TargetRef scopes every candidate this medic pass is willing to act on,
	// exactly like Worker.Deps.TargetRef scopes claimPreparation/finalize/
	// update (see worker.go's matchesTarget and its "worker target %q
	// refuses candidate" guard). A store can and does hold candidates for
	// multiple protected targets at once (worker_lifecycle_test.go,
	// staging_repair_test.go); a worker bound to one target has no authority
	// over another target's candidates — it did not admit them, does not
	// know who (if anyone) owns them, and cannot itself ever claim or
	// finalize them. Left empty, the medic matches every candidate
	// regardless of target, which is only safe for a single-target-per-store
	// deployment or the standalone `queue medic` run deliberately
	// unscoped.
	TargetRef string
}

// matchesTarget reports whether c is within this medic pass's authority: an
// empty TargetRef matches everything (single-target deployments, or an
// operator's deliberate unscoped standalone pass); otherwise c.TargetRef must
// match exactly, mirroring Worker.matchesTarget precisely so `queue worker
// --target X --medic` never touches a candidate bound to a different target.
func (d MedicDeps) matchesTarget(c *Candidate) bool {
	target := strings.TrimSpace(d.TargetRef)
	return target == "" || c.TargetRef == target
}

const (
	DefaultMedicMaxDispatches        = 3
	DefaultMedicDeadline             = 2 * time.Hour
	DefaultMedicGateFailureThreshold = 2
)

func (d MedicDeps) maxDispatches() int {
	if d.MaxDispatches > 0 {
		return d.MaxDispatches
	}
	return DefaultMedicMaxDispatches
}
func (d MedicDeps) deadline() time.Duration {
	return firstDuration(d.Deadline, DefaultMedicDeadline)
}
func (d MedicDeps) gateFailureThreshold() int {
	if d.GateFailureThreshold > 0 {
		return d.GateFailureThreshold
	}
	return DefaultMedicGateFailureThreshold
}
func (d MedicDeps) medicID() string { return first(d.MedicID, "queue-medic") }
func (d MedicDeps) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

// MedicAction records one candidate-scoped decision the medic made in a
// cycle — the transient per-cycle log a caller (the worker loop, the
// `kitsoki queue medic` CLI) can print or forward. The durable, per-candidate
// equivalent (MedicLastAction/At/By) is what `queue status` renders; this is
// "what happened just now" rather than "what is the current state".
type MedicAction struct {
	CandidateID string    `json:"candidate_id"`
	Verb        string    `json:"verb"`
	Reason      string    `json:"reason"`
	At          time.Time `json:"at"`
}

// MedicResult is the outcome of one bounded MedicRunOnce pass.
type MedicResult struct {
	Actions []MedicAction `json:"actions,omitempty"`
}

// MedicRunOnce performs exactly one bounded pass over every candidate this
// store's queue currently holds that matches deps.TargetRef (see
// MedicDeps.matchesTarget): needs_conflict_input candidates get a resolver
// dispatch (or an exhaustion escalation), repairer-eligible
// repeated-gate-failure retry_wait candidates get an early kick (or an
// exhaustion escalation), and a candidate the medic itself already dispatched
// back to queued/reprepare that nothing has re-driven within the deadline
// (worker down, no worker claiming this target, or a standalone `queue
// medic` running with no worker at all) is escalated rather than left to
// carry a spent budget forever — see medicHandleStrandedDispatch. Every
// candidate touched either makes progress (returned to queued, or has its
// backoff cleared) or is escalated to needs_human with a typed reason and an
// evidence pointer — the medic never leaves a candidate as it found it
// forever, and never spins one unboundedly.
//
// Safe to call repeatedly and from multiple processes without any separate
// medic lease: the scan and every mutation happen inside one
// lock-protected read-mutate-write, exactly like Worker.claimPreparation's
// own expiry sweep. A candidate a prior (or concurrent, or
// pre-restart) pass already moved out of needs_conflict_input/retry_wait is
// simply not matched by the next pass's switch — there is nothing left to
// double-dispatch once the phase itself has moved, and the durable write is
// atomic, so a restart before it lands leaves the candidate exactly as if
// this pass never ran.
func (s Store) MedicRunOnce(deps MedicDeps) (MedicResult, error) {
	var result MedicResult
	_, err := s.withLock(func(path string) (State, error) {
		state, dirty, err := s.readCompacted(path)
		if err != nil {
			return State{}, err
		}
		n := deps.now()
		for i := range state.Candidates {
			c := &state.Candidates[i]
			if !deps.matchesTarget(c) {
				continue
			}
			var act MedicAction
			var touched bool
			switch {
			case c.phase() == NeedsConflictInput:
				act, touched = medicHandleConflict(c, &state, deps, n)
			case c.phase() == RetryWait && deps.RepairerConfigured && c.ReasonCode == ReasonGateFailed &&
				c.Attempt >= deps.gateFailureThreshold() && c.MedicKickedAttempt != c.Attempt:
				act, touched = medicHandleGateRetry(c, deps, n)
			case (c.phase() == Queued || c.phase() == Reprepare) && c.MedicDispatches > 0:
				act, touched = medicHandleStrandedDispatch(c, &state, deps, n)
			}
			if touched {
				result.Actions = append(result.Actions, act)
				dirty = true
			}
		}
		if !dirty {
			return state, nil
		}
		return state, write(path, state)
	})
	return result, err
}

// medicDeadlineExceeded reports whether c's medic-owned wall-clock budget has
// run out as of now. A zero MedicFirstDispatchAt means the medic has never
// touched c, so there is nothing to time out yet.
func medicDeadlineExceeded(c *Candidate, deps MedicDeps, now time.Time) bool {
	return !c.MedicFirstDispatchAt.IsZero() && now.Sub(c.MedicFirstDispatchAt) >= deps.deadline()
}

// medicBudgetExceeded reports whether c's medic-owned productive-retry
// budget is exhausted as of now, and if so, the ReasonCode and RetryReason
// tag to escalate with. The wall-clock deadline is checked first and always
// reports ReasonBudgetExhausted regardless of dispatchedCode: it is a
// distinct failure mode from "kept failing" — the allotted time ran out
// regardless of how many dispatches were actually spent — so it must never
// borrow the more specific resolver/repairer code.
func medicBudgetExceeded(c *Candidate, deps MedicDeps, now time.Time, dispatchedCode ReasonCode, dispatchedTag string) (bool, ReasonCode, string) {
	if medicDeadlineExceeded(c, deps, now) {
		return true, ReasonBudgetExhausted, "medic_deadline_exceeded"
	}
	if c.MedicDispatches >= deps.maxDispatches() {
		return true, dispatchedCode, dispatchedTag
	}
	return false, "", ""
}

// medicEscalate is parkHuman's medic-attributed twin: the only differences
// are the actor stamped into ParkedBy/evidence (the medic's own ID, not
// "queue-worker") and that it never clears MedicDispatches/
// MedicFirstDispatchAt — that history is exactly what a human reading
// needs_human_evidence_ref plus this candidate's evidence needs to see, and
// Resume/Override (the only ways back out of needs_human) already reset it
// for a fresh budget on the next attempt.
func medicEscalate(c *Candidate, now time.Time, medicID, reason string, code ReasonCode) MedicAction {
	c.Phase, c.Status, c.RetryReason, c.ReasonCode = NeedsHuman, NeedsHuman, reason, code
	c.WorkerID, c.LeaseExpiresAt, c.RetryAt = "", time.Time{}, time.Time{}
	c.ParkedAt, c.ParkedBy = now, medicID
	c.NeedsHumanEvidenceRef = first(c.GateLog, c.FinalizationLog, c.WorkspacePath, c.ID)
	c.MedicLastAction, c.MedicLastAt, c.MedicLastBy = "escalate:"+reason, now, medicID
	c.Evidence = append(c.Evidence, fmt.Sprintf("queue:medic escalate by %s at %s reason=%s dispatches=%d", medicID, now.Format(time.RFC3339), reason, c.MedicDispatches))
	return MedicAction{CandidateID: c.ID, Verb: "escalate", Reason: reason, At: now}
}

// medicDispatch stamps the bookkeeping every productive medic action
// (dispatch_resolver, kick_gate_retry) shares: the wall-clock start of this
// candidate's medic-owned budget (set once, per stall, on the first
// dispatch), the running dispatch count, the Attempt this dispatch was made
// at (so the reaper arm can tell a never-re-driven dispatch from a re-claimed
// one — see medicHandleStrandedDispatch), and who/when/what for `queue
// status`.
func medicDispatch(c *Candidate, now time.Time, medicID, verb string) {
	if c.MedicFirstDispatchAt.IsZero() {
		c.MedicFirstDispatchAt = now
	}
	c.MedicDispatches++
	c.MedicDispatchAtAttempt = c.Attempt
	c.MedicLastAction, c.MedicLastAt, c.MedicLastBy = verb, now, medicID
}

// medicHandleConflict is the needs_conflict_input path: dispatch the
// resolver again by returning the candidate to queued — the ordinary worker
// loop's next prepare() re-runs Speculate, which re-drives resolveConflicts
// with whatever resolver is configured; the medic itself never launches an
// agent or runs a git command — or escalate on exhaustion.
//
// Position is reset to the back of the line (nextPosition(state.Candidates),
// exactly what Worker.retryOrPark does for an ordinary failure) rather than
// left at whatever it was when the candidate parked: before() picks the
// finalization head by Position, so an un-parked known-stalled candidate
// that kept its old (often earliest) Position would re-occupy the
// finalization head and block every healthy candidate behind it for up to
// MaxDispatches full prepare cycles. A medic dispatching several parked
// candidates in one pass must not let any of them cut back to the front of
// a train they already fell out of.
func medicHandleConflict(c *Candidate, state *State, deps MedicDeps, now time.Time) (MedicAction, bool) {
	if exceeded, code, tag := medicBudgetExceeded(c, deps, now, ReasonResolverExhausted, "medic_resolver_exhausted"); exceeded {
		return medicEscalate(c, now, deps.medicID(), tag, code), true
	}
	medicDispatch(c, now, deps.medicID(), "dispatch_resolver")
	c.Phase, c.Status = Queued, Queued
	c.ConflictContinuation = ""
	c.ParkedAt, c.ParkedBy = time.Time{}, ""
	c.RetryReason, c.ReasonCode = "", ""
	c.WorkerID, c.LeaseExpiresAt, c.RetryAt = "", time.Time{}, time.Time{}
	c.ProductFailureSignature, c.ProductRepeatStreak = "", 0
	c.Position = nextPosition(state.Candidates)
	c.Evidence = append(c.Evidence, fmt.Sprintf("queue:medic dispatch_resolver by %s at %s dispatch=%d/%d position=%d", deps.medicID(), now.Format(time.RFC3339), c.MedicDispatches, deps.maxDispatches(), c.Position))
	return MedicAction{CandidateID: c.ID, Verb: "dispatch_resolver", Reason: "needs_conflict_input", At: now}, true
}

// medicHandleStrandedDispatch is the reaper arm: a candidate the medic
// already dispatched back to Queued or Reprepare (MedicDispatches > 0) that
// nothing has re-driven into needs_conflict_input, retry_wait, or a terminal
// state before its wall-clock deadline. Without this, a dispatch the
// ordinary worker never re-claims — no worker running for this target, the
// worker down, or the standalone `queue medic` running with no worker
// process at all — would sit in Queued/Reprepare forever with its medic
// budget already spent and never revisited: the switch in MedicRunOnce only
// matches needs_conflict_input/retry_wait, so once the dispatch moved the
// candidate out of those phases nothing would ever check its deadline again.
// This closes that gap so the medic's "every candidate it touches ends up
// progressing or in needs_human" guarantee holds even when no worker ever
// re-drives the dispatch.
//
// "Stranded" is a claim about the queue, not about the clock, so the deadline
// alone is never sufficient. Two further conditions must hold, because
// escalating a candidate that is merely waiting its turn would undo the
// medic's own progress and manufacture false operator work under a reason
// code that actively misleads whoever opens it:
//
//   - Nothing has re-driven the dispatch: Attempt is still exactly what it
//     was when the dispatch happened (MedicDispatchAtAttempt). Any claim
//     increments Attempt (claimPreparation), so an advanced Attempt means a
//     worker did pick the dispatch up and the candidate's current
//     Queued/Reprepare state is the ordinary machinery at work — a lease
//     expiry reclaim or a stale-base repreparation — which needs no medic at
//     all. (A candidate whose durable record predates this field reads
//     MedicDispatchAtAttempt as 0; if it has any attempts at all that reads
//     as "re-driven", so the reaper stays quiet rather than escalating on
//     incomplete history.)
//   - No worker is demonstrably working this protected target: see
//     medicWorkerActivitySince. medicHandleConflict deliberately dispatches
//     to the back of the line, so on a deep train a correctly dispatched
//     candidate can easily sit queued for longer than the deadline with
//     nothing wrong with it at all — the workers are simply busy ahead of
//     it, and they will reach it.
//
// The cost of those conditions is that the reaper waits: a dispatched
// candidate on a train that keeps moving is left alone indefinitely rather
// than escalated. That is the correct trade — the "ends up progressing or in
// needs_human" guarantee is about candidates nothing is driving, and a moving
// train is driving this one.
func medicHandleStrandedDispatch(c *Candidate, state *State, deps MedicDeps, now time.Time) (MedicAction, bool) {
	if !medicDeadlineExceeded(c, deps, now) {
		return MedicAction{}, false
	}
	if c.Attempt != c.MedicDispatchAtAttempt {
		return MedicAction{}, false
	}
	if medicWorkerActivitySince(state, c, medicLastDispatchAt(c)) {
		return MedicAction{}, false
	}
	return medicEscalate(c, now, deps.medicID(), "medic_deadline_exceeded", ReasonBudgetExhausted), true
}

// medicLastDispatchAt is when the medic most recently dispatched c. For a
// Queued/Reprepare candidate with MedicDispatches > 0 — the only kind the
// reaper arm looks at — MedicLastAt is exactly that: the medic's only
// possible last actions on such a candidate are dispatch_resolver and
// kick_gate_retry (an escalate would have left it in needs_human, which the
// switch never matches). MedicFirstDispatchAt is the fallback for a durable
// record written before MedicLastAt existed.
func medicLastDispatchAt(c *Candidate) time.Time {
	if !c.MedicLastAt.IsZero() {
		return c.MedicLastAt
	}
	return c.MedicFirstDispatchAt
}

// medicWorkerActivitySince reports whether some worker has demonstrably been
// working c's own protected target at or after since — i.e. whether c is
// waiting behind a moving train rather than stranded. Any of four durable
// timestamps on another candidate for the same TargetRef counts, because each
// is written only by a worker actually doing work: Started (set when
// claimPreparation claims), PhaseStartedAt (set by every lease transition),
// Completed (set when finalization lands), and LeaseExpiresAt (extended by
// the heartbeat for as long as a worker holds an in-flight phase, which is
// what a single multi-hour gate run ahead of c looks like).
//
// Scoping to c.TargetRef rather than to deps.TargetRef is deliberate and
// stricter: workers are target-scoped (Worker.matchesTarget), so activity on
// another target proves nothing about whether anyone will ever claim c, and
// an unscoped medic pass (empty MedicDeps.TargetRef, which matches every
// candidate) must not read one target's healthy train as liveness for
// another target that has no worker at all.
func medicWorkerActivitySince(state *State, c *Candidate, since time.Time) bool {
	for i := range state.Candidates {
		other := &state.Candidates[i]
		if other.ID == c.ID || other.TargetRef != c.TargetRef {
			continue
		}
		if other.Started.After(since) || other.PhaseStartedAt.After(since) ||
			other.Completed.After(since) || other.LeaseExpiresAt.After(since) {
			return true
		}
	}
	return false
}

// medicHandleGateRetry is the repeated-gate-failure retry_wait path: clear
// the backoff timer (exactly what the existing `kick` verb does) so a
// configured repairer gets its shot sooner, or escalate on exhaustion. The
// ordinary MaxAttempts/exponential-backoff policy in retryOrPark is
// untouched — this only ever shortens the wait, never resets or extends the
// attempt count itself, so the base queue's own exhaustion path still
// applies exactly as before if the medic's own budget has not yet run out.
func medicHandleGateRetry(c *Candidate, deps MedicDeps, now time.Time) (MedicAction, bool) {
	if exceeded, code, tag := medicBudgetExceeded(c, deps, now, ReasonRepairerExhausted, "medic_repairer_exhausted"); exceeded {
		return medicEscalate(c, now, deps.medicID(), tag, code), true
	}
	medicDispatch(c, now, deps.medicID(), "kick_gate_retry")
	c.MedicKickedAttempt = c.Attempt
	c.RetryAt = time.Time{}
	c.Evidence = append(c.Evidence, fmt.Sprintf("queue:medic kick_gate_retry by %s at %s attempt=%d dispatch=%d/%d", deps.medicID(), now.Format(time.RFC3339), c.Attempt, c.MedicDispatches, deps.maxDispatches()))
	return MedicAction{CandidateID: c.ID, Verb: "kick_gate_retry", Reason: "repeated_gate_failure", At: now}, true
}

// resetMedicBudget clears every medic bookkeeping field, ending the stall the
// budget was scoped to. Called by Resume and Override (a human declaring the
// underlying cause fixed gets the medic a fresh productive-retry budget too,
// exactly like the ordinary attempt budget those verbs already reset) and by
// Worker.prepare on a clean preparation (the candidate made it through
// speculation and the deterministic gate, so whatever stall the medic was
// treating is over; a later, unrelated stall must not inherit an already-spent
// wall-clock deadline).
//
// Note it is deliberately NOT called merely because a worker claimed the
// candidate: a claim is an attempt, not progress. Resetting there would let
// the needs_conflict_input dispatch loop (park -> dispatch -> claim -> park
// again, which never reaches a clean preparation and whose conflict park does
// not itself consume the ordinary Attempt budget) re-dispatch without bound,
// which is exactly what MaxDispatches exists to prevent.
func resetMedicBudget(c *Candidate) {
	c.MedicDispatches, c.MedicFirstDispatchAt, c.MedicKickedAttempt = 0, time.Time{}, 0
	c.MedicDispatchAtAttempt = 0
	c.MedicLastAction, c.MedicLastAt, c.MedicLastBy = "", time.Time{}, ""
}
