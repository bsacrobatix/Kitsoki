package appdef

import (
	"context"
	"fmt"
	"sync"

	"kitsoki/internal/app"
)

// SessionBinder is the ONE thing appdef needs from a live session. It is
// satisfied structurally by orchestrator.SessionBinding, so neither package
// imports the other.
type SessionBinder interface {
	// Closure returns the entry path and file set of the definition the
	// session is serving right now. It returns the two primitives rather
	// than an appdef.Closure deliberately: that keeps internal/orchestrator
	// (whose SessionBinding satisfies this interface) free of any import of
	// this package. Binding assembles the Closure value itself.
	Closure() (entry string, files map[string][]byte, err error)
	// SwapDef installs def as the session's served definition, records the
	// story change into the session's trace, and reports whether the
	// session's current state still resolves in def. It must not be called
	// concurrently with a turn — Binding holds the Gate across it.
	SwapDef(ctx context.Context, def *app.AppDef) (prevStateExists bool, err error)
	// Reenter re-fires the current state's on_enter chain and re-renders,
	// so a view or on_enter change takes effect without the operator
	// re-typing.
	Reenter(ctx context.Context) error
	// AppID returns the application ID of the definition the session is
	// CURRENTLY serving, read off the already-live def at zero cost (no
	// compile). ReloadTo uses this — not a lazily-populated cache — to
	// refuse a cross-application revision swap, so the guard applies from
	// a session's very first ReloadTo call onward, not only once some
	// prior Current()/ReloadTo() call happens to have populated a cache.
	AppID() string
}

// Binding couples ONE live session to a revision pin. It owns:
//   - which revision the session is logically on (the pin),
//   - the lifetime of the handle currently backing the served definition,
//   - the Gate that keeps a swap from racing a turn.
//
// A session starts UNPINNED: it serves whatever its ordinary loader
// produced (a story on disk, a synthesized agent root, a config-synthesized
// root) and costs nothing extra. The first appdef.* call for that session
// captures its current closure as a root revision — lazily, so creating a
// session is unchanged for every surface that never edits a definition.
type Binding struct {
	svc    *Service
	binder SessionBinder
	gate   *Gate

	mu     sync.Mutex
	pinned string  // digest this session is logically on; "" until first Current/ReloadTo
	served *Handle // handle backing a revision this session was moved onto via ReloadTo; nil until the first ReloadTo
}

// NewBinding returns a Binding for one session, backed by the shared
// Service svc, using binder to read/install definitions, serialized by
// gate.
func NewBinding(svc *Service, binder SessionBinder, gate *Gate) *Binding {
	return &Binding{svc: svc, binder: binder, gate: gate}
}

// Gate returns the binding's gate so the session's turn driver can bracket
// turns with it (BeginTurn/EndTurn around every SubmitDirect/Turn/
// ContinueTurn).
func (b *Binding) Gate() *Gate {
	return b.gate
}

// Current returns the revision the session is logically serving, capturing
// and storing it on first call. Every later call reports the same pinned
// digest — either the captured root revision, or whatever ReloadTo most
// recently moved the session onto — without repeating the compile.
func (b *Binding) Current(ctx context.Context) (Revision, error) {
	b.mu.Lock()
	pinned := b.pinned
	b.mu.Unlock()

	if pinned != "" {
		rev, ok, err := b.svc.store.Get(ctx, pinned)
		if err != nil {
			return Revision{}, fmt.Errorf("appdef: current: get %s: %w", pinned, err)
		}
		if ok {
			return rev, nil
		}
		// The pinned digest vanished from storage (e.g. process restart
		// with MemStorage — see the implementation spec's Open Risks item
		// 9). Fall through and re-capture rather than erroring, since the
		// session itself is still alive and serving something.
	}

	entry, files, err := b.binder.Closure()
	if err != nil {
		return Revision{}, fmt.Errorf("appdef: current: closure: %w", err)
	}
	rev, err := b.svc.Capture(ctx, Closure{Entry: entry, Files: files})
	if err != nil {
		return Revision{}, err
	}

	b.mu.Lock()
	if b.pinned == "" {
		b.pinned = rev.Digest
	}
	b.mu.Unlock()

	return rev, nil
}

// Patch applies ops on top of the session's current revision. baseDigest is
// optional optimistic concurrency: "" means "whatever is current"; a
// mismatch is rejected with RejectStaleBase rather than silently rebasing —
// a client that read a digest, showed it to an operator, and patches
// against it must know if that digest is no longer current.
func (b *Binding) Patch(ctx context.Context, baseDigest string, ops []Op) (PatchResult, error) {
	current, err := b.Current(ctx)
	if err != nil {
		return PatchResult{}, err
	}
	if baseDigest == "" {
		baseDigest = current.Digest
	} else if baseDigest != current.Digest {
		return rejected(Reject{
			Code:    RejectStaleBase,
			Message: fmt.Sprintf("base_digest %q is not the session's current revision %q", baseDigest, current.Digest),
		}), nil
	}
	return b.svc.Patch(ctx, baseDigest, ops)
}

// Revisions lists stored revisions for this session's application, newest
// first.
func (b *Binding) Revisions(ctx context.Context) ([]Revision, error) {
	current, err := b.Current(ctx)
	if err != nil {
		return nil, err
	}
	return b.svc.Revisions(ctx, current.AppID)
}

// ReloadTo moves the session onto the revision named by digest: it checks
// the revision out, takes the Gate (refusing with ErrTurnInFlight or
// ErrReloadInProgress when a turn or another swap is running), swaps the
// def in, releases the PREVIOUSLY served handle, then re-enters the
// current state. World state is untouched — the journey is replayed
// against the new definition by the session's own machinery.
//
// On any failure the previously served handle stays installed and the pin
// does not move: a failed reload leaves the session exactly where it was.
//
// A cross-application guard: the target revision's AppID (known for free
// right after Checkout compiles it) is compared against the session's OWN
// current AppID, read live via binder.AppID() — not a Binding-local cache
// that starts empty. The implementation spec's Open Risks item 10 names why
// this matters: the registry holds one Service, so a digest produced
// against an unrelated session's application is otherwise addressable here
// too. Reading the session's AppID via the binder rather than caching it
// costs nothing (the binder's own live def is already compiled and
// resident), so the guard applies on a session's very first ReloadTo call,
// not only once some prior Current()/ReloadTo() call happens to have
// populated a cache.
func (b *Binding) ReloadTo(ctx context.Context, digest string) (prevStateExists bool, err error) {
	h, err := b.svc.Checkout(ctx, digest)
	if err != nil {
		return false, err
	}

	if knownAppID := b.binder.AppID(); knownAppID != "" && h.Def.App.ID != "" && knownAppID != h.Def.App.ID {
		h.Release()
		return false, fmt.Errorf("appdef: reload: revision %s is for application %q, not %q", digest, h.Def.App.ID, knownAppID)
	}

	if err := b.gate.BeginReload(); err != nil {
		h.Release()
		return false, err
	}
	defer b.gate.EndReload()

	prevExists, err := b.binder.SwapDef(ctx, h.Def)
	if err != nil {
		h.Release()
		return false, err
	}

	b.mu.Lock()
	old := b.served
	b.served = h
	b.pinned = digest
	b.mu.Unlock()

	if old != nil {
		old.Release()
	}

	if prevExists {
		// A Reenter failure is reported but does NOT roll the swap back:
		// the definition is already live, and rolling back would need a
		// second swap (with its own gate/race considerations) rather than
		// undoing this one.
		if err := b.binder.Reenter(ctx); err != nil {
			return prevExists, err
		}
	}
	return prevExists, nil
}

// CaptureAndReloadTo stores c as a new revision (via Capture) and
// immediately moves the session onto it (via ReloadTo), returning the
// revision that was captured. It exists for a caller with some source of
// "the definition to serve" other than what the binder is presently
// serving — e.g. a plain on-disk reload for a session that has already
// engaged the definition control plane, where silently re-reading disk
// would bypass the revision pin entirely. Capturing the disk content as its
// own new revision first keeps the pin, and the revision lineage
// (Revisions()), honest: the disk content becomes traceable history instead
// of an untracked side channel.
func (b *Binding) CaptureAndReloadTo(ctx context.Context, c Closure) (rev Revision, prevStateExists bool, err error) {
	rev, err = b.svc.Capture(ctx, c)
	if err != nil {
		return Revision{}, false, err
	}
	prevStateExists, err = b.ReloadTo(ctx, rev.Digest)
	return rev, prevStateExists, err
}

// Close releases the handle currently backing this session's served
// revision, if any, retiring its materialised temp tree and the Service's
// refcount entry for that digest. Call this when the SESSION ITSELF is
// being torn down (evicted from the registry, or the registry is shutting
// down) — never as part of an ordinary reload, which already releases its
// own previously-served handle via ReloadTo's "old.Release()" step above.
// Idempotent: Handle.Release only has effect on its first call, and Close
// clears b.served so a second Close is a safe no-op.
//
// Before this existed, nothing released a session's served handle on
// eviction: cleanupEvicted/SessionRegistry.Close closed e.sink and e.rt but
// never touched e.binding, so every revision-reloaded session permanently
// leaked its materialised temp tree (os.MkdirTemp("kitsoki-story-*")) and
// held the Service's refcount for that digest at >=1 forever, pinning the
// compiled *app.AppDef and its whole closure in memory for the rest of the
// process's life.
func (b *Binding) Close() {
	b.mu.Lock()
	h := b.served
	b.served = nil
	b.mu.Unlock()
	if h != nil {
		h.Release()
	}
}
