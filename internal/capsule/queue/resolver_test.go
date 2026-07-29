package queue

import (
	"context"
	"fmt"
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

// Neither a project-local stories/git-ops/app.yaml NOR an embedded-library
// git-ops story is available → there is no automatic resolver at all. This
// must still be a no-op (needs_conflict_input, continuation retained, no
// retry burn) but a LOUD one: the evidence names exactly why, not just an
// unqualified tag a silent skip would leave. The embedded fallback is
// explicitly disabled here (returns ("", nil), "nothing to offer") so the
// test is deterministic regardless of whether this machine's binary happens
// to have the story library staged — see basestories.ErrNotStaged and
// defaultGitOpsEmbeddedResolver.
func TestConflictWithNoResolverAnywhereParksLoudly(t *testing.T) {
	store, root, candidateSHA := conflictingCandidate(t)
	noEmbedded := func(context.Context) (string, error) { return "", nil }
	state, err := store.Process(context.Background(), ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: root, TargetRef: "main", GitOpsEmbeddedResolver: noEmbedded},
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
	// Loud: the reason is spelled out, not just the bare tag.
	if !hasEvidence(c, "no project-local stories/git-ops/app.yaml") || !hasEvidence(c, "no embedded @kitsoki/git-ops story available") {
		t.Fatalf("expected a detailed unavailable reason, got evidence=%v", c.Evidence)
	}
	if !strings.Contains(c.Failure, "unresolved") {
		t.Fatalf("expected the parked failure to surface the unresolved-conflict reason, got %q", c.Failure)
	}
	if got := git(t, root, "rev-parse", "main"); got == candidateSHA {
		t.Fatal("conflicting candidate moved protected main")
	}
}

// A project-local stories/git-ops/app.yaml always wins over the embedded
// fallback, even when a fallback is configured: the fallback resolver must
// never even be consulted (it panics if called).
func TestProjectLocalGitOpsStoryTakesPrecedenceOverEmbedded(t *testing.T) {
	store, root, candidateSHA := conflictingCandidate(t)
	appPath := filepath.Join(root, "stories", "git-ops", "app.yaml")
	if err := os.MkdirAll(filepath.Dir(appPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(appPath, []byte("agents:\n  conflict_resolver: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fallbackCalled := false
	neverCalled := func(context.Context) (string, error) {
		fallbackCalled = true
		return "", fmt.Errorf("embedded fallback must not be consulted when a project-local story exists")
	}
	var launchedApp string
	runner := CommandRunnerFunc(func(ctx context.Context, dir, program string, args ...string) ([]byte, error) {
		if program == "env" {
			for i, a := range args {
				if a == "--app" && i+1 < len(args) {
					launchedApp = args[i+1]
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("resolved\n"), 0o644); err != nil {
				return nil, err
			}
			return []byte("resolved"), nil
		}
		return execCommandRunner{}.Run(ctx, dir, program, args...)
	})
	state, err := store.Process(context.Background(), ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: root, TargetRef: "main", Runner: runner, KitsokiBin: "kitsoki-test", GitOpsEmbeddedResolver: neverCalled},
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
	if fallbackCalled {
		t.Fatal("embedded fallback resolver was consulted despite a project-local story existing")
	}
	if launchedApp != appPath {
		t.Fatalf("launched app = %q, want the project-local story %q", launchedApp, appPath)
	}
	if !hasEvidence(c, "queue:git-ops-resolver-source=project-local") {
		t.Fatalf("expected project-local source evidence, got %v", c.Evidence)
	}
	git(t, root, "merge-base", "--is-ancestor", candidateSHA, "main")
}

// No project-local stories/git-ops story: the embedded-library fallback is
// consulted and used instead, so a project like POG that ships none of its
// own git-ops story still gets a working automatic resolver. The fallback is
// injected (a fake, not the real basestories.Materialize) so this test never
// depends on the story library being staged into the test binary.
func TestEmbeddedGitOpsStoryUsedWhenProjectLacksOne(t *testing.T) {
	store, root, candidateSHA := conflictingCandidate(t)
	embeddedRoot := t.TempDir()
	embeddedApp := filepath.Join(embeddedRoot, "git-ops", "app.yaml")
	if err := os.MkdirAll(filepath.Dir(embeddedApp), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(embeddedApp, []byte("agents:\n  conflict_resolver: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fallbackCalled := false
	fromEmbeddedLibrary := func(context.Context) (string, error) {
		fallbackCalled = true
		return embeddedApp, nil
	}
	var launchedApp string
	runner := CommandRunnerFunc(func(ctx context.Context, dir, program string, args ...string) ([]byte, error) {
		if program == "env" {
			for i, a := range args {
				if a == "--app" && i+1 < len(args) {
					launchedApp = args[i+1]
				}
			}
			if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("resolved\n"), 0o644); err != nil {
				return nil, err
			}
			return []byte("resolved"), nil
		}
		return execCommandRunner{}.Run(ctx, dir, program, args...)
	})
	state, err := store.Process(context.Background(), ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: root, TargetRef: "main", Runner: runner, KitsokiBin: "kitsoki-test", GitOpsEmbeddedResolver: fromEmbeddedLibrary},
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
	if !fallbackCalled {
		t.Fatal("embedded fallback resolver was never consulted despite no project-local story")
	}
	if launchedApp != embeddedApp {
		t.Fatalf("launched app = %q, want the embedded story %q", launchedApp, embeddedApp)
	}
	if !hasEvidence(c, "queue:git-ops-resolver-source=embedded-library") {
		t.Fatalf("expected embedded-library source evidence, got %v", c.Evidence)
	}
	if !hasEvidence(c, "queue:conflicts-resolved=base.txt") {
		t.Fatalf("resolution not evidenced: %v", c.Evidence)
	}
	if got := strings.TrimSpace(git(t, root, "show", "main:base.txt")); got != "resolved" {
		t.Fatalf("main content=%q", got)
	}
	git(t, root, "merge-base", "--is-ancestor", candidateSHA, "main")
}

// A broken embedded-fallback mechanism itself (not merely "no story found")
// is a harness failure, not a silent no-op: it parks immediately as
// needs_input rather than burning a retry on a broken launch path.
func TestBrokenEmbeddedFallbackMechanismIsHarnessFailure(t *testing.T) {
	store, root, _ := conflictingCandidate(t)
	broken := func(context.Context) (string, error) {
		return "", fmt.Errorf("embedded library cache is corrupt")
	}
	state, err := store.Process(context.Background(), ProcessDeps{
		Integration: ProtectedIntegration{ProjectRoot: root, TargetRef: "main", GitOpsEmbeddedResolver: broken},
		Gate:        ShellGate{Command: "git diff --check"},
		Finalizer:   ProtectedFinalizer{ProjectRoot: root, TargetRef: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := state.Candidates[0]
	if c.phase() != NeedsInput || c.RetryReason != "resolver_harness_failure" {
		t.Fatalf("harness failure: phase=%s reason=%s", c.phase(), c.RetryReason)
	}
}

// defaultGitOpsEmbeddedResolver is the real production fallback
// (basestories.Materialize). This exercises it directly against the real
// embedded library rather than a fake, but — mirroring
// basestories.TestMaterialize — skips when the library was not staged into
// this test binary (a bare `go test` without `make embed-stories`), so the
// test never requires build-time staging to pass.
func TestDefaultGitOpsEmbeddedResolverUsesRealEmbeddedLibrary(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	path, err := defaultGitOpsEmbeddedResolver(context.Background())
	if err != nil {
		t.Fatalf("defaultGitOpsEmbeddedResolver: %v", err)
	}
	if path == "" {
		t.Skip("story library not staged into the test binary; run `make embed-stories`")
	}
	if filepath.Base(path) != "app.yaml" || filepath.Base(filepath.Dir(path)) != "git-ops" {
		t.Fatalf("resolved path = %q, want a .../git-ops/app.yaml", path)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Fatalf("resolved embedded git-ops app.yaml does not exist: %v", statErr)
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
