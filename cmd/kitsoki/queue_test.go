package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/queue"
	"kitsoki/internal/capsule/receipt"
)

func TestQueueProcessDepsDefaultsToStagingIntegration(t *testing.T) {
	deps := queueProcessDeps("/project", "make test", "", "", "", "worker-1")

	integration, ok := deps.Integration.(queue.StagingIntegration)
	require.True(t, ok)
	require.Equal(t, "/project", integration.ProjectRoot)
	require.Equal(t, "make test", integration.GateCommand)
	require.Nil(t, deps.Finalizer)
	require.Nil(t, deps.Repairer)
	require.Equal(t, "worker-1", deps.WorkerID)
	require.Equal(t, "staging/local", deps.TargetRef)
}

func TestQueueProcessDepsUsesRequestedProtectedTarget(t *testing.T) {
	deps := queueProcessDeps("/project", "make test", "release/2026.07", "resolve-conflicts", "repair-gate", "worker-1")

	integration, ok := deps.Integration.(queue.ProtectedIntegration)
	require.True(t, ok)
	require.Equal(t, "release/2026.07", integration.TargetRef)
	require.Equal(t, "resolve-conflicts", integration.ResolverCommand)
	finalizer, ok := deps.Finalizer.(queue.ProtectedFinalizer)
	require.True(t, ok)
	require.Equal(t, "release/2026.07", finalizer.TargetRef)
	repairer, ok := deps.Repairer.(queue.ShellRepairer)
	require.True(t, ok)
	require.Equal(t, "repair-gate", repairer.Command)
	require.Equal(t, "release/2026.07", deps.TargetRef)
}

func TestQueueProcessDepsSetsGateMemo(t *testing.T) {
	deps := queueProcessDeps("/project", "make test", "", "", "", "worker-1")
	memo, ok := deps.GateMemo.(queue.FileGateMemo)
	require.True(t, ok)
	require.Equal(t, "/project", memo.ProjectRoot)
}

type fakeCLIIntegration struct {
	speculate func(context.Context, queue.Candidate, []queue.Candidate) (queue.Speculation, error)
}

func (f fakeCLIIntegration) Speculate(ctx context.Context, c queue.Candidate, ahead []queue.Candidate) (queue.Speculation, error) {
	return f.speculate(ctx, c, ahead)
}
func (f fakeCLIIntegration) Land(context.Context, queue.Speculation) error { return nil }

type fakeCLIGate struct{}

func (fakeCLIGate) Run(context.Context, queue.Speculation) (queue.GateResult, error) {
	return queue.GateResult{Passed: true}, nil
}

// TestRunQueueWorkerLoopOnceProcessesExactlyOneStep pins the --once
// contract runQueueWorkerLoop must preserve for both the concurrency=1 and
// concurrency>1 paths in queueWorkerCmd's RunE.
func TestRunQueueWorkerLoopOnceProcessesExactlyOneStep(t *testing.T) {
	sha := strings.Repeat("a", 40)
	store := queue.Store{ProjectRoot: t.TempDir()}
	candidate, err := store.Submit(queue.Submit{Branch: "agent/a", SHA: sha, Receipt: testCLIReceipt(t, sha)})
	require.NoError(t, err)

	deps := queue.ProcessDeps{
		Integration: fakeCLIIntegration{speculate: func(_ context.Context, c queue.Candidate, _ []queue.Candidate) (queue.Speculation, error) {
			return queue.Speculation{SHA: "tree-" + c.SHA, BaseSHA: "base-" + c.SHA}, nil
		}},
		Gate: fakeCLIGate{}, GateVersion: "test/v1",
	}
	require.NoError(t, runQueueWorkerLoop(context.Background(), store, deps, true))

	state, err := store.List()
	require.NoError(t, err)
	require.Len(t, state.Candidates, 1)
	require.Equal(t, candidate.ID, state.Candidates[0].ID)
	require.Equal(t, queue.ReadyToFinalize, state.Candidates[0].Phase)
}

// TestQueueWorkerConcurrencyDrainsMultipleCandidatesInOnePass exercises
// exactly the fan-out queueWorkerCmd's RunE performs under --concurrency:
// N goroutines each calling runQueueWorkerLoop once, with distinct
// worker-id suffixes. A single (concurrency=1) --once pass only ever
// claims one candidate; this proves N concurrent loops claim and prepare N
// candidates in the same one-step pass, independently.
func TestQueueWorkerConcurrencyDrainsMultipleCandidatesInOnePass(t *testing.T) {
	store := queue.Store{ProjectRoot: t.TempDir(), LockWait: time.Second}
	var ids []string
	for _, letter := range []string{"a", "b"} {
		sha := strings.Repeat(letter, 40)
		c, err := store.Submit(queue.Submit{Branch: "agent/" + letter, SHA: sha, Receipt: testCLIReceipt(t, sha)})
		require.NoError(t, err)
		ids = append(ids, c.ID)
	}

	baseDeps := queue.ProcessDeps{
		Integration: fakeCLIIntegration{speculate: func(_ context.Context, c queue.Candidate, _ []queue.Candidate) (queue.Speculation, error) {
			return queue.Speculation{SHA: "tree-" + c.SHA, BaseSHA: "base-" + c.SHA}, nil
		}},
		Gate: fakeCLIGate{}, GateVersion: "test/v1",
	}

	const n = 2
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 1; i <= n; i++ {
		workerDeps := baseDeps
		workerDeps.WorkerID = "queue-worker-" + string(rune('0'+i))
		wg.Add(1)
		go func(d queue.ProcessDeps) {
			defer wg.Done()
			errs <- runQueueWorkerLoop(context.Background(), store, d, true)
		}(workerDeps)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	state, err := store.List()
	require.NoError(t, err)
	require.Len(t, state.Candidates, 2)
	for _, c := range state.Candidates {
		require.Containsf(t, ids, c.ID, "unexpected candidate %s", c.ID)
		require.Equalf(t, queue.ReadyToFinalize, c.Phase, "candidate %s phase=%s, want both claimed independently in one concurrent pass", c.ID, c.Phase)
	}
}

func testCLIReceipt(t *testing.T, sha string) receipt.Receipt {
	t.Helper()
	lock, err := environment.SealLock(environment.Lock{Schema: environment.LockSchema, ID: "ci", DefinitionDigest: "definition", Toolchains: map[string]string{}, Network: "none", Sandbox: "process"})
	require.NoError(t, err)
	env, err := executor.Seal(executor.Envelope{JobID: "job", ProjectID: "project", DefinitionDigest: "definition", Instance: control.Handle{ID: "workspace", Generation: 1}, SourceDigest: sha, StoryPath: "story", StoryDigest: "story-digest", Environment: lock})
	require.NoError(t, err)
	verdict := ci.NormalizeVerdict(ci.Verdict{Schema: ci.VerdictSchema, Pipeline: "test", Outcome: "passed", SourceDigest: sha, StoryDigest: env.StoryDigest, EnvironmentDigest: env.Environment.Digest, EnvelopeDigest: env.Digest})
	r, v, err := receipt.Build(receipt.BuildInput{Job: artifactjob.Job{ID: "job"}, Envelope: env, Verdict: verdict, TraceDigest: "trace"})
	require.NoError(t, err)
	require.Equal(t, "valid", v.Status)
	return r
}
