package host

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevOnboardingDiscoverUsesProjectMetadata(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(root, "projects", "example-service", "project.toml"), []byte("title = \"Example Service\"\n\ntest_command = \"go test ./...\"\nbuild = \"go build -o ./bin/example-service ./cmd/example-service\"\nstart = \"./bin/example-service --config configs/example-service.example.yml\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := DevOnboardingHandler(context.Background(), map[string]any{"op": "discover", "request": target, "workdir": root, "repo_root": root})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"project_id": "example-service", "project_title": "Example Service", "stack": "go project", "test_command": "go test ./...", "build_command": "go build -o ./bin/example-service ./cmd/example-service", "dev_command": "./bin/example-service --config configs/example-service.example.yml", "conventions": "project"} {
		if got := stringValue(result.Data, key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestDevOnboardingApplyWritesValidatedProfileAndInstance(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := DevOnboardingHandler(context.Background(), map[string]any{"op": "apply", "data": map[string]any{"target_path": root, "project_id": "example", "project_title": "Example", "stack": "go project", "test_command": "go test ./...", "build_command": "go build ./...", "repo_vcs": "none"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" || result.Data["status"] != "applied" {
		t.Fatalf("apply = %#v", result)
	}
	for _, path := range []string{".kitsoki.yaml", ".kitsoki/project-profile.yaml", ".kitsoki/stories/example-dev/app.yaml"} {
		if _, err := os.Stat(filepath.Join(root, path)); err != nil {
			t.Fatalf("missing %s: %v", path, err)
		}
	}
	profile, err := os.ReadFile(filepath.Join(root, ".kitsoki/project-profile.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(profile), "schema: project-profile/v1") {
		t.Fatalf("profile missing schema:\n%s", profile)
	}
}

func TestDevOnboardingReadinessRunsNativeChecksAndUpdatesProfile(t *testing.T) {
	root := t.TempDir()
	profileDir := filepath.Join(root, ".kitsoki")
	instance := filepath.Join(profileDir, "stories", "example-dev", "app.yaml")
	if err := os.MkdirAll(filepath.Dir(instance), 0o755); err != nil {
		t.Fatal(err)
	}
	profile := "schema: project-profile/v1\nid: example\ncommands:\n  test: true\n  build: ''\n"
	if err := os.WriteFile(filepath.Join(profileDir, "project-profile.yaml"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(instance, []byte("app:\n  id: example-dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := DevOnboardingHandler(context.Background(), map[string]any{
		"op":   "readiness",
		"data": map[string]any{"target_path": root, "project_id": "example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" || stringValue(result.Data, "status") != "pass" || result.Data["ok"] != true {
		t.Fatalf("readiness = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(root, ".artifacts", "kitsoki-readiness.json")); err != nil {
		t.Fatalf("missing readiness report: %v", err)
	}
	updated, err := os.ReadFile(filepath.Join(profileDir, "project-profile.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "status: pass") {
		t.Fatalf("profile readiness missing:\n%s", updated)
	}
}

func TestDevOnboardingCustomizationsAcceptsPendingEntries(t *testing.T) {
	root := t.TempDir()
	profileDir := filepath.Join(root, ".kitsoki")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatal(err)
	}
	profile := "schema: project-profile/v1\nid: example\nonboarding:\n  story_customizations:\n    - id: recipe-1\n      status: pending\n      summary: Use the project test command\n      evidence: analysis.json#recipe-1\n"
	if err := os.WriteFile(filepath.Join(profileDir, "project-profile.yaml"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := DevOnboardingHandler(context.Background(), map[string]any{
		"op":   "customizations",
		"data": map[string]any{"target_path": root, "action": "accept"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" || result.Data["updated"] != true {
		t.Fatalf("customizations = %#v", result)
	}
	updated, err := os.ReadFile(filepath.Join(profileDir, "project-profile.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "status: accepted") {
		t.Fatalf("accepted customization missing:\n%s", updated)
	}
}
