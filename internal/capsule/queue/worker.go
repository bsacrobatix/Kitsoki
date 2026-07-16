package queue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
		for i := range state.Candidates {
			c := &state.Candidates[i]
			if c.phase() != Queued && c.phase() != Reprepare {
				continue
			}
			if !c.RetryAt.IsZero() && n.Before(c.RetryAt) {
				continue
			}
			w.lease(c, Preparing, n)
			c.Attempt++
			c.Started = n
			c.Failure = ""
			claimed, ok = *c, true
			return state, write(path, state)
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
	if err := w.update(c.ID, func(cur *Candidate) {
		cur.SpeculativeSHA, cur.TreeSHA, cur.BaseSHA = spec.SHA, spec.SHA, spec.BaseSHA
		cur.IntegrationRef, cur.WorkspaceID, cur.WorkspacePath = spec.IntegrationRef, spec.WorkspaceID, spec.WorkspacePath
		cur.Evidence = append(cur.Evidence, spec.Evidence...)
		if specErr != nil {
			w.failPreparation(cur, specErr)
			return
		}
		w.lease(cur, Gating, now(w.Deps))
	}); err != nil {
		return err
	}
	if specErr != nil {
		return nil
	}
	result, gateErr := w.Deps.Gate.Run(ctx, spec)
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
	return w.update(c.ID, func(cur *Candidate) {
		cur.Evidence = append(cur.Evidence, result.Evidence...)
		cur.GateEvidence = append(cur.GateEvidence, result.Evidence...)
		cur.GateLog = result.Log
		if gateErr != nil || !result.Passed {
			if gateErr == nil {
				gateErr = fmt.Errorf("deterministic gate failed")
			}
			w.failGate(cur, gateErr)
			return
		}
		if cur.TreeSHA == "" || cur.BaseSHA == "" {
			w.failGate(cur, fmt.Errorf("prepared result is missing base or tree identity"))
			return
		}
		cur.GateVersion = first(result.GateVersion, w.Deps.GateVersion, "deterministic-gate/v1")
		// The CI receipt seals the candidate environment; this binds that receipt
		// and the effective gate version to this exact prospective tree.
		cur.DependencyFingerprint = preparedFingerprint(*cur)
		cur.ValidatedSHA, cur.Phase, cur.Status, cur.WorkerID, cur.LeaseExpiresAt = cur.TreeSHA, ReadyToFinalize, ReadyToFinalize, "", time.Time{}
	})
}

func (w Worker) finalize(ctx context.Context) (bool, error) {
	var candidate Candidate
	var ok bool
	_, err := w.Store.withLock(func(path string) (State, error) {
		state, err := read(path)
		if err != nil {
			return State{}, err
		}
		for i := range state.Candidates {
			c := &state.Candidates[i]
			if terminal(c.phase()) {
				continue
			}
			if c.phase() != ReadyToFinalize {
				return state, write(path, state)
			}
			w.lease(c, Finalizing, now(w.Deps))
			candidate, ok = *c, true
			return state, write(path, state)
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
	if updateErr := w.update(candidate.ID, func(cur *Candidate) {
		cur.FinalizationLog = result.Log
		if err != nil || result.Stale || result.NewMainSHA == "" && w.Deps.Finalizer != nil {
			cur.Phase, cur.Status, cur.WorkerID, cur.LeaseExpiresAt = Reprepare, Reprepare, "", time.Time{}
			if err != nil {
				cur.Failure = err.Error()
			} else {
				cur.Failure = "protected base changed after gate; reprepare required"
			}
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
func (w Worker) update(id string, mutate func(*Candidate)) error {
	_, err := w.Store.withLock(func(path string) (State, error) {
		state, err := read(path)
		if err != nil {
			return State{}, err
		}
		for i := range state.Candidates {
			if state.Candidates[i].ID == id {
				mutate(&state.Candidates[i])
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
func (w Worker) failPreparation(c *Candidate, err error) {
	c.WorkerID, c.LeaseExpiresAt, c.Failure = "", time.Time{}, err.Error()
	c.Evidence = append(c.Evidence, err.Error())
	if strings.Contains(err.Error(), "continuation") {
		c.Phase, c.Status, c.ConflictContinuation = NeedsConflictInput, NeedsConflictInput, err.Error()
	} else {
		c.Phase, c.Status, c.RetryReason = RetryWait, RetryWait, "speculation_failed"
	}
}
func (w Worker) failGate(c *Candidate, err error) {
	c.WorkerID, c.LeaseExpiresAt, c.Phase, c.Status, c.RetryReason, c.Failure = "", time.Time{}, RetryWait, RetryWait, "gate_failed", err.Error()
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
