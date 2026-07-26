package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path"
	"strings"

	"kitsoki/internal/applicationbuild"
)

const applicationBundlePrefix = "/application/"

// WithApplicationBundleRoot enables immutable production application assets.
// The root contains <application>/<digest>/application-manifest.json as emitted
// by `kitsoki app build`.
func WithApplicationBundleRoot(root string) Option {
	return func(c *serverConfig) { c.applicationBundleRoot = strings.TrimSpace(root) }
}

func (s *Server) handleApplicationBundle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.applicationBundleRoot == "" {
		http.NotFound(w, r)
		return
	}
	relative := strings.TrimPrefix(r.URL.Path, applicationBundlePrefix)
	parts := strings.SplitN(relative, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	bundle, err := applicationbuild.Latest(s.applicationBundleRoot, parts[0])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	asset := bundle.Manifest.Entry
	if len(parts) == 2 && parts[1] != "" {
		asset = path.Clean("/" + parts[1])[1:]
		if asset != parts[1] {
			http.NotFound(w, r)
			return
		}
	}
	file, err := bundle.Asset(asset)
	if err != nil {
		// Canonical application routes are presentation paths, not bundle
		// assets. A browser document navigation receives the immutable entry
		// shell; missing scripts, styles, and RPC-like requests remain 404s.
		if !strings.Contains(r.Header.Get("Accept"), "text/html") {
			http.NotFound(w, r)
			return
		}
		asset = bundle.Manifest.Entry
		file, err = bundle.Asset(asset)
		if err != nil {
			http.NotFound(w, r)
			return
		}
	}
	if asset == "application-manifest.json" {
		raw, readErr := os.ReadFile(file)
		if readErr != nil {
			http.NotFound(w, r)
			return
		}
		var manifest applicationbuild.Manifest
		if json.Unmarshal(raw, &manifest) != nil {
			http.NotFound(w, r)
			return
		}
		// Never leak the host's absolute artifact path.
		manifest.ArtifactDir = ""
		for i := range manifest.Components {
			manifest.Components[i].Module = ""
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-cache")
		_ = json.NewEncoder(w).Encode(manifest)
		return
	}
	if asset == bundle.Manifest.Entry {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeFile(w, r, file)
}
