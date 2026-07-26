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

func TestWireStoryDemoHostReplacesLegacyHandlerWithAppScopedProvider(t *testing.T) {
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
	wireStoryDemoHost(registry, runtimeConfig{
		AppPath: appPath,
		Def:     &app.AppDef{App: app.AppMeta{ID: "review-app"}},
	})

	_, err := registry.Invoke(context.Background(), "host.demo.plan", map[string]any{
		"node_id": "demo-1",
	})
	if err == nil || !strings.Contains(err.Error(), "authenticated actor") {
		t.Fatalf("actorless error = %v", err)
	}

	_, err = registry.Invoke(
		host.WithActor(context.Background(), "operator"),
		"host.demo.create",
		map[string]any{"scenario_path": "scenario.json", "out_path": "mockup.html"},
	)
	if err == nil || !strings.Contains(err.Error(), `unknown typed op "create"`) {
		t.Fatalf("legacy operation remained reachable: %v", err)
	}
}
