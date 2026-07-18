package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/capsule"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/control"
)

func TestCapsuleCIDoctorDefaultsToManagedWorkspaceSourceProject(t *testing.T) {
	project := t.TempDir()
	workspace := filepath.Join(project, ".capsules", "workspaces", "w")
	writeCapsuleCIDevelopmentDefinition(t, project)
	writeCapsuleCIDevelopmentDefinition(t, workspace)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, capsule.SentinelFile), []byte("dev-workspace\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(capsule.Manifest{
		CapsuleName: "dev-workspace",
		Workspace:   workspace,
		Source:      capsule.ManifestSource{Repo: project, Head: "sha256:source", Branch: "agent/w"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, capsule.ManifestFile), append(manifest, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	manager, err := capsuleWorkspaceManager(project)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := manager.Definition(context.Background(), "development")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Instances.Create(context.Background(), control.Instance{
		ID:               "w",
		DefinitionID:     definition.ID,
		DefinitionDigest: definition.Digest,
		Provider:         string(control.SourceDevWorkspaceScript),
		Path:             workspace,
		Head:             "sha256:source",
		Branch:           "agent/w",
		State:            control.StateReady,
		Generation:       1,
		Lease:            control.Lease{Owner: "test"},
	}); err != nil {
		t.Fatal(err)
	}

	t.Chdir(workspace)
	var out bytes.Buffer
	cmd := capsuleCIDoctorCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"change", "--workspace", "w", "--json=false"})
	err = cmd.Execute()
	if !errors.Is(err, ci.ErrDoctorNotReady) {
		t.Fatalf("doctor error = %v, want typed not-ready report; output:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "ready: false") || !strings.Contains(out.String(), "workspace: w") {
		t.Fatalf("doctor did not inspect the source project's registered workspace:\n%s", out.String())
	}
}

func TestCapsuleCIProjectRootKeepsExplicitPath(t *testing.T) {
	project := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, capsule.SentinelFile), []byte("fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(capsule.Manifest{Workspace: workspace, Source: capsule.ManifestSource{Repo: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, capsule.ManifestFile), append(manifest, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Chdir(workspace)
	got, err := capsuleCIProjectRoot(project)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(project)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("explicit project root = %q, want %q", got, want)
	}
}

func writeCapsuleCIDevelopmentDefinition(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, ".kitsoki", "capsules", "development.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	const definition = `schema: capsule-definition/v1
id: development
source:
  kind: dev-workspace-script
  development:
    base: staging/local
    target: staging/local
    branch_prefix: agent/
policy:
  network: none
`
	if err := os.WriteFile(path, []byte(definition), 0o644); err != nil {
		t.Fatal(err)
	}
}
