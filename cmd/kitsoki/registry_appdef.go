// registry_appdef.go implements server.AppDefProvider on SessionRegistry: the
// definition control-plane RPCs (runstatus.appdef.current/patch/revisions,
// and the revision-targeted form of runstatus.session.reload dispatched from
// server.go) delegate to each session's own appdef.Binding — registry.go's
// e.binding, built alongside the session's runtime in newSessionWithOrigin
// and AttachExternal.
//
// Every method here follows the same shape: look the entry up under r.mu,
// RELEASE the mutex, then call into e.binding. appdef.Binding and appdef.Gate
// are already safe for concurrent use and do their own, finer-grained
// (per-session) locking — holding r.mu across a compile or a swap would
// serialize every session's definition traffic through one global lock for
// no reason, and (for AppDefReload specifically) risks the exact
// beginTurn/r.mu deadlock the package doc on beginTurn warns about.
package main

import (
	"context"
	"errors"
	"fmt"

	"kitsoki/internal/appdef"
	"kitsoki/internal/runstatus/server"
)

// busyWrap wraps a Gate refusal ([appdef.ErrTurnInFlight],
// [appdef.ErrReloadInProgress]) in [server.ErrSessionBusy] so the RPC layer
// (server.lifecycleOrBusyErr) can recognize it via errors.Is without
// importing internal/appdef — the server package must stay free of the
// definition control-plane package (see appdef.go's own doc comment on that
// boundary). Any other error, and nil, pass through unchanged.
func busyWrap(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, appdef.ErrTurnInFlight) || errors.Is(err, appdef.ErrReloadInProgress) {
		return fmt.Errorf("%w: %w", server.ErrSessionBusy, err)
	}
	return err
}

// appdefEntry looks up sessionID's entry under r.mu and releases the lock
// before returning — every method below calls into e.binding OUTSIDE the
// registry mutex.
func (r *SessionRegistry) appdefEntry(sessionID string) (*entry, error) {
	r.mu.Lock()
	e, ok := r.sessions[sessionID]
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("appdef: unknown session %q", sessionID)
	}
	return e, nil
}

// markPinned stamps sessionID's cached pinned-revision digest under r.mu —
// the signal Reload/Staleness use to refuse a plain disk reload once a
// session has engaged the definition control plane (see entry.pinnedDigest's
// doc comment for why this is a registry-level cache rather than a live read
// of e.binding's own state). No-op if the session is no longer live, or if
// digest is empty (never called that way by this file, but defensive).
func (r *SessionRegistry) markPinned(sessionID, digest string) {
	if digest == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.sessions[sessionID]; ok {
		e.pinnedDigest = digest
	}
}

// appDefRevisionWire projects an appdef.Revision onto its wire form via
// Header() — the same digest/parent/file-list projection appdef.Header
// already defines, just re-typed onto server.AppDefRevision so this package
// never has to import internal/appdef into the server package.
func appDefRevisionWire(rev appdef.Revision) server.AppDefRevision {
	h := rev.Header()
	return server.AppDefRevision{
		Schema:       h.Schema,
		Digest:       h.Digest,
		AppID:        h.AppID,
		Entry:        h.Entry,
		ParentDigest: h.ParentDigest,
		Source:       h.Source,
		CreatedAt:    h.CreatedAt,
		Files:        h.Files,
		TotalBytes:   h.TotalBytes,
	}
}

// AppDefCurrent implements [server.AppDefProvider]: it returns the revision
// the session is serving, capturing and pinning it (via e.binding.Current) on
// first call.
func (r *SessionRegistry) AppDefCurrent(ctx context.Context, sessionID string) (server.AppDefRevision, error) {
	e, err := r.appdefEntry(sessionID)
	if err != nil {
		return server.AppDefRevision{}, err
	}
	rev, err := e.binding.Current(ctx)
	if err != nil {
		return server.AppDefRevision{}, busyWrap(err)
	}
	r.markPinned(sessionID, rev.Digest)
	return appDefRevisionWire(rev), nil
}

// AppDefPatch implements [server.AppDefProvider]: it applies ops on top of
// the session's current revision and stores the compiled result as a NEW
// immutable revision, without moving the session onto it — moving is the
// separate AppDefReload / runstatus.session.reload{revision} call.
//
// e.binding.Patch itself resolves/pins the session's current revision as its
// first step (whether the patch is later accepted or rejected), so this
// makes an explicit e.binding.Current call of its own, after Patch returns,
// purely to learn that (unchanged) pinned digest for markPinned — Patch never
// moves the pin, so this does not race or duplicate any state Patch itself
// wrote.
func (r *SessionRegistry) AppDefPatch(ctx context.Context, sessionID, baseDigest string, ops []server.AppDefPatchOp) (server.AppDefPatchResult, error) {
	e, err := r.appdefEntry(sessionID)
	if err != nil {
		return server.AppDefPatchResult{}, err
	}
	defOps := make([]appdef.Op, len(ops))
	for i, op := range ops {
		defOps[i] = appdef.Op{Op: op.Op, Path: op.Path, Content: op.Content}
	}
	res, err := e.binding.Patch(ctx, baseDigest, defOps)
	if err != nil {
		return server.AppDefPatchResult{}, busyWrap(err)
	}
	if cur, curErr := e.binding.Current(ctx); curErr == nil {
		r.markPinned(sessionID, cur.Digest)
	}

	out := server.AppDefPatchResult{Rejected: res.Rejected}
	for _, rej := range res.Rejects {
		out.Rejects = append(out.Rejects, server.AppDefReject{Code: rej.Code, File: rej.File, Message: rej.Message})
	}
	if !res.Rejected {
		wire := appDefRevisionWire(res.Revision)
		out.Revision = &wire
	}
	return out, nil
}

// AppDefRevisions implements [server.AppDefProvider]: it lists stored
// revisions for the session's application, newest first.
func (r *SessionRegistry) AppDefRevisions(ctx context.Context, sessionID string) ([]server.AppDefRevision, error) {
	e, err := r.appdefEntry(sessionID)
	if err != nil {
		return nil, err
	}
	revs, err := e.binding.Revisions(ctx)
	if err != nil {
		return nil, busyWrap(err)
	}
	if cur, curErr := e.binding.Current(ctx); curErr == nil {
		r.markPinned(sessionID, cur.Digest)
	}

	out := make([]server.AppDefRevision, len(revs))
	for i, rev := range revs {
		out[i] = appDefRevisionWire(rev)
	}
	return out, nil
}

// AppDefReload implements [server.AppDefProvider]: it moves sessionID onto
// the revision named by digest via e.binding's Gate-guarded ReloadTo (which
// swaps the definition, keeps the live world state, and re-fires on_enter
// when the current state survived the edit), then pins the entry to digest —
// no extra lookup needed, ReloadTo's own target digest IS the new pin.
func (r *SessionRegistry) AppDefReload(ctx context.Context, sessionID, digest string) (bool, error) {
	e, err := r.appdefEntry(sessionID)
	if err != nil {
		return false, err
	}
	prevStateExists, err := e.binding.ReloadTo(ctx, digest)
	if err != nil {
		return false, busyWrap(err)
	}
	r.markPinned(sessionID, digest)

	// Drop the cached meta controller so the next meta turn rebuilds against
	// the reloaded AppDef — the same invalidation the disk-reload path
	// (SessionRegistry.Reload) already performs after a successful swap.
	r.mu.Lock()
	e.metaController = nil
	r.mu.Unlock()

	return prevStateExists, nil
}
