// Package storydigest computes a stable runtime-closure digest for a Kitsoki
// story. A Capsule CI receipt must bind more than app.yaml: included rooms,
// imported stories, prompts, Starlark, schemas, views, and the project kit lock
// can all change behavior without changing the entry manifest.
//
// # Cross-repo `@kitsoki/<name>` imports
//
// Compute resolves `@kitsoki/<name>` imports the same way the CLI does
// (basestories.DefaultResolver: the $KITSOKI_REPO override, else the engine's
// embedded story library) instead of the resolver-less app.Load a bare
// import-closure walk would otherwise use. A resolved manifest legitimately
// lives outside the project root — the whole point of the embedded-library
// tier is that the importing project carries no kitsoki checkout at all — so
// the project-escape guard below admits ONLY manifests reached through one of
// externalStoryRoots' recognized tiers; anything else stays a hard
// "escapes project" error. See externalStoryRoots for how those files are
// keyed into the digest so it stays a deterministic pin on the RESOLVED
// content (reproducible across processes/machines resolving the identical
// bytes, and different whenever that content differs) rather than depending
// on any machine-local cache path. This is what lets a wrapper project (e.g.
// POG) import `@kitsoki/bugfix` directly and pass `capsule ci run`'s
// story-closure seal without vendoring a byte-copy of the story in-project.
package storydigest

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"kitsoki/internal/app"
	"kitsoki/internal/appclosure"
	"kitsoki/internal/basestories"
	"kitsoki/internal/kitrepo"
)

const Schema = "capsule-story-closure/v1"

// Result is the content-addressed story closure. Files are project-relative
// forward-slash paths and are useful in doctor/receipt diagnostics.
type Result struct {
	Schema string   `json:"schema"`
	Digest string   `json:"digest"`
	Files  []string `json:"files"`
}

var ignoredDirs = map[string]bool{
	".artifacts":   true,
	".capsules":    true,
	".git":         true,
	".temp":        true,
	"baked":        true,
	"cassettes":    true,
	"flows":        true,
	"node_modules": true,
	"scenarios":    true,
	"testdata":     true,
}

var runtimeExtensions = map[string]bool{
	".json":   true,
	".md":     true,
	".pongo":  true,
	".pongo2": true,
	".star":   true,
	".tmpl":   true,
	".txt":    true,
	".yaml":   true,
	".yml":    true,
}

// Compute loads the story through the real app loader, then hashes the
// behavior-bearing files below every loaded manifest root. It deliberately
// uses a conservative superset: changing a runtime-adjacent file may invalidate
// a receipt, but changing a flow/cassette/test fixture does not.
func Compute(projectRoot, storyPath string) (Result, error) {
	project, err := canonicalDir(projectRoot)
	if err != nil {
		return Result{}, err
	}
	story, err := filepath.Abs(storyPath)
	if err != nil {
		return Result{}, err
	}
	if !filepath.IsAbs(storyPath) {
		story = filepath.Join(project, storyPath)
	}
	story = filepath.Clean(story)
	if !within(project, story) {
		return Result{}, fmt.Errorf("capsule story digest: story escapes project: %s", storyPath)
	}
	def, err := app.LoadWithResolver(story, nil, basestories.DefaultResolver())
	if err != nil {
		return Result{}, fmt.Errorf("capsule story digest: load story: %w", err)
	}
	manifests := append([]string(nil), def.LoadedManifests...)
	if len(manifests) == 0 {
		manifests = []string{story}
	}
	// extRoots is resolved lazily — most stories import nothing outside the
	// project, and materializing the embedded story library (a one-time
	// on-disk extraction; see internal/basestories) is real I/O this common
	// case shouldn't pay for.
	var extRoots map[string]string
	files := map[string]string{}
	for _, manifest := range manifests {
		manifest, err = filepath.Abs(manifest)
		if err != nil {
			return Result{}, err
		}
		if within(project, manifest) {
			if err := collectRoot(project, filepath.Dir(manifest), "", files); err != nil {
				return Result{}, err
			}
			continue
		}
		if extRoots == nil {
			extRoots, err = externalStoryRoots()
			if err != nil {
				return Result{}, fmt.Errorf("capsule story digest: resolve external story roots: %w", err)
			}
		}
		root, prefix, ok := matchExternalRoot(extRoots, manifest)
		if !ok {
			return Result{}, fmt.Errorf("capsule story digest: imported manifest escapes project: %s", manifest)
		}
		if err := collectRoot(root, filepath.Dir(manifest), prefix, files); err != nil {
			return Result{}, err
		}
	}
	for _, rel := range []string{filepath.Join(".kitsoki", "kits.lock"), "kits.lock", "kit.yaml"} {
		path := filepath.Join(project, rel)
		if info, statErr := os.Lstat(path); statErr == nil && info.Mode().IsRegular() {
			files[filepath.ToSlash(rel)] = path
		}
	}
	logical := make([]string, 0, len(files))
	for path := range files {
		logical = append(logical, path)
	}
	sort.Strings(logical)
	// The actual hashing byte layout lives in internal/appclosure.Digest, not
	// here — see that package's doc for why. This loop only resolves each
	// logical path down to (mode, bytes), preserving the exact stat/read
	// error messages this digest has always returned.
	closure := make(map[string]appclosure.File, len(logical))
	for _, rel := range logical {
		path := files[rel]
		info, err := os.Lstat(path)
		if err != nil {
			return Result{}, fmt.Errorf("capsule story digest: stat %s: %w", rel, err)
		}
		if !info.Mode().IsRegular() {
			return Result{}, fmt.Errorf("capsule story digest: dependency is not a regular file: %s", rel)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return Result{}, fmt.Errorf("capsule story digest: read %s: %w", rel, err)
		}
		closure[rel] = appclosure.File{Mode: info.Mode().Perm(), Bytes: raw}
	}
	return Result{Schema: Schema, Digest: appclosure.Digest(Schema, closure), Files: logical}, nil
}

// collectRoot walks dir (a loaded manifest's own directory) collecting every
// runtime-bearing file into files, keyed by prefix + the file's path relative
// to base. base doubles as the escape-check confinement boundary: a walked
// file that isn't under base (a manifest relatively importing above its own
// tree) is a hard error rather than a silent tree escape.
//
// For an in-project manifest, base is the project root and prefix is ""
// (files key by plain project-relative path, unchanged from before
// external-root support). For a manifest resolved through the
// `@kitsoki/<name>` embedded-library / $KITSOKI_REPO tiers, base is that
// tier's own root directory (see externalStoryRoots) and prefix is
// "@kitsoki/" — so the SAME `@kitsoki/<name>` content keys identically
// whichever tier resolved it (or on whichever machine, with whatever
// machine-local cache path, resolved it): only the hashed bytes at that key
// decide whether a closure digest matches.
func collectRoot(base, dir, prefix string, files map[string]string) error {
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == dir {
			return nil
		}
		if entry.IsDir() {
			if ignoredDirs[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("capsule story digest: symlink dependency is not allowed: %s", path)
		}
		if !info.Mode().IsRegular() || !runtimeExtensions[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		if strings.EqualFold(entry.Name(), "README.md") {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("capsule story digest: dependency escapes project: %s", path)
		}
		files[prefix+filepath.ToSlash(rel)] = path
		return nil
	})
}

// externalStoryRoots returns the base directories a `@kitsoki/<name>` import
// may legitimately resolve OUTSIDE the project, mapped to the logical
// namespace prefix given to files found under them: the engine's embedded
// story library (materialized on demand — see internal/basestories) and,
// when set, the $KITSOKI_REPO override's stories/ directory
// (internal/kitrepo). Both tiers share the "@kitsoki/" prefix (see
// collectRoot) so identical `@kitsoki/<name>` content hashes identically
// regardless of which tier — or which machine's cache path — produced it.
// Any out-of-project manifest that matches neither root is a real escape and
// stays a hard error in Compute.
func externalStoryRoots() (map[string]string, error) {
	roots := map[string]string{}
	if libRoot, err := basestories.Materialize(context.Background()); err != nil {
		if !errors.Is(err, basestories.ErrNotStaged) {
			return nil, fmt.Errorf("materialize embedded story library: %w", err)
		}
	} else {
		roots[libRoot] = "@kitsoki/"
	}
	if repo := strings.TrimSpace(os.Getenv(kitrepo.EnvVar)); repo != "" {
		roots[filepath.Join(repo, "stories")] = "@kitsoki/"
	}
	return roots, nil
}

// matchExternalRoot returns the first root in roots that contains manifest,
// plus its logical prefix. ok is false when manifest matches no recognized
// external root — the caller treats that as a real project-escape error.
func matchExternalRoot(roots map[string]string, manifest string) (root, prefix string, ok bool) {
	for r, p := range roots {
		if within(r, manifest) {
			return r, p, true
		}
	}
	return "", "", false
}

func canonicalDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("capsule story digest: project root is not a directory: %s", abs)
	}
	return filepath.Clean(abs), nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
