package storydigest

import (
	"os"
	"path/filepath"
	"testing"

	"kitsoki/internal/kitrepo"
)

// TestComputeGoldenClosureWrapperDigest pins the exact byte layout of the
// capsule-story-closure/v1 digest against the closure-wrapper fixture
// (testdata/closure-wrapper/app.yaml, the same cross-repo @kitsoki/bugfix
// import exercised by TestComputeSealsCrossRepoBugfixImport). Live Capsule CI
// receipts already carry digests produced by this exact loop (see
// storydigest.Compute's package doc and the 8 callers listed in
// internal/appclosure's package doc) — changing the byte layout here,
// silently or otherwise, invalidates every historical receipt that pinned a
// story closure. `want` was captured by running this test with -v BEFORE
// Compute's hashing loop was refactored to call internal/appclosure.Digest;
// it must keep passing, unchanged, after that refactor. If this test is ever
// rewritten to compute `want` from the refactored code instead of the
// pre-refactor golden value, it proves nothing about byte-identity.
func TestComputeGoldenClosureWrapperDigest(t *testing.T) {
	t.Setenv(kitrepo.EnvVar, "") // isolate from any ambient --kitsoki-repo override

	const want = "sha256:86b463808c1c02aa46860597ceebe120a13985fdfcf354f65a2510ee3ceece32"

	fixture, err := os.ReadFile(filepath.Join("testdata", "closure-wrapper", "app.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	write(t, filepath.Join(project, "app.yaml"), string(fixture))

	got, err := Compute(project, "app.yaml")
	if err != nil {
		t.Fatalf("Compute cross-repo @kitsoki/bugfix import: %v", err)
	}
	t.Logf("golden digest: %s", got.Digest)
	if got.Digest != want {
		t.Fatalf("capsule-story-closure/v1 byte layout changed:\n got:  %s\n want: %s\nThis either means the hashing loop's byte layout changed (breaking every historical receipt that pinned a story closure) or the closure-wrapper fixture / embedded @kitsoki/bugfix story content changed. If the layout changed intentionally, update this constant AND treat it as a breaking schema change for anything comparing digests across a deploy boundary.", got.Digest, want)
	}
}
