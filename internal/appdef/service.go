package appdef

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"kitsoki/internal/appclosure"
)

// Service is the revision control plane: capture, patch, list, and
// refcounted checkout of stored revisions. One Service is shared by every
// live session in a process, so a revision produced against one session is
// addressable from any of them (see Binding.ReloadTo and the implementation
// spec's Open Risks item 10 on cross-application digests).
type Service struct {
	store  Storage
	loader Loader
	now    func() time.Time

	mu   sync.Mutex
	open map[string]*openRevision // keyed by digest
}

// openRevision tracks the live, refcounted materialisation of one stored
// revision. Checkout increments refs and hands out a Handle that decrements
// it on Release; the underlying compiled tree is torn down only when the
// last outstanding Handle releases. handle is the underlying compiled
// Handle — it is never Released directly by a Checkout caller, only by
// leasedHandle's teardown once refs reaches zero.
type openRevision struct {
	handle *Handle
	refs   int
}

// Option configures a Service.
type Option func(*Service)

// WithLoader overrides the default DefaultLoader{}.
func WithLoader(l Loader) Option {
	return func(s *Service) { s.loader = l }
}

// WithClock overrides the default time.Now, for deterministic tests.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// NewService returns a Service backed by store, with DefaultLoader{} and
// time.Now unless overridden.
func NewService(store Storage, opts ...Option) *Service {
	s := &Service{
		store:  store,
		loader: DefaultLoader{},
		now:    time.Now,
		open:   make(map[string]*openRevision),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// ErrUnknownRevision is returned by Checkout when digest names no stored
// revision.
var ErrUnknownRevision = errors.New("appdef: unknown revision digest")

// Capture stores c as a root revision (Source=SourceCapture, no parent). It
// COMPILES c first — a definition that cannot load is never stored, even
// when it came from something already running. Re-capturing identical bytes
// returns the existing revision (Put is idempotent), so Capture is safe to
// call repeatedly, e.g. once per session on first use.
//
// Capture compiles a PROBE handle purely to validate the closure, then
// releases it immediately; a later Checkout produces the handle that
// actually serves. That costs one extra materialisation and buys
// unambiguous ownership: the Service, not the caller of Capture, decides
// when a served tree is torn down.
func (s *Service) Capture(ctx context.Context, c Closure) (Revision, error) {
	probe, err := s.loader.Load(ctx, c)
	if err != nil {
		return Revision{}, fmt.Errorf("appdef: capture: %w", err)
	}
	appID := probe.Def.App.ID
	probe.Release()

	digest := appclosure.DigestBytes(RevisionSchema, c.Files)
	rev := Revision{
		Schema:    RevisionSchema,
		Digest:    digest,
		AppID:     appID,
		Entry:     c.Entry,
		Files:     cloneFiles(c.Files),
		Source:    SourceCapture,
		CreatedAt: s.now(),
	}
	if existing, ok, err := s.store.Get(ctx, digest); err != nil {
		return Revision{}, fmt.Errorf("appdef: capture: get: %w", err)
	} else if ok {
		return existing, nil
	}
	if err := s.store.Put(ctx, rev); err != nil {
		return Revision{}, fmt.Errorf("appdef: capture: put: %w", err)
	}
	return rev, nil
}

// Patch applies ops to the revision named by baseDigest, compiles the
// result, and stores it as a new revision with ParentDigest=baseDigest. It
// never mutates the base and never moves any session.
//
// A rejected patch returns (PatchResult{Rejected:true, Rejects:...}, nil) —
// error is reserved for operational faults (storage failure, context
// cancellation).
func (s *Service) Patch(ctx context.Context, baseDigest string, ops []Op) (PatchResult, error) {
	base, ok, err := s.store.Get(ctx, baseDigest)
	if err != nil {
		return PatchResult{}, fmt.Errorf("appdef: patch: get base: %w", err)
	}
	if !ok {
		return rejected(Reject{Code: RejectUnknownBase, Message: fmt.Sprintf("no stored revision with digest %q", baseDigest)}), nil
	}

	if len(ops) == 0 {
		return rejected(Reject{Code: RejectNoOps, Message: "ops must not be empty"}), nil
	}

	var rejects []Reject
	for _, op := range ops {
		switch op.Op {
		case OpSetFile:
			if r := validatePath(op.Path); r != nil {
				rejects = append(rejects, *r)
			}
		default:
			rejects = append(rejects, Reject{Code: RejectUnknownOp, File: op.Path, Message: fmt.Sprintf("unknown op %q", op.Op)})
		}
	}
	if len(rejects) > 0 {
		return PatchResult{Rejected: true, Rejects: rejects}, nil
	}

	candidate := cloneFiles(base.Files)
	for _, op := range ops {
		if op.Op == OpSetFile {
			candidate[op.Path] = []byte(op.Content)
		}
	}

	if filesEqual(candidate, base.Files) {
		return rejected(Reject{Code: RejectNoChange, Message: "patch produced a byte-identical closure"}), nil
	}

	if _, ok := candidate[base.Entry]; !ok {
		return rejected(Reject{Code: RejectEntryRemoved, File: base.Entry, Message: fmt.Sprintf("entry %q is missing from the patched closure", base.Entry)}), nil
	}

	closure := Closure{Entry: base.Entry, Files: candidate}
	probe, loadErr := s.loader.Load(ctx, closure)
	if loadErr != nil {
		return rejected(Reject{Code: RejectCompileFailed, File: base.Entry, Message: loadErr.Error()}), nil
	}
	appID := probe.Def.App.ID
	probe.Release()

	digest := appclosure.DigestBytes(RevisionSchema, candidate)
	rev := Revision{
		Schema:       RevisionSchema,
		Digest:       digest,
		AppID:        appID,
		Entry:        base.Entry,
		Files:        cloneFiles(candidate),
		ParentDigest: baseDigest,
		Source:       SourcePatch,
		CreatedAt:    s.now(),
	}
	if err := s.store.Put(ctx, rev); err != nil {
		return PatchResult{}, fmt.Errorf("appdef: patch: put: %w", err)
	}
	return PatchResult{Revision: rev}, nil
}

// rejected wraps a single Reject into a PatchResult.
func rejected(r Reject) PatchResult {
	return PatchResult{Rejected: true, Rejects: []Reject{r}}
}

// Revisions lists stored revisions newest-first. appID "" means all.
func (s *Service) Revisions(ctx context.Context, appID string) ([]Revision, error) {
	return s.store.List(ctx, appID)
}

// Checkout compiles the stored revision named by digest and returns a live
// Handle. Handles are refcounted per digest: two callers checking out the
// same digest share one materialised tree, and the tree is removed only
// when the LAST handle for that digest is released. The Service owns that
// lifetime — a caller only Releases what it Checked out; it must never
// reach into another handle's teardown. Unknown digest returns
// ErrUnknownRevision.
func (s *Service) Checkout(ctx context.Context, digest string) (*Handle, error) {
	rev, ok, err := s.store.Get(ctx, digest)
	if err != nil {
		return nil, fmt.Errorf("appdef: checkout: %w", err)
	}
	if !ok {
		return nil, ErrUnknownRevision
	}

	s.mu.Lock()
	if or, exists := s.open[digest]; exists {
		or.refs++
		s.mu.Unlock()
		return s.leasedHandle(digest, or.handle), nil
	}
	s.mu.Unlock()

	// Compile OUTSIDE the lock: loading can be slow (temp dir materialise +
	// full loader pass) and must not block every other digest's checkout.
	closure := Closure{Entry: rev.Entry, Files: rev.Files}
	compiled, err := s.loader.Load(ctx, closure)
	if err != nil {
		return nil, fmt.Errorf("appdef: checkout: recompile %s: %w", digest, err)
	}
	compiled.Digest = digest

	s.mu.Lock()
	if or, exists := s.open[digest]; exists {
		// Lost a race with a concurrent Checkout of the same digest: keep
		// the winner's compiled tree, discard ours.
		or.refs++
		s.mu.Unlock()
		compiled.Release()
		return s.leasedHandle(digest, or.handle), nil
	}
	s.open[digest] = &openRevision{handle: compiled, refs: 1}
	s.mu.Unlock()

	return s.leasedHandle(digest, compiled), nil
}

// leasedHandle returns a new *Handle sharing shared's Def/Digest, whose
// Release decrements this digest's refcount and only tears down the
// underlying compiled tree when the count reaches zero.
func (s *Service) leasedHandle(digest string, shared *Handle) *Handle {
	return newHandle(shared.Def, digest, func() {
		s.mu.Lock()
		or, ok := s.open[digest]
		if !ok {
			s.mu.Unlock()
			return
		}
		or.refs--
		if or.refs > 0 {
			s.mu.Unlock()
			return
		}
		delete(s.open, digest)
		s.mu.Unlock()
		or.handle.Release()
	})
}
