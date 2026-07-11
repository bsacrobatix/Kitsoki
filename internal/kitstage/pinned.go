package kitstage

import (
	"fmt"
	"os"
	"path/filepath"

	"kitsoki/internal/app"
	"kitsoki/internal/kitgit"
	"kitsoki/internal/kitlock"
)

// PinnedResolver resolves the given kit names to fixed on-disk trees at the
// override tier, falling through to base for everything else. This is how
// `kit trial` pins BOTH legs of a cross-version replay: leg A pins the
// accepted lockfile trees (AcceptedTree), leg B the staged candidates
// (ResolveTree). Normal resolution of `@kitsoki/<name>` is live (a checkout,
// the embedded library) — for a trial that liveness is exactly wrong, since
// the source directory may already contain the NEXT version while the lock
// still pins the previous one.
func PinnedResolver(base app.ImportResolver, pins map[string]string) app.ImportResolver {
	return func(name, importerDir string, override bool) (string, error) {
		if override {
			if dir, ok := pins[name]; ok {
				candidate := filepath.Join(dir, "app.yaml")
				if _, err := os.Stat(candidate); err != nil {
					return "", fmt.Errorf("pinned kit %s: app.yaml not found (%s): %w", name, candidate, err)
				}
				return candidate, nil
			}
		}
		if base == nil {
			return "", nil
		}
		return base(name, importerDir, override)
	}
}

// AcceptedTree finds an accepted lockfile entry's pinned bytes in the
// content-addressed caches (commit cache for the git tier, tree cache for
// everything else). ok is false when the resolution predates the caches or
// was evicted — callers degrade (skip the pinned leg with a note) rather
// than fall back to live resolution, which could silently pin the wrong
// version.
func AcceptedTree(e *kitlock.Entry) (dir string, ok bool) {
	if e == nil {
		return "", false
	}
	if e.Commit != "" {
		res, cached, err := kitgit.CachedResult(e.Commit)
		if err != nil || !cached {
			return "", false
		}
		return res.Root, true
	}
	if e.TreeHash != "" {
		root, cached, err := kitgit.CachedTree(e.TreeHash)
		if err != nil || !cached {
			return "", false
		}
		return root, true
	}
	return "", false
}
