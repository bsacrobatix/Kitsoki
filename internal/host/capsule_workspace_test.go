package host_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"kitsoki/internal/host"
)

func TestCapsuleWorkspace_CreateGetClose(t *testing.T) {
	project := t.TempDir()
	spec := filepath.Join(project, ".kitsoki", "capsules", "clean.yaml")
	if err := os.MkdirAll(filepath.Dir(spec), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec, []byte("schema: capsule-definition/v1\nid: clean\nsource:\n  kind: synthetic\n  synthetic_spec: capsules/clean/capsule.yaml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	legacySpec := filepath.Join(project, "capsules", "clean", "capsule.yaml")
	if err := os.MkdirAll(filepath.Dir(legacySpec), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacySpec, []byte("name: clean\nsource:\n  synthetic: true\n  steps:\n    - action: write\n      path: initial.txt\n      content: initial\n    - action: commit\n      message: init\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	created, err := host.CapsuleWorkspaceHandler(ctx, map[string]any{
		"op":         "create",
		"repo":       project,
		"id":         "run",
		"definition": "clean",
		"owner":      "agent",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if created.Error != "" {
		t.Fatalf("domain: %s", created.Error)
	}
	if created.Data["id"] != "run" {
		t.Fatalf("id: %v", created.Data["id"])
	}
	path, _ := created.Data["path"].(string)
	wantRoot := filepath.Join(project, ".capsules", "workspaces")
	if realRoot, err := filepath.EvalSymlinks(wantRoot); err == nil {
		wantRoot = realRoot
	}
	if !filepath.IsAbs(path) || filepath.Dir(path) != wantRoot {
		t.Fatalf("path %q not under %q", path, wantRoot)
	}
	if _, err := os.Stat(filepath.Join(path, "initial.txt")); err != nil {
		t.Fatalf("workspace materialization: %v", err)
	}

	got, err := host.CapsuleWorkspaceHandler(ctx, map[string]any{
		"op":    "get",
		"repo":  project,
		"id":    "run",
		"owner": "agent",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if got.Error != "" {
		t.Fatalf("domain: %s", got.Error)
	}
	if got.Data["path"] != path {
		t.Fatalf("get path: %v", got.Data["path"])
	}
	if fmt.Sprint(got.Data["state"]) != "ready" {
		t.Fatalf("state: %v", got.Data["state"])
	}

	closed, err := host.CapsuleWorkspaceHandler(ctx, map[string]any{
		"op":    "close",
		"repo":  project,
		"id":    "run",
		"owner": "agent",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if closed.Error != "" {
		t.Fatalf("domain: %s", closed.Error)
	}
	if closed.Data["ok"] != true {
		t.Fatalf("close: %v", closed.Data)
	}
}
