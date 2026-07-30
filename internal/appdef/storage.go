package appdef

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"sync"
)

// Storage is the injected append-only revision store. Implementations must
// be safe for concurrent use.
//
// Put is idempotent for identical content and REFUSES conflicting content
// for the same digest — the same "immutable content mismatch fails instead
// of replacing" discipline internal/applicationbuild holds for its own
// content-addressed bundles. A digest collision with different bytes is a
// bug (a schema/hash mismatch upstream) or an attack, never an overwrite.
type Storage interface {
	Put(ctx context.Context, rev Revision) error
	Get(ctx context.Context, digest string) (Revision, bool, error)
	// List returns revisions newest-first. appID "" means every application.
	List(ctx context.Context, appID string) ([]Revision, error)
}

// ErrDigestConflict is returned by Put when digest already exists with
// content that does not byte-match what is being stored.
var ErrDigestConflict = errors.New("appdef: revision digest already stored with different content")

// MemStorage is the in-memory Storage used by the live registry today and by
// every test in this package. A durable Storage (digest-named file, or the
// daemon's SQLite dialect) is deliberately deferred — see the implementation
// spec's out-of-scope section; Storage is an injected interface precisely so
// a durable implementation is a drop-in later, with zero change to Service.
//
// MemStorage never evicts. Every accepted revision (up to the RPC layer's
// max body size per patch) is retained for the lifetime of the process, with
// no per-application cap and no LRU on unpinned digests — a client that
// calls runstatus.appdef.patch repeatedly grows this store without bound.
// That is an accepted, explicitly-named exposure for this milestone (a
// bounded/LRU MemStorage, or a durable Storage with its own retention
// policy, is the follow-up), not an oversight — but an operator embedding
// MemStorage directly should know it before relying on it in a
// long-running, never-restarted process.
type MemStorage struct {
	mu       sync.RWMutex
	byDigest map[string]Revision
	// order records insertion sequence, used only to break CreatedAt ties in
	// List deterministically under a coarse clock (two revisions stored
	// within the same clock tick must still list in a stable order).
	order []string
	seq   map[string]int
}

// NewMemStorage returns an empty MemStorage.
func NewMemStorage() *MemStorage {
	return &MemStorage{
		byDigest: make(map[string]Revision),
		seq:      make(map[string]int),
	}
}

// Put stores rev. A second Put of the same digest with byte-identical Files
// (and Entry) is a no-op success; a second Put of the same digest with
// different content returns ErrDigestConflict and does not modify the
// stored revision.
func (m *MemStorage) Put(_ context.Context, rev Revision) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.byDigest[rev.Digest]; ok {
		if !filesEqual(existing.Files, rev.Files) || existing.Entry != rev.Entry {
			return ErrDigestConflict
		}
		return nil
	}

	stored := rev
	stored.Files = cloneFiles(rev.Files)
	m.byDigest[rev.Digest] = stored
	m.seq[rev.Digest] = len(m.order)
	m.order = append(m.order, rev.Digest)
	return nil
}

// Get returns the revision named by digest, or (_, false, nil) when absent.
func (m *MemStorage) Get(_ context.Context, digest string) (Revision, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	rev, ok := m.byDigest[digest]
	if !ok {
		return Revision{}, false, nil
	}
	rev.Files = cloneFiles(rev.Files)
	return rev, true, nil
}

// List returns stored revisions newest-first (CreatedAt descending, ties
// broken by insertion order descending). appID "" returns every revision.
func (m *MemStorage) List(_ context.Context, appID string) ([]Revision, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]Revision, 0, len(m.byDigest))
	for _, rev := range m.byDigest {
		if appID != "" && rev.AppID != appID {
			continue
		}
		cp := rev
		cp.Files = cloneFiles(rev.Files)
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return m.seq[out[i].Digest] > m.seq[out[j].Digest]
	})
	return out, nil
}

// filesEqual reports whether two closure file sets are byte-identical.
func filesEqual(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for path, ab := range a {
		bb, ok := b[path]
		if !ok || !bytes.Equal(ab, bb) {
			return false
		}
	}
	return true
}
