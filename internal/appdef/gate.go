package appdef

import (
	"errors"
	"sync"
)

// Gate serializes definition swaps against turns for ONE session.
//
// Orchestrator.Reload is documented as unsafe concurrently with Turn /
// SubmitDirect / ContinueTurn, and the server path has no guard for it:
// Reload swaps o.def/o.machine under the orchestrator's global mutex while a
// turn reads them under a different, per-session mutex. Gate is that
// missing guard.
//
// The two directions are deliberately ASYMMETRIC:
//
//   - BeginTurn BLOCKS until any in-progress swap finishes. A swap is
//     bounded work — compilation already happened at patch time; the swap
//     itself is machine.New plus one re-render — so making a user's turn
//     wait a few milliseconds is strictly better than refusing their input.
//   - BeginReload REFUSES, immediately, with a NAMED error, when a turn is
//     in flight or another swap is already running. A turn can involve an
//     agent and run for minutes; blocking an RPC handler behind one — or
//     worse, swapping the definition out from under it mid-turn — are both
//     wrong. The client retries.
//
// A refusal the caller can name and act on beats a silent race. That is the
// whole design.
//
// A zero-value Gate is usable: the sync.Cond is lazily initialized under the
// mutex, so an embedder never has to remember NewGate.
type Gate struct {
	mu       sync.Mutex
	cond     *sync.Cond
	turns    int
	swapping bool
}

// NewGate returns a ready Gate. Equivalent to the zero value; provided for
// symmetry with the rest of this package's constructors.
func NewGate() *Gate {
	return &Gate{}
}

// ErrTurnInFlight is returned by BeginReload when a turn is currently
// executing for this session.
var ErrTurnInFlight = errors.New("appdef: a turn is in flight for this session; retry the reload after it completes")

// ErrReloadInProgress is returned by BeginReload when another definition
// swap is already running for this session.
var ErrReloadInProgress = errors.New("appdef: a definition reload is already in progress for this session")

// cond lazily initializes g.cond. Callers must hold g.mu.
func (g *Gate) initLocked() *sync.Cond {
	if g.cond == nil {
		g.cond = sync.NewCond(&g.mu)
	}
	return g.cond
}

// BeginTurn marks one turn as starting, blocking first if a swap is
// currently in progress. Pair with a deferred EndTurn.
func (g *Gate) BeginTurn() {
	g.mu.Lock()
	defer g.mu.Unlock()
	cond := g.initLocked()
	for g.swapping {
		cond.Wait()
	}
	g.turns++
}

// EndTurn marks one turn as finished. It broadcasts to any goroutine waiting
// in BeginReload once the turn count reaches zero.
func (g *Gate) EndTurn() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.turns > 0 {
		g.turns--
	}
	g.initLocked().Broadcast()
}

// BeginReload attempts to start a definition swap. It refuses immediately —
// never blocks — returning ErrTurnInFlight when a turn is executing, or
// ErrReloadInProgress when another swap already holds the gate. On success
// the caller must call EndReload exactly once, typically via defer.
func (g *Gate) BeginReload() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.initLocked()
	if g.swapping {
		return ErrReloadInProgress
	}
	if g.turns > 0 {
		return ErrTurnInFlight
	}
	g.swapping = true
	return nil
}

// EndReload finishes a swap started by a successful BeginReload. It
// broadcasts unconditionally so every goroutine blocked in BeginTurn
// re-checks.
func (g *Gate) EndReload() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.swapping = false
	g.initLocked().Broadcast()
}

// TurnsActive reports how many turns are currently executing. Callers use
// this as an eviction/idle hint only — never as a lock; the correctness
// guarantee lives entirely in BeginTurn/BeginReload.
func (g *Gate) TurnsActive() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.turns
}
