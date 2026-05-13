package metamode

import (
	"testing"
	"time"
)

func TestProposalLedger_AddGetUpdateDiscard(t *testing.T) {
	cases := []struct {
		name string
		do   func(t *testing.T, l *ProposalLedger)
	}{
		{
			name: "Add returns non-empty unique id and a draft entry",
			do: func(t *testing.T, l *ProposalLedger) {
				id := l.Add(nil)
				if id == "" {
					t.Fatal("Add returned empty id")
				}
				pp, ok := l.Get(id)
				if !ok {
					t.Fatal("Get: not found")
				}
				if pp.State != ProposalDraft {
					t.Errorf("State = %q, want draft", pp.State)
				}
				if pp.CreatedAt.IsZero() {
					t.Error("CreatedAt not stamped")
				}
			},
		},
		{
			name: "Update mutates state under the lock",
			do: func(t *testing.T, l *ProposalLedger) {
				id := l.Add(nil)
				l.Update(id, func(pp *PendingProposal) {
					pp.State = ProposalEndorsed
				})
				pp, _ := l.Get(id)
				if pp.State != ProposalEndorsed {
					t.Errorf("State after Update = %q, want endorsed", pp.State)
				}
			},
		},
		{
			name: "Update on unknown id is a no-op",
			do: func(t *testing.T, l *ProposalLedger) {
				called := false
				l.Update("no-such-id", func(_ *PendingProposal) { called = true })
				if called {
					t.Error("Update fn was called for unknown id")
				}
			},
		},
		{
			name: "Discard on unknown id is an error",
			do: func(t *testing.T, l *ProposalLedger) {
				if err := l.Discard("no-such-id"); err == nil {
					t.Error("Discard: want error for unknown id")
				}
			},
		},
		{
			name: "Discard marks an entry without a proposal (nil) discarded",
			do: func(t *testing.T, l *ProposalLedger) {
				id := l.Add(nil)
				if err := l.Discard(id); err != nil {
					t.Fatalf("Discard: %v", err)
				}
				pp, _ := l.Get(id)
				if pp.State != ProposalDiscarded {
					t.Errorf("State after Discard = %q, want discarded", pp.State)
				}
			},
		},
		{
			name: "Discard is idempotent on already-discarded entries",
			do: func(t *testing.T, l *ProposalLedger) {
				id := l.Add(nil)
				if err := l.Discard(id); err != nil {
					t.Fatalf("first Discard: %v", err)
				}
				if err := l.Discard(id); err != nil {
					t.Errorf("second Discard: want nil (idempotent), got %v", err)
				}
			},
		},
		{
			name: "Discard refuses to undo an applied entry",
			do: func(t *testing.T, l *ProposalLedger) {
				id := l.Add(nil)
				l.Update(id, func(pp *PendingProposal) { pp.State = ProposalApplied })
				if err := l.Discard(id); err != nil {
					t.Errorf("Discard on applied: want nil, got %v", err)
				}
				pp, _ := l.Get(id)
				if pp.State != ProposalApplied {
					t.Errorf("State = %q, want applied (unchanged)", pp.State)
				}
			},
		},
		{
			name: "List orders by CreatedAt then ID",
			do: func(t *testing.T, l *ProposalLedger) {
				// inject a deterministic clock so we can pin order.
				ts := time.Unix(0, 0)
				next := time.Unix(0, 1000)
				clk := newSequencedClock(ts, next)
				lg := newProposalLedgerWithClock(clk)
				idA := lg.Add(nil)
				idB := lg.Add(nil)
				items := lg.List()
				if len(items) != 2 {
					t.Fatalf("List len = %d", len(items))
				}
				if items[0].ID != idA || items[1].ID != idB {
					t.Errorf("List order = [%s,%s], want [%s,%s]",
						items[0].ID, items[1].ID, idA, idB)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := NewProposalLedger()
			tc.do(t, l)
		})
	}
}

func TestProposalLedger_IDUniqueness(t *testing.T) {
	l := NewProposalLedger()
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id := l.Add(nil)
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id at iteration %d: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}

// newSequencedClock returns a closure that yields the supplied times
// in order and then keeps returning the last one. Used by the
// List-order test to ensure CreatedAt strictly increases.
func newSequencedClock(times ...time.Time) func() time.Time {
	i := 0
	return func() time.Time {
		if i >= len(times) {
			return times[len(times)-1]
		}
		t := times[i]
		i++
		return t
	}
}

// TestProposalLedger_RecordAppliedFlipsReloadAndState verifies the
// WS-A4 reload-pending contract: RecordApplied flips the flag AND
// transitions the entry's state to ProposalApplied. The flag is then
// consumed by ConsumeReload.
func TestProposalLedger_RecordAppliedFlipsReloadAndState(t *testing.T) {
	l := NewProposalLedger()
	if l.ReloadPending() {
		t.Fatal("freshly-constructed ledger has ReloadPending=true; want false")
	}
	id := l.Add(nil)

	l.RecordApplied(id)
	if !l.ReloadPending() {
		t.Fatal("RecordApplied did not flip ReloadPending")
	}
	pp, ok := l.Get(id)
	if !ok || pp.State != ProposalApplied {
		t.Errorf("entry state after RecordApplied = %q, want %q", pp.State, ProposalApplied)
	}

	// ReloadPending is a pure read — should still be true.
	if !l.ReloadPending() {
		t.Error("ReloadPending() flipped on read; should only clear via ConsumeReload")
	}

	// Consume drains the flag and reports the prior value.
	if !l.ConsumeReload() {
		t.Error("ConsumeReload() = false; want true on first call after apply")
	}
	if l.ReloadPending() {
		t.Error("ConsumeReload did not clear the flag")
	}
	if l.ConsumeReload() {
		t.Error("ConsumeReload() = true on second call; want false after drain")
	}
}

// TestProposalLedger_RecordAppliedUnknownIDStillFlipsFlag covers the
// edge case the brief calls out: even if the host-side handler has
// no matching entry (GC'd, race), RecordApplied still flips the flag
// so the controller surfaces the reload. The state of the unknown
// entry obviously can't be mutated since it doesn't exist.
func TestProposalLedger_RecordAppliedUnknownIDStillFlipsFlag(t *testing.T) {
	l := NewProposalLedger()
	l.RecordApplied("does-not-exist")
	if !l.ReloadPending() {
		t.Error("RecordApplied on unknown id failed to flip the reload flag")
	}
}
