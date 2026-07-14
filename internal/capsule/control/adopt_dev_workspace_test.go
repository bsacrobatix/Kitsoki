package control

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdoptDevWorkspaceRecordsAndRefreshesVerifiedScriptClone(t *testing.T) {
	project, workspace, manager := testAdoptableDevWorkspace(t, "adoptable")
	ctx := context.Background()

	first, err := manager.AdoptDevWorkspace(ctx, "adoptable")
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := manager.Status(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if recorded.State != StateReady || recorded.Provider != string(SourceDevWorkspaceScript) || recorded.DefinitionDigest != "sha256:development" {
		t.Fatalf("recorded instance = %#v", recorded)
	}
	if !sameCleanPath(recorded.Path, workspace) || recorded.Head != controlGitOutput(t, workspace, "rev-parse", "HEAD") {
		t.Fatalf("recorded path/head = %#v", recorded)
	}

	if err := os.WriteFile(filepath.Join(workspace, "next.txt"), []byte("next\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runControlGit(t, workspace, "add", "next.txt")
	runControlGit(t, workspace, "commit", "-m", "next")
	second, err := manager.AdoptDevWorkspace(ctx, "adoptable")
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation <= first.Generation {
		t.Fatalf("generation did not advance: first=%#v second=%#v", first, second)
	}
	refreshed, err := manager.Status(ctx, second)
	if err != nil || refreshed.Head != controlGitOutput(t, workspace, "rev-parse", "HEAD") {
		t.Fatalf("refreshed instance = %#v, %v", refreshed, err)
	}
	if got := manager.Grant.ProjectRoot; got != project {
		t.Fatalf("project root changed: %q", got)
	}
}

func TestAdoptDevWorkspaceRejectsForeignManifest(t *testing.T) {
	_, workspace, manager := testAdoptableDevWorkspace(t, "foreign")
	manifestPath := filepath.Join(workspace, ".kitsoki-dev-workspace.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest devWorkspaceManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Source = t.TempDir()
	raw, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = manager.AdoptDevWorkspace(context.Background(), "foreign")
	if err == nil || !strings.Contains(err.Error(), "escapes its managed project") {
		t.Fatalf("foreign adoption error = %v", err)
	}
	if _, err := manager.Instances.Get(context.Background(), "foreign"); err == nil {
		t.Fatal("foreign workspace was recorded")
	}
}

func testAdoptableDevWorkspace(t *testing.T, id string) (string, string, *Manager) {
	t.Helper()
	project := t.TempDir()
	workspaceRoot := filepath.Join(project, ".capsules", "workspaces")
	workspace := filepath.Join(workspaceRoot, id)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	runControlGit(t, workspace, "init", "-b", "agent/"+id)
	runControlGit(t, workspace, "config", "user.name", "Capsule Test")
	runControlGit(t, workspace, "config", "user.email", "capsule@example.test")
	if err := os.WriteFile(filepath.Join(workspace, "README.md"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runControlGit(t, workspace, "add", "README.md")
	runControlGit(t, workspace, "commit", "-m", "base")
	for _, name := range []string{instanceSentinel, ".kitsoki-clone"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte("managed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manifest := devWorkspaceManifest{
		ID:        id,
		Source:    project,
		Root:      workspaceRoot,
		Branch:    "agent/" + id,
		Base:      "staging/local",
		Target:    "staging/local",
		Workspace: workspace,
		ManagedBy: "scripts/dev-workspace.sh",
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".kitsoki-dev-workspace.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{
		Definitions: defs{"development": {
			ID:     "development",
			Digest: "sha256:development",
			Source: Source{Kind: SourceDevWorkspaceScript, Development: DevelopmentSource{Base: "staging/local", Target: "staging/local", BranchPrefix: "agent/"}},
		}},
		Instances: NewMemoryInstanceStore(),
		Grant: ScopeGrant{
			ProjectRoot:    project,
			WorkspaceRoots: []string{workspaceRoot},
			Definitions:    []string{"development"},
			Executors:      []string{string(SourceDevWorkspaceScript)},
		},
	}
	return project, workspace, manager
}
