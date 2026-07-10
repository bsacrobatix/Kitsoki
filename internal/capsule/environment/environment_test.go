package environment

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveIsStableRedactsSecretsAndRefusesToolMismatch(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".kitsoki", "environments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.sum"), []byte("sum"), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := []byte("schema: capsule-environment/v1\nid: ci\nsource:\n  host_probe: true\ntoolchains:\n  go: '1.25'\nlockfiles: [go.sum]\nbootstrap:\n  command: bootstrap-workspace\nsecret_refs: [CI_TOKEN]\n")
	if err := os.WriteFile(filepath.Join(dir, "ci.yaml"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	r := Resolver{ProjectRoot: root, Probe: ToolProbeFunc(func(context.Context, string) (string, error) { return "go version go1.25.0", nil })}
	first, err := r.Resolve(context.Background(), "ci")
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Resolve(context.Background(), "ci")
	if err != nil || first.Digest != second.Digest {
		t.Fatalf("unstable lock %q %q %v", first.Digest, second.Digest, err)
	}
	encoded, _ := json.Marshal(first)
	if strings.Contains(string(encoded), "CI_TOKEN") {
		t.Fatal("secret ref leaked into lock")
	}
	r.Probe = ToolProbeFunc(func(context.Context, string) (string, error) { return "go version go1.24.0", nil })
	if _, err := r.Resolve(context.Background(), "ci"); err == nil {
		t.Fatal("mismatched host toolchain was accepted")
	}
}
