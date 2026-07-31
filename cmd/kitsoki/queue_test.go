package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
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
	require.Equal(t, "change", deps.GateTier)
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
	require.Equal(t, "release", deps.GateTier)
}

func TestQueueWorkerFlagsDefaultToDerivedTierAndSharedCapacity(t *testing.T) {
	cmd := queueWorkerCmd()
	require.Empty(t, cmd.Flag("gate-tier").DefValue)
	require.Empty(t, cmd.Flag("executor-pipeline").DefValue)
	require.Equal(t, "default", cmd.Flag("capacity-pool").DefValue)
	require.Equal(t, "1", cmd.Flag("capacity").DefValue)
	require.True(t, filepath.IsAbs(cmd.Flag("capacity-root").DefValue))
	require.Equal(t, "full", queueGateTier("main"))
	require.Equal(t, "release", queueGateTier("deploy/prod"))
	require.Equal(t, "change", queueGateTier("staging/local"))
}

func TestQueueWorkerRejectsExecutorPipelineTierMismatchAtStartup(t *testing.T) {
	cmd := queueWorkerCmd()
	cmd.SetArgs([]string{
		"--project", t.TempDir(),
		"--executor", "capsule",
		"--executor-pipeline", "change",
		"--target", "main",
		"--capacity-root", t.TempDir(),
		"--capacity-pool", "test",
		"--once",
	})
	err := cmd.Execute()
	require.ErrorContains(t, err, `--executor-pipeline "change" must equal effective --gate-tier "full"`)
}

func TestQueueRepairRejectsSameIdentityBeforeAnyStage(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{
			name: "process",
			args: []string{"--project", t.TempDir(), "--gate", "true"},
		},
		{
			name: "worker",
			args: []string{"--project", t.TempDir(), "--gate", "true", "--once"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := queueProcessCmd()
			if tc.name == "worker" {
				cmd = queueWorkerCmd()
			}
			cmd.SetArgs(append(tc.args,
				"--repair", "repair",
				"--repair-review", "review",
				"--repairer-id", "same-agent",
				"--reviewer-id", "same-agent",
				"--review-policy-digest", "sha256:policy",
			))
			err := cmd.Execute()
			require.ErrorContains(t, err, "repairer and reviewer identities must differ")
		})
	}
}

func TestQueueGateRunIsReentrantAcrossNestedCLIProcesses(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "kitsoki")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build nested CLI: %v\n%s", err, out)
	}
	capacityRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := queueGateRunCmd()
	cmd.SetContext(ctx)
	cmd.SetArgs([]string{
		"--project", t.TempDir(),
		"--capacity-root", capacityRoot,
		"--capacity-pool", "nested-cli",
		"--gate-tier", "full",
		"--", bin, "queue", "gate-run",
		"--project", ".",
		"--capacity-root", capacityRoot,
		"--capacity-pool", "nested-cli",
		"--gate-tier", "full",
		"--", "sh", "-c", `test "$KITSOKI_GATE_TIER" = full`,
	})
	require.NoError(t, cmd.Execute())
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

// TestQueueSummaryLineFormatsNeedsHumanAndTypedReasonCodes pins the typed
// enum's visibility in the DEFAULT human `queue status` roll-up. Both views
// are printed side by side because they answer different questions:
// retry_reasons[...] is the unconstrained prose/stage-tag view, and
// reason_codes[...] is the closed-set classification an operator can act on
// ("two candidates are repairer-exhausted" rather than two distinct strings).
// Without the second, the enum would be invisible outside --json.
func TestQueueSummaryLineFormatsNeedsHumanAndTypedReasonCodes(t *testing.T) {
	summary := queue.StatusSummary{
		PhaseCounts:       map[queue.Status]int{queue.Queued: 1, queue.NeedsHuman: 2, queue.NeedsInput: 1},
		RetryReasonCounts: map[string]int{"max_attempts_exhausted": 2, "parked_by_operator": 1},
		ReasonCodeCounts: map[queue.ReasonCode]int{
			queue.ReasonRepairerExhausted: 2,
			queue.ReasonOperatorParked:    1,
		},
		TrainDepth:      4,
		ParkedCount:     3,
		NeedsHumanCount: 2,
		OldestParkedAge: "2h0m0s",
	}
	line := queueSummaryLine(summary)
	require.Contains(t, line, "parked=3")
	require.Contains(t, line, "needs_human=2")
	require.Contains(t, line, "oldest_parked=2h0m0s")
	require.Contains(t, line, "reason_codes[")
	require.Contains(t, line, "operator-parked=1")
	require.Contains(t, line, "repairer-exhausted=2")
	// The free-text roll-up is carried alongside, never replaced.
	require.Contains(t, line, "retry_reasons[")
	require.Contains(t, line, "max_attempts_exhausted=2")

	// No typed codes at all (a queue with nothing parked or retrying) prints
	// no empty bracket group.
	bare := queueSummaryLine(queue.StatusSummary{PhaseCounts: map[queue.Status]int{queue.Queued: 1}, TrainDepth: 1})
	require.NotContains(t, bare, "reason_codes[")
}

func TestQueueProcessDepsSetsGateMemo(t *testing.T) {
	deps := queueProcessDeps("/project", "make test", "", "", "", "worker-1")
	memo, ok := deps.GateMemo.(queue.FileGateMemo)
	require.True(t, ok)
	require.Equal(t, "/project", memo.ProjectRoot)
}

func TestQueueMigrateRejectsRefTargetBaseWithoutMutatingState(t *testing.T) {
	root := queueMigrationGitRepo(t)
	path, before := writeLegacyQueueState(t, root)

	cmd := queueMigrateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--project", root, "--target", "main", "--target-base-sha", "staging/local"})
	err := cmd.Execute()
	require.ErrorContains(t, err, "--target-base-sha must be a lowercase full Git SHA")
	after, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, before, after)
}

func TestQueueMigrateAcceptsResolvedFullTargetBaseSHA(t *testing.T) {
	root := queueMigrationGitRepo(t)
	_, _ = writeLegacyQueueState(t, root)
	base := promoteExistingGit(t, root, "rev-parse", "HEAD")

	cmd := queueMigrateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--project", root, "--target", "main", "--target-base-sha", base})
	require.NoError(t, cmd.Execute())
	var state queue.State
	require.NoError(t, json.Unmarshal(out.Bytes(), &state))
	require.Equal(t, queue.Schema, state.Schema)
	require.Len(t, state.Candidates, 1)
	require.Equal(t, base, state.Candidates[0].TargetBaseSHAAtAdmission)
}

func queueMigrationGitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	promoteExistingGit(t, root, "init", "-q", "-b", "main")
	promoteExistingGit(t, root, "config", "user.name", "Queue Test")
	promoteExistingGit(t, root, "config", "user.email", "queue@example.invalid")
	require.NoError(t, os.WriteFile(filepath.Join(root, "base.txt"), []byte("base\n"), 0o644))
	promoteExistingGit(t, root, "add", "base.txt")
	promoteExistingGit(t, root, "commit", "-qm", "base")
	promoteExistingGit(t, root, "branch", "staging/local")
	return root
}

func writeLegacyQueueState(t *testing.T, root string) (string, []byte) {
	t.Helper()
	dir := filepath.Join(root, ".capsules", "queue")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	raw := []byte(`{"schema":"capsule-merge-queue/v1","candidates":[{"id":"legacy","sha":"dddddddddddddddddddddddddddddddddddddddd","branch":"agent/d","project_id":"project","status":"queued"}]}`)
	path := filepath.Join(dir, "state.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	return path, raw
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
	require.NoError(t, runQueueWorkerLoop(context.Background(), store, deps, true, nil))

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
			errs <- runQueueWorkerLoop(context.Background(), store, d, true, nil)
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

// TestQueueWorkerMedicFlagDispatchesStalledConflictCandidate pins the
// worker-loop wiring for P1.7 part 2: passing a non-nil medic to
// runQueueWorkerLoop (what queueWorkerCmd's RunE does when --medic is set)
// actually runs one queue.Store.MedicRunOnce pass per outer iteration, even
// on a --once run where the ordinary claim/finalize step has nothing to do
// (a needs_conflict_input candidate is never claimable). The candidate is
// seeded directly onto state.json — every field on queue.State/Candidate is
// exported for exactly this kind of durable-record fixture — so this test
// never has to reconstruct a real git conflict just to exercise the CLI
// wiring (internal/capsule/queue's own tests already cover the resolver
// dispatch semantics end to end).
func TestQueueWorkerMedicFlagDispatchesStalledConflictCandidate(t *testing.T) {
	project := t.TempDir()
	dir := filepath.Join(project, ".capsules", "queue")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	state := queue.State{Schema: queue.Schema, Candidates: []queue.Candidate{{
		ID: "queue-medic-cli-test", ProjectID: "p", TargetRef: "main", TargetPolicy: queue.WaveAutoPolicy,
		Sequence: 1, Position: 1, Branch: "agent/conflict", SHA: strings.Repeat("c", 40),
		Admission: queue.ReceiptAdmission, ReceiptID: "sha256:receipt",
		Status: queue.NeedsConflictInput, Phase: queue.NeedsConflictInput,
		ConflictContinuation: "cont-1",
	}}}
	raw, err := json.MarshalIndent(state, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state.json"), raw, 0o600))

	store := queueWorkerStore(project, "")
	deps := queue.ProcessDeps{Integration: fakeCLIIntegration{}, Gate: fakeCLIGate{}}
	medic := &queue.MedicDeps{MedicID: "cli-medic", MaxDispatches: 3, Deadline: time.Hour}
	require.NoError(t, runQueueWorkerLoop(context.Background(), store, deps, true, medic))

	got, err := store.Get("queue-medic-cli-test")
	require.NoError(t, err)
	require.Equal(t, queue.Queued, got.Phase, "the medic pass inside the worker loop must have dispatched the stalled conflict candidate")
	require.Equal(t, 1, got.MedicDispatches)
	require.Equal(t, "cli-medic", got.MedicLastBy)
}

// TestQueueWorkerLoopWithNilMedicNeverTouchesConflictCandidate is the
// control: the existing (--medic unset) behavior must be exactly preserved
// — a needs_conflict_input candidate sits untouched, as it always has.
func TestQueueWorkerLoopWithNilMedicNeverTouchesConflictCandidate(t *testing.T) {
	project := t.TempDir()
	dir := filepath.Join(project, ".capsules", "queue")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	state := queue.State{Schema: queue.Schema, Candidates: []queue.Candidate{{
		ID: "queue-no-medic-cli-test", ProjectID: "p", TargetRef: "main", TargetPolicy: queue.WaveAutoPolicy,
		Sequence: 1, Position: 1, Branch: "agent/conflict", SHA: strings.Repeat("e", 40),
		Admission: queue.ReceiptAdmission, ReceiptID: "sha256:receipt",
		Status: queue.NeedsConflictInput, Phase: queue.NeedsConflictInput,
		ConflictContinuation: "cont-1",
	}}}
	raw, err := json.MarshalIndent(state, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state.json"), raw, 0o600))

	store := queueWorkerStore(project, "")
	deps := queue.ProcessDeps{Integration: fakeCLIIntegration{}, Gate: fakeCLIGate{}}
	require.NoError(t, runQueueWorkerLoop(context.Background(), store, deps, true, nil))

	got, err := store.Get("queue-no-medic-cli-test")
	require.NoError(t, err)
	require.Equal(t, queue.NeedsConflictInput, got.Phase)
	require.Zero(t, got.MedicDispatches)
}

// TestQueueMedicCmdOnceDispatchesConflictCandidate exercises the standalone
// `kitsoki queue medic` command end to end.
func TestQueueMedicCmdOnceDispatchesConflictCandidate(t *testing.T) {
	project := t.TempDir()
	dir := filepath.Join(project, ".capsules", "queue")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	state := queue.State{Schema: queue.Schema, Candidates: []queue.Candidate{{
		ID: "queue-medic-standalone-test", ProjectID: "p", TargetRef: "main", TargetPolicy: queue.WaveAutoPolicy,
		Sequence: 1, Position: 1, Branch: "agent/conflict", SHA: strings.Repeat("f", 40),
		Admission: queue.ReceiptAdmission, ReceiptID: "sha256:receipt",
		Status: queue.NeedsConflictInput, Phase: queue.NeedsConflictInput,
		ConflictContinuation: "cont-1",
	}}}
	raw, err := json.MarshalIndent(state, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state.json"), raw, 0o600))

	cmd := queueMedicCmd()
	cmd.SetArgs([]string{"--project", project, "--once"})
	var out strings.Builder
	cmd.SetOut(&out)
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "dispatch_resolver")

	store := queue.Store{ProjectRoot: project}
	got, err := store.Get("queue-medic-standalone-test")
	require.NoError(t, err)
	require.Equal(t, queue.Queued, got.Phase)
	require.Equal(t, 1, got.MedicDispatches)
}

// TestQueueWorkerMedicTargetScopingIgnoresMismatchedTarget pins the
// target-scoping fix at the worker-loop wiring layer: a MedicDeps.TargetRef
// that does not match a candidate's own TargetRef leaves it completely
// untouched, exactly like the worker's own claim/finalize/update already
// refuse a candidate bound to a different target.
func TestQueueWorkerMedicTargetScopingIgnoresMismatchedTarget(t *testing.T) {
	project := t.TempDir()
	dir := filepath.Join(project, ".capsules", "queue")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	state := queue.State{Schema: queue.Schema, Candidates: []queue.Candidate{{
		ID: "queue-medic-target-mismatch-test", ProjectID: "p", TargetRef: "other-target", TargetPolicy: queue.WaveAutoPolicy,
		Sequence: 1, Position: 1, Branch: "agent/conflict", SHA: strings.Repeat("d", 40),
		Admission: queue.ReceiptAdmission, ReceiptID: "sha256:receipt",
		Status: queue.NeedsConflictInput, Phase: queue.NeedsConflictInput,
		ConflictContinuation: "cont-1",
	}}}
	raw, err := json.MarshalIndent(state, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state.json"), raw, 0o600))

	store := queueWorkerStore(project, "")
	deps := queue.ProcessDeps{Integration: fakeCLIIntegration{}, Gate: fakeCLIGate{}, TargetRef: "main"}
	medic := &queue.MedicDeps{MedicID: "cli-medic", MaxDispatches: 3, Deadline: time.Hour, TargetRef: "main"}
	require.NoError(t, runQueueWorkerLoop(context.Background(), store, deps, true, medic))

	got, err := store.Get("queue-medic-target-mismatch-test")
	require.NoError(t, err)
	require.Equal(t, queue.NeedsConflictInput, got.Phase, "a worker scoped to a different target must never dispatch a candidate it has no authority over")
	require.Zero(t, got.MedicDispatches)
}

// TestQueueMedicCmdTargetFlagScopesActions exercises --target end to end on
// the standalone `kitsoki queue medic` command: a candidate for a different
// target is left alone, and the same candidate is dispatched once --target
// is corrected to match.
func TestQueueMedicCmdTargetFlagScopesActions(t *testing.T) {
	project := t.TempDir()
	dir := filepath.Join(project, ".capsules", "queue")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	state := queue.State{Schema: queue.Schema, Candidates: []queue.Candidate{{
		ID: "queue-medic-standalone-target-test", ProjectID: "p", TargetRef: "main", TargetPolicy: queue.WaveAutoPolicy,
		Sequence: 1, Position: 1, Branch: "agent/conflict", SHA: strings.Repeat("a", 40),
		Admission: queue.ReceiptAdmission, ReceiptID: "sha256:receipt",
		Status: queue.NeedsConflictInput, Phase: queue.NeedsConflictInput,
		ConflictContinuation: "cont-1",
	}}}
	raw, err := json.MarshalIndent(state, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "state.json"), raw, 0o600))

	wrongTarget := queueMedicCmd()
	wrongTarget.SetArgs([]string{"--project", project, "--once", "--target", "other-target"})
	wrongTarget.SetOut(&strings.Builder{})
	require.NoError(t, wrongTarget.Execute())

	store := queue.Store{ProjectRoot: project}
	got, err := store.Get("queue-medic-standalone-target-test")
	require.NoError(t, err)
	require.Equal(t, queue.NeedsConflictInput, got.Phase, "queue medic --target other-target must not touch a main-target candidate")
	require.Zero(t, got.MedicDispatches)

	rightTarget := queueMedicCmd()
	rightTarget.SetArgs([]string{"--project", project, "--once", "--target", "main"})
	var out strings.Builder
	rightTarget.SetOut(&out)
	require.NoError(t, rightTarget.Execute())
	require.Contains(t, out.String(), "dispatch_resolver")

	got, err = store.Get("queue-medic-standalone-target-test")
	require.NoError(t, err)
	require.Equal(t, queue.Queued, got.Phase)
	require.Equal(t, 1, got.MedicDispatches)
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

func TestQueueProcessCLIRejectsWeakMainGateFromTrackedProfile(t *testing.T) {
	project := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.name", "CLI Gate Test"},
		{"config", "user.email", "cli-gate@example.invalid"},
	} {
		command := exec.Command("git", append([]string{"-C", project}, args...)...)
		require.NoError(t, command.Run())
	}
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".kitsoki"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".kitsoki", "project-profile.yaml"), []byte(
		"schema: project-profile/v1\ncommands:\n  change: make quick\n  full: make full\n"), 0o644))
	for _, args := range [][]string{
		{"add", ".kitsoki/project-profile.yaml"},
		{"commit", "-m", "tracked gate policy"},
	} {
		command := exec.Command("git", append([]string{"-C", project}, args...)...)
		require.NoError(t, command.Run())
	}
	cmd := queueProcessCmd()
	cmd.SetArgs([]string{"--project", project, "--target", "main", "--gate", "true"})
	cmd.SetOut(new(strings.Builder))
	err := cmd.Execute()
	require.ErrorContains(t, err, `requires tracked full gate "make full"`)
}

func TestQueueGatePolicyCLIResolvesKitsokiTrackedMainGate(t *testing.T) {
	project := testProjectRoot(t)
	required, configured, err := queue.RequiredGateCommand(project, "main")
	require.NoError(t, err)
	require.True(t, configured)
	require.Equal(t, "make test", required)

	cmd := queueGatePolicyCmd()
	cmd.SetArgs([]string{"--project", project, "--target", "main"})
	out := new(strings.Builder)
	cmd.SetOut(out)
	require.NoError(t, cmd.Execute())
	require.Equal(t, required, strings.TrimSpace(out.String()))
}

func testProjectRoot(t *testing.T) string {
	t.Helper()
	for _, root := range []string{os.Getenv("KITSOKI_TEST_PROJECT_ROOT"), os.Getenv("PWD")} {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			return filepath.Clean(root)
		}
	}
	cwd, err := os.Getwd()
	require.NoError(t, err)
	return filepath.Clean(filepath.Join(cwd, "..", ".."))
}
