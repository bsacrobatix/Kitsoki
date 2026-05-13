package metamode

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sort"
	"sync"
	"time"

	"kitsoki/internal/authoring"
)

// ProposalState tracks the lifecycle of a pending authoring proposal
// inside a meta-mode session. The shape mirrors the §3.2 status
// vocabulary in the meta-mode proposal ("draft / endorsed / applied /
// discarded").
type ProposalState string

const (
	ProposalDraft     ProposalState = "draft"
	ProposalEndorsed  ProposalState = "endorsed"
	ProposalApplied   ProposalState = "applied"
	ProposalDiscarded ProposalState = "discarded"
)

// PendingProposal is one entry in a session's ProposalLedger. The
// owning *authoring.Proposal carries the shadow-dir path Discard needs
// to clean up.
type PendingProposal struct {
	ID        string
	State     ProposalState
	Proposal  *authoring.Proposal
	CreatedAt time.Time
}

// ProposalLedger is the mutate-under-lock collection of pending
// authoring proposals raised during a meta-mode session.
//
// WS-A4 wires the authoring.{propose,apply,discard} tool handlers
// into this ledger; the lifecycle primitives live here and are
// thread-safe so handlers only reason about the per-proposal
// lifecycle.
//
// The reload flag (set by RecordApplied / inspected by ReloadPending /
// cleared by ConsumeReload) is the WS-A4 ↔ WS-A5 handshake: when
// an authoring.apply tool call lands during a turn, RecordApplied
// flips reloadPending. Controller.Send drains it via ConsumeReload
// after Oracle.Ask returns and threads the result into
// SendResult.ReloadRequested so the TUI knows to reload the
// orchestrator before the next turn.
type ProposalLedger struct {
	mu            sync.Mutex
	items         map[string]*PendingProposal
	reloadPending bool
	// now is injected for deterministic CreatedAt in tests.
	now func() time.Time
}

// NewProposalLedger returns an empty ledger using time.Now for
// CreatedAt timestamps.
func NewProposalLedger() *ProposalLedger {
	return &ProposalLedger{
		items: make(map[string]*PendingProposal),
		now:   time.Now,
	}
}

// newProposalLedgerWithClock is the testing constructor.
func newProposalLedgerWithClock(now func() time.Time) *ProposalLedger {
	return &ProposalLedger{
		items: make(map[string]*PendingProposal),
		now:   now,
	}
}

// Add registers p as a new draft entry and returns its short ID. p may
// be nil if the caller wants a placeholder slot (rare; WS-A4 will
// always supply a real proposal). The ID is short random hex, generated
// inline to avoid pulling in a ULID dep.
func (l *ProposalLedger) Add(p *authoring.Proposal) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	id := newProposalID()
	// Generated IDs are random hex; guard against the negligible
	// collision case by retrying.
	for _, exists := l.items[id]; exists; _, exists = l.items[id] {
		id = newProposalID()
	}
	l.items[id] = &PendingProposal{
		ID:        id,
		State:     ProposalDraft,
		Proposal:  p,
		CreatedAt: l.now(),
	}
	return id
}

// Get returns the entry with the given ID and whether it was found.
func (l *ProposalLedger) Get(id string) (*PendingProposal, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	pp, ok := l.items[id]
	return pp, ok
}

// Update calls fn with a pointer to the entry under the ledger lock.
// fn must not block on anything that re-enters the ledger. If id is
// unknown, fn is not called.
func (l *ProposalLedger) Update(id string, fn func(*PendingProposal)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if pp, ok := l.items[id]; ok && fn != nil {
		fn(pp)
	}
}

// Discard cleans up the shadow directory of the proposal with the
// given ID (via authoring.Discard) and marks its state as
// ProposalDiscarded. The entry remains in the ledger so callers can
// see it in List() until the session ends. Returns an error if the ID
// is unknown or authoring.Discard fails.
func (l *ProposalLedger) Discard(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	pp, ok := l.items[id]
	if !ok {
		return fmt.Errorf("metamode: unknown proposal id %q", id)
	}
	if pp.State == ProposalDiscarded || pp.State == ProposalApplied {
		// Already terminal — no-op (idempotent).
		return nil
	}
	if err := authoring.Discard(pp.Proposal); err != nil {
		return fmt.Errorf("metamode: discard proposal %q: %w", id, err)
	}
	pp.State = ProposalDiscarded
	return nil
}

// RecordApplied marks the proposal with the given ID as applied and
// flips the reload-pending flag. Idempotent — recording the same ID
// twice does not double-flip the flag (the flag is a sticky bool,
// not a counter). Unknown IDs still flip the flag because the
// host-side handler may have applied the proposal even if the ledger
// entry has been GC'd or never existed; the reload-pending semantics
// are "did an apply happen this turn?", not "did a tracked apply
// happen this turn?".
func (l *ProposalLedger) RecordApplied(proposalID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if pp, ok := l.items[proposalID]; ok {
		pp.State = ProposalApplied
	}
	l.reloadPending = true
}

// ReloadPending reports whether an apply has been recorded since the
// last ConsumeReload call. Pure read — does NOT clear the flag.
func (l *ProposalLedger) ReloadPending() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reloadPending
}

// ConsumeReload reports whether reload is pending and clears the flag
// atomically. Returns the value the flag had at entry.
func (l *ProposalLedger) ConsumeReload() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	v := l.reloadPending
	l.reloadPending = false
	return v
}

// List returns all entries ordered by CreatedAt ascending. Returned
// slice is a copy; mutating it does not affect the ledger.
func (l *ProposalLedger) List() []*PendingProposal {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]*PendingProposal, 0, len(l.items))
	for _, pp := range l.items {
		out = append(out, pp)
	}
	sort.Slice(out, func(i, j int) bool {
		ti, tj := out[i].CreatedAt, out[j].CreatedAt
		if ti.Equal(tj) {
			return out[i].ID < out[j].ID
		}
		return ti.Before(tj)
	})
	return out
}

// newProposalID returns a short random hex identifier (8 bytes → 16
// hex chars). Sufficient entropy for per-session uniqueness; the Add
// loop retries on collisions to be doubly safe.
func newProposalID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read failing on a modern OS is essentially impossible
		// (it'd mean /dev/urandom is gone). Fall back to a time-based
		// id so the package doesn't hard-fail in pathological envs.
		return fmt.Sprintf("ts%016x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
