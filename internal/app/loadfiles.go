package app

// loadfiles.go reconstructs an AppDef from an in-memory file set — the story
// source embedded in a trace (see internal/store/story.go), or a freshly
// revised story materialised from a content-addressed revision digest. It
// materialises the files to a temp dir laid out exactly as they were on disk
// (paths are relative to a shared capture root, preserving `import:
// ../sibling` layouts) and loads it via LoadWithOptions. Because Load roots
// BaseDir at the entry manifest's directory, views/prompts/scripts resolve
// through the temp tree with no special-casing — the reconstructed machine
// behaves byte-for-byte as the live one did, even when the on-disk story has
// since changed or vanished.
//
// `${KITSOKI_APP_DIR}` resolution is scoped to this one load via
// LoadOptions.EnvLookup rather than os.Setenv: two revisions (of the same
// story, or different stories) may be reconstructed concurrently in the same
// process — e.g. one revision still serving live turns while an editor
// validates a candidate patch — and each must resolve its OWN temp tree, not
// whichever one last called os.Setenv. See AppDef.envLookup.

import (
	"fmt"
	"os"
	"path/filepath"
)

// LoadFromFiles materialises files (keyed by capture-root-relative, forward-
// slash paths → raw bytes) under a fresh temp directory and loads the story
// rooted at entry (also capture-root-relative). It returns the loaded AppDef
// and a cleanup func that removes the temp directory.
//
// The temp directory must outlive all use of the returned AppDef: Load reads
// view/prompt templates lazily via BaseDir, so the renderer touches the temp
// tree on every render. Call cleanup only once the AppDef (and any machine
// built from it) is done. cleanup is always non-nil and safe to call even when
// LoadFromFiles returns an error.
func LoadFromFiles(files map[string][]byte, entry string) (*AppDef, func(), error) {
	dir, err := os.MkdirTemp("", "kitsoki-story-*")
	if err != nil {
		return nil, func() {}, fmt.Errorf("app.LoadFromFiles: mkdir temp: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	for rel, b := range files {
		dest := filepath.Join(dir, filepath.FromSlash(rel))
		if mkErr := os.MkdirAll(filepath.Dir(dest), 0o755); mkErr != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("app.LoadFromFiles: mkdir %q: %w", dest, mkErr)
		}
		if wErr := os.WriteFile(dest, b, 0o644); wErr != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("app.LoadFromFiles: write %q: %w", dest, wErr)
		}
	}

	entryPath := filepath.Join(dir, filepath.FromSlash(entry))
	absEntryDir, absErr := filepath.Abs(filepath.Dir(entryPath))
	if absErr != nil {
		cleanup()
		return nil, func() {}, fmt.Errorf("app.LoadFromFiles: abs %q: %w", filepath.Dir(entryPath), absErr)
	}

	// Point `${KITSOKI_APP_DIR}` at the materialised tree via a per-load
	// EnvLookup override (see AppDef.envLookup / LoadOptions), NOT by
	// publishing a process-global env var. os.Setenv here would race any
	// other AppDef loading concurrently in this process — including another
	// LoadFromFiles call materialising a different revision of the SAME
	// story to a different temp dir. Every other env var still falls
	// through to the real process environment, matching the live
	// loadAppWithEnv path in cmd/kitsoki.
	def, err := LoadWithOptions(entryPath, LoadOptions{
		EnvLookup: func(name string) (string, bool) {
			if name == "KITSOKI_APP_DIR" {
				return absEntryDir, true
			}
			return os.LookupEnv(name)
		},
	})
	if err != nil {
		cleanup()
		return nil, func() {}, fmt.Errorf("app.LoadFromFiles: load %q: %w", entry, err)
	}
	return def, cleanup, nil
}
