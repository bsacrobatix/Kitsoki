package orchestrator

// appdef_binding.go adapts a live session to the appdef.SessionBinder
// interface (internal/appdef/binding.go) WITHOUT this package importing
// internal/appdef. appdef declares the four-method interface it needs from a
// session; SessionBinding satisfies it structurally. Neither package imports
// the other — appdef stays a pure library (compile/store/patch, no
// orchestrator dependency) and orchestrator stays free of the revision/patch
// vocabulary. The wiring that hands a *appdef.Binding a SessionBinding lives
// one layer up, in the RPC server.
//
// SessionBinding is deliberately a thin adapter: every step it performs is a
// method the TUI's /reload path (cmd/kitsoki/registry.go's SessionRegistry.Reload)
// already calls — Reload, RecordEffectiveStory, RerunOnEnter. A revision
// reload and a disk reload travel the identical underlying code path; the
// only thing SessionBinding adds is arming the pending-def cell (SetPendingDef
// / takePendingDef, below, consulted by Reload in reload.go) so Reload
// installs the revision's own compiled def instead of falling through to a
// WithReloader closure or a disk read.

import (
	"context"
	"fmt"

	"kitsoki/internal/app"
	"kitsoki/internal/store"
)

// SessionBinding adapts one (Orchestrator, SessionID) pair to
// appdef.SessionBinder. It carries no state of its own beyond the pointer and
// ID — everything it reads or mutates lives on the Orchestrator and in the
// session's store-backed journey, so a SessionBinding is cheap to construct
// per-call and safe to discard.
type SessionBinding struct {
	Orch *Orchestrator
	SID  app.SessionID
}

// Closure captures the session's current effective story: every file the
// loader touched, keyed by capture-root-relative path, plus the root
// manifest's relative path as Entry. This is the SAME capture
// RecordEffectiveStory's trace snapshot uses (store.CollectEffectiveStory), so
// a revision built from this closure is byte-identical to what a trace replay
// would reconstruct from the session's own history — a captured revision and
// a replayed story are the same bytes by construction, not by convention.
func (b SessionBinding) Closure() (entry string, files map[string][]byte, err error) {
	def := b.Orch.currentDef()
	es, err := store.CollectEffectiveStory(def)
	if err != nil {
		return "", nil, fmt.Errorf("orchestrator.SessionBinding.Closure: %w", err)
	}
	return es.Entry, es.Files, nil
}

// SwapDef installs def as the session's served definition and reports
// whether the session's current state still exists in it. It mirrors steps
// (1)-(3) of SessionRegistry.Reload (cmd/kitsoki/registry.go) exactly:
//
//  1. read the current state from the session's live snapshot, so there is no
//     window in which the caller's idea of "where we are" is stale relative
//     to what actually gets reloaded;
//  2. arm the pending-def cell with def and call Reload — the cell (see
//     reload.go's consultation of takePendingDef) is what makes Reload
//     install THIS exact compiled definition instead of falling through to
//     an injected reloader closure or a disk read;
//  3. RecordEffectiveStory so the trace stays self-contained across the swap.
//
// It deliberately does NOT re-fire on_enter — that is Reenter's job, called
// separately once the caller has decided the swap itself succeeded. This
// mirrors appdef.Binding.ReloadTo's own two-step shape (swap, then reenter)
// and keeps a caller free to inspect prevStateExists before deciding whether
// re-entering makes sense.
//
// A RecordEffectiveStory failure is reported but does not roll the swap back
// — the def is already live at that point, matching
// SessionRegistry.Reload's existing behaviour (registry.go:2355) of returning
// (prevStateExists, err) rather than trying to undo an already-atomic swap.
func (b SessionBinding) SwapDef(ctx context.Context, def *app.AppDef) (bool, error) {
	journey, err := b.Orch.loadJourney(b.SID)
	if err != nil {
		return false, fmt.Errorf("orchestrator.SessionBinding.SwapDef: load journey: %w", err)
	}
	currentState := journey.State

	// ReloadWithDef arms the pending-def cell AND calls Reload under the
	// SAME per-session lock, so no concurrent reload for this session
	// (background job listener, another ReloadForSession/ReloadWithDef
	// call) can observe torn state or steal the armed def between the two
	// steps. See ReloadWithDef's doc comment in reload.go for the failure
	// mode this closes.
	res, err := b.Orch.ReloadWithDef(b.SID, def, currentState)
	if err != nil {
		return false, fmt.Errorf("orchestrator.SessionBinding.SwapDef: reload: %w", err)
	}

	if err := b.Orch.RecordEffectiveStory(ctx, b.SID); err != nil {
		return res.PrevStateExists, fmt.Errorf("orchestrator.SessionBinding.SwapDef: record effective story: %w", err)
	}
	return res.PrevStateExists, nil
}

// Reenter re-fires the current state's on_enter chain and re-renders,
// exactly as the TUI's /reload does when the prior state survived the edit
// (SessionRegistry.Reload step 4). A plain RerunOnEnter — no
// RerunOnEnterOptions{ForceOnce: true} — is sufficient: Slice 0's
// TestAppdefRetryProbe (appdef_retry_probe_test.go) measured this directly
// against the exact error-room-retry scenario this whole design serves and
// found ForceOnce unnecessary. See that test's doc comment and the "ForceOnce:
// SETTLED" section appended to
// .context/appdef-live-edit-implementation-spec.md.
func (b SessionBinding) Reenter(ctx context.Context) error {
	_, err := b.Orch.RerunOnEnter(ctx, b.SID)
	return err
}

// AppID returns the application ID of the definition this session is
// CURRENTLY serving — read straight off the already-compiled live def
// (o.currentDef().App.ID), never a compile of its own. This is what
// appdef.Binding.ReloadTo consults to refuse a cross-application revision
// swap: unlike a cached, lazily-populated field that starts empty until
// some prior Current()/ReloadTo() call happens to populate it, AppID is
// always answerable for free because a session always has SOME live def
// the moment it exists — so the guard applies on a session's very first
// ReloadTo call too, not only its second.
func (b SessionBinding) AppID() string {
	def := b.Orch.currentDef()
	if def == nil {
		return ""
	}
	return def.App.ID
}

// currentDef reads the orchestrator's live def under its mutex. It is the
// same read-then-unlock idiom RecordEffectiveStory already uses (see
// story_record.go) — kept as its own tiny method here because
// SessionBinding.Closure needs it and there was previously no exported (or
// even unexported, standalone) accessor for it.
func (o *Orchestrator) currentDef() *app.AppDef {
	o.mu.Lock()
	def := o.def
	o.mu.Unlock()
	return def
}

// SetPendingDef arms the one-shot pending-def cell: the NEXT call to Reload
// (from any caller, not just SessionBinding) installs def instead of
// consulting the injected reloader closure or reading appPath from disk. The
// cell is consumed — cleared — by that Reload regardless of whether it
// succeeds, via takePendingDef, so a failed revision swap can never leak into
// a later, unrelated reload. See reload.go's Reload for the consultation
// point and its doc comment for why this exists at all.
func (o *Orchestrator) SetPendingDef(def *app.AppDef) {
	o.mu.Lock()
	o.pendingDef = def
	o.mu.Unlock()
}

// takePendingDef reads and clears the pending-def cell in one locked step, so
// two overlapping Reload calls can never both observe (and both consume) the
// same armed def. Returns nil when nothing is armed, in which case Reload
// falls back to its historical reloader-closure/disk-read behaviour.
func (o *Orchestrator) takePendingDef() *app.AppDef {
	o.mu.Lock()
	def := o.pendingDef
	o.pendingDef = nil
	o.mu.Unlock()
	return def
}
