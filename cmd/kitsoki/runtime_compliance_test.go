package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/app"
	"kitsoki/internal/host"
)

func TestWireComplianceHostReplacesSentinelWithAppScopedProvider(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".kitsoki-root"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	appPath := filepath.Join(root, "stories", "review", "app.yaml")
	if err := os.MkdirAll(filepath.Dir(appPath), 0o700); err != nil {
		t.Fatal(err)
	}

	registry := host.NewRegistry()
	host.RegisterBuiltins(registry)
	wireComplianceHost(registry, runtimeConfig{
		AppPath: appPath,
		Def:     &app.AppDef{App: app.AppMeta{ID: "review-app"}},
	})

	_, err := registry.Invoke(context.Background(), "host.compliance.run", map[string]any{
		"catalog_path": "missing-catalog", "node_id": "control-1",
	})
	if err == nil || !strings.Contains(err.Error(), "resolve checks") {
		t.Fatalf("error = %v, want concrete provider resolution failure", err)
	}
	if strings.Contains(err.Error(), "provider is unavailable") {
		t.Fatalf("runtime retained compliance sentinel: %v", err)
	}
}
