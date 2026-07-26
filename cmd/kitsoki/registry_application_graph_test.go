package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/host"
	"kitsoki/internal/webconfig"
)

func TestRegistryWiresApplicationGraphOnlyForExactApplication(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".kitsoki.yaml"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	storyDir := filepath.Join(root, "stories", "pog")
	if err := os.MkdirAll(storyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	appPath := filepath.Join(storyDir, "app.yaml")
	if err := os.WriteFile(appPath, []byte("app: {id: pog}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalogSource, err := os.ReadFile(filepath.Join("..", "..", "internal", "graph", "testdata", "good", "minimal.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "pog"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pog", "catalog.yaml"), catalogSource, 0o600); err != nil {
		t.Fatal(err)
	}

	registry := NewRegistry(webconfig.WebConfig{
		ApplicationGraphs: map[string]webconfig.ApplicationGraphConfig{
			"pog": {
				ProjectRoot: ".", Catalog: "pog/catalog.yaml",
				MaxNodes: 2, MaxBytes: 1 << 20, WritePolicy: host.ApplicationGraphWriteRead,
			},
		},
	}, nil, runtimeBase{})
	pogHosts := host.NewRegistry()
	host.RegisterBuiltins(pogHosts)
	if err := registry.wireApplicationGraph(
		&sessionRuntime{HostRegistry: pogHosts}, "pog", appPath,
	); err != nil {
		t.Fatal(err)
	}
	result, err := pogHosts.Invoke(context.Background(), "host.graph.snapshot", map[string]any{
		"audience": "public", "fields": []any{"title", "status", "visibility"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if nodes := result.Data["snapshot"].(map[string]any)["nodes"].([]any); len(nodes) != 2 {
		t.Fatalf("nodes = %d, want 2", len(nodes))
	}

	otherHosts := host.NewRegistry()
	host.RegisterBuiltins(otherHosts)
	if err := registry.wireApplicationGraph(
		&sessionRuntime{HostRegistry: otherHosts}, "other", appPath,
	); err != nil {
		t.Fatal(err)
	}
	_, err = otherHosts.Invoke(context.Background(), "host.graph.snapshot", map[string]any{
		"audience": "public", "fields": []any{"title"}, "max_nodes": 2,
	})
	if err == nil || !strings.Contains(err.Error(), "catalog_path") {
		t.Fatalf("unconfigured application error = %v", err)
	}
}

func TestResolveApplicationGraphBindingRejectsEscapesAndNonFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".kitsoki.yaml"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	storyDir := filepath.Join(root, "stories")
	if err := os.MkdirAll(storyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	appPath := filepath.Join(storyDir, "app.yaml")
	if err := os.WriteFile(appPath, []byte("app: {id: pog}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	outsideCatalog := filepath.Join(outside, "catalog.yaml")
	if err := os.WriteFile(outsideCatalog, []byte("schema: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}

	base := webconfig.ApplicationGraphConfig{
		ProjectRoot: ".", MaxNodes: 2, MaxBytes: 1 << 20,
		WritePolicy: host.ApplicationGraphWriteRead,
	}
	tests := []struct {
		name    string
		catalog string
		want    string
	}{
		{"absolute", outsideCatalog, "repository-relative"},
		{"traversal", "../catalog.yaml", "traverse"},
		{"symlink escape", "escape/catalog.yaml", "symlink"},
		{"non regular", "directory.yaml", "regular file"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configured := base
			configured.Catalog = test.catalog
			_, err := resolveApplicationGraphBinding("pog", appPath, configured)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}
