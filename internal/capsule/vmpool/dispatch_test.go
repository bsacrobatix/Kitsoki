package vmpool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"kitsoki/internal/objectstore"
)

// dspActivateAll activates every instance the fake provisioner currently
// knows about with a fixed public/private IP pair. Tests use it from inside
// an injected Dispatcher.Sleep so the fake instance becomes "active" between
// readiness polls without depending on which sequential ID the pool
// assigned it (which is timing-dependent under concurrent leases).
func dspActivateAll(ctx context.Context, t *testing.T, fake *Fake, tag, publicIP, privateIP string) {
	t.Helper()
	instances, err := fake.ListByTag(ctx, tag)
	if err != nil {
		t.Fatalf("dspActivateAll: list by tag: %v", err)
	}
	for _, inst := range instances {
		fake.Activate(inst.ID, publicIP, privateIP)
	}
}

func dspTestConfig() Config {
	return Config{
		Tag:              "dsp-test-tag",
		NamePrefix:       "dsp-worker-",
		MaxConcurrent:    5,
		Region:           "sgp1",
		Size:             "s-1vcpu-1gb",
		Image:            "dsp-test-image",
		ProvisionTimeout: time.Hour,
		ActivityTimeout:  time.Hour,
		MaxLifetime:      24 * time.Hour,
	}
}

func dspNewPool(root string, cfg Config, prov Provisioner) *Pool {
	return &Pool{
		// LockWait tolerates the lock-file contention that concurrent Lease
		// calls on one Dispatcher legitimately produce (each Lease drives
		// several Store Update/Load calls); Store's zero-value default would
		// instead fail immediately with ErrBusy under that contention.
		Store:       &Store{ProjectRoot: root, LockWait: 2 * time.Second},
		Provisioner: prov,
		Config:      cfg,
	}
}

// dspInstantSleep is a Dispatcher.Sleep that never actually sleeps; onEach,
// if set, runs once per invocation (1-indexed call count) before returning,
// letting a test script Provisioner state transitions in lockstep with the
// Dispatcher's readiness loop.
func dspInstantSleep(onEach func(call int)) func(context.Context, time.Duration) error {
	var mu sync.Mutex
	calls := 0
	return func(ctx context.Context, _ time.Duration) error {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		if onEach != nil {
			onEach(call)
		}
		return ctx.Err()
	}
}

// dspAlwaysHealthy is a Dispatcher.HealthProbe stub that always reports
// healthy once the worker has a public IP, without making any network call —
// standing up a real TLS listener bound to the minted per-lease identity is
// out of scope for these hermetic tests.
func dspAlwaysHealthy(_ context.Context, w Worker, _ *http.Client, _ string, _ int) error {
	if w.PublicIP == "" {
		return fmt.Errorf("dsp: not ready yet")
	}
	return nil
}

// dspNeverHealthy always reports unhealthy, regardless of worker state.
func dspNeverHealthy(context.Context, Worker, *http.Client, string, int) error {
	return fmt.Errorf("dsp: never healthy")
}

func TestDspLeaseHappyPath(t *testing.T) {
	root := t.TempDir()
	cfg := dspTestConfig()
	fake := NewFake()
	pool := dspNewPool(root, cfg, fake)

	sleep := dspInstantSleep(func(call int) {
		if call == 1 {
			dspActivateAll(context.Background(), t, fake, cfg.Tag, "203.0.113.9", "10.0.0.9")
		}
	})
	dispatcher := &Dispatcher{Pool: pool, Sleep: sleep, HealthProbe: dspAlwaysHealthy}

	lease, err := dispatcher.Lease(context.Background(), LeaseSpec{JobID: "job1"})
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if lease == nil {
		t.Fatal("Lease returned nil lease with nil error")
	}

	wantEndpoint := "https://203.0.113.9:7443"
	if lease.Endpoint != wantEndpoint {
		t.Fatalf("Endpoint=%q, want %q", lease.Endpoint, wantEndpoint)
	}
	if lease.Remote.Endpoint != wantEndpoint {
		t.Fatalf("Remote.Endpoint=%q, want %q", lease.Remote.Endpoint, wantEndpoint)
	}
	if lease.Worker.Status != StatusRunning {
		t.Fatalf("Worker.Status=%s, want running", lease.Worker.Status)
	}
	if lease.Remote.Credential == nil {
		t.Fatal("Remote.Credential is nil")
	}
	if tok, err := lease.Remote.Credential(context.Background()); err != nil || tok == "" {
		t.Fatalf("Remote.Credential() = %q, %v", tok, err)
	}
	if lease.Remote.SourceObjects != nil {
		t.Fatal("Remote.SourceObjects should be nil when LeaseSpec.Objects is unset")
	}
	if lease.Remote.Client == nil {
		t.Fatal("Remote.Client is nil")
	}
	transport, ok := lease.Remote.Client.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil {
		t.Fatalf("Remote.Client.Transport = %#v, want *http.Transport with TLSClientConfig", lease.Remote.Client.Transport)
	}
	if transport.TLSClientConfig.RootCAs == nil {
		t.Fatal("TLSClientConfig.RootCAs is nil, want the pinned per-lease CA")
	}
	if transport.TLSClientConfig.ServerName == "" {
		t.Fatal("TLSClientConfig.ServerName is empty, want the minted certificate's CommonName")
	}
	wantServerName := cfg.NamePrefix + "job1"
	if transport.TLSClientConfig.ServerName != wantServerName {
		t.Fatalf("TLSClientConfig.ServerName=%q, want %q", transport.TLSClientConfig.ServerName, wantServerName)
	}

	// The pool record must be released cleanly on request, and idempotently.
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if err := lease.Release(context.Background()); err != nil {
		t.Fatalf("second Release should be a no-op, got %v", err)
	}
	if got := len(fake.Destroyed()); got != 1 {
		t.Fatalf("destroyed %d instances, want exactly 1", got)
	}
}

func TestDspLeaseUserDataCarriesMintedSecrets(t *testing.T) {
	root := t.TempDir()
	cfg := dspTestConfig()
	fake := NewFake()
	pool := dspNewPool(root, cfg, fake)

	sleep := dspInstantSleep(func(call int) {
		if call == 1 {
			dspActivateAll(context.Background(), t, fake, cfg.Tag, "203.0.113.10", "10.0.0.10")
		}
	})
	dispatcher := &Dispatcher{Pool: pool, Sleep: sleep, HealthProbe: dspAlwaysHealthy}

	lease, err := dispatcher.Lease(context.Background(), LeaseSpec{JobID: "job2"})
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	t.Cleanup(func() { _ = lease.Release(context.Background()) })

	created := fake.Created()
	if len(created) != 1 {
		t.Fatalf("created %d instances, want exactly 1", len(created))
	}
	userData := created[0].UserData

	token, err := lease.Remote.Credential(context.Background())
	if err != nil {
		t.Fatalf("Remote.Credential: %v", err)
	}
	if !strings.Contains(userData, token) {
		t.Fatal("user data does not contain the minted worker token")
	}
	if !strings.Contains(userData, "BEGIN CERTIFICATE") {
		t.Fatal("user data does not contain the minted TLS certificate")
	}
	if !strings.Contains(userData, "EC PRIVATE KEY") {
		t.Fatal("user data does not contain the minted TLS private key")
	}
}

func TestDspLeaseReadyTimeoutDestroysAndReturnsTypedError(t *testing.T) {
	root := t.TempDir()
	cfg := dspTestConfig()
	fake := NewFake()
	pool := dspNewPool(root, cfg, fake)

	// The instance never activates and the health probe never succeeds, so
	// waitReady must exhaust ReadyTimeout via its poll-interval counter
	// (no real sleeping, since Sleep is an instant no-op).
	dispatcher := &Dispatcher{
		Pool:        pool,
		Sleep:       dspInstantSleep(nil),
		HealthProbe: dspNeverHealthy,
	}

	spec := LeaseSpec{JobID: "job3", PollInterval: time.Second, ReadyTimeout: 3 * time.Second}
	lease, err := dispatcher.Lease(context.Background(), spec)
	if lease != nil {
		t.Fatalf("expected nil lease on timeout, got %#v", lease)
	}
	if !errors.Is(err, ErrWorkerNotReady) {
		t.Fatalf("err=%v, want ErrWorkerNotReady", err)
	}
	if got := len(fake.Destroyed()); got != 1 {
		t.Fatalf("destroyed %d instances, want exactly 1 (the never-ready droplet)", got)
	}

	workers, err := pool.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 1 || !workers[0].Status.Terminal() {
		t.Fatalf("workers=%#v, want the lease's worker record terminal", workers)
	}
}

func TestDspLeaseHealthFlappingThenSuccess(t *testing.T) {
	root := t.TempDir()
	cfg := dspTestConfig()
	fake := NewFake()
	pool := dspNewPool(root, cfg, fake)

	var probeCalls int
	var mu sync.Mutex
	flappingProbe := func(ctx context.Context, w Worker, client *http.Client, token string, port int) error {
		mu.Lock()
		probeCalls++
		call := probeCalls
		mu.Unlock()
		if w.PublicIP == "" {
			return fmt.Errorf("dsp: no ip yet")
		}
		if call < 3 {
			return fmt.Errorf("dsp: not healthy yet (call %d)", call)
		}
		return nil
	}

	sleep := dspInstantSleep(func(call int) {
		if call == 1 {
			dspActivateAll(context.Background(), t, fake, cfg.Tag, "203.0.113.11", "10.0.0.11")
		}
	})
	dispatcher := &Dispatcher{Pool: pool, Sleep: sleep, HealthProbe: flappingProbe}

	spec := LeaseSpec{JobID: "job4", PollInterval: time.Second, ReadyTimeout: time.Minute}
	lease, err := dispatcher.Lease(context.Background(), spec)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	t.Cleanup(func() { _ = lease.Release(context.Background()) })

	if lease.Worker.Status != StatusRunning {
		t.Fatalf("Worker.Status=%s, want running", lease.Worker.Status)
	}
	mu.Lock()
	calls := probeCalls
	mu.Unlock()
	if calls < 3 {
		t.Fatalf("health probe called %d times, want at least 3 (flap then succeed)", calls)
	}
}

func TestDspLeaseWithObjectsSetsSourceObjects(t *testing.T) {
	root := t.TempDir()
	cfg := dspTestConfig()
	fake := NewFake()
	pool := dspNewPool(root, cfg, fake)

	sleep := dspInstantSleep(func(call int) {
		if call == 1 {
			dspActivateAll(context.Background(), t, fake, cfg.Tag, "203.0.113.12", "10.0.0.12")
		}
	})
	dispatcher := &Dispatcher{Pool: pool, Sleep: sleep, HealthProbe: dspAlwaysHealthy}

	lease, err := dispatcher.Lease(context.Background(), LeaseSpec{JobID: "job5", Objects: dspFakeObjectStore{}})
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	t.Cleanup(func() { _ = lease.Release(context.Background()) })

	if lease.Remote.SourceObjects == nil {
		t.Fatal("Remote.SourceObjects should be set when LeaseSpec.Objects is configured")
	}
}

func TestDspLeaseOutputsEnvMissingCredentialErrors(t *testing.T) {
	root := t.TempDir()
	cfg := dspTestConfig()
	fake := NewFake()
	pool := dspNewPool(root, cfg, fake)
	dispatcher := &Dispatcher{Pool: pool, Sleep: dspInstantSleep(nil), HealthProbe: dspAlwaysHealthy}

	spec := LeaseSpec{
		JobID:            "job6",
		BucketURL:        "https://bucket.sgp1.digitaloceanspaces.com",
		OutputsKeyEnv:    "DSP_TEST_MISSING_KEY_ENV",
		OutputsSecretEnv: "DSP_TEST_MISSING_SECRET_ENV",
	}
	lease, err := dispatcher.Lease(context.Background(), spec)
	if lease != nil {
		t.Fatalf("expected nil lease, got %#v", lease)
	}
	if err == nil || !strings.Contains(err.Error(), "DSP_TEST_MISSING_KEY_ENV") {
		t.Fatalf("err=%v, want it to name the missing env var", err)
	}
	// No worker record or instance should exist: the credential resolution
	// failure happens before Acquire.
	if got := len(fake.Created()); got != 0 {
		t.Fatalf("created %d instances, want 0 (should fail before acquiring)", got)
	}
	workers, err := pool.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 0 {
		t.Fatalf("workers=%#v, want none", workers)
	}
}

func TestDspLeaseOutputsEnvWiresWorkerBootEnv(t *testing.T) {
	t.Setenv("DSP_TEST_ACCESS_KEY", "dsp-access-key-value")
	t.Setenv("DSP_TEST_SECRET_KEY", "dsp-secret-key-value")

	root := t.TempDir()
	cfg := dspTestConfig()
	fake := NewFake()
	pool := dspNewPool(root, cfg, fake)

	sleep := dspInstantSleep(func(call int) {
		if call == 1 {
			dspActivateAll(context.Background(), t, fake, cfg.Tag, "203.0.113.13", "10.0.0.13")
		}
	})
	dispatcher := &Dispatcher{Pool: pool, Sleep: sleep, HealthProbe: dspAlwaysHealthy}

	spec := LeaseSpec{
		JobID:            "job7",
		BucketURL:        "https://bucket.sgp1.digitaloceanspaces.com",
		OutputsKeyEnv:    "DSP_TEST_ACCESS_KEY",
		OutputsSecretEnv: "DSP_TEST_SECRET_KEY",
	}
	lease, err := dispatcher.Lease(context.Background(), spec)
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	t.Cleanup(func() { _ = lease.Release(context.Background()) })

	created := fake.Created()
	if len(created) != 1 {
		t.Fatalf("created %d instances, want exactly 1", len(created))
	}
	userData := created[0].UserData
	for _, want := range []string{
		"KITSOKI_WORKER_OUTPUTS_URL=" + spec.BucketURL,
		"KITSOKI_WORKER_OUTPUTS_KEY_ENV=KITSOKI_WORKER_OUTPUTS_ACCESS_KEY",
		"KITSOKI_WORKER_OUTPUTS_SECRET_ENV=KITSOKI_WORKER_OUTPUTS_SECRET_KEY",
		"KITSOKI_WORKER_OUTPUTS_ACCESS_KEY=dsp-access-key-value",
		"KITSOKI_WORKER_OUTPUTS_SECRET_KEY=dsp-secret-key-value",
	} {
		if !strings.Contains(userData, want) {
			t.Fatalf("user data missing %q", want)
		}
	}
}

func TestDspLeaseConcurrentDoesNotRaceOnSharedPool(t *testing.T) {
	root := t.TempDir()
	cfg := dspTestConfig()
	cfg.MaxConcurrent = 10
	fake := NewFake()
	pool := dspNewPool(root, cfg, fake)

	// A single Sleep hook shared by every concurrent Lease call: since it
	// activates every known fake instance regardless of which job's lease
	// invoked it, it is safe to reuse across concurrent callers.
	sleep := dspInstantSleep(func(int) {
		dspActivateAll(context.Background(), t, fake, cfg.Tag, "203.0.113.14", "10.0.0.14")
	})
	dispatcher := &Dispatcher{Pool: pool, Sleep: sleep, HealthProbe: dspAlwaysHealthy}

	const n = 6
	var wg sync.WaitGroup
	leases := make([]*WorkerLease, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			leases[i], errs[i] = dispatcher.Lease(context.Background(), LeaseSpec{JobID: fmt.Sprintf("job-conc-%d", i)})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("lease %d: %v", i, err)
		}
		if leases[i] == nil {
			t.Fatalf("lease %d: nil lease with nil error", i)
		}
	}
	for i, lease := range leases {
		if err := lease.Release(context.Background()); err != nil {
			t.Fatalf("release %d: %v", i, err)
		}
	}
	if got := len(fake.Destroyed()); got != n {
		t.Fatalf("destroyed %d instances, want %d", got, n)
	}
}

// dspFakeObjectStore is a minimal objectstore.Store double: LeaseSpec.Objects
// only needs to be non-nil to exercise the SourceObjects wiring on the
// returned executor.HTTPRemoteWorker (this test never calls Run, so no
// method needs a real implementation).
type dspFakeObjectStore struct{}

func (dspFakeObjectStore) Put(context.Context, string, io.Reader, int64, objectstore.PutOptions) (objectstore.Meta, error) {
	panic("dspFakeObjectStore: Put not implemented")
}
func (dspFakeObjectStore) Get(context.Context, string) (io.ReadCloser, objectstore.Meta, error) {
	panic("dspFakeObjectStore: Get not implemented")
}
func (dspFakeObjectStore) Head(context.Context, string) (objectstore.Meta, error) {
	panic("dspFakeObjectStore: Head not implemented")
}
func (dspFakeObjectStore) List(context.Context, string) ([]objectstore.Meta, error) {
	panic("dspFakeObjectStore: List not implemented")
}
func (dspFakeObjectStore) Delete(context.Context, string) error {
	panic("dspFakeObjectStore: Delete not implemented")
}
func (dspFakeObjectStore) Presign(context.Context, string, string, time.Duration) (string, error) {
	panic("dspFakeObjectStore: Presign not implemented")
}

var _ objectstore.Store = dspFakeObjectStore{}
