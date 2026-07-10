package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/host"
)

func TestOnboardingGeneratedDevStoryInstanceValidates(t *testing.T) {
	root := t.TempDir()
	result, err := host.DevOnboardingHandler(context.Background(), map[string]any{
		"op": "apply",
		"data": map[string]any{
			"target_path": root, "project_id": "platform-presentation", "project_title": "Platform Presentation",
			"stack": "go project", "build_command": "go build ./...", "test_command": "go test ./...", "repo_vcs": "none",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" || result.Data["status"] != "applied" {
		t.Fatalf("apply = %#v", result)
	}
	appPath := filepath.Join(root, ".kitsoki", "stories", "platform-presentation-dev", "app.yaml")
	out, err := execRoot(t, "validate", appPath)
	if err != nil {
		t.Fatalf("generated app should validate: %v\n%s", err, out)
	}
}

func TestOnboardingDiscoveryUsesMetaRepoProjectMetadata(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "services", "example-service")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "projects", "example-service"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "go.mod"), []byte("module example/service\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "projects", "example-service", "project.toml"), []byte(`
schema_version = 1
title = "Example Service"
description = "Example service workspace."

[[repos]]
submodule = "services/example-service"
role = "primary"
description = "Go service that composes API responses."
test_command = "go test ./..."

[repos.local_run]
build = "go build -o ./bin/example-service ./cmd/example-service"
start = "./bin/example-service --config configs/example-service.example.yml"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := host.DevOnboardingHandler(context.Background(), map[string]any{"op": "discover", "request": target, "workdir": root, "repo_root": root})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(result.Data)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{`"project_id":"example-service"`, `"project_title":"Example Service"`, `"stack":"go project"`, `"test_command":"go test ./..."`, `"build_command":"go build -o ./bin/example-service ./cmd/example-service"`, `"dev_command":"./bin/example-service --config configs/example-service.example.yml"`, `"conventions":"project"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("discovery missing %q:\n%s", want, text)
		}
	}
}
