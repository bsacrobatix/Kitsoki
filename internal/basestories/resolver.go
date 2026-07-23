package basestories

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"kitsoki/internal/app"
	"kitsoki/internal/kitrepo"
)

// DefaultResolver returns the app.ImportResolver every `@kitsoki/<name>`
// import falls back to once no on-disk kitsoki checkout is found relative to
// the importing manifest (internal/app.resolveImportSource's tier 2, the
// dev-checkout/dogfood path). It implements exactly the two tiers
// [app.ImportResolver] documents, in the same order the loader calls them:
//
//   - override=true: the $KITSOKI_REPO environment-variable override
//     (internal/kitrepo.EnvVar) — an operator's explicit checkout always
//     wins. A story missing under a SET override is a hard error (never a
//     silent fallback to the embedded copy); an UNSET override returns
//     ("", nil) so resolution falls through to on-disk discovery and then
//     this resolver's own override=false call.
//   - override=false: this package's embedded story library ([Materialize]).
//     A story missing there is a hard error.
//
// This is the process-global, side-effect-free subset of cmd/kitsoki's
// buildImportResolver (cmd/kitsoki/resolver.go): no kit-dev / staged-candidate
// overrides. Those are CLI-authoring conveniences backed by their own on-disk
// state (internal/kitdev, internal/kitstage) — the right shape for a human
// iterating on a kit locally, but wrong for a resolver that must reproduce
// the SAME resolution from process environment plus the compiled binary
// alone, independent of which command or machine calls it. cmd/kitsoki's
// buildImportResolver wraps THIS resolver with those CLI-only tiers layered
// on top; it does not duplicate this logic.
//
// Callers: internal/capsule/storydigest.Compute and
// internal/capsule/storylauncher.Launcher.Launch both thread this resolver
// into the app loader as their default, so a cross-repo `@kitsoki/<name>`
// import — e.g. `@kitsoki/bugfix` from a project that carries no on-disk
// kitsoki checkout — resolves, seals into the story-closure digest, and
// launches, instead of failing story-closure the way an out-of-project
// relative/`@kitsoki/` import does under the plain (resolver-less) app.Load.
// See docs/tech-debt (POG) "VENDORED bugfix closure" for the motivating gap
// and internal/capsule/storydigest's externalStoryRoots for how the digest
// stays pinned to the RESOLVED content once the manifest lives outside the
// project root.
func DefaultResolver() app.ImportResolver {
	return func(name, _ string, override bool) (string, error) {
		if override {
			repo := strings.TrimSpace(os.Getenv(kitrepo.EnvVar))
			if repo == "" {
				return "", nil // no override configured; fall through
			}
			candidate := filepath.Join(repo, "stories", name, "app.yaml")
			if _, err := os.Stat(candidate); err != nil {
				return "", fmt.Errorf("%s=%s: story %q not found (looked for %s): %w",
					kitrepo.EnvVar, repo, name, candidate, err)
			}
			return candidate, nil
		}

		root, err := Materialize(context.Background())
		if err != nil {
			return "", fmt.Errorf("resolve @kitsoki/%s from embedded library: %w", name, err)
		}
		candidate := filepath.Join(root, name, "app.yaml")
		if _, statErr := os.Stat(candidate); statErr != nil {
			return "", fmt.Errorf("@kitsoki/%s: not in the embedded story library (looked for %s): %w",
				name, candidate, statErr)
		}
		return candidate, nil
	}
}
