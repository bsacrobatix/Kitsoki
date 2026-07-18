package queue

import (
	"fmt"
	"strings"
	"time"
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
