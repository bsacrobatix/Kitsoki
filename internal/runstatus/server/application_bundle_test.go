package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/applicationbuild"
)

func TestApplicationBundleHostServesBuiltManifestAndAssets(t *testing.T) {
	root := t.TempDir()
	manifest := applicationbuild.Manifest{
		ApplicationID: "demo", CreatedAt: time.Unix(2, 0),
		Theme: map[string]string{"accent": "#176b5b"}, ArtifactDir: "/private/build/path",
		Components: []applicationbuild.ComponentModule{{
			ID: "demo.card", Module: "/Users/operator/project/Card.vue", Export: "default",
		}},
	}
	manifest, dir := publishHostTestBundle(t, root, manifest, map[string]string{
		"assets/app.js": "globalThis.loaded=true",
		"index.html":    "<script src=\"./assets/app.js\"></script>",
	})

	srv := httptest.NewServer((&Server{applicationBundleRoot: root}).Handler())
	defer srv.Close()
	for _, asset := range []string{"index.html", "assets/app.js"} {
		response, getErr := http.Get(srv.URL + "/application/demo/" + asset)
		if getErr != nil {
			t.Fatal(getErr)
		}
		body := response.Body
		defer body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", asset, response.StatusCode)
		}
		wantCache := "public, max-age=31536000, immutable"
		if asset == "index.html" {
			wantCache = "no-cache"
		}
		if got := response.Header.Get("Cache-Control"); got != wantCache {
			t.Fatalf("%s Cache-Control = %q, want %q", asset, got, wantCache)
		}
	}
	deepLinkRequest, err := http.NewRequest(http.MethodGet, srv.URL+"/application/demo/changes/chg-42", nil)
	if err != nil {
		t.Fatal(err)
	}
	deepLinkRequest.Header.Set("Accept", "text/html")
	deepLink, err := http.DefaultClient.Do(deepLinkRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer deepLink.Body.Close()
	deepLinkBody, _ := io.ReadAll(deepLink.Body)
	if deepLink.StatusCode != http.StatusOK || !strings.Contains(string(deepLinkBody), "assets/app.js") {
		t.Fatalf("deep link status=%d body=%s", deepLink.StatusCode, deepLinkBody)
	}
	response, err := http.Get(srv.URL + "/application/demo/application-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var served applicationbuild.Manifest
	if err := json.NewDecoder(response.Body).Decode(&served); err != nil {
		t.Fatal(err)
	}
	servedRaw, _ := json.Marshal(served)
	if served.Digest != manifest.Digest ||
		served.Theme["accent"] != manifest.Theme["accent"] ||
		strings.Contains(string(servedRaw), "/private/") ||
		strings.Contains(string(servedRaw), "/Users/") {
		t.Fatalf("served manifest = %#v", served)
	}

	denied, err := http.Get(srv.URL + "/application/demo/private.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer denied.Body.Close()
	if denied.StatusCode != http.StatusNotFound {
		t.Fatalf("undeclared asset status = %d", denied.StatusCode)
	}

	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("globalThis.tampered=true"), 0o644); err != nil {
		t.Fatal(err)
	}
	tampered, err := http.Get(srv.URL + "/application/demo/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	defer tampered.Body.Close()
	if tampered.StatusCode != http.StatusNotFound {
		t.Fatalf("tampered immutable asset status = %d", tampered.StatusCode)
	}

	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("globalThis.loaded=true"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest.Theme["accent"] = "#ff0000"
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "application-manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	tampered, err = http.Get(srv.URL + "/application/demo/index.html")
	if err != nil {
		t.Fatal(err)
	}
	defer tampered.Body.Close()
	if tampered.StatusCode != http.StatusNotFound {
		t.Fatalf("identity-tampered bundle status = %d", tampered.StatusCode)
	}
}

func publishHostTestBundle(
	t *testing.T,
	root string,
	manifest applicationbuild.Manifest,
	assets map[string]string,
) (applicationbuild.Manifest, string) {
	t.Helper()
	appRoot := filepath.Join(root, manifest.ApplicationID)
	if err := os.MkdirAll(appRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	stage, err := os.MkdirTemp(appRoot, ".fixture-")
	if err != nil {
		t.Fatal(err)
	}
	files := make([]string, 0, len(assets))
	for name, contents := range assets {
		files = append(files, name)
		path := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(files)
	outputHash := sha256.New()
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join(stage, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(outputHash, name)
		_, _ = outputHash.Write([]byte{0})
		_, _ = outputHash.Write(raw)
		_, _ = outputHash.Write([]byte{0})
	}
	outputDigest := "sha256:" + hex.EncodeToString(outputHash.Sum(nil))
	manifest.Schema = applicationbuild.ManifestSchema
	manifest.Entry = "index.html"
	manifest.Files = files
	identity, err := json.Marshal(struct {
		Output        string
		Entry         string
		Components    []applicationbuild.ComponentModule
		Theme         map[string]string
		Native        map[string]applicationbuild.NativeSurface
		Compatibility applicationbuild.Compatibility
	}{
		Output: outputDigest, Entry: manifest.Entry, Components: manifest.Components,
		Theme: manifest.Theme, Native: manifest.Native, Compatibility: manifest.Compatibility,
	})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(identity)
	manifest.Digest = "sha256:" + hex.EncodeToString(sum[:])
	dir := filepath.Join(appRoot, strings.TrimPrefix(manifest.Digest, "sha256:"))
	if err := os.Rename(stage, dir); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "application-manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return manifest, dir
}
