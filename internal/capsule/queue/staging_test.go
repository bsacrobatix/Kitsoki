package queue

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"kitsoki/internal/capsule/headroom"
	capsuleproject "kitsoki/internal/capsule/project"
)

func TestStagingIntegrationProcessesReceiptBoundCandidateThroughProtectedStaging(t *testing.T) {
	root := protectedQueueRepo(t)
	base := git(t, root, "rev-parse", "HEAD")
	commit(t, root, "candidate.txt", "candidate\n", "candidate")
	sha := git(t, root, "rev-parse", "HEAD")
	git(t, root, "branch", "agent/candidate", sha)
	git(t, root, "reset", "--hard", base)

	store := Store{ProjectRoot: root}
	if _, err := store.Submit(Submit{Branch: "agent/candidate", SHA: sha, Receipt: testReceipt(t, sha)}); err != nil {
		t.Fatal(err)
	}
	gate := "git diff --check"
	state, err := store.Process(context.Background(), ProcessDeps{
		Integration: StagingIntegration{ProjectRoot: root, GateCommand: gate},
		Gate:        ShellGate{Command: gate},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Candidates) != 1 || state.Candidates[0].Status != Landed {
		t.Fatalf("queue state=%#v", state.Candidates)
	}
	if got := git(t, root, "rev-parse", "main"); got != base {
		t.Fatalf("protected main=%s, want unchanged base=%s", got, base)
	}
	if got := git(t, root, "rev-parse", "staging/local"); got != sha {
		t.Fatalf("staging=%s, want candidate=%s", got, sha)
	}
}

func TestStagingIntegrationLandAcceptsPersistedReleaseAlias(t *testing.T) {
	root := t.TempDir()
	stable := filepath.Join(root, "stable-capsules")
	oldRelease := filepath.Join(root, "releases", "old")
	newRelease := filepath.Join(root, "releases", "new")
	workspaceID := "queue-release-alias"
	if err := os.MkdirAll(filepath.Join(stable, "workspaces", workspaceID), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, release := range []string{oldRelease, newRelease} {
		if err := os.MkdirAll(filepath.Join(release, "scripts"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(stable, filepath.Join(release, ".capsules")); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(newRelease, "scripts", "dev-workspace.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	called := false
	integration := StagingIntegration{
		ProjectRoot: newRelease,
		GateCommand: "git diff --check",
		Runner: CommandRunnerFunc(func(_ context.Context, dir, program string, args ...string) ([]byte, error) {
			called = true
			if dir != newRelease || program != filepath.Join(newRelease, "scripts", "dev-workspace.sh") {
				t.Fatalf("unexpected lifecycle call dir=%q program=%q args=%v", dir, program, args)
			}
			return nil, nil
		}),
	}
	spec := Speculation{WorkspaceID: workspaceID, WorkspacePath: filepath.Join(oldRelease, ".capsules", "workspaces", workspaceID)}
	if err := integration.Land(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("managed lifecycle was not called")
	}
	outside := filepath.Join(root, "outside", workspaceID)
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := integration.Land(context.Background(), Speculation{WorkspaceID: workspaceID, WorkspacePath: outside}); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("outside workspace err=%v", err)
	}
}

func TestProtectedIntegrationRetainsExactCandidateWorkspaceForSourcePromotion(t *testing.T) {
	root := protectedQueueRepo(t)
	base := git(t, root, "rev-parse", "HEAD")
	commit(t, root, "candidate.txt", "candidate\n", "candidate")
	sha := git(t, root, "rev-parse", "HEAD")
	git(t, root, "branch", "agent/candidate", sha)
	git(t, root, "reset", "--hard", base)

	store := Store{ProjectRoot: root}
	if _, err := store.Submit(Submit{Branch: "agent/candidate", SHA: sha, Receipt: testReceipt(t, sha)}); err != nil {
		t.Fatal(err)
	}
	state, err := store.Process(context.Background(), ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: root, TargetRef: "main"},
		Gate:        ShellGate{Command: "git diff --check"},
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := state.Candidates[0]
	if candidate.Status != Landed || candidate.SpeculativeSHA != sha || candidate.ValidatedSHA != sha || candidate.WorkspacePath == "" {
		t.Fatalf("candidate=%#v", candidate)
	}
	if got := git(t, candidate.WorkspacePath, "rev-parse", "HEAD"); got != sha {
		t.Fatalf("integration head=%s, want %s", got, sha)
	}
	if got := git(t, root, "rev-parse", "main"); got != base {
		t.Fatalf("source main=%s, want unchanged %s", got, base)
	}
}

func TestProtectedIntegrationNativeSelfCapsulePromotesWithoutDevWorkspaceScript(t *testing.T) {
	root := nativeSelfQueueRepo(t)
	base := git(t, root, "rev-parse", "HEAD")
	commit(t, root, "candidate.txt", "candidate\n", "candidate")
	sha := git(t, root, "rev-parse", "HEAD")
	git(t, root, "branch", "agent/candidate", sha)
	git(t, root, "reset", "--hard", base)
	if _, err := os.Stat(filepath.Join(root, "scripts", "dev-workspace.sh")); !os.IsNotExist(err) {
		t.Fatalf("native fixture unexpectedly has dev-workspace script: %v", err)
	}

	store := Store{ProjectRoot: root}
	if _, err := store.Submit(Submit{Branch: "agent/candidate", SHA: sha, Receipt: persistedReceipt(t, root, sha), TargetRef: "main"}); err != nil {
		t.Fatal(err)
	}
	guard := headroom.Guard{Enabled: true, FreeBytes: func(string) (int64, error) {
		return headroom.MinimumFloorBytes, nil
	}}
	manager, err := capsuleproject.Open(root, []string{"main"})
	if err != nil {
		t.Fatal(err)
	}
	manager.Headroom = guard
	state, err := store.Process(context.Background(), ProcessDeps{
		Integration: ProtectedIntegration{
			ProjectRoot: root, TargetRef: "main", Manager: manager, Headroom: guard,
		},
		Gate:      ShellGate{Command: "git diff --check"},
		Finalizer: ProtectedFinalizer{ProjectRoot: root, TargetRef: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := state.Candidates[0]
	if candidate.Status != Landed || candidate.ValidatedSHA != sha {
		t.Fatalf("candidate=%#v", candidate)
	}
	if !hasEvidence(candidate, "queue:native-capsule-definition=development") {
		t.Fatalf("native lifecycle evidence missing: %v", candidate.Evidence)
	}
	if got := git(t, root, "rev-parse", "main"); got != sha {
		t.Fatalf("main=%s, want candidate %s", got, sha)
	}
}

func TestProtectedIntegrationDevWorkspaceScriptUsesScriptAdapter(t *testing.T) {
	root := protectedQueueRepo(t)
	base := git(t, root, "rev-parse", "HEAD")
	commit(t, root, "candidate.txt", "candidate\n", "candidate")
	sha := git(t, root, "rev-parse", "HEAD")
	git(t, root, "branch", "agent/candidate", sha)
	git(t, root, "reset", "--hard", base)
	script := filepath.Join(root, "scripts", "dev-workspace.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho script-adapter-selected >&2\nexit 42\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := (ProtectedIntegration{ProjectRoot: root, TargetRef: "main"}).Speculate(context.Background(), Candidate{ID: "script-adapter", SHA: sha, TargetRef: "main"}, nil)
	if err == nil || !strings.Contains(err.Error(), "script-adapter-selected") {
		t.Fatalf("err=%v, want script adapter marker", err)
	}
}

func TestProtectedFinalizerAllowsExplicitEmergencySkipTestsAdmission(t *testing.T) {
	root := protectedQueueRepo(t)
	base := git(t, root, "rev-parse", "HEAD")
	commit(t, root, "candidate.txt", "candidate\n", "candidate")
	sha := git(t, root, "rev-parse", "HEAD")
	git(t, root, "branch", "agent/emergency", sha)
	git(t, root, "reset", "--hard", base)

	store := Store{ProjectRoot: root}
	if _, err := store.Submit(Submit{Branch: "agent/emergency", SHA: sha, Admission: EmergencySkipTestsAdmission}); err != nil {
		t.Fatal(err)
	}
	state, err := store.Process(context.Background(), ProcessDeps{
		Integration: queueProtectedIntegration(root),
		Gate:        ShellGate{Command: "git diff --check"},
		Finalizer:   ProtectedFinalizer{ProjectRoot: root, TargetRef: "main"},
		GateVersion: "emergency-skip-tests:git diff --check",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Candidates) != 1 || state.Candidates[0].Status != Landed || state.Candidates[0].Admission != EmergencySkipTestsAdmission {
		t.Fatalf("queue state=%#v", state.Candidates)
	}
	if got := git(t, root, "rev-parse", "main"); got != sha {
		t.Fatalf("main=%s, want emergency candidate %s", got, sha)
	}
}

func queueProtectedIntegration(root string) ProtectedIntegration {
	return ProtectedIntegration{ProjectRoot: root, TargetRef: "main"}
}

func TestProtectedIntegrationPersistsDivergedContinuationInsteadOfEjecting(t *testing.T) {
	root := protectedQueueRepo(t)
	git(t, root, "checkout", "-b", "agent/conflict")
	commit(t, root, "base.txt", "candidate\n", "candidate")
	candidateSHA := git(t, root, "rev-parse", "HEAD")
	git(t, root, "checkout", "main")
	commit(t, root, "base.txt", "target\n", "target")

	store := Store{ProjectRoot: root}
	if _, err := store.Submit(Submit{Branch: "agent/conflict", SHA: candidateSHA, Receipt: testReceipt(t, candidateSHA)}); err != nil {
		t.Fatal(err)
	}
	// Neither a project-local stories/git-ops story nor the embedded-library
	// fallback is configured here (the latter explicitly disabled): this test
	// is about the continuation-persistence behavior, independent of whether
	// an automatic resolver happens to be available — see resolver_test.go
	// for the resolver-availability matrix itself.
	state, err := store.Process(context.Background(), ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: root, TargetRef: "main", GitOpsEmbeddedResolver: func(context.Context) (string, error) { return "", nil }},
		Gate:        ShellGate{Command: "git diff --check"},
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := state.Candidates[0]
	if candidate.Status != NeedsConflictInput || candidate.WorkspacePath == "" {
		t.Fatalf("candidate=%#v", candidate)
	}
	if entries, err := filepath.Glob(filepath.Join(root, ".capsules", "sync", "*.integration.json")); err != nil || len(entries) != 1 {
		t.Fatalf("integration artifacts=%v err=%v", entries, err)
	}
	if got := git(t, root, "rev-parse", "main"); got == candidateSHA {
		t.Fatal("conflicting candidate moved protected main")
	}
}

func TestShellGateRejectsDirtySpeculativeWorkspace(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.name", "Queue Test")
	git(t, root, "config", "user.email", "queue@example.invalid")
	commit(t, root, "base.txt", "base\n", "base")
	_, err := (ShellGate{Command: "touch leaked.txt"}).Run(context.Background(), Speculation{WorkspacePath: root})
	if err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("err=%v", err)
	}
}

func protectedQueueRepo(t *testing.T) string {
	t.Helper()
	parallelQueueIntegrationTest(t)
	root := t.TempDir()
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.name", "Queue Test")
	git(t, root, "config", "user.email", "queue@example.invalid")
	writeDevelopmentDefinition(t, root, "dev-workspace-script")
	if err := os.WriteFile(filepath.Join(root, ".kitsoki", "project-profile.yaml"), []byte("schema: project-profile/v1\ncommands:\n  test: git diff --check\n  change: git diff --check\n  full: git diff --check\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".kitsoki/project-profile.yaml")
	copyQueueScript(t, root, "dev-workspace.sh")
	copyQueueScript(t, root, "refresh-staging-local.sh")
	copyQueueScript(t, root, "protected-main-mode.sh")
	commit(t, root, "base.txt", "base\n", "base")
	base := git(t, root, "rev-parse", "HEAD")
	git(t, root, "branch", "staging/local", base)
	staging := filepath.Join(root, ".capsules", "staging", "local")
	if err := os.MkdirAll(filepath.Dir(staging), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, root, "git", "clone", "--no-local", root, staging)
	git(t, staging, "remote", "rename", "origin", "source")
	git(t, staging, "config", "user.name", "Queue Test")
	git(t, staging, "config", "user.email", "queue@example.invalid")
	git(t, staging, "switch", "-c", "staging/local", "source/staging/local")
	if err := os.WriteFile(filepath.Join(staging, ".kitsoki-capsule"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func nativeSelfQueueRepo(t *testing.T) string {
	t.Helper()
	parallelQueueIntegrationTest(t)
	root := t.TempDir()
	git(t, root, "init", "-b", "main")
	git(t, root, "config", "user.name", "Queue Test")
	git(t, root, "config", "user.email", "queue@example.invalid")
	writeDevelopmentDefinition(t, root, "self")
	if err := os.WriteFile(filepath.Join(root, ".kitsoki", "project-profile.yaml"), []byte("schema: project-profile/v1\ncommands:\n  test: git diff --check\n  change: git diff --check\n  full: git diff --check\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".kitsoki/project-profile.yaml")
	commit(t, root, "base.txt", "base\n", "base")
	return root
}

func writeDevelopmentDefinition(t *testing.T, root, kind string) {
	t.Helper()
	path := filepath.Join(root, ".kitsoki", "capsules", "development.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var raw string
	switch kind {
	case "self":
		raw = `schema: capsule-definition/v1
id: development
source:
  kind: self
policy:
  network: none
`
	case "dev-workspace-script":
		raw = `schema: capsule-definition/v1
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
	default:
		t.Fatalf("unsupported development definition kind %q", kind)
	}
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", ".kitsoki/capsules/development.yaml")
}

func copyQueueScript(t *testing.T, root, name string) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller unavailable")
	}
	source := filepath.Join(filepath.Dir(file), "..", "..", "..", "scripts", name)
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "scripts", name)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, dir, path, text, message string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, path), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", path)
	git(t, dir, "commit", "-m", message)
}

func git(t *testing.T, dir string, args ...string) string { return run(t, dir, "git", args...) }

func run(t *testing.T, dir, program string, args ...string) string {
	t.Helper()
	cmd := exec.Command(program, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", program, strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}
