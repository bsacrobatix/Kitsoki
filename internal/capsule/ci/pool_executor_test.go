package ci

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/vmpool"
)

// -- config parse/validate -------------------------------------------------

func TestPoolExecutorConfigParsesAndValidates(t *testing.T) {
	raw := []byte(`
pool:
  token_env: DO_POOL_TEST_TOKEN
  image: "237561892"
  size: m-2vcpu-16gb
  region: sgp1
  vpc_uuid: 28009dea-0000-0000-0000-000000000000
  ssh_key_ids: ["53686578"]
  max_concurrent: 3
  source_bucket:
    url: https://kitsoki-test.sgp1.digitaloceanspaces.com
    key_env: DO_SPACES_KEY_ID
    secret_env: DO_KITSOKI_TEST_API_KEY
`)
	var remote Remote
	if err := yaml.Unmarshal(raw, &remote); err != nil {
		t.Fatal(err)
	}
	if remote.Pool == nil {
		t.Fatal("expected pool block to parse into a non-nil block")
	}
	if err := validatePoolExecutor("vm-pool", *remote.Pool); err != nil {
		t.Fatalf("valid pool executor rejected: %v", err)
	}
	if remote.Pool.MaxConcurrent != 3 || remote.Pool.Region != "sgp1" || remote.Pool.Size != "m-2vcpu-16gb" {
		t.Fatalf("pool = %#v", remote.Pool)
	}
}

func TestValidateRejectsPoolCombinedWithEndpointOrSourceBucket(t *testing.T) {
	base := PoolExecutor{TokenEnv: "DO_TOKEN", Size: "s-1vcpu-1gb", Region: "sgp1"}
	cases := []Remote{
		{Pool: &base, Endpoint: "https://worker.invalid"},
		{Pool: &base, CredentialEnv: "SOME_ENV"},
		{Pool: &base, CAFile: "ca.pem"},
		{Pool: &base, SourceBucket: &SourceBucket{URL: testBucketURL, KeyEnv: "K", SecretEnv: "S"}},
	}
	for i, remote := range cases {
		cfg := Config{Schema: Schema, Pipelines: map[string]Pipeline{}, Remotes: map[string]Remote{"vm-pool": remote}}
		cfg.Pipelines["noop"] = Pipeline{Story: "story", Triggers: []string{"local"}, Environment: "ci"}
		err := Validate(t.TempDir(), cfg)
		if err == nil || !strings.Contains(err.Error(), "pool executors cannot also set") {
			t.Fatalf("case %d: expected combination rejection, got %v", i, err)
		}
	}
}

func TestValidatePoolExecutorRequiresTokenEnvSizeRegion(t *testing.T) {
	valid := PoolExecutor{TokenEnv: "DO_TOKEN", Size: "s-1vcpu-1gb", Region: "sgp1"}
	if err := validatePoolExecutor("vm-pool", valid); err != nil {
		t.Fatalf("valid pool executor rejected: %v", err)
	}

	badTokenEnv := valid
	badTokenEnv.TokenEnv = "not-an-env-name"
	if err := validatePoolExecutor("vm-pool", badTokenEnv); err == nil || !strings.Contains(err.Error(), "token_env") {
		t.Fatalf("expected token_env rejection, got %v", err)
	}

	missingSize := valid
	missingSize.Size = ""
	if err := validatePoolExecutor("vm-pool", missingSize); err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("expected size rejection, got %v", err)
	}

	missingRegion := valid
	missingRegion.Region = ""
	if err := validatePoolExecutor("vm-pool", missingRegion); err == nil || !strings.Contains(err.Error(), "region") {
		t.Fatalf("expected region rejection, got %v", err)
	}

	negativeConcurrency := valid
	negativeConcurrency.MaxConcurrent = -1
	if err := validatePoolExecutor("vm-pool", negativeConcurrency); err == nil || !strings.Contains(err.Error(), "max_concurrent") {
		t.Fatalf("expected max_concurrent rejection, got %v", err)
	}

	emptySSHKeyID := valid
	emptySSHKeyID.SSHKeyIDs = []string{"53686578", "  "}
	if err := validatePoolExecutor("vm-pool", emptySSHKeyID); err == nil || !strings.Contains(err.Error(), "ssh_key_ids") {
		t.Fatalf("expected ssh_key_ids rejection, got %v", err)
	}
}

// TestValidatePoolExecutorAllowsEmptyImage documents the deliberate
// asymmetry with size/region: an unresolved image reference is a valid
// checked-in state (e.g. a snapshot still being published by a separate
// pipeline); Validate defers that check to executor-selection time
// (newPoolProvider/Select), never to Load/Validate, so doctor's no-spend
// preflight against .kitsoki/ci.yaml does not fail merely because the image
// pointer has not resolved yet.
func TestValidatePoolExecutorAllowsEmptyImage(t *testing.T) {
	pool := PoolExecutor{TokenEnv: "DO_TOKEN", Size: "s-1vcpu-1gb", Region: "sgp1"}
	if err := validatePoolExecutor("vm-pool", pool); err != nil {
		t.Fatalf("empty image should validate cleanly, got %v", err)
	}
}

func TestValidatePoolExecutorRejectsSourceBucketPrefixOrPresignTTL(t *testing.T) {
	base := PoolExecutor{TokenEnv: "DO_TOKEN", Size: "s-1vcpu-1gb", Region: "sgp1"}

	withPrefix := base
	withPrefix.SourceBucket = &SourceBucket{URL: testBucketURL, KeyEnv: "K", SecretEnv: "S", Prefix: "custom"}
	if err := validatePoolExecutor("vm-pool", withPrefix); err == nil || !strings.Contains(err.Error(), "prefix") {
		t.Fatalf("expected prefix rejection, got %v", err)
	}

	withTTL := base
	withTTL.SourceBucket = &SourceBucket{URL: testBucketURL, KeyEnv: "K", SecretEnv: "S", PresignTTL: "30m"}
	if err := validatePoolExecutor("vm-pool", withTTL); err == nil || !strings.Contains(err.Error(), "presign_ttl") {
		t.Fatalf("expected presign_ttl rejection, got %v", err)
	}
}

func TestValidatePoolExecutorAcceptsPlainSourceBucket(t *testing.T) {
	pool := PoolExecutor{
		TokenEnv: "DO_TOKEN", Size: "s-1vcpu-1gb", Region: "sgp1",
		SourceBucket: &SourceBucket{URL: testBucketURL, KeyEnv: "DO_SPACES_KEY_ID", SecretEnv: "DO_KITSOKI_TEST_API_KEY"},
	}
	if err := validatePoolExecutor("vm-pool", pool); err != nil {
		t.Fatalf("valid pool source_bucket rejected: %v", err)
	}
}

// TestValidatePoolExecutorNeverReadsCredentialsFromEnvironment pins the
// no-spend invariant: token_env and the source_bucket's key/secret env names
// are checked as names only, never resolved, so Validate succeeds with none
// of them set in the process environment.
func TestValidatePoolExecutorNeverReadsCredentialsFromEnvironment(t *testing.T) {
	pool := PoolExecutor{
		TokenEnv: "KITSOKI_NEVER_SET_POOL_TOKEN_PROBE", Size: "s-1vcpu-1gb", Region: "sgp1",
		SourceBucket: &SourceBucket{URL: testBucketURL, KeyEnv: "KITSOKI_NEVER_SET_KEY_PROBE", SecretEnv: "KITSOKI_NEVER_SET_SECRET_PROBE"},
	}
	if err := validatePoolExecutor("vm-pool", pool); err != nil {
		t.Fatalf("pool validation must not require credentials to be set: %v", err)
	}
}

// TestValidatePoolExecutorRejectsBadPassEnvUserAgentBackend covers the
// PassEnv/User/AgentBackend validation added alongside vmpool's service-user
// and pass-env support: each field is checked independently, and a valid
// combination of all three is accepted.
func TestValidatePoolExecutorRejectsBadPassEnvUserAgentBackend(t *testing.T) {
	valid := PoolExecutor{TokenEnv: "DO_TOKEN", Size: "s-1vcpu-1gb", Region: "sgp1"}

	badPassEnv := valid
	badPassEnv.PassEnv = []string{"not-an-env-name"}
	if err := validatePoolExecutor("vm-pool", badPassEnv); err == nil || !strings.Contains(err.Error(), "pass_env") {
		t.Fatalf("expected pass_env rejection, got %v", err)
	}

	badUser := valid
	badUser.User = "Bad User"
	if err := validatePoolExecutor("vm-pool", badUser); err == nil || !strings.Contains(err.Error(), "user") {
		t.Fatalf("expected user rejection, got %v", err)
	}

	for _, bad := range []string{"agent backend", "agent,backend", "agent\tbackend", " agent-backend", "agent-backend "} {
		badBackend := valid
		badBackend.AgentBackend = bad
		if err := validatePoolExecutor("vm-pool", badBackend); err == nil || !strings.Contains(err.Error(), "agent_backend") {
			t.Fatalf("expected agent_backend rejection for %q, got %v", bad, err)
		}
	}

	validCombo := valid
	validCombo.PassEnv = []string{"SYNTHETIC_API_KEY"}
	validCombo.User = "kitsoki"
	validCombo.AgentBackend = "claude"
	if err := validatePoolExecutor("vm-pool", validCombo); err != nil {
		t.Fatalf("valid pass_env/user/agent_backend combination rejected: %v", err)
	}
}

// -- ConfiguredExecutors.Select ---------------------------------------------

func TestConfiguredExecutorsSelectPoolReturnsPoolProvider(t *testing.T) {
	executors := ConfiguredExecutors{
		Builtins:    NewBuiltinExecutors(),
		ProjectRoot: "/project",
		Remotes: map[string]Remote{
			"vm-pool": {Pool: &PoolExecutor{TokenEnv: "DO_TOKEN", Image: "237561892", Size: "s-1vcpu-1gb", Region: "sgp1"}},
		},
	}
	provider, err := executors.Select(context.Background(), "vm-pool")
	if err != nil {
		t.Fatal(err)
	}
	pool, ok := provider.(*poolProvider)
	if !ok {
		t.Fatalf("provider type %T", provider)
	}
	if pool.name != "vm-pool" || pool.projectRoot != "/project" || pool.cfg.Image != "237561892" {
		t.Fatalf("pool provider = %#v", pool)
	}
}

func TestConfiguredExecutorsSelectPoolRequiresImage(t *testing.T) {
	executors := ConfiguredExecutors{
		Builtins: NewBuiltinExecutors(),
		Remotes: map[string]Remote{
			"vm-pool": {Pool: &PoolExecutor{TokenEnv: "DO_TOKEN", Size: "s-1vcpu-1gb", Region: "sgp1"}},
		},
	}
	_, err := executors.Select(context.Background(), "vm-pool")
	if err == nil || !strings.Contains(err.Error(), "image is required") {
		t.Fatalf("expected image-required rejection, got %v", err)
	}
}

// -- Describe / Prepare / AcceptPrepared: no-spend, never lease -------------

// failIfCalledProvisioner overrides ciVMPoolNewProvisioner for a test and
// fails it if the factory is ever invoked, proving the code path under test
// never reaches the point of resolving a Provisioner (and therefore never
// leases a worker).
func failIfCalledProvisioner(t *testing.T) {
	t.Helper()
	orig := ciVMPoolNewProvisioner
	ciVMPoolNewProvisioner = func(tokenEnv string) (vmpool.Provisioner, error) {
		t.Fatalf("ciVMPoolNewProvisioner unexpectedly invoked for token env %q", tokenEnv)
		return nil, nil
	}
	t.Cleanup(func() { ciVMPoolNewProvisioner = orig })
}

func testPoolEnvelope(t *testing.T, jobID string) executor.Envelope {
	t.Helper()
	lock, err := environment.SealLock(environment.Lock{Schema: environment.LockSchema, ID: "ci", DefinitionDigest: "sha256:env-def", Network: "live", Sandbox: "supervised"})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := executor.Seal(executor.Envelope{
		JobID:            jobID,
		ProjectID:        "project",
		DefinitionDigest: "sha256:def",
		Instance:         control.Handle{ID: "w", Generation: 1},
		SourceDigest:     "sha256:source",
		StoryPath:        "story",
		StoryDigest:      "sha256:story",
		Environment:      lock,
		// Empty ExternalWrite defaults to deny, which cannot be enforced
		// alongside live networking; a pool worker truthfully offers live.
		Policy: executor.Policy{Network: "live", ExternalWrite: "allow", MinimumSandbox: "supervised"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func TestPoolProviderDescribeIsStaticAndNeverLeases(t *testing.T) {
	failIfCalledProvisioner(t)
	provider, err := newPoolProvider("vm-pool", PoolExecutor{TokenEnv: "DO_TOKEN", Image: "img", Size: "s-1vcpu-1gb", Region: "sgp1"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	caps, err := provider.Describe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if caps.ID != "vmpool-worker" || caps.Isolation != "vm" || caps.Cancellable {
		t.Fatalf("capabilities = %#v", caps)
	}
	if len(caps.Networks) != 1 || caps.Networks[0] != "live" {
		t.Fatalf("networks = %v", caps.Networks)
	}
}

func TestPoolProviderPrepareValidatesWithoutLeasing(t *testing.T) {
	failIfCalledProvisioner(t)
	provider, err := newPoolProvider("vm-pool", PoolExecutor{TokenEnv: "DO_TOKEN", Image: "img", Size: "s-1vcpu-1gb", Region: "sgp1"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	envelope := testPoolEnvelope(t, "job-prepare")
	prepared, err := provider.Prepare(context.Background(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Placement != "remote" || prepared.Envelope.Digest != envelope.Digest {
		t.Fatalf("prepared = %#v", prepared)
	}
	// A second Prepare for the same envelope must hit the cache, not re-derive.
	again, err := provider.Prepare(context.Background(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != prepared.ID {
		t.Fatalf("expected cached prepared id, got %q vs %q", again.ID, prepared.ID)
	}
}

func TestPoolProviderAcceptPreparedNeverLeases(t *testing.T) {
	failIfCalledProvisioner(t)
	provider, err := newPoolProvider("vm-pool", PoolExecutor{TokenEnv: "DO_TOKEN", Image: "img", Size: "s-1vcpu-1gb", Region: "sgp1"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	envelope := testPoolEnvelope(t, "job-accept")
	prepared, err := provider.Prepare(context.Background(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	accepter, ok := provider.(executor.PreparedAcceptor)
	if !ok {
		t.Fatalf("provider %T does not implement PreparedAcceptor", provider)
	}
	if err := accepter.AcceptPrepared(context.Background(), prepared); err != nil {
		t.Fatalf("AcceptPrepared: %v", err)
	}
}

func TestPoolProviderCancelIsNotSupported(t *testing.T) {
	provider, err := newPoolProvider("vm-pool", PoolExecutor{TokenEnv: "DO_TOKEN", Image: "img", Size: "s-1vcpu-1gb", Region: "sgp1"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Cancel(context.Background(), "some-id"); err == nil {
		t.Fatal("expected Cancel to report unsupported")
	}
}

// -- preserve-on-failure / preserve TTL / timeout plumbing ------------------

// TestBuildPoolPlumbsPreserveOnFailureAndTTL covers defect 1's config
// threading in isolation from any lease/dispatch machinery: PoolExecutor's
// PreserveOnFailure/PreserveFailedTTL flow straight onto the constructed
// vmpool.Pool/vmpool.Config, and are off/zero (vmpool default) when unset.
func TestBuildPoolPlumbsPreserveOnFailureAndTTL(t *testing.T) {
	origProvisioner := ciVMPoolNewProvisioner
	ciVMPoolNewProvisioner = func(string) (vmpool.Provisioner, error) { return vmpool.NewFake(), nil }
	t.Cleanup(func() { ciVMPoolNewProvisioner = origProvisioner })

	def, err := newPoolProvider("vm-pool", PoolExecutor{TokenEnv: "DO_TOKEN", Image: "img", Size: "s-1vcpu-1gb", Region: "sgp1"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := def.(*poolProvider).buildPool()
	if err != nil {
		t.Fatal(err)
	}
	if pool.PreserveFailed {
		t.Fatalf("PreserveFailed should default to false")
	}
	if pool.Config.PreserveFailedTTL != 0 {
		t.Fatalf("PreserveFailedTTL = %v, want zero (vmpool applies its own default)", pool.Config.PreserveFailedTTL)
	}

	set, err := newPoolProvider("vm-pool", PoolExecutor{
		TokenEnv: "DO_TOKEN", Image: "img", Size: "s-1vcpu-1gb", Region: "sgp1",
		PreserveOnFailure: true, PreserveFailedTTL: 90 * time.Minute,
	}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	pool, err = set.(*poolProvider).buildPool()
	if err != nil {
		t.Fatal(err)
	}
	if !pool.PreserveFailed {
		t.Fatalf("PreserveFailed should be true when PreserveOnFailure is set")
	}
	if pool.Config.PreserveFailedTTL != 90*time.Minute {
		t.Fatalf("PreserveFailedTTL = %v, want 90m", pool.Config.PreserveFailedTTL)
	}
}

// TestBuildPoolDerivesReadyTimeoutFromProvisionTimeout pins defect 2's fix at
// the config-construction level: leaseWorker no longer sets an independent,
// shorter LeaseSpec.ReadyTimeout, so vmpool.LeaseSpec{}.withDefaults derives
// it from Config.ProvisionTimeout and the two can never drift apart again.
func TestBuildPoolDerivesReadyTimeoutFromProvisionTimeout(t *testing.T) {
	origProvisioner := ciVMPoolNewProvisioner
	ciVMPoolNewProvisioner = func(string) (vmpool.Provisioner, error) { return vmpool.NewFake(), nil }
	t.Cleanup(func() { ciVMPoolNewProvisioner = origProvisioner })

	provider, err := newPoolProvider("vm-pool", PoolExecutor{TokenEnv: "DO_TOKEN", Image: "img", Size: "s-1vcpu-1gb", Region: "sgp1"}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := provider.(*poolProvider).buildPool()
	if err != nil {
		t.Fatal(err)
	}
	if pool.Config.ProvisionTimeout != ciPoolProvisionTimeout {
		t.Fatalf("ProvisionTimeout = %v, want %v", pool.Config.ProvisionTimeout, ciPoolProvisionTimeout)
	}
}

// TestPoolProviderRunReadyTimeoutCoversFullProvisionBudget is the end-to-end
// regression for defect 2: a worker that becomes ready at 7 minutes — inside
// the old, wrong hardcoded 6m ReadyTimeout's dead zone, but well inside the
// 8m ProvisionTimeout budget — must still complete the lease instead of
// being abandoned by waitReady.
func TestPoolProviderRunReadyTimeoutCoversFullProvisionBudget(t *testing.T) {
	fake := vmpool.NewFake()
	origProvisioner := ciVMPoolNewProvisioner
	ciVMPoolNewProvisioner = func(string) (vmpool.Provisioner, error) { return fake, nil }
	t.Cleanup(func() { ciVMPoolNewProvisioner = origProvisioner })

	origHook := ciVMPoolDispatcherHook
	ciVMPoolDispatcherHook = func(d *vmpool.Dispatcher) {
		var elapsed time.Duration
		d.Sleep = func(ctx context.Context, dur time.Duration) error {
			elapsed += dur
			if elapsed >= 7*time.Minute {
				for _, inst := range mustList(t, fake, d.Pool.Config.WithDefaults().Tag) {
					fake.Activate(inst.ID, "203.0.113.60", "10.0.0.60")
				}
			}
			return ctx.Err()
		}
		d.HealthProbe = func(_ context.Context, w vmpool.Worker, _ *http.Client, _ string, _ int) error {
			if w.PublicIP == "" {
				return errors.New("not ready yet")
			}
			return nil
		}
	}
	t.Cleanup(func() { ciVMPoolDispatcherHook = origHook })

	poolTestStubRun(t, executor.Result{ExitCode: 0}, nil)
	provider, err := newPoolProvider("vm-pool", PoolExecutor{TokenEnv: "DO_TOKEN", Image: "img", Size: "s-1vcpu-1gb", Region: "sgp1"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Run(context.Background(), poolTestPrepared(t, "job-slow-boot", "exec-slow-boot"), nil, nil)
	if err != nil {
		t.Fatalf("expected the worker to be given the full ProvisionTimeout budget to become ready, got: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("result = %+v", result)
	}
}

// TestPoolProviderRunDoesNotInjectEngineURLEnv pins defect 3's removal: the
// retired KITSOKI_WORKER_ENGINE_URL boot hack must never reach the leased
// worker's user data, even when the controller process happens to have the
// (now-dead) variable set in its own environment.
func TestPoolProviderRunDoesNotInjectEngineURLEnv(t *testing.T) {
	t.Setenv("KITSOKI_WORKER_ENGINE_URL", "https://example.invalid/engine")
	fixture := newPoolTestFixture(t, true)
	poolTestStubRun(t, executor.Result{ExitCode: 0}, nil)
	provider, err := newPoolProvider("vm-pool", PoolExecutor{TokenEnv: "DO_TOKEN", Image: "img", Size: "s-1vcpu-1gb", Region: "sgp1"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Run(context.Background(), poolTestPrepared(t, "job-no-engine-url", "exec-no-engine-url"), nil, nil); err != nil {
		t.Fatal(err)
	}
	created := fixture.fake.Created()
	if len(created) != 1 {
		t.Fatalf("created %d instances, want exactly 1", len(created))
	}
	if strings.Contains(created[0].UserData, "KITSOKI_WORKER_ENGINE_URL") {
		t.Fatalf("user data still injects the retired KITSOKI_WORKER_ENGINE_URL:\n%s", created[0].UserData)
	}
}

// -- Run: lease / run / release orchestration -------------------------------

// poolTestFixture wires ciVMPoolNewProvisioner to a vmpool.Fake and
// ciVMPoolDispatcherHook to an instant Sleep that activates every fake
// instance and a stub HealthProbe, mirroring internal/capsule/vmpool's own
// dispatch_test.go helpers. It restores every package var on test cleanup.
type poolTestFixture struct {
	fake    *vmpool.Fake
	healthy bool // when false, HealthProbe always reports unhealthy
}

func newPoolTestFixture(t *testing.T, healthy bool) *poolTestFixture {
	t.Helper()
	fixture := &poolTestFixture{fake: vmpool.NewFake(), healthy: healthy}

	origProvisioner := ciVMPoolNewProvisioner
	ciVMPoolNewProvisioner = func(string) (vmpool.Provisioner, error) { return fixture.fake, nil }
	t.Cleanup(func() { ciVMPoolNewProvisioner = origProvisioner })

	origHook := ciVMPoolDispatcherHook
	ciVMPoolDispatcherHook = func(d *vmpool.Dispatcher) {
		var calls int
		var mu sync.Mutex
		d.Sleep = func(ctx context.Context, _ time.Duration) error {
			mu.Lock()
			calls++
			mu.Unlock()
			if fixture.healthy {
				for _, inst := range mustList(t, fixture.fake, d.Pool.Config.WithDefaults().Tag) {
					fixture.fake.Activate(inst.ID, "203.0.113.50", "10.0.0.50")
				}
			}
			return ctx.Err()
		}
		d.HealthProbe = func(_ context.Context, w vmpool.Worker, _ *http.Client, _ string, _ int) error { return nil }
	}
	t.Cleanup(func() { ciVMPoolDispatcherHook = origHook })
	return fixture
}

func mustList(t *testing.T, fake *vmpool.Fake, tag string) []vmpool.Instance {
	t.Helper()
	instances, err := fake.ListByTag(context.Background(), tag)
	if err != nil {
		t.Fatal(err)
	}
	return instances
}

func poolTestStubRun(t *testing.T, result executor.Result, err error) *int {
	t.Helper()
	calls := new(int)
	orig := poolRemoteRun
	poolRemoteRun = func(context.Context, executor.HTTPRemoteWorker, executor.Prepared, executor.Task, executor.EventSink) (executor.Result, error) {
		*calls++
		return result, err
	}
	t.Cleanup(func() { poolRemoteRun = orig })
	return calls
}

func poolTestPrepared(t *testing.T, jobID, execID string) executor.Prepared {
	t.Helper()
	envelope := testPoolEnvelope(t, jobID)
	return executor.Prepared{ID: execID, Envelope: envelope, Placement: "remote", Applied: envelope.Policy}
}

func TestPoolProviderRunLeasesRunsAndReleases(t *testing.T) {
	fixture := newPoolTestFixture(t, true)
	calls := poolTestStubRun(t, executor.Result{ExitCode: 0}, nil)
	provider, err := newPoolProvider("vm-pool", PoolExecutor{TokenEnv: "DO_TOKEN", Image: "img", Size: "s-1vcpu-1gb", Region: "sgp1"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Run(context.Background(), poolTestPrepared(t, "job-run-ok", "exec-run-ok"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || *calls != 1 {
		t.Fatalf("result=%+v calls=%d", result, *calls)
	}
	if created, destroyed := len(fixture.fake.Created()), len(fixture.fake.Destroyed()); created != 1 || destroyed != 1 {
		t.Fatalf("created=%d destroyed=%d, want lease+release exactly once", created, destroyed)
	}
}

// TestPoolProviderRunWiresUserPassEnvAndAgentBackendIntoLeaseUserData covers
// Run's threading of cfg.User/PassEnv/AgentBackend into the lease it takes:
// PassEnv is joined into KITSOKI_WORKER_PASS_ENV, AgentBackend into
// KITSOKI_WORKER_AGENT_BACKEND, and User onto the boot user data's
// systemd User= line — all observable on the fake provisioner's created
// instance.
func TestPoolProviderRunWiresUserPassEnvAndAgentBackendIntoLeaseUserData(t *testing.T) {
	fixture := newPoolTestFixture(t, true)
	poolTestStubRun(t, executor.Result{ExitCode: 0}, nil)
	cfg := PoolExecutor{
		TokenEnv:     "DO_TOKEN",
		Image:        "img",
		Size:         "s-1vcpu-1gb",
		Region:       "sgp1",
		User:         "kitsoki",
		PassEnv:      []string{"SYNTHETIC_API_KEY", "OTHER_KEY"},
		AgentBackend: "claude",
	}
	provider, err := newPoolProvider("vm-pool", cfg, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Run(context.Background(), poolTestPrepared(t, "job-run-wired", "exec-run-wired"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("result = %+v", result)
	}

	created := fixture.fake.Created()
	if len(created) != 1 {
		t.Fatalf("created %d instances, want exactly 1", len(created))
	}
	userData := created[0].UserData
	for _, want := range []string{
		"User=kitsoki",
		"KITSOKI_WORKER_PASS_ENV=SYNTHETIC_API_KEY,OTHER_KEY",
		"KITSOKI_WORKER_AGENT_BACKEND=claude",
	} {
		if !strings.Contains(userData, want) {
			t.Fatalf("user data missing %q\nfull user data:\n%s", want, userData)
		}
	}
}

func TestPoolProviderRunReleasesOnRunFailure(t *testing.T) {
	fixture := newPoolTestFixture(t, true)
	_ = poolTestStubRun(t, executor.Result{}, context.DeadlineExceeded)
	provider, err := newPoolProvider("vm-pool", PoolExecutor{TokenEnv: "DO_TOKEN", Image: "img", Size: "s-1vcpu-1gb", Region: "sgp1"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Run(context.Background(), poolTestPrepared(t, "job-run-fail", "exec-run-fail"), nil, nil); err == nil {
		t.Fatal("expected run failure to propagate")
	}
	// A post-ready run failure still releases: preservation guards the
	// pre-ready boot path, not a worker that answered and then failed.
	if destroyed := len(fixture.fake.Destroyed()); destroyed != 1 {
		t.Fatalf("destroyed=%d, want release after failed run", destroyed)
	}
}

// TestPoolProviderRunDestroysNeverReadyWorkerByDefault pins defect 1's fix:
// PreserveOnFailure defaults to false (the zero value), so autonomous CI
// dispatch destroys a pre-ready lease failure instead of silently billing a
// preserved droplet forever. Preservation is opt-in — see
// TestPoolProviderRunPreservesNeverReadyWorkerWhenPreserveOnFailureSet.
func TestPoolProviderRunDestroysNeverReadyWorkerByDefault(t *testing.T) {
	fixture := newPoolTestFixture(t, false)
	calls := poolTestStubRun(t, executor.Result{}, nil)
	provider, err := newPoolProvider("vm-pool", PoolExecutor{TokenEnv: "DO_TOKEN", Image: "img", Size: "s-1vcpu-1gb", Region: "sgp1"}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Run(context.Background(), poolTestPrepared(t, "job-never-ready-default", "exec-never-ready-default"), nil, nil)
	if err == nil || *calls != 0 {
		t.Fatalf("expected pre-ready failure without a remote run, err=%v calls=%d", err, *calls)
	}
	if !errors.Is(err, vmpool.ErrWorkerNotReady) {
		t.Fatalf("err=%v, want ErrWorkerNotReady in chain", err)
	}
	if destroyed := len(fixture.fake.Destroyed()); destroyed != 1 {
		t.Fatalf("destroyed=%d, want the never-ready instance destroyed (preserve_on_failure defaults off)", destroyed)
	}
}

// TestPoolProviderRunPreservesNeverReadyWorkerWhenPreserveOnFailureSet covers
// the explicit opt-in: PreserveOnFailure: true keeps the never-ready
// instance running for post-mortem, exactly as the old hardcoded-true
// behavior did.
func TestPoolProviderRunPreservesNeverReadyWorkerWhenPreserveOnFailureSet(t *testing.T) {
	fixture := newPoolTestFixture(t, false)
	calls := poolTestStubRun(t, executor.Result{}, nil)
	provider, err := newPoolProvider("vm-pool", PoolExecutor{TokenEnv: "DO_TOKEN", Image: "img", Size: "s-1vcpu-1gb", Region: "sgp1", PreserveOnFailure: true}, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Run(context.Background(), poolTestPrepared(t, "job-never-ready", "exec-never-ready"), nil, nil)
	if err == nil || *calls != 0 {
		t.Fatalf("expected pre-ready failure without a remote run, err=%v calls=%d", err, *calls)
	}
	if !errors.Is(err, vmpool.ErrWorkerNotReady) {
		t.Fatalf("err=%v, want ErrWorkerNotReady in chain", err)
	}
	// PreserveOnFailure: the never-ready instance is kept for post-mortem.
	if destroyed := len(fixture.fake.Destroyed()); destroyed != 0 {
		t.Fatalf("destroyed=%d, want preserved instance", destroyed)
	}
}
