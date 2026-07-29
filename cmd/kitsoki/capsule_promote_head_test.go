package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/capsule/control"
)

func promoteHeadGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// promoteHeadFixture is a registered managed workspace backed by a real git
// checkout, which is where promotion admission actually reads its candidate.
type promoteHeadFixture struct {
	workspace string
	manager   *control.Manager
	store     control.InstanceStore
	id        string
}

func newPromoteHeadFixture(t *testing.T, id string) *promoteHeadFixture {
	t.Helper()
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, ".capsules", "workspaces")
	workspace := filepath.Join(workspaceRoot, id)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	promoteHeadGit(t, workspace, "init")
	promoteHeadGit(t, workspace, "config", "user.name", "Capsule Test")
	promoteHeadGit(t, workspace, "config", "user.email", "capsule@example.test")
	if err := os.WriteFile(filepath.Join(workspace, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	promoteHeadGit(t, workspace, "add", "base.txt")
	promoteHeadGit(t, workspace, "commit", "--signoff", "-m", "base")
	baseline := promoteHeadGit(t, workspace, "rev-parse", "HEAD")

	store := control.NewMemoryInstanceStore()
	if _, err := store.Create(context.Background(), control.Instance{
		ID: id, DefinitionID: "development", Provider: "dev-workspace-script", Path: workspace,
		Head: baseline, State: control.StateReady, Lease: control.Lease{Owner: "agent"},
	}); err != nil {
		t.Fatal(err)
	}
	return &promoteHeadFixture{
		workspace: workspace, store: store, id: id,
		manager: &control.Manager{
			Definitions: capsuleWorkspaceTestDefinitions{"development": {ID: "development"}},
			Instances:   store,
			Grant: control.ScopeGrant{
				ProjectRoot:    root,
				WorkspaceRoots: []string{workspaceRoot},
				Effects:        []string{"vcs_commit"},
			},
		},
	}
}

func (f *promoteHeadFixture) handle(t *testing.T) control.Handle {
	t.Helper()
	in, err := f.store.Get(context.Background(), f.id)
	if err != nil {
		t.Fatal(err)
	}
	return control.Handle{ID: in.ID, Generation: in.Generation}
}

func (f *promoteHeadFixture) registeredHead(t *testing.T) string {
	t.Helper()
	in, err := f.store.Get(context.Background(), f.id)
	if err != nil {
		t.Fatal(err)
	}
	return in.Head
}

func (f *promoteHeadFixture) rawGitCommit(t *testing.T, name string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.workspace, name), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	promoteHeadGit(t, f.workspace, "add", name)
	promoteHeadGit(t, f.workspace, "commit", "--signoff", "-m", "raw git: "+name)
	return promoteHeadGit(t, f.workspace, "rev-parse", "HEAD")
}

// Requirement 2: raw-git-committed work is admitted for promotion.
func TestCapsulePromoteAdoptsHeadAdvancedByRawGit(t *testing.T) {
	f := newPromoteHeadFixture(t, "raw-git-promote")
	baseline := f.registeredHead(t)
	f.rawGitCommit(t, "one.txt")
	gitHead := f.rawGitCommit(t, "two.txt")

	adoption, err := capsulePromoteReconcileHead(context.Background(), f.manager, f.handle(t))
	if err != nil {
		t.Fatalf("promote refused raw-git work: %v", err)
	}
	if !adoption.Adopted || adoption.Previous != baseline || adoption.Head != gitHead || adoption.Commits != 2 {
		t.Fatalf("unexpected adoption: %#v", adoption)
	}
	if got := f.registeredHead(t); got != gitHead {
		t.Fatalf("registered head is %s, want %s", got, gitHead)
	}
	// The registered head now equals live git HEAD with a clean tree, which is
	// exactly the invariant readiness demands (see
	// TestDoctorWorkspaceHeadDriftRefusalNamesTheReconcileCommand).
	drift, err := f.manager.InspectHead(context.Background(), f.handle(t))
	if err != nil {
		t.Fatal(err)
	}
	if drift.Relation != control.HeadInSync || drift.Dirty {
		t.Fatalf("adopted workspace is not readiness-clean: %#v", drift)
	}
}

// Requirement 2 / safety property: a dirty tree is NOT adopted and readiness
// still refuses it, so partially committed work can never promote.
func TestCapsulePromoteStillRefusesDirtyWorkspace(t *testing.T) {
	f := newPromoteHeadFixture(t, "dirty-promote")
	f.rawGitCommit(t, "one.txt")
	registered := f.registeredHead(t)
	if err := os.WriteFile(filepath.Join(f.workspace, "wip.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	adoption, err := capsulePromoteReconcileHead(context.Background(), f.manager, f.handle(t))
	if err != nil {
		t.Fatal(err)
	}
	if adoption.Adopted {
		t.Fatal("a dirty workspace head was adopted")
	}
	if got := f.registeredHead(t); got != registered {
		t.Fatalf("registered head changed to %s for a dirty workspace", got)
	}
	// The workspace stays dirty, which readiness still refuses; see
	// TestDoctorWorkspaceDirtyRefusalOffersBothCommitPaths.
	drift, err := f.manager.InspectHead(context.Background(), f.handle(t))
	if err != nil {
		t.Fatal(err)
	}
	if !drift.Dirty {
		t.Fatal("fixture lost its uncommitted change")
	}
}

// Requirement 2 / safety property: a rewrite fails LOUDLY and names it.
func TestCapsulePromoteRefusesRewrittenHeadLoudly(t *testing.T) {
	f := newPromoteHeadFixture(t, "rewrite-promote")
	registered := f.rawGitCommit(t, "one.txt")
	if _, err := f.manager.AdoptHead(context.Background(), f.handle(t)); err != nil {
		t.Fatal(err)
	}
	promoteHeadGit(t, f.workspace, "reset", "--hard", "HEAD~1")
	rewritten := f.rawGitCommit(t, "rewritten.txt")

	_, err := capsulePromoteReconcileHead(context.Background(), f.manager, f.handle(t))
	if err == nil {
		t.Fatal("promotion accepted a rewritten head")
	}
	for _, want := range []string{"capsule promote: refusing to promote", "DIVERGED", registered[:12], rewritten[:12], "would be dropped"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not name %q", err, want)
		}
	}
	if got := f.registeredHead(t); got != registered {
		t.Fatalf("registered head was laundered to %s", got)
	}
}
