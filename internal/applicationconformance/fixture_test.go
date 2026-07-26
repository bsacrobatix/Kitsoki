package applicationconformance

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCoverageManifestRequiresEveryAdapterConsumer(t *testing.T) {
	fixture, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	required := map[string]bool{
		"jsonrpc": false, "web-go": false, "cli": false, "studio-mcp": false,
		"tui": false, "web-ts": false, "vscode-ts": false,
	}
	_, current, _, _ := runtime.Caller(0)
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	for _, coverage := range fixture.Coverage {
		if _, ok := required[coverage.Surface]; !ok {
			t.Fatalf("unknown coverage surface %q", coverage.Surface)
		}
		if required[coverage.Surface] {
			t.Fatalf("duplicate coverage surface %q", coverage.Surface)
		}
		raw, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(coverage.Consumer)))
		if err != nil {
			t.Fatalf("%s consumer %q: %v", coverage.Surface, coverage.Consumer, err)
		}
		if !strings.Contains(string(raw), coverage.Marker) ||
			!strings.Contains(string(raw), "application-conformance-v1.json") {
			t.Fatalf("%s consumer %q does not load the canonical fixture", coverage.Surface, coverage.Consumer)
		}
		required[coverage.Surface] = true
	}
	for surface, covered := range required {
		if !covered {
			t.Fatalf("required surface %q has no fixture consumer", surface)
		}
	}
}
