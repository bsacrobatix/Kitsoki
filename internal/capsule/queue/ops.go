package queue

import (
	"context"
	"fmt"
	"strings"
	"time"

	"kitsoki/internal/capsule/reconcile"
	"kitsoki/internal/capsule/record"
)

// Operator verbs. These are the durable human-override surface of the merge
// queue: every non-terminal state the worker can produce has at least one verb
// that moves it, so the queue can never reach a state a human cannot exit.
// Each verb runs under the state lock, validates the candidate's phase, and
// appends an audited evidence line (actor + timestamp). Worker results never
// clobber an operator decision (see Worker.operatorIntervened).

// Op identifies a candidate plus the human context of an operator action.
type Op struct {
	ID     string
	Actor  string
	Reason string
	Now    time.Time
}

// ApprovalOp carries the state identities a steward observed. Every non-empty
// value is compared with the durable candidate before approval is recorded.
type ApprovalOp struct {
	ID, Actor, Reason string
	ManifestDigest    string
	TreeSHA           string
	ReceiptDigest     string
	Now               time.Time
}

func (o ApprovalOp) at() time.Time {
	if o.Now.IsZero() {
		return time.Now().UTC()
	}
	return o.Now.UTC()
}

func (o ApprovalOp) actor() string {
	if strings.TrimSpace(o.Actor) == "" {
		return "operator"
	}
	return o.Actor
}

// Approve records a steward decision only after the prepared tuple, required
// receipts, and supplied review identities match current durable state.
func (s Store) Approve(op ApprovalOp) (Candidate, error) {
	if strings.TrimSpace(op.ID) == "" {
		return Candidate{}, fmt.Errorf("queue: approve requires a candidate id")
	}
	return s.mutate(func(state *State) (Candidate, error) {
		for i := range state.Candidates {
			c := &state.Candidates[i]
			if c.ID != op.ID {
				continue
			}
			if c.finalizationPolicy() != StewardReviewFinalization {
				return Candidate{}, fmt.Errorf("queue: candidate %s does not require steward approval", c.ID)
			}
			if c.phase() != AwaitingApproval {
				return Candidate{}, fmt.Errorf("queue: approve requires awaiting_approval; candidate %s is %s", c.ID, c.phase())
			}
			if err := validatePreparedTuple(*c); err != nil {
				return Candidate{}, fmt.Errorf("queue: approve requires green current gate: %w", err)
			}
			if strings.TrimSpace(op.ManifestDigest) == "" || op.ManifestDigest != c.ManifestDigest {
				return Candidate{}, fmt.Errorf("queue: approval manifest digest does not match current candidate")
			}
			if op.TreeSHA != "" && op.TreeSHA != c.TreeSHA {
				return Candidate{}, fmt.Errorf("queue: approval tree SHA does not match current candidate")
			}
			if op.ReceiptDigest != "" && op.ReceiptDigest != c.ReceiptDigest {
				return Candidate{}, fmt.Errorf("queue: approval receipt digest does not match current candidate")
			}
			if err := validateRequiredReceipts(*c); err != nil {
				return Candidate{}, err
			}
			c.Approval = &Approval{Actor: op.actor(), Reason: first(op.Reason, "steward approval"), At: op.at(), Fingerprint: approvalFingerprint(*c)}
			c.Phase, c.Status = ReadyToFinalize, ReadyToFinalize
			c.Evidence = append(c.Evidence, fmt.Sprintf("queue:approve by %s at %s reason=%s", c.Approval.Actor, c.Approval.At.Format(time.RFC3339), c.Approval.Reason))
			return *c, nil
		}
		return Candidate{}, fmt.Errorf("queue: unknown candidate %q", op.ID)
	})
}

// Unapprove withdraws a decision. A finalizing worker cannot overwrite the
// returned awaiting_approval state when it later records its result.
func (s Store) Unapprove(op Op) (Candidate, error) {
	return s.operate(op, "unapprove", func(_ *State, c *Candidate) error {
		if c.finalizationPolicy() != StewardReviewFinalization {
			return fmt.Errorf("queue: candidate %s does not require steward approval", c.ID)
		}
		if c.phase() != ReadyToFinalize && c.phase() != Finalizing {
			return fmt.Errorf("queue: unapprove requires ready_to_finalize or finalizing; candidate %s is %s", c.ID, c.phase())
		}
		c.Approval = nil
		c.Phase, c.Status, c.WorkerID, c.LeaseExpiresAt = AwaitingApproval, AwaitingApproval, "", time.Time{}
		return nil
	})
}

func (o Op) at() time.Time {
	if o.Now.IsZero() {
		return time.Now().UTC()
	}
	return o.Now.UTC()
}

func (o Op) actor() string {
	if strings.TrimSpace(o.Actor) == "" {
		return "operator"
	}
	return o.Actor
}

// Kick clears a retry_wait candidate's backoff timer so the next worker pass
// picks it up immediately. It never changes the attempt count.
func (s Store) Kick(op Op) (Candidate, error) {
	return s.operate(op, "kick", func(_ *State, c *Candidate) error {
		if c.phase() != RetryWait {
			return fmt.Errorf("queue: kick requires retry_wait; candidate %s is %s", c.ID, c.phase())
		}
		c.RetryAt = time.Time{}
		return nil
	})
}

// Park moves any non-terminal candidate to needs_input so it stops consuming
// worker passes and stops delaying the train. A live worker lease is respected
// via the operator-intervention guard: the in-flight result is discarded when
// it lands.
func (s Store) Park(op Op) (Candidate, error) {
	return s.operate(op, "park", func(_ *State, c *Candidate) error {
		if terminal(c.phase()) {
			return fmt.Errorf("queue: cannot park %s candidate %s", c.phase(), c.ID)
		}
		c.Phase, c.Status = NeedsInput, NeedsInput
		c.RetryReason = first(op.Reason, "parked_by_operator")
		c.WorkerID, c.LeaseExpiresAt, c.RetryAt = "", time.Time{}, time.Time{}
		c.ParkedAt, c.ParkedBy = op.at(), op.actor()
		return nil
	})
}

// Resume returns a parked candidate to the queue. The attempt budget is reset:
// a human resuming a candidate is asserting the underlying cause was repaired,
// and the prior count stays in evidence.
func (s Store) Resume(op Op) (Candidate, error) {
	return s.operate(op, "resume", func(_ *State, c *Candidate) error {
		if !parked(c.phase()) && c.phase() != RetryWait {
			return fmt.Errorf("queue: resume requires needs_input, needs_conflict_input, or retry_wait; candidate %s is %s", c.ID, c.phase())
		}
		if c.Attempt > 0 {
			c.Evidence = append(c.Evidence, fmt.Sprintf("queue:attempt budget reset from %d by %s", c.Attempt, op.actor()))
		}
		c.Phase, c.Status = Queued, Queued
		c.Attempt = 0
		c.RetryAt, c.ParkedAt = time.Time{}, time.Time{}
		c.ParkedBy, c.RetryReason, c.Failure, c.ConflictContinuation = "", "", "", ""
		c.EnvRetries, c.FirstEnvFailureAt = 0, time.Time{}
		return nil
	})
}

// MarkEmergency moves a candidate into the emergency lane, which is claimed
// and finalized ahead of the normal FIFO but is FIFO within itself. It never
// interrupts an in-flight preparation.
func (s Store) MarkEmergency(op Op) (Candidate, error) {
	return s.operate(op, "emergency", func(state *State, c *Candidate) error {
		if terminal(c.phase()) {
			return fmt.Errorf("queue: cannot mark %s candidate %s emergency", c.phase(), c.ID)
		}
		if c.EmergencySequence == 0 {
			c.EmergencySequence = nextEmergencySequence(state.Candidates)
		}
		return nil
	})
}

// Override is the human immediate-merge path: emergency-lane priority plus a
// durable waiver of the deterministic gate. The integration tree is still
// built and the protected compare-and-swap still applies, so git-level safety
// is preserved; only validation is waived, attributably.
func (s Store) Override(op Op) (Candidate, error) {
	return s.operate(op, "override", func(state *State, c *Candidate) error {
		if terminal(c.phase()) {
			return fmt.Errorf("queue: cannot override %s candidate %s", c.phase(), c.ID)
		}
		c.OverrideGate = true
		c.OverrideBy, c.OverrideReason = op.actor(), first(op.Reason, "operator_override")
		if c.EmergencySequence == 0 {
			c.EmergencySequence = nextEmergencySequence(state.Candidates)
		}
		if parked(c.phase()) || c.phase() == RetryWait {
			if c.Attempt > 0 {
				c.Evidence = append(c.Evidence, fmt.Sprintf("queue:attempt budget reset from %d by %s", c.Attempt, op.actor()))
			}
			c.Phase, c.Status = Queued, Queued
			c.Attempt = 0
			c.RetryAt, c.ParkedAt = time.Time{}, time.Time{}
			c.ParkedBy, c.RetryReason, c.Failure, c.ConflictContinuation = "", "", "", ""
		}
		// Override remains the explicit human emergency/waiver path. It is
		// distinct from a normal steward approval and can release its hold.
		if c.phase() == AwaitingApproval {
			c.Phase, c.Status = ReadyToFinalize, ReadyToFinalize
		}
		return nil
	})
}

// Reject removes a candidate from the queue permanently. Nothing else is
// deleted: branches, integration workspaces, and evidence all remain.
func (s Store) Reject(op Op) (Candidate, error) {
	return s.operate(op, "reject", func(_ *State, c *Candidate) error {
		if terminal(c.phase()) {
			return fmt.Errorf("queue: cannot reject %s candidate %s", c.phase(), c.ID)
		}
		c.Phase, c.Status = Rejected, Rejected
		c.WorkerID, c.LeaseExpiresAt, c.RetryAt = "", time.Time{}, time.Time{}
		c.EjectionReason = first(op.Reason, "rejected_by_operator")
		c.Completed = op.at()
		return nil
	})
}

// Get returns the durable record of one candidate.
func (s Store) Get(id string) (Candidate, error) {
	state, err := s.readState()
	if err != nil {
		return Candidate{}, err
	}
	for _, c := range state.Candidates {
		if c.ID == id {
			return c, nil
		}
	}
	return Candidate{}, fmt.Errorf("queue: unknown candidate %q", id)
}

// ReconcileLanding repairs the recorded result of a historical staging
// finalization that used the managed workspace helper before the helper's
// actual protected commit was returned to Worker. It is deliberately narrower
// than a generic state-edit command: the current target must be the newest
// reflog transition, that transition must name this exact queue candidate,
// its prior value must be the candidate's prepared base, and the protected,
// speculative, validated, and previously recorded results must all resolve to
// one identical tree.
func (s Store) ReconcileLanding(op Op) (Candidate, error) {
	if strings.TrimSpace(op.ID) == "" {
		return Candidate{}, fmt.Errorf("queue: reconcile-landing requires a candidate id")
	}
	ctx := context.Background()
	return s.mutate(func(state *State) (Candidate, error) {
		for i := range state.Candidates {
			c := &state.Candidates[i]
			if c.ID != op.ID {
				continue
			}
			if c.phase() != Landed {
				return Candidate{}, fmt.Errorf("queue: reconcile-landing requires landed; candidate %s is %s", c.ID, c.phase())
			}
			if c.TargetRef != "staging/local" {
				return Candidate{}, fmt.Errorf("queue: reconcile-landing only supports managed staging helper results, not %q", c.TargetRef)
			}
			if c.ReceiptID == "" || c.BaseSHA == "" || c.TreeSHA == "" || c.ValidatedSHA != c.TreeSHA || c.ResultMainSHA == "" {
				return Candidate{}, fmt.Errorf("queue: reconcile-landing requires a complete receipt-bearing landed tuple")
			}
			root := s.ProjectRoot
			if err := (record.PromotionGate{ProjectRoot: root}).Verify(ctx, c.ReceiptID, reconcile.Plan{Candidate: c.SHA}); err != nil {
				return Candidate{}, fmt.Errorf("queue: reconcile-landing verify source receipt %s: %w", c.ReceiptID, err)
			}
			current, err := gitOutput(ctx, root, "rev-parse", "--verify", "refs/heads/"+c.TargetRef)
			if err != nil {
				return Candidate{}, fmt.Errorf("queue: reconcile-landing read target: %w", err)
			}
			if current == c.ResultMainSHA {
				return *c, nil
			}
			reflog, err := gitOutput(ctx, root, "reflog", "show", "--max-count=2", "--format=%H%x1f%gs", "refs/heads/"+c.TargetRef)
			if err != nil {
				return Candidate{}, fmt.Errorf("queue: reconcile-landing read target reflog: %w", err)
			}
			lines := strings.Split(reflog, "\n")
			if len(lines) != 2 {
				return Candidate{}, fmt.Errorf("queue: reconcile-landing requires exactly one unambiguous latest target transition")
			}
			top := strings.SplitN(lines[0], "\x1f", 2)
			previous := strings.SplitN(lines[1], "\x1f", 2)
			if len(top) != 2 || len(previous) != 2 || top[0] != current {
				return Candidate{}, fmt.Errorf("queue: reconcile-landing target reflog does not identify the current protected head")
			}
			expectedMessage := "dev-workspace merge queue/speculative/" + c.ID + " into " + c.TargetRef
			if top[1] != expectedMessage {
				return Candidate{}, fmt.Errorf("queue: reconcile-landing latest target transition %q does not name candidate %s", top[1], c.ID)
			}
			if previous[0] != c.BaseSHA {
				return Candidate{}, fmt.Errorf("queue: reconcile-landing prior target %s does not match prepared base %s", previous[0], c.BaseSHA)
			}
			var commonTree string
			for label, sha := range map[string]string{
				"protected": current, "speculative": c.TreeSHA,
				"validated": c.ValidatedSHA, "recorded": c.ResultMainSHA,
			} {
				tree, err := gitOutput(ctx, root, "rev-parse", sha+"^{tree}")
				if err != nil {
					return Candidate{}, fmt.Errorf("queue: reconcile-landing read %s tree: %w", label, err)
				}
				if commonTree == "" {
					commonTree = tree
				} else if tree != commonTree {
					return Candidate{}, fmt.Errorf("queue: reconcile-landing %s tree %s does not match expected tree %s", label, tree, commonTree)
				}
			}
			oldResult := c.ResultMainSHA
			c.ResultMainSHA = current
			line := fmt.Sprintf(
				"queue:reconcile-landing by %s at %s reason=%s old_result=%s actual_target=%s base=%s tree=%s reflog=%q",
				op.actor(), op.at().Format(time.RFC3339), first(op.Reason, "repair staging helper result identity"),
				oldResult, current, c.BaseSHA, commonTree, top[1],
			)
			c.Evidence = append(c.Evidence, line)
			return *c, nil
		}
		return Candidate{}, fmt.Errorf("queue: unknown candidate %q", op.ID)
	})
}

func (s Store) operate(op Op, verb string, fn func(*State, *Candidate) error) (Candidate, error) {
	if strings.TrimSpace(op.ID) == "" {
		return Candidate{}, fmt.Errorf("queue: %s requires a candidate id", verb)
	}
	return s.mutate(func(state *State) (Candidate, error) {
		for i := range state.Candidates {
			c := &state.Candidates[i]
			if c.ID != op.ID {
				continue
			}
			if err := fn(state, c); err != nil {
				return Candidate{}, err
			}
			line := fmt.Sprintf("queue:%s by %s at %s", verb, op.actor(), op.at().Format(time.RFC3339))
			if strings.TrimSpace(op.Reason) != "" {
				line += " reason=" + op.Reason
			}
			c.Evidence = append(c.Evidence, line)
			return *c, nil
		}
		return Candidate{}, fmt.Errorf("queue: unknown candidate %q", op.ID)
	})
}
