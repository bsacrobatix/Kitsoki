package main

import (
	"context"
	"os"
	"path/filepath"
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

func TestReadQueueExternalResultIsStrictAndRejectsSymlinks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.json")
	valid := `{
  "schema":"capsule-external-worker-result/v1",
  "execution_id":"execution-1",
  "job_id":"job-1",
  "train_id":"train-1",
  "manifest_digest":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
  "branch":"worker/train-1/job-1",
  "candidate_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  "base_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  "target_ref":"main",
  "receipt_id":"sha256:receipt",
  "bundle_digest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
  "bundle_bytes":123,
  "bundle_key":"trains/train-1/job-1/refs.bundle"
}`
	require.NoError(t, os.WriteFile(path, []byte(valid), 0o600))
	result, err := readQueueExternalResult(path)
	require.NoError(t, err)
	require.Equal(t, "execution-1", result.ExecutionID)

	require.NoError(t, os.WriteFile(path, []byte(strings.Replace(valid, `"execution_id"`, `"unknown"`, 1)), 0o600))
	_, err = readQueueExternalResult(path)
	require.ErrorContains(t, err, "unknown field")

	require.NoError(t, os.WriteFile(path, []byte(valid), 0o600))
	link := filepath.Join(dir, "result-link.json")
	require.NoError(t, os.Symlink(path, link))
	_, err = readQueueExternalResult(link)
	require.ErrorContains(t, err, "regular non-symlink")
}

func TestQueueProcessDepsDefaultsToExactProtectedStagingCAS(t *testing.T) {
	deps := queueProcessDeps("/project", "make test", "", "", "", "worker-1")

	integration, ok := deps.Integration.(queue.ProtectedIntegration)
	require.True(t, ok)
	require.Equal(t, "/project", integration.ProjectRoot)
	require.Equal(t, "staging/local", integration.TargetRef)
	finalizer, ok := deps.Finalizer.(queue.ProtectedFinalizer)
	require.True(t, ok)
	require.Equal(t, "staging/local", finalizer.TargetRef)
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

func TestQueueSummaryLineFormatsPhasesAndRetryReasons(t *testing.T) {
	summary := queue.StatusSummary{
		PhaseCounts:       map[queue.Status]int{queue.Queued: 2, queue.Landed: 1},
		RetryReasonCounts: map[string]int{"gate_failed": 1},
		TrainDepth:        2,
		ParkedCount:       0,
	}
	line := queueSummaryLine(summary)
	require.Contains(t, line, "train_depth=2")
	require.Contains(t, line, "parked=0")
	require.Contains(t, line, "queued=2")
	require.Contains(t, line, "landed=1")
	require.Contains(t, line, "gate_failed=1")
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

type fakeCLIFinalizer struct{}

func (fakeCLIFinalizer) Finalize(_ context.Context, c queue.Candidate) (queue.FinalizeResult, error) {
	return queue.FinalizeResult{NewMainSHA: c.TreeSHA}, nil
}

// TestQueueSweepCmdDryRunReportsWithoutMutatingAndApplyRejectsSuperseded
// exercises the actual cobra command end to end: dry-run (the default) must
// report the plan without touching state; --apply must reject only the
// superseded entry (another candidate for the identical commit already
// landed) and leave a merely-parked candidate untouched.
func TestQueueSweepCmdDryRunReportsWithoutMutatingAndApplyRejectsSuperseded(t *testing.T) {
	project := t.TempDir()
	store := queue.Store{ProjectRoot: project}
	dupSHA := strings.Repeat("d", 40)

	landedTwin, err := store.Submit(queue.Submit{Branch: "agent/landed-twin", SHA: dupSHA, Receipt: testCLIReceipt(t, dupSHA)})
	require.NoError(t, err)
	fakeDeps := queue.ProcessDeps{
		Integration: fakeCLIIntegration{speculate: func(_ context.Context, c queue.Candidate, _ []queue.Candidate) (queue.Speculation, error) {
			return queue.Speculation{SHA: "tree-" + c.SHA, BaseSHA: "base-" + c.SHA}, nil
		}},
		Gate: fakeCLIGate{}, GateVersion: "test/v1", Finalizer: fakeCLIFinalizer{},
	}
	_, err = store.Process(context.Background(), fakeDeps)
	require.NoError(t, err)
	landed, err := store.Get(landedTwin.ID)
	require.NoError(t, err)
	require.Equal(t, queue.Landed, landed.Phase)

	// A second, unrelated parked candidate for the same commit: superseded.
	superseded, err := store.Submit(queue.Submit{Branch: "agent/superseded", SHA: dupSHA, Admission: queue.EmergencySkipTestsAdmission})
	require.NoError(t, err)
	_, err = store.Park(queue.Op{ID: superseded.ID, Actor: "test", Reason: "gate_failed"})
	require.NoError(t, err)

	// A merely-parked candidate for a different commit: not superseded, must
	// survive both the dry run and the apply untouched.
	otherSHA := strings.Repeat("f", 40)
	other, err := store.Submit(queue.Submit{Branch: "agent/other", SHA: otherSHA, Receipt: testCLIReceipt(t, otherSHA)})
	require.NoError(t, err)
	_, err = store.Park(queue.Op{ID: other.ID, Actor: "test", Reason: "gate_failed"})
	require.NoError(t, err)

	dryRun := queueSweepCmd()
	dryRun.SetArgs([]string{"--project", project})
	dryRun.SetOut(new(strings.Builder))
	require.NoError(t, dryRun.Execute())

	state, err := store.List()
	require.NoError(t, err)
	byID := map[string]queue.Candidate{}
	for _, c := range state.Candidates {
		byID[c.ID] = c
	}
	require.Equal(t, queue.NeedsInput, byID[superseded.ID].Phase, "dry-run sweep must not mutate state")
	require.Equal(t, queue.NeedsInput, byID[other.ID].Phase)

	apply := queueSweepCmd()
	apply.SetArgs([]string{"--project", project, "--apply", "--actor", "test"})
	apply.SetOut(new(strings.Builder))
	require.NoError(t, apply.Execute())

	state, err = store.List()
	require.NoError(t, err)
	byID = map[string]queue.Candidate{}
	for _, c := range state.Candidates {
		byID[c.ID] = c
	}
	require.Equal(t, queue.Rejected, byID[superseded.ID].Phase)
	require.Contains(t, byID[superseded.ID].EjectionReason, "identical commit")
	require.Equal(t, queue.NeedsInput, byID[other.ID].Phase, "a merely-parked, non-superseded candidate must never be auto-rejected")
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
