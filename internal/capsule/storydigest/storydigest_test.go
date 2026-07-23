package storydigest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/kitrepo"
)

func TestComputeBindsRuntimeClosureAndIgnoresFixtures(t *testing.T) {
	root := t.TempDir()
	story := filepath.Join(root, ".kitsoki", "stories", "ci")
	write(t, filepath.Join(story, "app.yaml"), `app:
  id: closure
  version: 0.1.0
  title: Closure
  author: Test
  license: CC0
intents:
  run: { description: run, examples: [run], priority: 1 }
root: idle
states:
  idle:
    view: [{ prose: idle }]
    on:
      run: [{ target: done }]
  done:
    terminal: true
    view: [{ prose: done }]
`)
	write(t, filepath.Join(story, "rooms", "included.yaml"), "state: included\n")
	write(t, filepath.Join(story, "prompts", "review.md"), "review v1\n")
	write(t, filepath.Join(story, "views", "status.pongo"), "status v1\n")
	write(t, filepath.Join(story, "scripts", "verdict.star"), "def main(ctx): return {}\n")
	write(t, filepath.Join(story, "scripts", "verdict.star.yaml"), "schema: host-starlark/v1\n")
	write(t, filepath.Join(story, "schemas", "verdict.json"), "{}\n")
	write(t, filepath.Join(story, "flows", "fixture.yaml"), "fixture: v1\n")
	write(t, filepath.Join(root, ".kitsoki", "kits.lock"), "schema: kitsoki-lock/v1\n")

	first, err := Compute(root, filepath.Join(".kitsoki", "stories", "ci", "app.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		".kitsoki/stories/ci/app.yaml",
		".kitsoki/stories/ci/rooms/included.yaml",
		".kitsoki/stories/ci/prompts/review.md",
		".kitsoki/stories/ci/views/status.pongo",
		".kitsoki/stories/ci/scripts/verdict.star",
		".kitsoki/stories/ci/scripts/verdict.star.yaml",
		".kitsoki/stories/ci/schemas/verdict.json",
		".kitsoki/kits.lock",
	} {
		if !contains(first.Files, path) {
			t.Fatalf("closure missing %s: %#v", path, first.Files)
		}
	}
	if contains(first.Files, ".kitsoki/stories/ci/flows/fixture.yaml") {
		t.Fatalf("flow fixture entered runtime closure: %#v", first.Files)
	}

	write(t, filepath.Join(story, "prompts", "review.md"), "review v2\n")
	second, err := Compute(root, filepath.Join(".kitsoki", "stories", "ci", "app.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Digest == first.Digest {
		t.Fatal("prompt mutation did not change story closure digest")
	}

	write(t, filepath.Join(story, "flows", "fixture.yaml"), "fixture: v2\n")
	third, err := Compute(root, filepath.Join(".kitsoki", "stories", "ci", "app.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if third.Digest != second.Digest {
		t.Fatal("fixture-only mutation changed runtime closure digest")
	}
}

// TestComputeSealsCrossRepoBugfixImport is the acceptance case for the
// VENDORED bugfix closure tech debt (POG docs/tech-debt-pog-bugfix.md): a
// wrapper story OUTSIDE the kitsoki repo (a bare temp project with no
// on-disk kitsoki checkout above it, mimicking POG's actual layout) imports
// `@kitsoki/bugfix` directly instead of a vendored in-project copy, and
// Compute still seals a story-closure digest — resolving through the
// engine's embedded story library (internal/basestories), not the
// resolver-less app.Load that used to reject this as an out-of-project
// import. testdata/closure-wrapper/app.yaml mirrors POG's
// .kitsoki/stories/pog-bugfix/app.yaml import shape.
func TestComputeSealsCrossRepoBugfixImport(t *testing.T) {
	t.Setenv(kitrepo.EnvVar, "") // isolate from any ambient --kitsoki-repo override

	// Copied into a bare t.TempDir() rather than run in place: this package's
	// own testdata/ sits inside a real kitsoki checkout (this repo), so
	// running the wrapper from there would resolve `@kitsoki/bugfix` through
	// resolveImportSource's on-disk-checkout tier (findRepoRoot walking up
	// to this repo's own go.mod) instead of the embedded-library tier the
	// fixture exists to exercise — POG's actual wrapper has no such ancestor
	// checkout. A bare temp dir reproduces that real topology.
	fixture, err := os.ReadFile(filepath.Join("testdata", "closure-wrapper", "app.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	write(t, filepath.Join(project, "app.yaml"), string(fixture))

	first, err := Compute(project, "app.yaml")
	if err != nil {
		t.Fatalf("Compute cross-repo @kitsoki/bugfix import: %v", err)
	}
	if first.Digest == "" {
		t.Fatal("empty digest")
	}
	for _, want := range []string{
		"app.yaml", // the wrapper itself, project-relative (no @kitsoki/ prefix)
		"@kitsoki/bugfix/app.yaml",
		"@kitsoki/delivery-tail/app.yaml",
		"@kitsoki/conflict-resolve/app.yaml",
	} {
		if !contains(first.Files, want) {
			t.Fatalf("closure missing %s: %#v", want, first.Files)
		}
	}
	for _, unwanted := range first.Files {
		if strings.Contains(unwanted, "..") {
			t.Fatalf("closure key leaked a non-portable relative path: %q", unwanted)
		}
	}

	// Determinism: recomputing the identical import against the identical
	// embedded library must reproduce the identical digest and file set —
	// the whole point of pinning RESOLVED content rather than depending on
	// a machine-local cache path (see externalStoryRoots).
	second, err := Compute(project, "app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if second.Digest != first.Digest {
		t.Fatalf("cross-repo closure digest is not stable across recompute: %s != %s", first.Digest, second.Digest)
	}
	if len(second.Files) != len(first.Files) {
		t.Fatalf("cross-repo closure file count is not stable across recompute: %d != %d", len(second.Files), len(first.Files))
	}
}

// TestComputeDigestChangesWithResolvedKitsokiImportContent is the other half
// of the acceptance bar: the closure digest must pin the RESOLVED content of
// a `@kitsoki/<name>` import, not just the import name — mutating the
// resolved bytes must change the digest, or a stale/mismatched engine build
// could seal a receipt that verifies against different content on another
// worker (see storydigest's package doc and internal/basestories.DefaultResolver).
// Real @kitsoki/bugfix content is embedded at build time and can't be
// mutated from a test, so this uses the SAME resolver's other tier
// ($KITSOKI_REPO override) with a small synthetic "kitsoki checkout" this
// test fully controls.
func TestComputeDigestChangesWithResolvedKitsokiImportContent(t *testing.T) {
	repo := t.TempDir()
	writeImportedStory(t, repo, "v1")

	project := t.TempDir()
	write(t, filepath.Join(project, "app.yaml"), wrapperImportingKit)
	t.Setenv(kitrepo.EnvVar, repo)

	first, err := Compute(project, "app.yaml")
	if err != nil {
		t.Fatalf("Compute with $KITSOKI_REPO override: %v", err)
	}
	if !contains(first.Files, "@kitsoki/childkit/app.yaml") {
		t.Fatalf("closure missing overridden import: %#v", first.Files)
	}

	// Recompute unchanged: stable.
	repeat, err := Compute(project, "app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if repeat.Digest != first.Digest {
		t.Fatalf("digest not stable across recompute with unchanged content: %s != %s", first.Digest, repeat.Digest)
	}

	// Mutate the RESOLVED content (not the wrapper, not the import name) and
	// recompute: the digest must change.
	writeImportedStory(t, repo, "v2")
	second, err := Compute(project, "app.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if second.Digest == first.Digest {
		t.Fatal("digest did not change when the resolved @kitsoki/<name> import content changed")
	}
}

// TestComputeStillRejectsUnrecognizedProjectEscape guards the boundary the
// external-root allowance sits inside: a plain relative import that climbs
// above the project root WITHOUT going through a recognized `@kitsoki/<name>`
// tier is still a hard "escapes project" error, not silently admitted.
func TestComputeStillRejectsUnrecognizedProjectEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "escaped")
	write(t, filepath.Join(outside, "app.yaml"), `app:
  id: escaped
  version: 0.1.0
  title: Escaped
  author: Test
  license: CC0
intents: { run: { description: run, examples: [run], priority: 1 } }
root: idle
states:
  idle:
    view: [{ prose: idle }]
    on: { run: [{ target: idle }] }
`)
	project := filepath.Join(root, "project")
	write(t, filepath.Join(project, "app.yaml"), fmt.Sprintf(`app:
  id: wrapper
  version: 0.1.0
  title: Wrapper
  author: Test
  license: CC0
intents: { run: { description: run, examples: [run], priority: 1 } }
root: idle
states:
  idle:
    view: [{ prose: idle }]
imports:
  esc:
    source: %q
    entry: idle
`, outside))
	if _, err := Compute(project, "app.yaml"); err == nil || !strings.Contains(err.Error(), "escapes project") {
		t.Fatalf("expected an escapes-project error, got %v", err)
	}
}

const wrapperImportingKit = `app:
  id: wrapper
  version: 0.1.0
  title: Wrapper
  author: Test
  license: CC0
intents:
  run: { description: run, examples: [run], priority: 1 }
root: idle
states:
  idle:
    view: [{ prose: idle }]
    on:
      run:
        - target: k
imports:
  k:
    source: "@kitsoki/childkit"
    entry: idle
`

// writeImportedStory writes a minimal "kitsoki checkout" at repo whose
// stories/childkit/app.yaml content is a function of marker, so a test can
// prove the closure digest tracks the RESOLVED bytes.
func writeImportedStory(t *testing.T, repo, marker string) {
	t.Helper()
	write(t, filepath.Join(repo, "stories", "childkit", "app.yaml"), fmt.Sprintf(`app:
  id: childkit
  version: 0.1.0
  title: "Child Kit %s"
  author: Test
  license: CC0
intents: { run: { description: run, examples: [run], priority: 1 } }
root: idle
states:
  idle:
    view: [{ prose: %q }]
    on: { run: [{ target: idle }] }
`, marker, marker))
}

func TestComputeRejectsStorySymlink(t *testing.T) {
	root := t.TempDir()
	story := filepath.Join(root, "story")
	write(t, filepath.Join(story, "app.yaml"), `app:
  id: closure
  version: 0.1.0
  title: Closure
  author: Test
  license: CC0
intents: { run: { description: run, examples: [run], priority: 1 } }
root: idle
states:
  idle:
    view: [{ prose: idle }]
    on: { run: [{ target: idle }] }
`)
	outside := filepath.Join(t.TempDir(), "secret.md")
	write(t, outside, "secret\n")
	if err := os.Symlink(outside, filepath.Join(story, "prompt.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := Compute(root, filepath.Join("story", "app.yaml")); err == nil {
		t.Fatal("expected symlink dependency rejection")
	}
}

func write(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
