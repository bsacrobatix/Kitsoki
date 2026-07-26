package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
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

func TestCapsuleCISourceInputsUseExactHeadWithoutWorkspace(t *testing.T) {
	project := t.TempDir()
	writeCapsuleCIDevelopmentDefinition(t, project)
	files := map[string]string{
		".kitsoki/ci.yaml": `schema: capsule-ci/v1
default_environment: ci
pipelines:
  change:
    story: .kitsoki/stories/ci/app.yaml
    triggers: [local]
    result:
      schema: capsule-ci-verdict/v1
`,
		".kitsoki/environments/ci.yaml": "schema: capsule-environment/v1\nid: ci\nnetwork: none\nsandbox: supervised\n",
		".kitsoki/stories/ci/app.yaml": `app:
  id: ci
  version: 0.1.0
  title: CI
  author: Test
  license: CC0
world: {}
intents: {}
root: idle
states:
  idle:
    view:
      - prose: ok
`,
	}
	for name, body := range files {
		path := filepath.Join(project, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.name", "test"},
		{"config", "user.email", "test@example.invalid"},
		{"add", "."},
		{"commit", "-qm", "fixture"},
	} {
		command := exec.Command("git", args...)
		command.Dir = project
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	head, err := gitTrim(context.Background(), project, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	inputs := map[string]any{"report_id": "fb-1"}
	_, in, _, envelope, root, err := ciSourceInputs(
		context.Background(), project, project, head, "dispatch-fb-1",
		"development", "change", ci.Trigger{Kind: "local"}, inputs,
	)
	if err != nil {
		t.Fatal(err)
	}
	if in.ID != "dispatch-fb-1" || in.Generation != 1 || in.Path != "" || in.Head != head {
		t.Fatalf("synthetic source instance = %#v", in)
	}
	if root != project || envelope.SourceDigest != head || envelope.JobInputs["report_id"] != "fb-1" {
		t.Fatalf("source envelope = %#v, root = %q", envelope, root)
	}
	if _, err := os.Stat(filepath.Join(project, ".capsules", "workspaces", "dispatch-fb-1")); !os.IsNotExist(err) {
		t.Fatalf("source mode materialized a workspace: %v", err)
	}

	if err := os.WriteFile(filepath.Join(project, ".kitsoki", "ci.yaml"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, _, err := ciSourceInputs(
		context.Background(), project, project, head, "dispatch-fb-2",
		"development", "change", ci.Trigger{Kind: "local"}, inputs,
	); err == nil || !strings.Contains(err.Error(), "tracked changes") {
		t.Fatalf("expected dirty immutable source rejection, got %v", err)
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
