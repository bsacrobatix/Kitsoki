package queue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Worker performs one durable queue step. Multiple workers may prepare
// different candidates concurrently; only the FIFO head can hold finalization.
type Worker struct {
	Store Store
	Deps  ProcessDeps
}

func (w Worker) RunOnce(ctx context.Context) (bool, error) {
	if w.Deps.Integration == nil || w.Deps.Gate == nil {
		return false, fmt.Errorf("queue: integration and gate are required")
	}
	c, ok, err := w.claimPreparation()
	if err != nil {
		return false, err
	}
	if ok {
		if err := w.prepare(ctx, c); err != nil {
			return true, err
		}
		return true, nil
	}
	return w.finalize(ctx)
}

func (w Worker) claimPreparation() (Candidate, bool, error) {
	var claimed Candidate
	var ok bool
	_, err := w.Store.withLock(func(path string) (State, error) {
		state, dirty, err := w.Store.readCompacted(path)
		if err != nil {
			return State{}, err
		}
		n := now(w.Deps)
		for i := range state.Candidates {
			c := &state.Candidates[i]
			if !w.matchesTarget(*c) {
				continue
			}
			if (c.phase() == Preparing || c.phase() == Gating || c.phase() == Finalizing) && !c.LeaseExpiresAt.IsZero() && !n.Before(c.LeaseExpiresAt) {
				if c.phase() == Finalizing {
					// Finalizing already persists the exact expected-old/new
					// tuple (BaseSHA/TreeSHA). Replay finalization first: if the
					// CAS won before the daemon crashed, ProtectedFinalizer
					// observes the target at TreeSHA and records the landing
					// without paying for another gate.
					c.Phase, c.Status = ReadyToFinalize, ReadyToFinalize
					c.Failure = "finalization lease expired; replaying protected CAS intent"
				} else {
					c.Phase, c.Status = Reprepare, Reprepare
					c.Failure = "worker lease expired; preserved attempt evidence requires reprepare"
				}
				c.WorkerID, c.LeaseExpiresAt = "", time.Time{}
				c.ReasonCode = ReasonLeaseLost
				dirty = true
			}
		}
		var pick *Candidate
		for i := range state.Candidates {
			c := &state.Candidates[i]
			if !w.matchesTarget(*c) {
				continue
			}
			if c.phase() != Queued && c.phase() != Reprepare && c.phase() != RetryWait {
				continue
			}
			if !c.RetryAt.IsZero() && n.Before(c.RetryAt) {
				continue
			}
			if pick == nil || before(*c, *pick) {
				pick = c
			}
		}
		if pick != nil {
			clearApproval(pick)
			clearPreparedIdentity(pick)
			w.lease(pick, Preparing, n)
			pick.Attempt++
			pick.Started = n
			pick.Failure = ""
			pick.ReasonCode = ""
			pick.RetryAt = time.Time{}
			claimed, ok = *pick, true
			dirty = true
		}
		if !dirty {
			return state, nil
		}
		return state, write(path, state)
	})
	return claimed, ok, err
}

func (w Worker) prepare(ctx context.Context, c Candidate) error {
	ahead, err := w.ahead(c.Sequence)
	if err != nil {
		return err
	}
	var spec Speculation
	var specErr error
	w.heartbeat(ctx, c.ID, func() { spec, specErr = w.Deps.Integration.Speculate(ctx, c, ahead) })
	if err := w.update(c.ID, func(state *State, cur *Candidate) {
		if w.operatorIntervened(cur, "speculation") || w.leaseLost(cur, Preparing, "speculation") {
			return
		}
		cur.SpeculativeSHA, cur.TreeSHA, cur.BaseSHA = spec.SHA, spec.SHA, spec.BaseSHA
		cur.RuntimeConfigDigest = spec.RuntimeConfigDigest
		cur.IntegrationRef, cur.WorkspaceID, cur.WorkspacePath = spec.IntegrationRef, spec.WorkspaceID, spec.WorkspacePath
		cur.Evidence = append(cur.Evidence, spec.Evidence...)
		if specErr != nil {
			w.failPreparation(state, cur, specErr)
			return
		}
		w.lease(cur, Gating, now(w.Deps))
	}); err != nil {
		return err
	}
	if specErr != nil {
		return nil
	}
	var result GateResult
	var gateErr error
	if c.OverrideGate {
		// Operator override: a human explicitly approved this candidate for
		// immediate landing. The integration tree is still built and the
		// protected CAS still applies; only the deterministic gate is waived,
		// and the waiver is recorded durably.
		result = GateResult{Passed: true, GateVersion: "operator-override/v1", Evidence: []string{fmt.Sprintf("queue:gate-overridden-by=%s reason=%s", first(c.OverrideBy, "operator"), first(c.OverrideReason, "unspecified"))}}
	} else if memo, ok := w.gateMemoLookup(c.TargetRef, spec.SHA, spec.RuntimeConfigDigest); ok {
		// This exact tree already passed this exact gate identity — a
		// reprepare onto an unchanged tree (a stale-base Reprepare whose
		// fresh classification still lands here, or a retry after an
		// unrelated environmental failure) does not need to pay for the
		// gate again.
		result = memo
	} else {
		w.heartbeat(ctx, c.ID, func() { result, gateErr = w.runGate(ctx, c, spec) })
		beforeRepair := spec
		repaired := false
		if (gateErr != nil || !result.Passed) && w.Deps.Repairer != nil {
			stageTimeout := w.Deps.stageTimeout()
			repairCtx, cancelRepair := context.WithTimeout(ctx, stageTimeout)
			var repairEvidence []string
			var repairErr error
			w.heartbeat(ctx, c.ID, func() {
				repairEvidence, repairErr = w.Deps.Repairer.Repair(repairCtx, spec, firstGateError(gateErr))
			})
			if repairCtx.Err() != nil {
				repairErr = fmt.Errorf("queue: repair timed out after %s: %w", stageTimeout, repairCtx.Err())
			}
			cancelRepair()
			result.Evidence = append(result.Evidence, repairEvidence...)
			if repairErr == nil {
				repaired = true
				// A successful repair commits into the speculative workspace, so
				// the tree identity the rerun gate validates — and the finalizer
				// later CAS-checks — must follow the repaired HEAD. Leaving the
				// pre-repair SHA behind wedges every repaired candidate at
				// finalization ("prepared tree changed after deterministic gate").
				if head, headErr := workspaceHead(ctx, spec.WorkspacePath); headErr != nil {
					gateErr = headErr
				} else {
					if head != "" {
						spec.SHA = head
					}
					w.heartbeat(ctx, c.ID, func() { result, gateErr = w.runGate(ctx, c, spec) })
					result.Evidence = append(result.Evidence, repairEvidence...)
				}
			} else if gateErr == nil {
				gateErr = repairErr
			}
		}
		if repaired && gateErr == nil && result.Passed {
			switch {
			case w.Deps.RepairReviewer == nil:
				gateErr = fmt.Errorf("queue: repaired gate requires an independent anti-weakening reviewer")
			case strings.TrimSpace(w.Deps.ReviewPolicyDigest) == "":
				gateErr = fmt.Errorf("queue: repaired gate requires a deterministic review policy digest")
			default:
				var review RepairReviewResult
				var reviewErr error
				stageTimeout := w.Deps.stageTimeout()
				reviewCtx, cancelReview := context.WithTimeout(ctx, stageTimeout)
				w.heartbeat(ctx, c.ID, func() {
					review, reviewErr = w.Deps.RepairReviewer.Review(reviewCtx, RepairReview{Before: beforeRepair, After: spec, Gate: result})
				})
				if reviewCtx.Err() != nil {
					reviewErr = fmt.Errorf("queue: repair review timed out after %s: %w", stageTimeout, reviewCtx.Err())
				}
				cancelReview()
				result.Evidence = append(result.Evidence, review.Evidence...)
				if review.Log != "" {
					result.Evidence = append(result.Evidence, "queue:repair-review-log="+review.Log)
				}
				if reviewErr != nil {
					gateErr = fmt.Errorf("queue: repair review failed: %w", reviewErr)
				} else if !review.Passed {
					gateErr = fmt.Errorf("queue: repair rejected by anti-weakening review")
				} else {
					repairerID := strings.TrimSpace(w.Deps.RepairerID)
					reviewerID := strings.TrimSpace(review.ReviewerID)
					if repairerID == "" || reviewerID == "" {
						gateErr = fmt.Errorf("queue: independent repair review requires repairer and reviewer identities")
					} else if repairerID == reviewerID {
						gateErr = fmt.Errorf("queue: repairer %q cannot review its own repair", repairerID)
					} else {
						result.Evidence = append(result.Evidence, fmt.Sprintf("queue:repair-reviewed-by=%s repairer=%s", reviewerID, repairerID))
					}
				}
			}
		}
		if gateErr == nil && result.Passed {
			w.gateMemoStore(c.TargetRef, spec.SHA, spec.RuntimeConfigDigest, result)
		}
	}
	return w.update(c.ID, func(state *State, cur *Candidate) {
		if w.operatorIntervened(cur, "gate") || w.leaseLost(cur, Gating, "gate") {
			return
		}
		cur.Evidence = append(cur.Evidence, result.Evidence...)
		cur.GateEvidence = append(cur.GateEvidence, result.Evidence...)
		cur.GateLog = result.Log
		if gateErr != nil || !result.Passed {
			if gateErr == nil {
				gateErr = fmt.Errorf("deterministic gate failed")
			}
			w.failGate(state, cur, gateErr)
			return
		}
		// spec.SHA may have advanced past the first closure's snapshot when a
		// repair committed into the workspace; the durable identity follows it.
		cur.SpeculativeSHA, cur.TreeSHA = spec.SHA, spec.SHA
		if cur.TreeSHA == "" || cur.BaseSHA == "" {
			w.failGate(state, cur, fmt.Errorf("prepared result is missing base or tree identity"))
			return
		}
		cur.GateVersion = first(result.GateVersion, w.Deps.GateVersion, "deterministic-gate/v1")
		cur.GatePolicyDigest = w.gatePolicyDigest(cur.TargetRef, spec.RuntimeConfigDigest)
		// The CI receipt seals the candidate environment; this binds that receipt
		// and the effective gate version to this exact prospective tree.
		cur.DependencyFingerprint = preparedFingerprint(*cur)
		cur.ValidatedSHA, cur.WorkerID, cur.LeaseExpiresAt = cur.TreeSHA, "", time.Time{}
		if cur.finalizationPolicy() == StewardReviewFinalization && !cur.OverrideGate {
			cur.Phase, cur.Status = AwaitingApproval, AwaitingApproval
		} else {
			cur.Phase, cur.Status = ReadyToFinalize, ReadyToFinalize
		}
		// A clean preparation ends whatever environmental-failure streak this
		// candidate was on; stale streak state must not linger into a later,
		// unrelated environmental blip.
		cur.EnvRetries, cur.FirstEnvFailureAt = 0, time.Time{}
		cur.EnvFailureSignature, cur.EnvRepeatStreak = "", 0
	})
}

func (w Worker) runGate(ctx context.Context, c Candidate, spec Speculation) (GateResult, error) {
	ctx, cancel := context.WithTimeout(ctx, w.Deps.stageTimeout())
	defer cancel()
	admission := w.Deps.GateAdmission
	if admission == nil {
		admission = DefaultFileGateCapacity()
	}
	request := GateAdmissionRequest{
		ProjectID: c.ProjectID,
		TargetRef: c.TargetRef,
		Tier:      w.gateTier(c.TargetRef),
		WorkerID:  first(w.Deps.WorkerID, "queue-worker"),
	}
	type leaseAdmission interface {
		AcquireLease(context.Context, GateAdmissionRequest) (*FileGateLease, error)
	}
	if capacity, ok := admission.(leaseAdmission); ok {
		lease, err := capacity.AcquireLease(ctx, request)
		if err != nil {
			return GateResult{}, Environmental(fmt.Errorf("queue: acquire gate capacity: %w", err))
		}
		defer lease.Release()
		ctx = withGateCapacityLease(ctx, lease)
	} else {
		release, err := admission.Acquire(ctx, request)
		if err != nil {
			return GateResult{}, Environmental(fmt.Errorf("queue: acquire gate capacity: %w", err))
		}
		defer release()
	}
	ctx = context.WithValue(ctx, gateTierContextKey{}, request.Tier)
	return w.Deps.Gate.Run(ctx, spec)
}

// operatorIntervened reports whether the candidate was parked or rejected by an
// operator while this worker held its lease. Worker results never clobber a
// durable operator decision; the discarded outcome is recorded as evidence.
func (w Worker) operatorIntervened(cur *Candidate, stage string) bool {
	if parked(cur.phase()) || terminal(cur.phase()) || cur.phase() == AwaitingApproval {
		cur.Evidence = append(cur.Evidence, fmt.Sprintf("queue:%s result discarded; operator moved candidate to %s", stage, cur.phase()))
		return true
	}
	return false
}

// leaseLost is the fencing check for delayed workers: it reports whether this
// worker no longer holds cur's lease in the expected in-flight phase. A worker
// that is slow — not dead — (GC pause, CPU pressure, remote-worker lag) can
// blow past its lease TTL, have the candidate reclaimed by another worker, and
// only then return from its in-flight call; without this check its stale
// result would be applied on top of the reclaiming worker's, double-running
// finalization. The stale outcome is discarded and recorded as evidence; the
// reclaiming worker owns the candidate. Relies on WorkerID being unique per
// worker, which the CLI guarantees (base, base-1..N).
func (w Worker) leaseLost(cur *Candidate, expected Status, stage string) bool {
	if cur.WorkerID == first(w.Deps.WorkerID, "queue-worker") && cur.phase() == expected {
		return false
	}
	cur.Evidence = append(cur.Evidence, fmt.Sprintf("queue:%s result from worker %s discarded; lease no longer held (candidate is %s, worker %q)", stage, first(w.Deps.WorkerID, "queue-worker"), cur.phase(), cur.WorkerID))
	return true
}

func (w Worker) finalize(ctx context.Context) (bool, error) {
	var candidate Candidate
	var ok bool
	_, err := w.Store.withLock(func(path string) (State, error) {
		state, dirty, err := w.Store.readCompacted(path)
		if err != nil {
			return State{}, err
		}
		// The finalization head is the first candidate in claim order that is
		// still on the train. Parked candidates (needs_input /
		// needs_conflict_input) and retry-waiting candidates whose timer has
		// not elapsed are skipped: a stuck candidate delays only itself.
		n := now(w.Deps)
		var head *Candidate
		for i := range state.Candidates {
			c := &state.Candidates[i]
			if !w.matchesTarget(*c) {
				continue
			}
			if terminal(c.phase()) || parked(c.phase()) {
				continue
			}
			if c.phase() == RetryWait && !c.RetryAt.IsZero() && n.Before(c.RetryAt) {
				continue
			}
			if head == nil || before(*c, *head) {
				head = c
			}
		}
		if head != nil && head.phase() == ReadyToFinalize &&
			head.GatePolicyDigest != w.gatePolicyDigest(head.TargetRef, head.RuntimeConfigDigest) {
			clearApproval(head)
			clearPreparedIdentity(head)
			head.Phase, head.Status = Reprepare, Reprepare
			head.Failure = "gate or repair-review policy changed after preparation"
			head.Evidence = append(head.Evidence, "queue:prepared-policy-invalidated")
			return state, write(path, state)
		}
		if head != nil && head.phase() == ReadyToFinalize && finalizationAuthorized(*head) {
			w.lease(head, Finalizing, n)
			candidate, ok = *head, true
			return state, write(path, state)
		}
		if dirty {
			return state, write(path, state)
		}
		// An idle worker must be a read-only observer. Rewriting the whole
		// queue here turns a quiet, large train into a perpetual JSON
		// marshal/fsync loop even though no durable state changed.
		return state, nil
	})
	if err != nil || !ok {
		return false, err
	}
	var result FinalizeResult
	w.heartbeat(ctx, candidate.ID, func() {
		if w.Deps.Finalizer != nil {
			result, err = w.Deps.Finalizer.Finalize(ctx, candidate)
		} else {
			err = w.Deps.Integration.Land(ctx, Speculation{SHA: candidate.TreeSHA, BaseSHA: candidate.BaseSHA, IntegrationRef: candidate.IntegrationRef, WorkspaceID: candidate.WorkspaceID, WorkspacePath: candidate.WorkspacePath})
		}
	})
	if updateErr := w.update(candidate.ID, func(state *State, cur *Candidate) {
		if w.operatorIntervened(cur, "finalization") {
			return
		}
		// A successful compare-and-swap is authoritative even when this
		// worker's lease has lapsed: exactly one CAS can win per target
		// revision, so the ref genuinely moved and refusing to record it
		// would leave the durable state claiming un-landed while the target
		// advanced (livelocking on repeated stale finalizations). Fencing
		// therefore discards only stale and failed results from a lapsed
		// lease.
		landed := err == nil && !result.Stale && !(result.NewMainSHA == "" && w.Deps.Finalizer != nil)
		if !landed && w.leaseLost(cur, Finalizing, "finalization") {
			return
		}
		if landed && (cur.WorkerID != first(w.Deps.WorkerID, "queue-worker") || cur.phase() != Finalizing) {
			cur.Evidence = append(cur.Evidence, fmt.Sprintf("queue:finalization CAS won by worker %s after lease loss; landing recorded authoritatively", first(w.Deps.WorkerID, "queue-worker")))
		}
		cur.FinalizationLog = result.Log
		if err != nil {
			// Hard finalization failure consumes a bounded attempt; only a
			// stale base (normal train movement) re-prepares for free.
			cur.WorkerID, cur.LeaseExpiresAt, cur.Failure = "", time.Time{}, err.Error()
			cur.Evidence = append(cur.Evidence, err.Error())
			var harness HarnessError
			if errors.As(err, &harness) {
				w.parkHuman(cur, "finalization_harness_failure", ReasonHarnessFailure)
				return
			}
			var envErr EnvError
			if errors.As(err, &envErr) {
				w.retryOrParkEnv(cur, "finalization_failed", envErr.Err)
				return
			}
			w.retryOrPark(state, cur, "finalization_failed")
			return
		}
		if result.Stale || result.NewMainSHA == "" && w.Deps.Finalizer != nil {
			clearApproval(cur)
			cur.Phase, cur.Status, cur.WorkerID, cur.LeaseExpiresAt = Reprepare, Reprepare, "", time.Time{}
			cur.Failure = "protected base changed after gate; reprepare required"
			return
		}
		cur.Phase, cur.Status, cur.WorkerID, cur.LeaseExpiresAt, cur.Completed = Landed, Landed, "", time.Time{}, now(w.Deps)
		cur.ResultMainSHA = first(result.NewMainSHA, cur.TreeSHA)
		cur.FinalizationLog = first(result.Log, cur.FinalizationLog)
		cur.EnvRetries, cur.FirstEnvFailureAt = 0, time.Time{}
		cur.EnvFailureSignature, cur.EnvRepeatStreak = "", 0
	}); updateErr != nil {
		return true, updateErr
	}
	return true, nil
}

func (w Worker) ahead(sequence uint64) ([]Candidate, error) {
	state, err := w.Store.List()
	if err != nil {
		return nil, err
	}
	var out []Candidate
	var target, project string
	for _, c := range state.Candidates {
		if c.Sequence == sequence {
			target, project = c.TargetRef, c.ProjectID
			break
		}
	}
	for _, c := range state.Candidates {
		if c.Sequence < sequence && c.TargetRef == target && c.ProjectID == project && !terminal(c.phase()) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (w Worker) targetRef() string { return strings.TrimSpace(w.Deps.TargetRef) }

func (w Worker) matchesTarget(c Candidate) bool {
	target := w.targetRef()
	if target != "" && c.TargetRef != target {
		return false
	}
	return c.RequiredGateTier == w.gateTier(c.TargetRef)
}

func (w Worker) gateTier(target string) string {
	if tier := strings.TrimSpace(w.Deps.GateTier); tier != "" {
		return tier
	}
	if configured := w.targetRef(); configured != "" {
		target = configured
	}
	return RequiredGateTierForTarget(target)
}
func (w Worker) update(id string, mutate func(*State, *Candidate)) error {
	_, err := w.Store.withLock(func(path string) (State, error) {
		state, _, err := w.Store.read(path)
		if err != nil {
			return State{}, err
		}
		for i := range state.Candidates {
			if state.Candidates[i].ID == id {
				if !w.matchesTarget(state.Candidates[i]) {
					return State{}, fmt.Errorf("queue: worker target %q refuses candidate %s for target %q", w.targetRef(), id, state.Candidates[i].TargetRef)
				}
				mutate(&state, &state.Candidates[i])
				return state, write(path, state)
			}
		}
		return State{}, fmt.Errorf("queue: candidate %s disappeared", id)
	})
	return err
}
func (w Worker) lease(c *Candidate, phase Status, at time.Time) {
	c.Phase, c.Status, c.PhaseStartedAt, c.WorkerID, c.LeaseExpiresAt = phase, phase, at, first(w.Deps.WorkerID, "queue-worker"), at.Add(firstDuration(w.Deps.Lease, 30*time.Second))
}

// heartbeat renews c's lease on a ticker for the duration of fn, so a
// long-running Speculate/Gate.Run/Finalize call — which can run well past a
// single lease window (the deterministic gate in particular may be a full
// CI run) — never has its lease reclaimed by a second worker mid-operation.
// The renewal is advisory: it only ever extends a lease this worker still
// actively holds in an in-flight phase (see renewLease), so a candidate an
// operator has since parked or rejected is never touched by it.
func (w Worker) heartbeat(ctx context.Context, id string, fn func()) {
	lease := firstDuration(w.Deps.Lease, 30*time.Second)
	interval := lease / 3
	if interval < 50*time.Millisecond {
		interval = 50 * time.Millisecond
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				_ = w.renewLease(id)
			}
		}
	}()
	fn()
	close(done)
	<-stopped
}

// renewLease extends id's lease if, and only if, it is still actively held
// by this worker in one of the in-flight phases (Preparing, Gating,
// Finalizing). It never changes phase or any other field — a candidate that
// has already moved on (lease reclaimed elsewhere, operator intervention)
// is left completely untouched, so the heartbeat can never resurrect a
// stolen or parked candidate's lease.
func (w Worker) renewLease(id string) error {
	return w.update(id, func(_ *State, cur *Candidate) {
		if cur.WorkerID != first(w.Deps.WorkerID, "queue-worker") {
			return
		}
		switch cur.phase() {
		case Preparing, Gating, Finalizing:
			cur.LeaseExpiresAt = now(w.Deps).Add(firstDuration(w.Deps.Lease, 30*time.Second))
		}
	})
}
func (w Worker) gateMemoLookup(targetRef, treeSHA, runtimeConfigDigest string) (GateResult, bool) {
	if w.Deps.GateMemo == nil || strings.TrimSpace(w.Deps.GateVersion) == "" {
		return GateResult{}, false
	}
	return w.Deps.GateMemo.Lookup(treeSHA, w.Deps.GateVersion, w.gatePolicyDigest(targetRef, runtimeConfigDigest))
}
func (w Worker) gateMemoStore(targetRef, treeSHA, runtimeConfigDigest string, result GateResult) {
	if w.Deps.GateMemo == nil || strings.TrimSpace(w.Deps.GateVersion) == "" {
		return
	}
	_ = w.Deps.GateMemo.Store(treeSHA, w.Deps.GateVersion, w.gatePolicyDigest(targetRef, runtimeConfigDigest), result)
}
func (w Worker) gatePolicyDigest(targetRef, runtimeConfigDigest string) string {
	return fingerprint(
		strings.TrimSpace(runtimeConfigDigest),
		strings.TrimSpace(w.Deps.TargetRef),
		strings.TrimSpace(w.Deps.GateVersion),
		w.gateTier(targetRef),
		strings.TrimSpace(w.Deps.ReviewPolicyDigest),
	)
}
func (w Worker) failPreparation(state *State, c *Candidate, err error) {
	c.WorkerID, c.LeaseExpiresAt, c.Failure = "", time.Time{}, err.Error()
	c.Evidence = append(c.Evidence, err.Error())
	var harness HarnessError
	if errors.As(err, &harness) {
		w.parkHuman(c, "resolver_harness_failure", ReasonHarnessFailure)
		return
	}
	if strings.Contains(err.Error(), "continuation") {
		c.Phase, c.Status, c.ConflictContinuation = NeedsConflictInput, NeedsConflictInput, err.Error()
		c.RetryReason, c.ReasonCode = "merge_conflict_unresolved", ReasonMergeConflict
		c.ParkedAt, c.ParkedBy = now(w.Deps), "queue-worker"
		return
	}
	var envErr EnvError
	if errors.As(err, &envErr) {
		w.retryOrParkEnv(c, "speculation_failed", envErr.Err)
		return
	}
	w.retryOrPark(state, c, "speculation_failed")
}
func (w Worker) failGate(state *State, c *Candidate, err error) {
	c.WorkerID, c.LeaseExpiresAt, c.Failure = "", time.Time{}, err.Error()
	var harness HarnessError
	if errors.As(err, &harness) {
		w.parkHuman(c, "gate_harness_failure", ReasonHarnessFailure)
		return
	}
	// A remote gate can fail environmentally (worker died, transport lost)
	// without any verdict on the candidate; that burns the lenient env-retry
	// budget, matching speculation and finalization.
	var envErr EnvError
	if errors.As(err, &envErr) {
		if envErr.Immediate {
			w.parkNoVerdict(c, "gate_no_verdict", envErr.Cause)
			return
		}
		w.retryOrParkEnv(c, "gate_failed", envErr.Err)
		return
	}
	w.retryOrPark(state, c, "gate_failed")
}

// parkNoVerdict parks a candidate whose gate never produced a verdict for a
// nameable environmental reason — a missing toolchain, a killed or starved gate.
// It parks immediately and loudly rather than retrying: the condition will not
// clear on its own, and spending the retry window on it only makes the same
// answer arrive slower. Five attempts across ~25 minutes on an unchanged SHA is
// the failure mode being removed here.
//
// The claim-time attempt increment is rolled back because the gate never judged
// this candidate's code, so a later legitimate attempt still gets its full
// budget. The environmental bookkeeping is still recorded, so `queue status`
// shows the class instead of leaving first_env_failure_at at the zero value
// while retry_reason says a flat gate_failed.
func (w Worker) parkNoVerdict(c *Candidate, stage, cause string) {
	n := now(w.Deps)
	if c.Attempt > 0 {
		c.Attempt--
	}
	if c.FirstEnvFailureAt.IsZero() {
		c.FirstEnvFailureAt = n
	}
	c.EnvRetries++
	reason := stage
	if strings.TrimSpace(cause) != "" {
		reason = stage + "_" + cause
	}
	w.park(c, reason)
	c.Evidence = append(c.Evidence, fmt.Sprintf("queue:gate produced no verdict (%s); parked immediately as needs_input without consuming the product attempt budget", first(cause, "unclassified")))
}

// retryOrPark applies the bounded retry policy: requeue to the back of the
// line under exponential backoff, or park as needs_input once attempts are
// exhausted. Both outcomes are durable and human-recoverable (kick / resume /
// override), so a failing candidate can delay only itself, never wedge the
// train, and never spin unbounded.
func (w Worker) retryOrPark(state *State, c *Candidate, reason string) {
	if c.Attempt >= w.Deps.maxAttempts() {
		code := exhaustionReasonCode(reason, w.Deps.Repairer != nil)
		w.parkHuman(c, "max_attempts_exhausted", code)
		c.Evidence = append(c.Evidence, fmt.Sprintf("queue:max attempts (%d) exhausted after %s; parked as needs_human (%s)", c.Attempt, reason, code))
		return
	}
	n := now(w.Deps)
	c.Phase, c.Status, c.RetryReason, c.ReasonCode = RetryWait, RetryWait, reason, reasonCodeForStage(reason)
	c.RetryAt = n.Add(backoff(w.Deps.retryDelay(), w.Deps.maxRetryDelay(), c.Attempt))
	c.Position = nextPosition(state.Candidates)
	c.Evidence = append(c.Evidence, fmt.Sprintf("queue:attempt %d/%d failed (%s); retry_at=%s position=%d", c.Attempt, w.Deps.maxAttempts(), reason, c.RetryAt.Format(time.RFC3339), c.Position))
}

// retryOrParkEnv applies the environmental retry policy: a short fixed
// backoff that does not consume the bounded product-failure attempt budget
// — the claim-time increment in claimPreparation is rolled back here — and
// is bounded two ways, not by an attempt count. The candidate keeps its
// queue position: an environmental failure said nothing about this
// candidate's own tree, so unlike retryOrPark it does not need to lose its
// place in line, only to be skipped by claim/finalize while its timer is
// running (the same skip every retry_wait candidate already gets).
//
// The first bound is wall-clock time elapsed since this candidate's current
// environmental-failure streak began: a candidate stuck in a persistently
// degraded environment still eventually parks instead of retrying forever.
//
// The second bound is repetition: cause's exact message is compared against
// the immediately preceding environmental failure's. Retrying is only useful
// when the situation might have changed, and a remote gate returning the
// byte-identical non-answer is proof it has not — POG candidate
// queue-58a9285a62d8 retried 172 times over 2 hours against a static
// "resolve worker image: ... no such file or directory" failure that could
// never have cleared by waiting. An unbroken streak of maxEnvRepeat()
// identical messages (default 2: the first failure plus one retry that
// reproduces it exactly) parks at once, well before the wall-clock bound
// would. A *different* message each attempt — a different lock holder, a
// different transient network symptom — resets the streak to 1 and keeps
// the full wall-clock-bounded leniency, since varying messages are what
// genuine transient infrastructure actually looks like; only sameness is
// the signal, not the mere presence of an environmental classification like
// outcome=unknown.
func (w Worker) retryOrParkEnv(c *Candidate, reason string, cause error) {
	n := now(w.Deps)
	if c.Attempt > 0 {
		c.Attempt--
	}
	if c.FirstEnvFailureAt.IsZero() {
		c.FirstEnvFailureAt = n
	}
	c.EnvRetries++

	sig := envFailureSignature(cause)
	if sig != "" && sig == c.EnvFailureSignature {
		c.EnvRepeatStreak++
	} else {
		c.EnvFailureSignature, c.EnvRepeatStreak = sig, 1
	}
	if sig != "" && c.EnvRepeatStreak >= w.Deps.maxEnvRepeat() {
		w.park(c, reason+"_repeated_outcome")
		c.Evidence = append(c.Evidence, fmt.Sprintf("queue:environmental failure repeated identically %d time(s) (%q); parked as needs_input instead of retrying an unchanged condition", c.EnvRepeatStreak, sig))
		return
	}

	if n.Sub(c.FirstEnvFailureAt) >= w.Deps.maxEnvDuration() {
		w.parkHuman(c, reason+"_environment_degraded", ReasonEnvironmentDegraded)
		c.Evidence = append(c.Evidence, fmt.Sprintf("queue:environment degraded for %s since %s; parked as needs_human", n.Sub(c.FirstEnvFailureAt).Round(time.Second), c.FirstEnvFailureAt.Format(time.RFC3339)))
		return
	}
	c.Phase, c.Status, c.RetryReason, c.ReasonCode = RetryWait, RetryWait, reason, reasonCodeForStage(reason)
	c.RetryAt = n.Add(w.Deps.envRetryDelay())
	c.Evidence = append(c.Evidence, fmt.Sprintf("queue:environmental failure (%s), retry %d, retry_at=%s", reason, c.EnvRetries, c.RetryAt.Format(time.RFC3339)))
}

// envFailureSignature normalizes an environmental failure's cause into a
// comparable string for repeat detection in retryOrParkEnv. Comparison is
// intentionally exact, not fuzzy: any difference in the message — a
// different verdict summary, a different infrastructure symptom — is a real
// change worth another attempt, not noise to be smoothed over. A nil cause
// (defensive only; Environmental/EnvironmentalImmediate never wrap a nil
// error) signs out of repeat detection entirely rather than matching itself.
func envFailureSignature(cause error) string {
	if cause == nil {
		return ""
	}
	return strings.TrimSpace(cause.Error())
}

func (w Worker) park(c *Candidate, reason string) {
	c.Phase, c.Status, c.RetryReason = NeedsInput, NeedsInput, reason
	c.ReasonCode = reasonCodeForStage(reason)
	c.WorkerID, c.LeaseExpiresAt, c.RetryAt = "", time.Time{}, time.Time{}
	c.ParkedAt, c.ParkedBy = now(w.Deps), "queue-worker"
}

// parkHuman is the queue's own declaration that automation is out of options
// for c: a broken harness, an exhausted bounded attempt budget, or an
// environment that stayed degraded past its wall-clock bound. It is the only
// producer of the NeedsHuman status (see its doc) and always stamps the
// typed ReasonCode, an evidence pointer (a log path if one is durably
// recorded, else this candidate's own ID), and a timestamp — the three
// things a human or a future medic needs without parsing Evidence prose.
// Otherwise it is identical to a plain park: never auto-retried (see
// parked), resumable by Store.Resume.
func (w Worker) parkHuman(c *Candidate, reason string, code ReasonCode) {
	c.Phase, c.Status, c.RetryReason, c.ReasonCode = NeedsHuman, NeedsHuman, reason, code
	c.WorkerID, c.LeaseExpiresAt, c.RetryAt = "", time.Time{}, time.Time{}
	c.ParkedAt, c.ParkedBy = now(w.Deps), "queue-worker"
	c.NeedsHumanEvidenceRef = first(c.GateLog, c.FinalizationLog, c.WorkspacePath, c.ID)
}

func backoff(base, max time.Duration, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= max {
			return max
		}
	}
	if d > max {
		return max
	}
	return d
}
func first(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
func firstDuration(v, fallback time.Duration) time.Duration {
	if v > 0 {
		return v
	}
	return fallback
}
func fingerprint(values ...string) string {
	h := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return "sha256:" + hex.EncodeToString(h[:])
}

func preparedFingerprint(c Candidate) string {
	return fingerprint(c.ReceiptDigest, c.TargetRef, c.TargetBaseSHAAtAdmission, c.SHA, c.BaseSHA, c.TreeSHA, c.GateVersion, c.RuntimeConfigDigest, c.GatePolicyDigest)
}

func approvalFingerprint(c Candidate) string {
	return fingerprint(preparedFingerprint(c), string(c.TargetPolicy), c.ManifestDigest, c.RuntimeInstance, c.RuntimeReceipt, strings.Join(c.RequiredReceiptIDs, "\x00"))
}

func validateRequiredReceipts(c Candidate) error {
	for _, id := range c.RequiredReceiptIDs {
		if id != c.ReceiptID {
			return fmt.Errorf("queue: required receipt %q is not present on current candidate", id)
		}
	}
	return nil
}

func finalizationAuthorized(c Candidate) bool {
	if c.OverrideGate {
		return true
	}
	if c.finalizationPolicy() != StewardReviewFinalization {
		return true
	}
	if c.Approval == nil || c.Approval.Fingerprint != approvalFingerprint(c) {
		return false
	}
	return validateRequiredReceipts(c) == nil && validatePreparedTuple(c) == nil
}

func clearApproval(c *Candidate) {
	if c.Approval != nil {
		c.Evidence = append(c.Evidence, "queue:approval invalidated by repreparation")
	}
	c.Approval = nil
}

// clearPreparedIdentity prevents a failed or interrupted repreparation from
// leaving the prior tree, gate, or runtime-policy proof looking current.
func clearPreparedIdentity(c *Candidate) {
	c.BaseSHA = ""
	c.TreeSHA = ""
	c.SpeculativeSHA = ""
	c.ValidatedSHA = ""
	c.GateVersion = ""
	c.GatePolicyDigest = ""
	c.DependencyFingerprint = ""
	c.RuntimeConfigDigest = ""
	c.IntegrationRef = ""
	c.WorkspaceID = ""
	c.WorkspacePath = ""
}

func validatePreparedTuple(c Candidate) error {
	if c.BaseSHA == "" || c.TreeSHA == "" || c.GateVersion == "" || c.GatePolicyDigest == "" || c.DependencyFingerprint == "" {
		return fmt.Errorf("queue: finalization requires a complete prepared receipt tuple")
	}
	if c.DependencyFingerprint != preparedFingerprint(c) {
		return fmt.Errorf("queue: prepared receipt tuple does not match candidate, base, tree, and gate identity")
	}
	return nil
}

func firstGateError(err error) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("deterministic gate failed")
}
