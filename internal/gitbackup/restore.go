package gitbackup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"kitsoki/internal/objectstore"
)

// ErrNoBackup reports that the prefix holds no manifest (or an empty chain).
var ErrNoBackup = errors.New("gitbackup: no backup found at prefix")

// Restore rebuilds the repository from the bundle chain under prefix into a
// new bare repository at targetPath. It refuses to touch an existing
// non-empty directory. The chain is replayed from the newest full backup
// through each later increment in order; every downloaded bundle is verified
// against the manifest's size and sha256 before git sees it. Afterwards the
// target's refs are set to exactly the manifest tip snapshot (covering ref
// deletions and force-pushes), HEAD is pointed at a restored branch when one
// exists, and the result is checked with git fsck plus a ref comparison.
func Restore(ctx context.Context, store objectstore.Store, prefix, targetPath string) (Manifest, error) {
	prefix = strings.Trim(prefix, "/")
	if prefix == "" {
		return Manifest{}, fmt.Errorf("gitbackup: empty object prefix")
	}
	target, err := filepath.Abs(targetPath)
	if err != nil {
		return Manifest{}, fmt.Errorf("gitbackup: resolve target path: %w", err)
	}
	if entries, err := os.ReadDir(target); err == nil && len(entries) > 0 {
		return Manifest{}, fmt.Errorf("gitbackup: restore target %s exists and is not empty", target)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, fmt.Errorf("gitbackup: inspect target: %w", err)
	}

	manifest, err := loadManifest(ctx, store, prefix)
	if err != nil {
		return Manifest{}, err
	}
	tip, ok := manifest.Tip()
	if !ok {
		return Manifest{}, ErrNoBackup
	}

	// Replay window: the newest full entry and everything after it.
	start := -1
	for i := len(manifest.Entries) - 1; i >= 0; i-- {
		if manifest.Entries[i].Kind == KindFull {
			start = i
			break
		}
	}
	if start < 0 {
		return Manifest{}, fmt.Errorf("gitbackup: manifest chain has no full backup")
	}

	// Restore into a staging sibling and rename into place only on success:
	// a failed or interrupted restore must leave the target retryable, never
	// half-initialized (the non-empty guard above would otherwise poison the
	// target for every later attempt).
	parent := filepath.Dir(target)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return Manifest{}, fmt.Errorf("gitbackup: create target parent: %w", err)
	}
	staging, err := os.MkdirTemp(parent, ".kitsoki-restore-*")
	if err != nil {
		return Manifest{}, fmt.Errorf("gitbackup: create staging directory: %w", err)
	}
	defer os.RemoveAll(staging) // no-op once staging has been renamed to target
	if err := os.Chmod(staging, 0o755); err != nil {
		return Manifest{}, fmt.Errorf("gitbackup: prepare staging directory: %w", err)
	}
	if _, err := gitOutput(ctx, staging, "init", "--bare", "--quiet", "."); err != nil {
		return Manifest{}, fmt.Errorf("gitbackup: init target: %w", err)
	}

	tmpDir, err := os.MkdirTemp("", "kitsoki-gitrestore-*")
	if err != nil {
		return Manifest{}, err
	}
	defer os.RemoveAll(tmpDir)

	for _, entry := range manifest.Entries[start:] {
		if entry.Kind == KindRefs {
			continue // bundle-less snapshot change; objects already present
		}
		path := filepath.Join(tmpDir, fmt.Sprintf("%06d.bundle", entry.Seq))
		if err := fetchVerified(ctx, store, entry, path); err != nil {
			return Manifest{}, err
		}
		if _, err := gitOutput(ctx, staging, "fetch", "--quiet", path, "+refs/*:refs/*"); err != nil {
			return Manifest{}, fmt.Errorf("gitbackup: apply bundle seq %d: %w", entry.Seq, err)
		}
	}

	if err := setRefs(ctx, staging, tip.Refs); err != nil {
		return Manifest{}, err
	}
	if _, err := gitOutput(ctx, staging, "fsck", "--no-progress"); err != nil {
		return Manifest{}, fmt.Errorf("gitbackup: fsck after restore: %w", err)
	}
	restored, err := readRefs(ctx, staging)
	if err != nil {
		return Manifest{}, err
	}
	if !maps.Equal(restored, tip.Refs) {
		return Manifest{}, fmt.Errorf("gitbackup: restored refs do not match manifest tip snapshot")
	}

	// The entry guard admitted target only as absent or an empty directory;
	// remove the empty directory so the rename can claim its place. A
	// concurrent writer that filled it in the meantime fails the removal,
	// which is the safe outcome.
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, fmt.Errorf("gitbackup: replace target: %w", err)
	}
	if err := os.Rename(staging, target); err != nil {
		return Manifest{}, fmt.Errorf("gitbackup: move restored repository into place: %w", err)
	}
	return manifest, nil
}

// fetchVerified downloads a bundle object to path, refusing when the bytes do
// not match the manifest entry's recorded size and sha256.
func fetchVerified(ctx context.Context, store objectstore.Store, entry Entry, path string) error {
	body, _, err := store.Get(ctx, entry.Key)
	if err != nil {
		return fmt.Errorf("gitbackup: fetch bundle seq %d (%s): %w", entry.Seq, entry.Key, err)
	}
	defer body.Close()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(file, hasher), body)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("gitbackup: download bundle seq %d: %w", entry.Seq, err)
	}
	if size != entry.Size {
		return fmt.Errorf("gitbackup: bundle seq %d size %d does not match manifest %d", entry.Seq, size, entry.Size)
	}
	if got := hex.EncodeToString(hasher.Sum(nil)); got != entry.SHA256 {
		return fmt.Errorf("gitbackup: bundle seq %d sha256 mismatch (chain integrity violated)", entry.Seq)
	}
	return nil
}

// setRefs pins the target repository's refs/heads and refs/tags namespaces to
// exactly the snapshot, then points HEAD at a restored branch when possible.
func setRefs(ctx context.Context, target string, snapshot map[string]string) error {
	current, err := readRefs(ctx, target)
	if err != nil {
		return err
	}
	for name := range current {
		if _, keep := snapshot[name]; !keep {
			if _, err := gitOutput(ctx, target, "update-ref", "-d", name); err != nil {
				return fmt.Errorf("gitbackup: delete ref %s: %w", name, err)
			}
		}
	}
	names := make([]string, 0, len(snapshot))
	for name := range snapshot {
		names = append(names, name)
	}
	sort.Strings(names)
	var branches []string
	for _, name := range names {
		if _, err := gitOutput(ctx, target, "update-ref", name, snapshot[name]); err != nil {
			return fmt.Errorf("gitbackup: set ref %s: %w", name, err)
		}
		if strings.HasPrefix(name, "refs/heads/") {
			branches = append(branches, name)
		}
	}
	if len(branches) > 0 {
		head, headErr := gitOutput(ctx, target, "symbolic-ref", "HEAD")
		headRef := strings.TrimSpace(head)
		if headErr != nil || snapshot[headRef] == "" {
			if _, err := gitOutput(ctx, target, "symbolic-ref", "HEAD", branches[0]); err != nil {
				return fmt.Errorf("gitbackup: set HEAD: %w", err)
			}
		}
	}
	return nil
}
