package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// adoptFixture builds a real git workspace behind a registered Capsule
// instance. It is the exact shape an agent lands in: a managed clone the agent
// then drives with plain git.
type adoptFixture struct {
	root      string
	workspace string
	manager   *Manager
	store     InstanceStore
	instance  Instance
}

func newAdoptFixture(t *testing.T, id string) *adoptFixture {
	t.Helper()
	root := t.TempDir()
	workspaceRoot := filepath.Join(root, ".capsules", "workspaces")
	workspace := filepath.Join(workspaceRoot, id)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	runControlGit(t, workspace, "init")
	runControlGit(t, workspace, "config", "user.name", "Capsule Test")
	runControlGit(t, workspace, "config", "user.email", "capsule@example.test")
	if err := os.WriteFile(filepath.Join(workspace, "tracked.txt"), []byte("baseline\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runControlGit(t, workspace, "add", "tracked.txt")
	runControlGit(t, workspace, "commit", "--signoff", "-m", "baseline")
	baseline := controlGitOutput(t, workspace, "rev-parse", "HEAD")

	store := NewMemoryInstanceStore()
	in, err := store.Create(context.Background(), Instance{
		ID: id, DefinitionID: "d", Provider: "synthetic", Path: workspace,
		Head: baseline, State: StateReady, Lease: Lease{Owner: "agent"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &adoptFixture{
		root: root, workspace: workspace, store: store, instance: in,
		manager: &Manager{
			Definitions: defs{"d": {ID: "d"}},
			Instances:   store,
			Grant: ScopeGrant{
				ProjectRoot:    root,
				WorkspaceRoots: []string{workspaceRoot},
				Effects:        []string{"vcs_commit"},
			},
		},
	}
}

func (f *adoptFixture) handle(t *testing.T) Handle {
	t.Helper()
	in, err := f.store.Get(context.Background(), f.instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	return Handle{ID: in.ID, Generation: in.Generation}
}

func (f *adoptFixture) registeredHead(t *testing.T) string {
	t.Helper()
	in, err := f.store.Get(context.Background(), f.instance.ID)
	if err != nil {
		t.Fatal(err)
	}
	return in.Head
}

// rawGitCommit is the natural agent behaviour this whole change exists to
// absorb: the agent never learns about the Capsule commit verb.
func (f *adoptFixture) rawGitCommit(t *testing.T, name, body string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.workspace, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	runControlGit(t, f.workspace, "add", name)
	runControlGit(t, f.workspace, "commit", "--signoff", "-m", "raw git: "+name)
	return controlGitOutput(t, f.workspace, "rev-parse", "HEAD")
}

// Requirement 1: work already committed with git is adopted, not refused.
func TestCommitVCSAdoptsWorkAlreadyCommittedWithRawGit(t *testing.T) {
	f := newAdoptFixture(t, "raw-git")
	baseline := f.registeredHead(t)
	f.rawGitCommit(t, "one.txt", "1\n")
	f.rawGitCommit(t, "two.txt", "2\n")
	gitHead := f.rawGitCommit(t, "three.txt", "3\n")

	result, err := f.manager.CommitVCSResult(context.Background(), f.handle(t), "capsule message")
	if err != nil {
		t.Fatalf("commit refused already-committed work: %v", err)
	}
	if !result.Adopted || result.Committed {
		t.Fatalf("expected adoption without a new commit, got %#v", result)
	}
	if result.Previous != baseline || result.Head != gitHead || result.Commits != 3 {
		t.Fatalf("adoption did not describe the fast-forward: %#v (baseline=%s head=%s)", result, baseline, gitHead)
	}
	if got := f.registeredHead(t); got != gitHead {
		t.Fatalf("registered head is %s, want %s", got, gitHead)
	}
	if !strings.Contains(result.Summary, "registered head advanced") || !strings.Contains(result.Summary, "3 commit(s) adopted") {
		t.Fatalf("summary does not report what was adopted: %q", result.Summary)
	}
	// The adopted head must not have been rewritten by the adoption itself.
	if live := controlGitOutput(t, f.workspace, "rev-parse", "HEAD"); live != gitHead {
		t.Fatalf("adoption moved git HEAD to %s", live)
	}
}

// Requirement 1: the remaining error state must name the state it found.
func TestCommitVCSNothingToDoNamesTheStateItFound(t *testing.T) {
	f := newAdoptFixture(t, "in-sync")
	_, err := f.manager.CommitVCSResult(context.Background(), f.handle(t), "capsule message")
	if err == nil {
		t.Fatal("expected a refusal for a fully reconciled workspace")
	}
	if !errors.Is(err, ErrNothingToCommit) {
		t.Fatalf("expected ErrNothingToCommit, got %v", err)
	}
	for _, want := range []string{"is in sync", "the tree is clean", "nothing to reconcile"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not contain %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "no staged changes") {
		t.Fatalf("the eliminated anti-pattern message came back: %v", err)
	}
}

// Requirement 5 / safety property: nothing uncommitted is ever adopted, so an
// adopted head always describes the complete source.
func TestAdoptHeadRefusesDirtyTree(t *testing.T) {
	f := newAdoptFixture(t, "dirty")
	f.rawGitCommit(t, "one.txt", "1\n")
	registered := f.registeredHead(t)
	if err := os.WriteFile(filepath.Join(f.workspace, "uncommitted.txt"), []byte("wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := f.manager.AdoptHead(context.Background(), f.handle(t))
	if err == nil {
		t.Fatal("expected adoption to refuse a dirty tree")
	}
	for _, want := range []string{"working tree has uncommitted or untracked changes", "would not describe the full source", "commit or discard"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not contain %q", err, want)
		}
	}
	if got := f.registeredHead(t); got != registered {
		t.Fatalf("registered head changed to %s despite refusal", got)
	}
}

// Requirement 2 / safety property: a rewrite is refused LOUDLY and named.
func TestAdoptHeadRefusesRewriteThatWouldDropRegisteredHistory(t *testing.T) {
	f := newAdoptFixture(t, "diverged")
	registered := f.rawGitCommit(t, "registered.txt", "registered\n")
	if _, err := f.manager.AdoptHead(context.Background(), f.handle(t)); err != nil {
		t.Fatal(err)
	}
	if f.registeredHead(t) != registered {
		t.Fatal("fixture failed to register the head under test")
	}
	// Rewrite history under Capsule's feet.
	runControlGit(t, f.workspace, "reset", "--hard", "HEAD~1")
	rewritten := f.rawGitCommit(t, "rewritten.txt", "rewritten\n")

	_, err := f.manager.AdoptHead(context.Background(), f.handle(t))
	if err == nil {
		t.Fatal("expected adoption to refuse a rewrite")
	}
	if !errors.Is(err, ErrHeadDiverged) {
		t.Fatalf("expected ErrHeadDiverged, got %v", err)
	}
	for _, want := range []string{"DIVERGED", registered[:12], rewritten[:12], "would be dropped", "git -C <workspace> log"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not name %q", err, want)
		}
	}
	if got := f.registeredHead(t); got != registered {
		t.Fatalf("registered head was laundered to %s", got)
	}
}

// A pure rollback below the registered head is the other rewrite shape.
func TestAdoptHeadRefusesHeadThatMovedBackwards(t *testing.T) {
	f := newAdoptFixture(t, "behind")
	registered := f.rawGitCommit(t, "one.txt", "1\n")
	if _, err := f.manager.AdoptHead(context.Background(), f.handle(t)); err != nil {
		t.Fatal(err)
	}
	runControlGit(t, f.workspace, "reset", "--hard", "HEAD~1")

	_, err := f.manager.AdoptHead(context.Background(), f.handle(t))
	if err == nil {
		t.Fatal("expected adoption to refuse a backwards head")
	}
	if !errors.Is(err, ErrHeadDiverged) {
		t.Fatalf("expected ErrHeadDiverged, got %v", err)
	}
	for _, want := range []string{"moved BACKWARDS", "1 registered commit(s) would be dropped"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not contain %q", err, want)
		}
	}
	if got := f.registeredHead(t); got != registered {
		t.Fatalf("registered head regressed to %s", got)
	}
}

// A rewrite must not be laundered by layering one more Capsule commit on top.
func TestCommitVCSRefusesRewriteBeforeTouchingTheIndex(t *testing.T) {
	f := newAdoptFixture(t, "rewrite-then-edit")
	registered := f.rawGitCommit(t, "one.txt", "1\n")
	if _, err := f.manager.AdoptHead(context.Background(), f.handle(t)); err != nil {
		t.Fatal(err)
	}
	runControlGit(t, f.workspace, "reset", "--hard", "HEAD~1")
	before := controlGitOutput(t, f.workspace, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(f.workspace, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := f.manager.CommitVCSResult(context.Background(), f.handle(t), "sneak past")
	if err == nil {
		t.Fatal("expected commit to refuse a rewritten head")
	}
	if !errors.Is(err, ErrHeadDiverged) {
		t.Fatalf("expected ErrHeadDiverged, got %v", err)
	}
	if after := controlGitOutput(t, f.workspace, "rev-parse", "HEAD"); after != before {
		t.Fatalf("a commit was created despite refusal: %s -> %s", before, after)
	}
	if got := f.registeredHead(t); got != registered {
		t.Fatalf("registered head changed to %s despite refusal", got)
	}
}

// Mixed case: raw git commits plus uncommitted work. Capsule commits the
// remainder and reports that it absorbed the earlier git commits too.
func TestCommitVCSCommitsRemainderAndReportsAdoptedGitCommits(t *testing.T) {
	f := newAdoptFixture(t, "mixed")
	baseline := f.registeredHead(t)
	f.rawGitCommit(t, "one.txt", "1\n")
	f.rawGitCommit(t, "two.txt", "2\n")
	if err := os.WriteFile(filepath.Join(f.workspace, "three.txt"), []byte("3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := f.manager.CommitVCSResult(context.Background(), f.handle(t), "capsule remainder")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Committed || result.Commits != 3 || result.Previous != baseline {
		t.Fatalf("unexpected commit result: %#v", result)
	}
	if !strings.Contains(result.Summary, "2 already existed from git") {
		t.Fatalf("summary hides the adopted git commits: %q", result.Summary)
	}
	if got := f.registeredHead(t); got != controlGitOutput(t, f.workspace, "rev-parse", "HEAD") {
		t.Fatalf("registered head %s is not git HEAD", got)
	}
}

// Requirement 3: diagnosis must be available without an effect grant and
// without mutating anything.
func TestInspectHeadDiagnosesDriftWithoutGrantOrMutation(t *testing.T) {
	f := newAdoptFixture(t, "diagnose")
	f.manager.Grant.Effects = nil
	baseline := f.registeredHead(t)
	gitHead := f.rawGitCommit(t, "one.txt", "1\n")

	drift, err := f.manager.InspectHead(context.Background(), f.handle(t))
	if err != nil {
		t.Fatal(err)
	}
	if drift.Relation != HeadAhead || drift.Ahead != 1 || drift.Behind != 0 || drift.Dirty {
		t.Fatalf("unexpected drift: %#v", drift)
	}
	if drift.RegisteredHead != baseline || drift.GitHead != gitHead {
		t.Fatalf("drift heads are wrong: %#v", drift)
	}
	if !strings.Contains(drift.Next(), "kitsoki capsule workspace reconcile --id diagnose") {
		t.Fatalf("next step is not actionable: %q", drift.Next())
	}
	if got := f.registeredHead(t); got != baseline {
		t.Fatalf("inspection mutated the registered head to %s", got)
	}
	// Without the grant, adoption itself must still be denied.
	if _, err := f.manager.AdoptHead(context.Background(), f.handle(t)); !errors.Is(err, ErrDenied) {
		t.Fatalf("expected ErrDenied without vcs_commit, got %v", err)
	}
}
