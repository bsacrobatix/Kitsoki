package graph

// store.go introduces the catalog storage seam: a minimal CatalogStore
// interface separating what the engine owns (changeset mechanics, guard
// fills, validation, the lint-delta gate's POLICY) from what storage owns
// (persistence plus optimistic concurrency). The file-backed implementation
// below wraps today's exact behavior — LoadCatalog for reads,
// commitScratchOperations (propose.go) for writes — so the YAML path stays
// byte-for-byte unchanged and remains the default. A later phase adds a
// Postgres-backed implementation behind the same interface; nothing in this
// file changes how Propose/Authorize/Withdraw/Rebase/Apply commit today
// (they still call commitScratchOperations directly).

import (
	"context"
	"errors"
	"fmt"
)

// CatalogRev is an opaque revision token identifying the exact catalog state
// a Load observed, used as the optimistic-concurrency base for a subsequent
// Commit (hazard guard #2). For the file-backed store this is the catalog's
// ContentDigest (guards.go); a different store may use any token whose
// equality means "the catalog has not changed since that Load".
type CatalogRev string

// CommitOptions carries the per-commit knobs a CatalogStore.Commit honors.
type CommitOptions struct {
	// DryRun builds and lint-gates the candidate but persists nothing —
	// commitScratchOperations' dryRun flag. ChangedFiles in the result
	// reflects what WOULD change.
	DryRun bool
}

// CommitResult reports the outcome of one CatalogStore.Commit attempt. It is
// the exported mirror of scratchCommit (propose.go): a commit either lands
// (ChangedFiles/CanonicalizedFiles), is rejected because the operations could
// not be applied (RejectReasons), or is rejected because the candidate
// introduced NEW error-severity lint (LintIssues). Rejections are results,
// not errors — a CAS conflict, by contrast, is an error (see IsCASConflict).
type CommitResult struct {
	// ChangedFiles are every file the commit rewrote, relative to the
	// catalog root: the operations' own edits plus any canonicalization
	// heal. Storage-neutral in name only — a non-file store reports the
	// logical units it touched.
	ChangedFiles []string
	// CanonicalizedFiles is the heal's subset of ChangedFiles.
	CanonicalizedFiles []string
	// LintIssues is non-empty when the candidate catalog introduced NEW
	// error-severity lint — a rejection; nothing was committed.
	LintIssues []LintIssue
	// RejectReasons is non-empty when the operations could not be applied
	// at all — also a rejection, also nothing committed.
	RejectReasons []string
}

// Rejected reports whether the commit was refused for any reason.
func (r CommitResult) Rejected() bool {
	return len(r.RejectReasons) > 0 || len(r.LintIssues) > 0
}

// CatalogStore is the storage seam a catalog lives behind: Load hands the
// engine a fully built in-memory Catalog plus the revision token it was read
// at; Commit persists a batch of already-validated Operations against that
// base revision, refusing with a CAS-conflict error (IsCASConflict) when the
// catalog moved underneath the caller. Everything above this line — parsing
// and validating changesets, guard fills, auto-authorize policy, which ops
// to commit — stays in the engine; the store owns durability, atomicity,
// and optimistic concurrency.
type CatalogStore interface {
	// Load reads the current catalog and its revision token. The returned
	// Catalog is the same fully resolved shape LoadCatalog produces.
	Load(ctx context.Context) (*Catalog, CatalogRev, error)
	// Commit applies ops against the catalog as of base. A base that no
	// longer matches the stored catalog returns an error satisfying
	// IsCASConflict — the caller's cue to reload, re-validate, and retry
	// (bounded, exactly like the verbs' casMaxAttempts loops). Rejections
	// (unappliable ops, new lint) come back inside CommitResult with a nil
	// error; nothing is persisted for a rejection or with opts.DryRun set.
	Commit(ctx context.Context, base CatalogRev, ops []Operation, opts CommitOptions) (CommitResult, error)
	// Ref names the stored catalog for diagnostics and routing — the
	// filesystem path for the file store; a DSN-ish locator elsewhere.
	Ref() string
}

// IsCASConflict reports whether err is the optimistic-concurrency conflict a
// CatalogStore.Commit returns when its base revision went stale — the
// exported face of guards.go's errCASConflict sentinel, so callers outside
// this package can drive the same bounded-retry loop the lifecycle verbs use.
func IsCASConflict(err error) bool {
	return errors.Is(err, errCASConflict)
}

// FileCatalogStore is the default CatalogStore: the YAML catalog on disk at
// Path, read via LoadCatalog and written via the exact scratch/canonicalize/
// apply/relint/CAS pipeline (commitScratchOperations) every lifecycle verb
// already commits through. It holds no state beyond the path — each Load is
// a fresh full read, matching the per-call reload contract every MCP/host
// read already has.
type FileCatalogStore struct {
	path string
}

var _ CatalogStore = (*FileCatalogStore)(nil)

// NewFileCatalogStore returns the file-backed store for the catalog at path
// (a bundle dir or a single catalog file, same as LoadCatalog).
func NewFileCatalogStore(path string) *FileCatalogStore {
	return &FileCatalogStore{path: path}
}

// Ref returns the catalog's filesystem path.
func (s *FileCatalogStore) Ref() string { return s.path }

// Load reads the catalog exactly as LoadCatalog does; the revision token is
// the load-time ContentDigest.
func (s *FileCatalogStore) Load(_ context.Context) (*Catalog, CatalogRev, error) {
	cat, err := LoadCatalog(s.path)
	if err != nil {
		return nil, "", err
	}
	return cat, CatalogRev(cat.ContentDigest), nil
}

// Commit materializes base back into a loaded Catalog and delegates to
// commitScratchOperations — the same functions, order, errors, and CAS
// behavior as every lifecycle verb. The up-front digest comparison is the
// interface's optimistic-concurrency contract: if the on-disk catalog no
// longer matches base, the caller's validation ran against stale state and
// the commit must conflict rather than proceed against the newer content
// (the end-of-pipeline commitWithCAS check alone could not catch that case,
// because the fresh load's digest would match the fresh disk state).
func (s *FileCatalogStore) Commit(_ context.Context, base CatalogRev, ops []Operation, opts CommitOptions) (CommitResult, error) {
	cat, err := LoadCatalog(s.path)
	if err != nil {
		return CommitResult{}, fmt.Errorf("graph store commit: load %s: %w", s.path, err)
	}
	if CatalogRev(cat.ContentDigest) != base {
		return CommitResult{}, errCASConflict
	}
	commit, err := commitScratchOperations(cat, ops, opts.DryRun)
	if err != nil {
		return CommitResult{}, err
	}
	return CommitResult{
		ChangedFiles:       commit.ChangedFiles,
		CanonicalizedFiles: commit.CanonicalizedFiles,
		LintIssues:         commit.LintIssues,
		RejectReasons:      commit.RejectReasons,
	}, nil
}
