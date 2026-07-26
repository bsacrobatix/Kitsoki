package applicationbuild

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLatestServesOnlyVerifiedManifestDeclaredAssets(t *testing.T) {
	root := t.TempDir()
	oldManifest, oldDir := publishStoreTestBundle(t, root, Manifest{
		ApplicationID: "demo", CreatedAt: time.Unix(1, 0),
	}, map[string]string{
		"index.html": "<html>old</html>",
	})
	newManifest, newDir := publishStoreTestBundle(t, root, Manifest{
		ApplicationID: "demo", CreatedAt: time.Unix(2, 0),
		Theme: map[string]string{"accent": "#176b5b"},
	}, map[string]string{
		"assets/app.js": "export {}",
		"index.html":    "<html>new</html>",
	})
	_, brokenDir := publishStoreTestBundle(t, root, Manifest{
		ApplicationID: "demo", CreatedAt: time.Unix(3, 0),
	}, map[string]string{
		"assets/app.js": "newer",
		"index.html":    "<html>broken</html>",
	})
	if err := os.Remove(filepath.Join(brokenDir, "assets", "app.js")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(newDir, "private.txt"), []byte("no"), 0o644); err != nil {
		t.Fatal(err)
	}

	bundle, err := Latest(root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.Digest != newManifest.Digest {
		t.Fatalf("digest = %q, want %q", bundle.Manifest.Digest, newManifest.Digest)
	}
	if _, err := bundle.Asset("assets/app.js"); err != nil {
		t.Fatal(err)
	}
	for _, denied := range []string{"private.txt", "../outside", "assets/missing.js"} {
		if _, err := bundle.Asset(denied); err == nil {
			t.Fatalf("asset %q unexpectedly resolved", denied)
		}
	}

	// A declared asset mutation invalidates the newest bundle instead of
	// serving it under the original immutable digest.
	if err := os.WriteFile(filepath.Join(newDir, "assets", "app.js"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	bundle, err = Latest(root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.Digest != oldManifest.Digest {
		t.Fatalf("asset-tampered bundle selected: %#v", bundle.Manifest)
	}

	// Restoring the bytes but changing identity-bearing manifest metadata is
	// also rejected because the final bundle digest no longer matches.
	if err := os.WriteFile(filepath.Join(newDir, "assets", "app.js"), []byte("export {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	newManifest.Theme["accent"] = "#ff0000"
	if err := writeJSON(filepath.Join(newDir, "application-manifest.json"), newManifest); err != nil {
		t.Fatal(err)
	}
	bundle, err = Latest(root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.Digest != oldManifest.Digest {
		t.Fatalf("metadata-tampered bundle selected: %#v", bundle.Manifest)
	}

	if err := os.WriteFile(filepath.Join(oldDir, "index.html"), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Latest(root, "demo"); err == nil || !strings.Contains(err.Error(), "no valid build") {
		t.Fatalf("Latest() with only tampered bundles error = %v", err)
	}
}

func publishStoreTestBundle(
	t *testing.T,
	root string,
	manifest Manifest,
	assets map[string]string,
) (Manifest, string) {
	t.Helper()
	appRoot := filepath.Join(root, manifest.ApplicationID)
	if err := os.MkdirAll(appRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	stage, err := os.MkdirTemp(appRoot, ".fixture-")
	if err != nil {
		t.Fatal(err)
	}
	for name, contents := range assets {
		path := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	outputDigest, files, err := digestDirectory(stage)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Schema = ManifestSchema
	manifest.Entry = "index.html"
	manifest.Files = files
	manifest.Digest, err = bundleIdentityDigest(
		outputDigest,
		manifest.Entry,
		manifest.Components,
		manifest.Theme,
		manifest.Native,
		manifest.Compatibility,
	)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(appRoot, strings.TrimPrefix(manifest.Digest, "sha256:"))
	if err := os.Rename(stage, dir); err != nil {
		t.Fatal(err)
	}
	manifest.ArtifactDir = dir
	if err := writeJSON(filepath.Join(dir, "application-manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	return manifest, dir
}
