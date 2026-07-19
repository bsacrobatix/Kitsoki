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
		state, err := read(path)
		if err != nil {
			return State{}, err
		}
		n := now(w.Deps)
		for i := range state.Candidates {
			c := &state.Candidates[i]
			if (c.phase() == Preparing || c.phase() == Gating || c.phase() == Finalizing) && !c.LeaseExpiresAt.IsZero() && !n.Before(c.LeaseExpiresAt) {
				c.Phase, c.Status, c.WorkerID, c.LeaseExpiresAt = Reprepare, Reprepare, "", time.Time{}
				c.Failure = "worker lease expired; preserved attempt evidence requires reprepare"
			}
		}
		var pick *Candidate
		for i := range state.Candidates {
			c := &state.Candidates[i]
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
			w.lease(pick, Preparing, n)
			pick.Attempt++
			pick.Started = n
			pick.Failure = ""
			pick.RetryAt = time.Time{}
			claimed, ok = *pick, true
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
	spec, specErr := w.Deps.Integration.Speculate(ctx, c, ahead)
	if err := w.update(c.ID, func(state *State, cur *Candidate) {
		if w.operatorIntervened(cur, "speculation") {
			return
		}
		cur.SpeculativeSHA, cur.TreeSHA, cur.BaseSHA = spec.SHA, spec.SHA, spec.BaseSHA
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
	} else {
		result, gateErr = w.Deps.Gate.Run(ctx, spec)
		if (gateErr != nil || !result.Passed) && w.Deps.Repairer != nil {
			repairEvidence, repairErr := w.Deps.Repairer.Repair(ctx, spec, firstGateError(gateErr))
			result.Evidence = append(result.Evidence, repairEvidence...)
			if repairErr == nil {
				result, gateErr = w.Deps.Gate.Run(ctx, spec)
				result.Evidence = append(result.Evidence, repairEvidence...)
			} else if gateErr == nil {
				gateErr = repairErr
			}
		}
	}
	return w.update(c.ID, func(state *State, cur *Candidate) {
		if w.operatorIntervened(cur, "gate") {
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
		if cur.TreeSHA == "" || cur.BaseSHA == "" {
			w.failGate(state, cur, fmt.Errorf("prepared result is missing base or tree identity"))
			return
		}
		cur.GateVersion = first(result.GateVersion, w.Deps.GateVersion, "deterministic-gate/v1")
		// The CI receipt seals the candidate environment; this binds that receipt
		// and the effective gate version to this exact prospective tree.
		cur.DependencyFingerprint = preparedFingerprint(*cur)
		cur.ValidatedSHA, cur.WorkerID, cur.LeaseExpiresAt = cur.TreeSHA, "", time.Time{}
		if cur.finalizationPolicy() == StewardReviewFinalization && !cur.OverrideGate {
			cur.Phase, cur.Status = AwaitingApproval, AwaitingApproval
		} else {
			cur.Phase, cur.Status = ReadyToFinalize, ReadyToFinalize
		}
	})
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

func (w Worker) finalize(ctx context.Context) (bool, error) {
	var candidate Candidate
	var ok bool
	_, err := w.Store.withLock(func(path string) (State, error) {
		state, err := read(path)
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
		if head != nil && head.phase() == ReadyToFinalize && finalizationAuthorized(*head) {
			w.lease(head, Finalizing, n)
			candidate, ok = *head, true
		}
		return state, write(path, state)
	})
	if err != nil || !ok {
		return false, err
	}
	var result FinalizeResult
	if w.Deps.Finalizer != nil {
		result, err = w.Deps.Finalizer.Finalize(ctx, candidate)
	} else {
		err = w.Deps.Integration.Land(ctx, Speculation{SHA: candidate.TreeSHA, BaseSHA: candidate.BaseSHA, IntegrationRef: candidate.IntegrationRef, WorkspaceID: candidate.WorkspaceID, WorkspacePath: candidate.WorkspacePath})
	}
	if updateErr := w.update(candidate.ID, func(state *State, cur *Candidate) {
		if w.operatorIntervened(cur, "finalization") {
			return
		}
		cur.FinalizationLog = result.Log
		if err != nil {
			// Hard finalization failure consumes a bounded attempt; only a
			// stale base (normal train movement) re-prepares for free.
			cur.WorkerID, cur.LeaseExpiresAt, cur.Failure = "", time.Time{}, err.Error()
			cur.Evidence = append(cur.Evidence, err.Error())
			var harness HarnessError
			if errors.As(err, &harness) {
				w.park(cur, "finalization_harness_failure")
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
	for _, c := range state.Candidates {
		if c.Sequence < sequence && !terminal(c.phase()) {
			out = append(out, c)
		}
	}
	return out, nil
}
func (w Worker) update(id string, mutate func(*State, *Candidate)) error {
	_, err := w.Store.withLock(func(path string) (State, error) {
		state, err := read(path)
		if err != nil {
			return State{}, err
		}
		for i := range state.Candidates {
			if state.Candidates[i].ID == id {
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
func (w Worker) failPreparation(state *State, c *Candidate, err error) {
	c.WorkerID, c.LeaseExpiresAt, c.Failure = "", time.Time{}, err.Error()
	c.Evidence = append(c.Evidence, err.Error())
	var harness HarnessError
	if errors.As(err, &harness) {
		w.park(c, "resolver_harness_failure")
		return
	}
	if strings.Contains(err.Error(), "continuation") {
		c.Phase, c.Status, c.ConflictContinuation = NeedsConflictInput, NeedsConflictInput, err.Error()
		return
	}
	w.retryOrPark(state, c, "speculation_failed")
}
func (w Worker) failGate(state *State, c *Candidate, err error) {
	c.WorkerID, c.LeaseExpiresAt, c.Failure = "", time.Time{}, err.Error()
	var harness HarnessError
	if errors.As(err, &harness) {
		w.park(c, "gate_harness_failure")
		return
	}
	w.retryOrPark(state, c, "gate_failed")
}

// retryOrPark applies the bounded retry policy: requeue to the back of the
// line under exponential backoff, or park as needs_input once attempts are
// exhausted. Both outcomes are durable and human-recoverable (kick / resume /
// override), so a failing candidate can delay only itself, never wedge the
// train, and never spin unbounded.
func (w Worker) retryOrPark(state *State, c *Candidate, reason string) {
	if c.Attempt >= w.Deps.maxAttempts() {
		w.park(c, reason)
		c.Evidence = append(c.Evidence, fmt.Sprintf("queue:max attempts (%d) exhausted; parked as needs_input", c.Attempt))
		c.RetryReason = "max_attempts_exhausted"
		return
	}
	n := now(w.Deps)
	c.Phase, c.Status, c.RetryReason = RetryWait, RetryWait, reason
	c.RetryAt = n.Add(backoff(w.Deps.retryDelay(), w.Deps.maxRetryDelay(), c.Attempt))
	c.Position = nextPosition(state.Candidates)
	c.Evidence = append(c.Evidence, fmt.Sprintf("queue:attempt %d/%d failed (%s); retry_at=%s position=%d", c.Attempt, w.Deps.maxAttempts(), reason, c.RetryAt.Format(time.RFC3339), c.Position))
}

func (w Worker) park(c *Candidate, reason string) {
	c.Phase, c.Status, c.RetryReason = NeedsInput, NeedsInput, reason
	c.WorkerID, c.LeaseExpiresAt, c.RetryAt = "", time.Time{}, time.Time{}
	c.ParkedAt = now(w.Deps)
	c.ParkedBy = "queue-worker"
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
	return fingerprint(c.ReceiptDigest, c.SHA, c.BaseSHA, c.TreeSHA, c.GateVersion)
}

func approvalFingerprint(c Candidate) string {
	return fingerprint(preparedFingerprint(c), c.ManifestDigest, c.RuntimeInstance, c.RuntimeReceipt, strings.Join(c.RequiredReceiptIDs, "\x00"))
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

func validatePreparedTuple(c Candidate) error {
	if c.BaseSHA == "" || c.TreeSHA == "" || c.GateVersion == "" || c.DependencyFingerprint == "" {
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
