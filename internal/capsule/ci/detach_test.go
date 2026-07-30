package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/vmpool"
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

	root := t.TempDir()
	provider, err := newPoolProvider("vm-pool", detachTestPoolConfig(), nil, root)
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

func TestPoolProviderStartDetachedRecoversActiveJobAfterControllerMappingLoss(t *testing.T) {
	fixture := newPoolTestFixture(t, true)
	poolTestStubObjectStore(t)
	t.Setenv("POOL_TEST_BUCKET_KEY", "key")
	t.Setenv("POOL_TEST_BUCKET_SECRET", "secret")
	calls := poolTestStubStartDetached(t, executor.ExecutionStatus{
		Schema: executor.ExecutionStatusSchema, Status: "running", Stage: "registered",
	}, nil)

	root := t.TempDir()
	prepared := poolTestPrepared(t, "job-detach-recover", "exec-detach-recover")
	first, err := newPoolProvider("vm-pool", detachTestPoolConfig(), nil, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.(executor.DetachedStarter).StartDetached(context.Background(), prepared, nil); err != nil {
		t.Fatal(err)
	}

	// Simulate a fresh controller process with no FileRunStore/dispatch
	// mapping. The provider must recover from the shared pool reservation,
	// not call Acquire again (which would return ErrJobActive).
	restarted, err := newPoolProvider("vm-pool", detachTestPoolConfig(), nil, root)
	if err != nil {
		t.Fatal(err)
	}
	status, err := restarted.(executor.DetachedStarter).StartDetached(context.Background(), prepared, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status.ExecutionID != prepared.ID || status.EnvelopeDigest != prepared.Envelope.Digest ||
		status.Status != "running" || status.Stage != "recovering" {
		t.Fatalf("recovered status = %+v", status)
	}
	if *calls != 1 {
		t.Fatalf("remote detached starts=%d, want exactly 1", *calls)
	}
	if created := fixture.fake.Created(); len(created) != 1 {
		t.Fatalf("created %d instances, want exactly 1", len(created))
	}
	state, err := (vmpool.Store{ProjectRoot: root}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Workers) != 1 || state.Workers[0].ExecutionID != prepared.ID ||
		state.Workers[0].EnvelopeDigest != prepared.Envelope.Digest {
		t.Fatalf("durable worker identity = %+v", state.Workers)
	}
}

func TestPoolProviderStartDetachedPreservesWorkerOnAmbiguousDispatchFailure(t *testing.T) {
	fixture := newPoolTestFixture(t, true)
	poolTestStubObjectStore(t)
	t.Setenv("POOL_TEST_BUCKET_KEY", "key")
	t.Setenv("POOL_TEST_BUCKET_SECRET", "secret")
	poolTestStubStartDetached(t, executor.ExecutionStatus{}, context.DeadlineExceeded)

	root := t.TempDir()
	provider, err := newPoolProvider("vm-pool", detachTestPoolConfig(), nil, root)
	if err != nil {
		t.Fatal(err)
	}
	starter := provider.(executor.DetachedStarter)
	if _, err := starter.StartDetached(context.Background(), poolTestPrepared(t, "job-detach-fail", "exec-detach-fail"), nil); err == nil {
		t.Fatal("dispatch failure did not propagate")
	}
	if destroyed := fixture.fake.Destroyed(); len(destroyed) != 0 {
		t.Fatalf("ambiguous detached dispatch must preserve its droplet for idempotent replay; destroyed = %v", destroyed)
	}
	state, err := (vmpool.Store{ProjectRoot: root}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Workers) != 1 || state.Workers[0].DispatchPhase != vmpool.DispatchStarting {
		t.Fatalf("dispatch state = %+v, want one starting reservation", state.Workers)
	}
}

func TestPoolProviderStartDetachedRecoversCrashAfterAcquireBeforeHTTP(t *testing.T) {
	fixture := newPoolTestFixture(t, true)
	poolTestStubObjectStore(t)
	t.Setenv("POOL_TEST_BUCKET_KEY", "key")
	t.Setenv("POOL_TEST_BUCKET_SECRET", "secret")
	calls := poolTestStubStartDetached(t, executor.ExecutionStatus{
		Schema: executor.ExecutionStatusSchema, Status: "running", Stage: "registered",
	}, nil)

	crash := errors.New("simulated crash after acquire before http")
	origHook := poolDetachedDispatchHook
	fired := false
	poolDetachedDispatchHook = func(stage string) error {
		if stage == "after_acquire_before_http" && !fired {
			fired = true
			return crash
		}
		return nil
	}
	t.Cleanup(func() { poolDetachedDispatchHook = origHook })

	root := t.TempDir()
	prepared := poolTestPrepared(t, "job-crash-before-http", "exec-crash-before-http")
	first, err := newPoolProvider("vm-pool", detachTestPoolConfig(), nil, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.(executor.DetachedStarter).StartDetached(context.Background(), prepared, nil); !errors.Is(err, crash) {
		t.Fatalf("first dispatch err = %v, want simulated crash", err)
	}
	if *calls != 0 {
		t.Fatalf("remote starts before retry = %d, want 0", *calls)
	}

	restarted, err := newPoolProvider("vm-pool", detachTestPoolConfig(), nil, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.(executor.DetachedStarter).StartDetached(context.Background(), prepared, nil); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatalf("remote starts after retry = %d, want 1", *calls)
	}
	assertSingleStartedDispatch(t, root, fixture, prepared)
}

func TestPoolProviderStartDetachedRecoversCrashAfterHTTPBeforeMarker(t *testing.T) {
	fixture := newPoolTestFixture(t, true)
	poolTestStubObjectStore(t)
	t.Setenv("POOL_TEST_BUCKET_KEY", "key")
	t.Setenv("POOL_TEST_BUCKET_SECRET", "secret")

	origStart := poolRemoteStartDetached
	httpCalls := 0
	registrations := map[string]struct{}{}
	poolRemoteStartDetached = func(_ context.Context, _ executor.HTTPRemoteWorker, prepared executor.Prepared, _ executor.EventSink) (executor.ExecutionStatus, error) {
		httpCalls++
		registrations[prepared.ID] = struct{}{}
		return executor.ExecutionStatus{
			Schema:         executor.ExecutionStatusSchema,
			ExecutionID:    prepared.ID,
			EnvelopeDigest: prepared.Envelope.Digest,
			Status:         "running",
			Stage:          "registered",
		}, nil
	}
	t.Cleanup(func() { poolRemoteStartDetached = origStart })

	crash := errors.New("simulated crash after http before marker")
	origHook := poolDetachedDispatchHook
	fired := false
	poolDetachedDispatchHook = func(stage string) error {
		if stage == "after_http_before_marker" && !fired {
			fired = true
			return crash
		}
		return nil
	}
	t.Cleanup(func() { poolDetachedDispatchHook = origHook })

	root := t.TempDir()
	prepared := poolTestPrepared(t, "job-crash-after-http", "exec-crash-after-http")
	first, err := newPoolProvider("vm-pool", detachTestPoolConfig(), nil, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.(executor.DetachedStarter).StartDetached(context.Background(), prepared, nil); !errors.Is(err, crash) {
		t.Fatalf("first dispatch err = %v, want simulated crash", err)
	}

	restarted, err := newPoolProvider("vm-pool", detachTestPoolConfig(), nil, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.(executor.DetachedStarter).StartDetached(context.Background(), prepared, nil); err != nil {
		t.Fatal(err)
	}
	if httpCalls != 2 {
		t.Fatalf("HTTP attempts = %d, want replay after ambiguous crash", httpCalls)
	}
	if len(registrations) != 1 {
		t.Fatalf("unique remote registrations = %d, want 1", len(registrations))
	}
	assertSingleStartedDispatch(t, root, fixture, prepared)
}

func assertSingleStartedDispatch(t *testing.T, root string, fixture *poolTestFixture, prepared executor.Prepared) {
	t.Helper()
	if created := fixture.fake.Created(); len(created) != 1 {
		t.Fatalf("created %d instances, want exactly 1", len(created))
	}
	if destroyed := fixture.fake.Destroyed(); len(destroyed) != 0 {
		t.Fatalf("destroyed instances = %v, want none", destroyed)
	}
	state, err := (vmpool.Store{ProjectRoot: root}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Workers) != 1 ||
		state.Workers[0].ExecutionID != prepared.ID ||
		state.Workers[0].EnvelopeDigest != prepared.Envelope.Digest ||
		state.Workers[0].DispatchPhase != vmpool.DispatchStarted {
		t.Fatalf("durable dispatch = %+v", state.Workers)
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

func TestConfiguredPoolStateRootCapsConcurrencyAcrossWorkspaces(t *testing.T) {
	fake := vmpool.NewFake()
	origProvisioner := ciVMPoolNewProvisioner
	ciVMPoolNewProvisioner = func(string) (vmpool.Provisioner, error) { return fake, nil }
	t.Cleanup(func() { ciVMPoolNewProvisioner = origProvisioner })

	origHook := ciVMPoolDispatcherHook
	ciVMPoolDispatcherHook = func(d *vmpool.Dispatcher) {
		d.Sleep = func(ctx context.Context, _ time.Duration) error {
			instances, err := fake.ListByTag(ctx, d.Pool.Config.WithDefaults().Tag)
			if err != nil {
				return err
			}
			for _, instance := range instances {
				fake.Activate(instance.ID, "203.0.113.70", "10.0.0.70")
			}
			return ctx.Err()
		}
		d.HealthProbe = func(context.Context, vmpool.Worker, *http.Client, string, int) error { return nil }
	}
	t.Cleanup(func() { ciVMPoolDispatcherHook = origHook })

	poolTestStubObjectStore(t)
	t.Setenv("POOL_TEST_BUCKET_KEY", "key")
	t.Setenv("POOL_TEST_BUCKET_SECRET", "secret")
	origStart := poolRemoteStartDetached
	poolRemoteStartDetached = func(_ context.Context, _ executor.HTTPRemoteWorker, prepared executor.Prepared, _ executor.EventSink) (executor.ExecutionStatus, error) {
		return executor.ExecutionStatus{Schema: executor.ExecutionStatusSchema, ExecutionID: prepared.ID, Status: "running", Stage: "registered"}, nil
	}
	t.Cleanup(func() { poolRemoteStartDetached = origStart })

	outer := t.TempDir()
	cfg := detachTestPoolConfig()
	cfg.MaxConcurrent = 3
	providers := make([]executor.Provider, 4)
	workspaces := make([]string, 4)
	for i := range providers {
		workspaces[i] = t.TempDir()
		configured := ConfiguredExecutors{
			Builtins:      NewBuiltinExecutors(),
			ProjectRoot:   workspaces[i],
			PoolStateRoot: outer,
			Remotes:       map[string]Remote{"vm-pool": {Pool: &cfg}},
		}
		var err error
		providers[i], err = configured.Select(context.Background(), "vm-pool")
		if err != nil {
			t.Fatal(err)
		}
	}

	errs := make([]error, len(providers))
	var wg sync.WaitGroup
	for i := range providers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = providers[i].(executor.DetachedStarter).StartDetached(
				context.Background(),
				poolTestPrepared(t, fmt.Sprintf("job-shared-%d", i), fmt.Sprintf("exec-shared-%d", i)),
				nil,
			)
		}(i)
	}
	wg.Wait()

	var full, admitted int
	var admittedJob string
	for i, err := range errs {
		switch {
		case err == nil:
			admitted++
			admittedJob = fmt.Sprintf("job-shared-%d", i)
		case errors.Is(err, vmpool.ErrPoolFull):
			full++
		default:
			t.Fatalf("workspace %d: unexpected acquire error: %v", i, err)
		}
	}
	if admitted != 3 || full != 1 {
		t.Fatalf("admitted=%d pool_full=%d errors=%v, want exactly 3/1", admitted, full, errs)
	}
	if created := len(fake.Created()); created != 3 {
		t.Fatalf("provision calls=%d, want exactly shared capacity 3", created)
	}
	state, err := (vmpool.Store{ProjectRoot: outer}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := state.ActiveCount(); got != 3 {
		t.Fatalf("outer active count=%d, want 3", got)
	}
	for _, workspace := range workspaces {
		if _, err := os.Stat(filepath.Join(workspace, ".capsules", "vmpool", "state.json")); !os.IsNotExist(err) {
			t.Fatalf("workspace-local pool state exists under %s: %v", workspace, err)
		}
	}

	if err := providers[0].(executor.DetachedReleaser).ReleaseDetached(context.Background(), admittedJob); err != nil {
		t.Fatal(err)
	}
	var blocked int
	for i, err := range errs {
		if errors.Is(err, vmpool.ErrPoolFull) {
			blocked = i
			break
		}
	}
	if _, err := providers[blocked].(executor.DetachedStarter).StartDetached(
		context.Background(),
		poolTestPrepared(t, "job-retry-after-release", "exec-retry-after-release"),
		nil,
	); err != nil {
		t.Fatalf("fourth workspace did not acquire released shared slot: %v", err)
	}
	if created := len(fake.Created()); created != 4 {
		t.Fatalf("provision calls after released slot=%d, want 4", created)
	}
}

func TestConfiguredPoolStateRootReopensDetachedLeaseFromAnotherWorkspace(t *testing.T) {
	fixture := newPoolTestFixture(t, true)
	poolTestStubObjectStore(t)
	t.Setenv("POOL_TEST_BUCKET_KEY", "key")
	t.Setenv("POOL_TEST_BUCKET_SECRET", "secret")
	poolTestStubStartDetached(t, executor.ExecutionStatus{Schema: executor.ExecutionStatusSchema, Status: "running", Stage: "registered"}, nil)

	outer := t.TempDir()
	firstWorkspace := t.TempDir()
	secondWorkspace := t.TempDir()
	cfg := Config{Remotes: map[string]Remote{"vm-pool": {Pool: ptrPoolExecutor(detachTestPoolConfig())}}}
	first := NewConfiguredExecutors(cfg)
	first.ProjectRoot = firstWorkspace
	first.PoolStateRoot = outer
	firstProvider, err := first.Select(context.Background(), "vm-pool")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := firstProvider.(executor.DetachedStarter).StartDetached(
		context.Background(),
		poolTestPrepared(t, "job-cross-workspace-release", "exec-cross-workspace-release"),
		nil,
	); err != nil {
		t.Fatal(err)
	}

	second := NewConfiguredExecutors(cfg)
	second.ProjectRoot = secondWorkspace
	second.PoolStateRoot = outer
	secondProvider, err := second.Select(context.Background(), "vm-pool")
	if err != nil {
		t.Fatal(err)
	}
	if err := secondProvider.(executor.DetachedReleaser).ReleaseDetached(context.Background(), "job-cross-workspace-release"); err != nil {
		t.Fatal(err)
	}
	if got := len(fixture.fake.Destroyed()); got != 1 {
		t.Fatalf("destroyed=%d, want exact shared worker released once", got)
	}
}

func TestConfiguredPoolWithoutPoolStateRootPreservesWorkspaceLocalStore(t *testing.T) {
	fixture := newPoolTestFixture(t, true)
	poolTestStubObjectStore(t)
	t.Setenv("POOL_TEST_BUCKET_KEY", "key")
	t.Setenv("POOL_TEST_BUCKET_SECRET", "secret")
	poolTestStubStartDetached(t, executor.ExecutionStatus{Schema: executor.ExecutionStatusSchema, Status: "running", Stage: "registered"}, nil)

	workspace := t.TempDir()
	cfg := Config{Remotes: map[string]Remote{"vm-pool": {Pool: ptrPoolExecutor(detachTestPoolConfig())}}}
	configured := NewConfiguredExecutors(cfg)
	configured.ProjectRoot = workspace
	provider, err := configured.Select(context.Background(), "vm-pool")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.(executor.DetachedStarter).StartDetached(
		context.Background(),
		poolTestPrepared(t, "job-compat-local-store", "exec-compat-local-store"),
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace, ".capsules", "vmpool", "state.json")); err != nil {
		t.Fatalf("standalone workspace-local pool state missing: %v", err)
	}
	if len(fixture.fake.Created()) != 1 {
		t.Fatalf("created=%d, want 1", len(fixture.fake.Created()))
	}
}

func ptrPoolExecutor(value PoolExecutor) *PoolExecutor { return &value }

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
