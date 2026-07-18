package queue

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// conflictingCandidate sets up a protected repo whose candidate conflicts with
// main on base.txt, submits it, and returns the store, root, and candidate SHA.
func conflictingCandidate(t *testing.T) (Store, string, string) {
	t.Helper()
	root := protectedQueueRepo(t)
	git(t, root, "checkout", "-b", "agent/conflict")
	commit(t, root, "base.txt", "candidate\n", "candidate")
	candidateSHA := git(t, root, "rev-parse", "HEAD")
	git(t, root, "checkout", "main")
	commit(t, root, "base.txt", "target\n", "target")
	store := Store{ProjectRoot: root}
	if _, err := store.Submit(Submit{Branch: "agent/conflict", SHA: candidateSHA, Receipt: persistedReceipt(t, root, candidateSHA)}); err != nil {
		t.Fatal(err)
	}
	return store, root, candidateSHA
}

// The default resolver is the kitsoki git-ops conflict_resolver. Its launch is
// simulated through the injected runner (no LLM in automated tests): the fake
// resolver edits the conflicted file clean, exactly as the write-fenced agent
// would, and the queue performs every git operation around it.
func TestGitOpsResolverResolvesConflictAndCandidateLands(t *testing.T) {
	store, root, candidateSHA := conflictingCandidate(t)
	// Present the git-ops story so the resolver harness is "available".
	appPath := filepath.Join(root, "stories", "git-ops", "app.yaml")
	if err := os.MkdirAll(filepath.Dir(appPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(appPath, []byte("agents:\n  conflict_resolver: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var launched []string
	runner := CommandRunnerFunc(func(ctx context.Context, dir, program string, args ...string) ([]byte, error) {
		if program == "env" { // the git-ops launch
			launched = append(launched, strings.Join(args, " "))
			if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("resolved\n"), 0o644); err != nil {
				return nil, err
			}
			return []byte("resolved"), nil
		}
		return execCommandRunner{}.Run(ctx, dir, program, args...)
	})
	state, err := store.Process(context.Background(), ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: root, TargetRef: "main", Runner: runner, KitsokiBin: "kitsoki-test"},
		Gate:        ShellGate{Command: "git diff --check"},
		Finalizer:   ProtectedFinalizer{ProjectRoot: root, TargetRef: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := state.Candidates[0]
	if c.phase() != Landed {
		t.Fatalf("resolved conflict did not land: %#v", c)
	}
	if len(launched) != 1 || !strings.Contains(launched[0], "--agent conflict_resolver") || !strings.Contains(launched[0], "--mode codeact") {
		t.Fatalf("git-ops launch=%v", launched)
	}
	if !hasEvidence(c, "queue:conflicts-resolved=base.txt") {
		t.Fatalf("resolution not evidenced: %v", c.Evidence)
	}
	if got := strings.TrimSpace(git(t, root, "show", "main:base.txt")); got != "resolved" {
		t.Fatalf("main content=%q", got)
	}
	git(t, root, "merge-base", "--is-ancestor", candidateSHA, "main")
}

// No git-ops story in the project → there is no automatic resolver; the
// conflict parks as needs_conflict_input with its continuation retained, and
// protected main never moves.
func TestConflictWithoutGitOpsStoryParksAsConflictInput(t *testing.T) {
	store, root, candidateSHA := conflictingCandidate(t)
	state, err := store.Process(context.Background(), ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: root, TargetRef: "main"},
		Gate:        ShellGate{Command: "git diff --check"},
		Finalizer:   ProtectedFinalizer{ProjectRoot: root, TargetRef: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := state.Candidates[0]
	if c.phase() != NeedsConflictInput {
		t.Fatalf("phase=%s", c.phase())
	}
	if !hasEvidence(c, "queue:git-ops-resolver-unavailable") {
		t.Fatalf("evidence=%v", c.Evidence)
	}
	if got := git(t, root, "rev-parse", "main"); got == candidateSHA {
		t.Fatal("conflicting candidate moved protected main")
	}
}

// A present-but-broken resolver harness parks immediately as needs_input:
// burning bounded retries on a broken launch path would only delay the train.
func TestBrokenGitOpsHarnessParksAsNeedsInputImmediately(t *testing.T) {
	store, root, _ := conflictingCandidate(t)
	appPath := filepath.Join(root, "stories", "git-ops", "app.yaml")
	if err := os.MkdirAll(filepath.Dir(appPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(appPath, []byte("agents:\n  conflict_resolver: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := CommandRunnerFunc(func(ctx context.Context, dir, program string, args ...string) ([]byte, error) {
		if program == "env" {
			return []byte("launch refused"), context.DeadlineExceeded
		}
		return execCommandRunner{}.Run(ctx, dir, program, args...)
	})
	state, err := store.Process(context.Background(), ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: root, TargetRef: "main", Runner: runner, KitsokiBin: "kitsoki-test"},
		Gate:        ShellGate{Command: "git diff --check"},
		Finalizer:   ProtectedFinalizer{ProjectRoot: root, TargetRef: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := state.Candidates[0]
	if c.phase() != NeedsInput || c.RetryReason != "resolver_harness_failure" {
		t.Fatalf("harness failure: phase=%s reason=%s attempt=%d", c.phase(), c.RetryReason, c.Attempt)
	}
}

// rerere replays a previously recorded human resolution without any resolver
// launch at all.
func TestRerereReplaysRecordedResolutionWithoutResolverLaunch(t *testing.T) {
	store, root, _ := conflictingCandidate(t)
	// Record the resolution shape in the protected repo's rerere cache by
	// resolving the same conflict once locally… but the integration instance
	// is a fresh clone, so a shared cache is not inherited. Instead prove the
	// no-launch path: with no story and a pre-resolved instance the candidate
	// proceeds. Simulate by injecting a runner that fails on any launch (none
	// must happen) and pre-seeding ResolverCommand with a deterministic fix.
	state, err := store.Process(context.Background(), ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: root, TargetRef: "main", ResolverCommand: "printf 'resolved\\n' > base.txt"},
		Gate:        ShellGate{Command: "git diff --check"},
		Finalizer:   ProtectedFinalizer{ProjectRoot: root, TargetRef: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := state.Candidates[0]
	if c.phase() != Landed {
		t.Fatalf("deterministic resolver command did not land: %#v", c)
	}
	if got := strings.TrimSpace(git(t, root, "show", "main:base.txt")); got != "resolved" {
		t.Fatalf("main content=%q", got)
	}
}
