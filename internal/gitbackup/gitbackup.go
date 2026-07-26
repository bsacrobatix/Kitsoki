// Package gitbackup provides commit-by-commit git backup to object storage as
// an incremental bundle chain, per the stateless-orchestrator storage plan
// (§1.4): every backup writes a git bundle covering the refs that changed
// since the previous snapshot, a periodic full bundle acts as a compaction
// point, and a small versioned JSON manifest records the chain. Restore
// replays the newest full bundle plus each later increment, then pins the
// target's refs to the manifest tip snapshot.
//
// Object layout under a caller-chosen prefix (e.g. repos/<repo>):
//
//	<prefix>/bundles/<seq>-full.bundle
//	<prefix>/bundles/<seq>-incr-<oldtip>-<newtip>.bundle
//	<prefix>/manifest.json            (git-backup-manifest/v1)
//
// where <oldtip>/<newtip> are short digests of the old and new refs
// snapshots. Data is written before the manifest (write-after-data), so a
// crash leaves at worst an orphan bundle object. See Manifest for the
// concurrency story (single writer per prefix assumed).
//
// The backed-up ref namespace is refs/heads/* and refs/tags/*. Git is driven
// through the git CLI, matching the rest of the repository.
package gitbackup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kitsoki/internal/objectstore"
)

// DefaultMaxBundleSize caps a single bundle object, matching the
// capsule-source-bundle/v1 contract.
const DefaultMaxBundleSize = int64(256 << 20)

// Backer performs backups of one local repository into an object-store
// prefix. It is not safe for concurrent use, and the chain assumes a single
// writer per prefix (see Manifest).
type Backer struct {
	store    objectstore.Store
	prefix   string
	repoPath string
	maxBytes int64
	now      func() time.Time
}

// Option customizes a Backer.
type Option func(*Backer)

// WithClock injects the time source used for manifest timestamps.
func WithClock(now func() time.Time) Option {
	return func(b *Backer) { b.now = now }
}

// WithMaxBundleSize overrides the per-bundle size cap.
func WithMaxBundleSize(maxBytes int64) Option {
	return func(b *Backer) {
		if maxBytes > 0 {
			b.maxBytes = maxBytes
		}
	}
}

// Open prepares a Backer for the git repository at repoPath, storing bundles
// under prefix in store. It verifies repoPath is a git repository.
func Open(store objectstore.Store, prefix, repoPath string, opts ...Option) (*Backer, error) {
	if store == nil {
		return nil, fmt.Errorf("gitbackup: nil object store")
	}
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return nil, fmt.Errorf("gitbackup: empty object prefix")
	}
	root, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, fmt.Errorf("gitbackup: resolve repository path: %w", err)
	}
	if _, err := gitOutput(context.Background(), root, "rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("gitbackup: %s is not a git repository: %w", root, err)
	}
	b := &Backer{
		store:    store,
		prefix:   prefix,
		repoPath: root,
		maxBytes: DefaultMaxBundleSize,
		now:      time.Now,
	}
	for _, opt := range opts {
		opt(b)
	}
	return b, nil
}

// Result reports the outcome of one backup call.
type Result struct {
	// Skipped is true when nothing changed since the manifest tip (or the
	// repository has no refs yet); no object or manifest write happened.
	Skipped bool
	// Entry is the manifest entry appended; zero when Skipped.
	Entry Entry
	// Manifest is the manifest after the write; the pre-existing manifest
	// when Skipped.
	Manifest Manifest
}

// BackupIncremental diffs the repository's current refs against the manifest
// tip snapshot and appends the smallest entry that captures the change:
//
//   - no manifest yet: an automatic full backup;
//   - changed or new refs: an incremental bundle excluding every basis tip;
//   - deletions or rewinds whose objects the chain already holds: a
//     bundle-less "refs" entry;
//   - a basis the local repository can no longer express (e.g. pruned
//     objects after a force-push plus gc): fall back to a full backup, the
//     only chain link git bundle can still produce.
//
// It is a no-op (Skipped) when the snapshot equals the manifest tip.
func (b *Backer) BackupIncremental(ctx context.Context) (Result, error) {
	manifest, err := loadManifest(ctx, b.store, b.prefix)
	if err != nil {
		return Result{}, err
	}
	refs, err := readRefs(ctx, b.repoPath)
	if err != nil {
		return Result{}, err
	}
	tip, hasTip := manifest.Tip()
	if !hasTip {
		if len(refs) == 0 {
			return Result{Skipped: true, Manifest: manifest}, nil
		}
		return b.backupFull(ctx, manifest, refs)
	}
	if maps.Equal(refs, tip.Refs) {
		return Result{Skipped: true, Manifest: manifest}, nil
	}

	var includes []string
	for name, sha := range refs {
		if tip.Refs[name] != sha {
			includes = append(includes, name)
		}
	}
	sort.Strings(includes)

	seq := tip.Seq + 1
	entry := Entry{
		Seq:       seq,
		Kind:      KindIncr,
		Refs:      refs,
		BasisRefs: tip.Refs,
		CreatedAt: b.now().UTC(),
	}
	if len(includes) == 0 {
		// Only deletions: the new snapshot's objects are all covered by
		// the chain, so record a bundle-less refs entry.
		entry.Kind = KindRefs
		next, err := putManifest(ctx, b.store, b.prefix, manifest.Generation, entry, b.now())
		if err != nil {
			return Result{}, err
		}
		return Result{Entry: entry, Manifest: next}, nil
	}

	data, bundleErr := b.createBundle(ctx, includes, exclusions(tip.Refs))
	switch {
	case bundleErr == nil:
		// fallthrough to upload below
	case isEmptyBundleErr(bundleErr):
		// Every object reachable from the changed refs is already
		// reachable from the basis snapshot — and therefore already in
		// the chain (chain invariant: a restore holds all objects
		// reachable from every recorded snapshot). Typical case: a
		// force-push rewinding a branch to an ancestor.
		entry.Kind = KindRefs
		next, err := putManifest(ctx, b.store, b.prefix, manifest.Generation, entry, b.now())
		if err != nil {
			return Result{}, err
		}
		return Result{Entry: entry, Manifest: next}, nil
	default:
		// git bundle cannot express this increment (e.g. a basis commit
		// was pruned locally). A full backup is always expressible and
		// restarts the chain at a compaction point.
		return b.backupFull(ctx, manifest, refs)
	}

	entry.Key = fmt.Sprintf("%s/bundles/%06d-incr-%s-%s.bundle", b.prefix, seq, refsDigest(tip.Refs), refsDigest(refs))
	return b.finishBundleEntry(ctx, manifest, entry, data)
}

// BackupFull writes a self-contained bundle of all refs — a compaction point:
// restore never needs entries older than the newest full backup. It is a
// no-op (Skipped) when the repository has no refs.
func (b *Backer) BackupFull(ctx context.Context) (Result, error) {
	manifest, err := loadManifest(ctx, b.store, b.prefix)
	if err != nil {
		return Result{}, err
	}
	refs, err := readRefs(ctx, b.repoPath)
	if err != nil {
		return Result{}, err
	}
	if len(refs) == 0 {
		return Result{Skipped: true, Manifest: manifest}, nil
	}
	return b.backupFull(ctx, manifest, refs)
}

func (b *Backer) backupFull(ctx context.Context, manifest Manifest, refs map[string]string) (Result, error) {
	names := make([]string, 0, len(refs))
	for name := range refs {
		names = append(names, name)
	}
	sort.Strings(names)
	data, err := b.createBundle(ctx, names, nil)
	if err != nil {
		return Result{}, err
	}
	seq := 1
	if tip, ok := manifest.Tip(); ok {
		seq = tip.Seq + 1
	}
	entry := Entry{
		Seq:       seq,
		Kind:      KindFull,
		Key:       fmt.Sprintf("%s/bundles/%06d-full.bundle", b.prefix, seq),
		Refs:      refs,
		CreatedAt: b.now().UTC(),
	}
	return b.finishBundleEntry(ctx, manifest, entry, data)
}

// finishBundleEntry uploads the bundle bytes, then records the manifest entry
// (write-after-data).
func (b *Backer) finishBundleEntry(ctx context.Context, manifest Manifest, entry Entry, data []byte) (Result, error) {
	sum := sha256.Sum256(data)
	entry.Size = int64(len(data))
	entry.SHA256 = hex.EncodeToString(sum[:])
	if _, err := b.store.Put(ctx, entry.Key, bytes.NewReader(data), entry.Size, objectstore.PutOptions{ContentType: "application/octet-stream"}); err != nil {
		return Result{}, fmt.Errorf("gitbackup: upload bundle: %w", err)
	}
	next, err := putManifest(ctx, b.store, b.prefix, manifest.Generation, entry, b.now())
	if err != nil {
		return Result{}, err
	}
	return Result{Entry: entry, Manifest: next}, nil
}

// exclusions returns deduplicated, sorted ^sha rev-list arguments for a basis
// refs snapshot.
func exclusions(basis map[string]string) []string {
	seen := make(map[string]bool, len(basis))
	var out []string
	for _, sha := range basis {
		if !seen[sha] {
			seen[sha] = true
			out = append(out, "^"+sha)
		}
	}
	sort.Strings(out)
	return out
}

// createBundle runs git bundle create with the given inclusions/exclusions
// and returns the bundle bytes, enforcing the size cap.
func (b *Backer) createBundle(ctx context.Context, includes, excludes []string) ([]byte, error) {
	tmpDir, err := os.MkdirTemp("", "kitsoki-gitbackup-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	path := filepath.Join(tmpDir, "backup.bundle")
	args := append([]string{"bundle", "create", path}, includes...)
	args = append(args, excludes...)
	if _, err := gitOutput(ctx, b.repoPath, args...); err != nil {
		return nil, fmt.Errorf("gitbackup: create bundle: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) == 0 || int64(len(data)) > b.maxBytes {
		return nil, fmt.Errorf("gitbackup: bundle size %d outside allowed range (max %d)", len(data), b.maxBytes)
	}
	return data, nil
}

func isEmptyBundleErr(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "empty bundle")
}

// readRefs snapshots refs/heads/* and refs/tags/* as name -> object id.
func readRefs(ctx context.Context, repo string) (map[string]string, error) {
	out, err := gitOutput(ctx, repo, "for-each-ref", "--format=%(objectname) %(refname)", "refs/heads", "refs/tags")
	if err != nil {
		return nil, fmt.Errorf("gitbackup: list refs: %w", err)
	}
	refs := make(map[string]string)
	for line := range strings.Lines(out) {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		sha, name, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("gitbackup: unexpected for-each-ref line %q", line)
		}
		refs[name] = sha
	}
	return refs, nil
}

func shortSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:6])
}

func gitOutput(ctx context.Context, root string, args ...string) (string, error) {
	argv := append([]string{"-C", root}, args...)
	out, err := exec.CommandContext(ctx, "git", argv...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
