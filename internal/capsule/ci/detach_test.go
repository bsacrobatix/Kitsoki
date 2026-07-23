package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/workerserver"
	"kitsoki/internal/objectstore"
)

// poolTestStubStartDetached substitutes the detached dispatch seam the same
// way poolTestStubRun substitutes the synchronous one.
func poolTestStubStartDetached(t *testing.T, status executor.ExecutionStatus, err error) *int {
	t.Helper()
	calls := new(int)
	orig := poolRemoteStartDetached
	poolRemoteStartDetached = func(_ context.Context, _ executor.HTTPRemoteWorker, prepared executor.Prepared, _ executor.EventSink) (executor.ExecutionStatus, error) {
		*calls++
		if status.ExecutionID == "" {
			status.ExecutionID = prepared.ID
		}
		return status, err
	}
	t.Cleanup(func() { poolRemoteStartDetached = orig })
	return calls
}

// poolTestStubObjectStore substitutes newPoolObjectStore with a shared
// in-memory fake so detached tests need no bucket credentials.
func poolTestStubObjectStore(t *testing.T) objectstore.Store {
	t.Helper()
	store := objectstore.NewFake()
	orig := newPoolObjectStore
	newPoolObjectStore = func(string, SourceBucket) (objectstore.Store, error) { return store, nil }
	t.Cleanup(func() { newPoolObjectStore = orig })
	return store
}

func detachTestPoolConfig() PoolExecutor {
	return PoolExecutor{
		TokenEnv:     "DO_TOKEN",
		Image:        "img",
		Size:         "s-1vcpu-1gb",
		Region:       "sgp1",
		SourceBucket: &SourceBucket{URL: "https://bucket.sgp1.digitaloceanspaces.com/kitsoki", KeyEnv: "POOL_TEST_BUCKET_KEY", SecretEnv: "POOL_TEST_BUCKET_SECRET"},
	}
}

func TestPoolProviderStartDetachedLeavesWorkerRunning(t *testing.T) {
	fixture := newPoolTestFixture(t, true)
	poolTestStubObjectStore(t)
	t.Setenv("POOL_TEST_BUCKET_KEY", "key")
	t.Setenv("POOL_TEST_BUCKET_SECRET", "secret")
	calls := poolTestStubStartDetached(t, executor.ExecutionStatus{Schema: executor.ExecutionStatusSchema, Status: "running", Stage: "registered"}, nil)

	provider, err := newPoolProvider("vm-pool", detachTestPoolConfig(), nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	starter := provider.(executor.DetachedStarter)
	status, err := starter.StartDetached(context.Background(), poolTestPrepared(t, "job-detach", "exec-detach"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 1 || status.ExecutionID != "exec-detach" || status.Status != "running" {
		t.Fatalf("calls=%d status=%+v", *calls, status)
	}
	if created := fixture.fake.Created(); len(created) != 1 {
		t.Fatalf("created %d instances, want 1", len(created))
	}
	// The droplet is executing the story: StartDetached must NOT destroy it.
	if destroyed := fixture.fake.Destroyed(); len(destroyed) != 0 {
		t.Fatalf("detached start destroyed workers: %v", destroyed)
	}
}

func TestPoolProviderStartDetachedReleasesWorkerOnDispatchFailure(t *testing.T) {
	fixture := newPoolTestFixture(t, true)
	poolTestStubObjectStore(t)
	t.Setenv("POOL_TEST_BUCKET_KEY", "key")
	t.Setenv("POOL_TEST_BUCKET_SECRET", "secret")
	poolTestStubStartDetached(t, executor.ExecutionStatus{}, context.DeadlineExceeded)

	provider, err := newPoolProvider("vm-pool", detachTestPoolConfig(), nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	starter := provider.(executor.DetachedStarter)
	if _, err := starter.StartDetached(context.Background(), poolTestPrepared(t, "job-detach-fail", "exec-detach-fail"), nil); err == nil {
		t.Fatal("dispatch failure did not propagate")
	}
	if destroyed := fixture.fake.Destroyed(); len(destroyed) != 1 {
		t.Fatalf("failed detached dispatch must release its droplet; destroyed = %v", destroyed)
	}
}

func TestPoolProviderStartDetachedRequiresSourceBucket(t *testing.T) {
	cfg := detachTestPoolConfig()
	cfg.SourceBucket = nil
	provider, err := newPoolProvider("vm-pool", cfg, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	starter := provider.(executor.DetachedStarter)
	_, err = starter.StartDetached(context.Background(), poolTestPrepared(t, "job-no-bucket", "exec-no-bucket"), nil)
	if err == nil || !strings.Contains(err.Error(), "source_bucket") {
		t.Fatalf("err = %v, want source_bucket requirement", err)
	}
}

func TestPoolProviderStatusReadsDurableBucketRunRecord(t *testing.T) {
	store := poolTestStubObjectStore(t)
	record := workerserver.RunRecord{
		Schema:         workerserver.RunRecordSchema,
		ExecutionID:    "exec-status",
		EnvelopeDigest: "sha256:envelope",
		SourceDigest:   "sha256:source",
		StoryDigest:    "sha256:story",
		Status:         "completed",
		Stage:          "terminal",
		StartedAt:      time.Date(2026, 7, 20, 1, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 7, 20, 1, 5, 0, 0, time.UTC),
		TerminalAt:     time.Date(2026, 7, 20, 1, 5, 0, 0, time.UTC),
		// A "completed" record has no failure class; the failed-record case
		// (FailureClass round-tripping through Status) is covered by
		// TestPoolProviderStatusSurfacesFailureClass below.
		Result: executor.Result{ExecutionID: "exec-status", ExitCode: 0, VerdictJSON: []byte(`{"schema":"capsule-ci-verdict/v1"}`)},
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), "runs/exec-status/run.json", bytes.NewReader(raw), int64(len(raw)), objectstore.PutOptions{ContentType: "application/json"}); err != nil {
		t.Fatal(err)
	}

	provider, err := newPoolProvider("vm-pool", detachTestPoolConfig(), nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	controller := provider.(executor.ExecutionController)
	status, err := controller.Status(context.Background(), "exec-status")
	if err != nil {
		t.Fatal(err)
	}
	if status.Schema != executor.ExecutionStatusSchema || status.Status != "completed" || status.Stage != "terminal" || status.ExecutionID != "exec-status" {
		t.Fatalf("status = %+v", status)
	}
	if len(status.Result.VerdictJSON) == 0 || status.Result.ExecutionID != "exec-status" {
		t.Fatalf("status result = %+v", status.Result)
	}

	if _, err := controller.Status(context.Background(), "exec-missing"); err == nil {
		t.Fatal("missing run record did not error")
	}
	if _, err := controller.RequestCancel(context.Background(), "exec-status"); err == nil {
		t.Fatal("detached cancel must refuse with a typed error")
	}
}

// TestPoolProviderStatusSurfacesFailureClass pins Status's additive
// RunRecord.FailureClass -> ExecutionStatus.FailureClass mapping for a
// failed durable run record.
func TestPoolProviderStatusSurfacesFailureClass(t *testing.T) {
	store := poolTestStubObjectStore(t)
	record := workerserver.RunRecord{
		Schema:         workerserver.RunRecordSchema,
		ExecutionID:    "exec-status-failed",
		EnvelopeDigest: "sha256:envelope",
		SourceDigest:   "sha256:source",
		StoryDigest:    "sha256:story",
		Status:         "failed",
		Stage:          "running_story",
		StartedAt:      time.Date(2026, 7, 20, 1, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 7, 20, 1, 5, 0, 0, time.UTC),
		TerminalAt:     time.Date(2026, 7, 20, 1, 5, 0, 0, time.UTC),
		Error:          "capsule worker: runner reported non-zero exit code 1",
		FailureClass:   executor.FailureClassAgentQuota,
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), "runs/exec-status-failed/run.json", bytes.NewReader(raw), int64(len(raw)), objectstore.PutOptions{ContentType: "application/json"}); err != nil {
		t.Fatal(err)
	}

	provider, err := newPoolProvider("vm-pool", detachTestPoolConfig(), nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	controller := provider.(executor.ExecutionController)
	status, err := controller.Status(context.Background(), "exec-status-failed")
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != "failed" || status.FailureClass != executor.FailureClassAgentQuota {
		t.Fatalf("status = %+v, want failure_class agent_quota", status)
	}
}

func TestPoolProviderReleaseDetachedDestroysLeasedWorkerIdempotently(t *testing.T) {
	fixture := newPoolTestFixture(t, true)
	poolTestStubObjectStore(t)
	t.Setenv("POOL_TEST_BUCKET_KEY", "key")
	t.Setenv("POOL_TEST_BUCKET_SECRET", "secret")
	poolTestStubStartDetached(t, executor.ExecutionStatus{Schema: executor.ExecutionStatusSchema, Status: "running", Stage: "registered"}, nil)

	root := t.TempDir()
	provider, err := newPoolProvider("vm-pool", detachTestPoolConfig(), nil, root)
	if err != nil {
		t.Fatal(err)
	}
	starter := provider.(executor.DetachedStarter)
	if _, err := starter.StartDetached(context.Background(), poolTestPrepared(t, "job-release", "exec-release"), nil); err != nil {
		t.Fatal(err)
	}
	releaser := provider.(executor.DetachedReleaser)
	if err := releaser.ReleaseDetached(context.Background(), "job-release"); err != nil {
		t.Fatal(err)
	}
	if destroyed := fixture.fake.Destroyed(); len(destroyed) != 1 {
		t.Fatalf("destroyed = %v, want the leased droplet destroyed", destroyed)
	}
	// Idempotent: the worker record is terminal now; nothing further happens.
	if err := releaser.ReleaseDetached(context.Background(), "job-release"); err != nil {
		t.Fatal(err)
	}
	if destroyed := fixture.fake.Destroyed(); len(destroyed) != 1 {
		t.Fatalf("second release destroyed again: %v", destroyed)
	}
}

// detachableFakeProvider wraps the executor fake with a DetachedStarter
// implementation so Service.Run's detach path is testable hermetically.
type detachableFakeProvider struct {
	executor.Provider
	status executor.ExecutionStatus
	calls  int
}

func (p *detachableFakeProvider) StartDetached(_ context.Context, prepared executor.Prepared, _ executor.EventSink) (executor.ExecutionStatus, error) {
	p.calls++
	status := p.status
	if status.ExecutionID == "" {
		status.ExecutionID = prepared.ID
	}
	return status, nil
}

func TestServiceRunDetachReturnsNonTerminalResultImmediately(t *testing.T) {
	root := t.TempDir()
	requireFiles(t, root)
	provider := &detachableFakeProvider{Provider: executor.NewFakeProvider("fake"), status: executor.ExecutionStatus{Schema: executor.ExecutionStatusSchema, Status: "running", Stage: "registered"}}
	service := Service{ProjectRoot: root, Jobs: artifactjob.NewMemoryStore(), Env: environment.Resolver{Probe: environment.ToolProbeFunc(func(context.Context, string) (string, error) { return "go1.25", nil })}, Provider: provider, Launcher: launcher(func(context.Context, executor.Prepared) (Verdict, error) {
		t.Fatal("detached dispatch must not drive the local launcher")
		return Verdict{}, nil
	})}
	result, err := service.Run(context.Background(), RunRequest{Pipeline: "change", Workspace: control.Handle{ID: "w", Generation: 1}, DefinitionDigest: "sha256:def", SourceDigest: "sha256:source", StoryDigest: "sha256:story", Trigger: Trigger{Kind: "local"}, Detach: true})
	if err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatalf("StartDetached calls = %d", provider.calls)
	}
	if result.Terminal || result.Job.Status != artifactjob.StatusRunning || result.Stage != RunStageRunning {
		t.Fatalf("detached result = job %s stage %s terminal %t", result.Job.Status, result.Stage, result.Terminal)
	}
	if result.Execution.ExecutionID == "" {
		t.Fatal("detached result has no execution id")
	}
	if result.PID != 0 {
		t.Fatalf("detached result recorded PID %d; orphan reconciliation would wrongly terminalize it", result.PID)
	}
}

func TestServiceRunDetachRejectsNonDetachableExecutor(t *testing.T) {
	root := t.TempDir()
	requireFiles(t, root)
	service := Service{ProjectRoot: root, Jobs: artifactjob.NewMemoryStore(), Env: environment.Resolver{Probe: environment.ToolProbeFunc(func(context.Context, string) (string, error) { return "go1.25", nil })}, Provider: executor.NewFakeProvider("fake"), Launcher: launcher(func(context.Context, executor.Prepared) (Verdict, error) {
		return Verdict{}, nil
	})}
	_, err := service.Run(context.Background(), RunRequest{Pipeline: "change", Workspace: control.Handle{ID: "w", Generation: 1}, DefinitionDigest: "sha256:def", SourceDigest: "sha256:source", StoryDigest: "sha256:story", Trigger: Trigger{Kind: "local"}, Detach: true})
	if err == nil || !strings.Contains(err.Error(), "does not support detached dispatch") {
		t.Fatalf("err = %v", err)
	}
}

func TestFileRunStoreFinalizeCollectedPromotesToTerminalDone(t *testing.T) {
	store := FileRunStore{ProjectRoot: t.TempDir()}
	verdict := Verdict{Schema: VerdictSchema, Pipeline: "change", Outcome: "passed", Summary: "collected", Checks: []Check{{ID: "test", Kind: "deterministic", Outcome: "passed", Evidence: []string{"artifact:test"}}}, PromotionEligible: true}
	seed := RunRecord{JobID: "job-collect", Result: RunResult{Job: artifactjob.Job{ID: "job-collect", Status: artifactjob.StatusInterrupted}, Stage: RunStageCollecting, Execution: executor.Result{ExecutionID: "exec-collect"}}}
	if err := store.Write(seed); err != nil {
		t.Fatal(err)
	}
	record, err := store.FinalizeCollected("job-collect", verdict, executor.Result{ExecutionID: "exec-collect", ExitCode: 0, VerdictJSON: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !record.Result.Terminal || record.Result.Job.Status != artifactjob.StatusDone || record.Result.Stage != RunStageFinished {
		t.Fatalf("finalized = job %s stage %s terminal %t", record.Result.Job.Status, record.Result.Stage, record.Result.Terminal)
	}
	if record.Result.Verdict.Outcome != "passed" || record.Result.Job.Summary != "collected" {
		t.Fatalf("finalized verdict = %+v", record.Result.Verdict)
	}
	// Idempotent for a terminal record.
	again, err := store.FinalizeCollected("job-collect", verdict, executor.Result{ExecutionID: "exec-collect"})
	if err != nil || !again.Result.Terminal {
		t.Fatalf("repeat finalize = %+v err=%v", again.Result.Terminal, err)
	}
	// A mismatched execution id must refuse.
	seed2 := seed
	seed2.JobID = "job-collect-2"
	seed2.Result.Job.ID = "job-collect-2"
	if err := store.Write(seed2); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FinalizeCollected("job-collect-2", verdict, executor.Result{ExecutionID: "exec-other"}); err == nil {
		t.Fatal("mismatched execution id did not refuse")
	}
}

func TestFileRunStoreProjectExposesExecutionIDWhileNonTerminal(t *testing.T) {
	store := FileRunStore{ProjectRoot: t.TempDir()}
	record := RunRecord{JobID: "job-projection", Result: RunResult{Job: artifactjob.Job{ID: "job-projection", Status: artifactjob.StatusRunning}, Stage: RunStageRunning, Execution: executor.Result{ExecutionID: "exec-projection"}}}
	projection := store.Project(record)
	if projection.Terminal {
		t.Fatalf("projection unexpectedly terminal: %+v", projection)
	}
	if projection.ExecutionID != "exec-projection" {
		t.Fatalf("projection.ExecutionID = %q, want exec-projection", projection.ExecutionID)
	}
}
