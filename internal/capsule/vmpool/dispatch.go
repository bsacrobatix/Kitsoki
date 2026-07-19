package vmpool

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"kitsoki/internal/capsule/bucketsource"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/objectstore"
)

// Worker env-file keys for the optional output-bucket mirror. These must
// match cmd/kitsoki/capsule_worker_config.go's workerEnvOutputs* constants
// byte-for-byte: that file is the worker-side consumer of
// /etc/kitsoki-worker/env and is the authoritative definition of this
// contract. They are duplicated here (rather than imported) because that file
// lives in package main and dispatch.go must not import a command package.
const (
	workerEnvOutputsURL    = "KITSOKI_WORKER_OUTPUTS_URL"
	workerEnvOutputsKeyEnv = "KITSOKI_WORKER_OUTPUTS_KEY_ENV"
	workerEnvOutputsSecEnv = "KITSOKI_WORKER_OUTPUTS_SECRET_ENV"

	// workerEnvOutputsAccessKey and workerEnvOutputsSecretKey are the fixed
	// env-file keys this dispatcher mints the resolved output-bucket
	// credential values under. They are single-job secrets: like the TLS key
	// and worker token, they are embedded in user data and destroyed with the
	// droplet. workerEnvOutputsKeyEnv/workerEnvOutputsSecEnv simply name them
	// so the worker's os.Getenv(keyEnv)/os.Getenv(secretEnv) lookup (see
	// workerOutputsFromEnv) resolves to the values written here.
	workerEnvOutputsAccessKey = "KITSOKI_WORKER_OUTPUTS_ACCESS_KEY"
	workerEnvOutputsSecretKey = "KITSOKI_WORKER_OUTPUTS_SECRET_KEY"
)

// Defaults for LeaseSpec.
const (
	DefaultLeaseListenPort   = 7443
	DefaultLeasePollInterval = 5 * time.Second

	// capabilitiesPath is the worker endpoint healthProbe polls to confirm
	// the kitsoki-worker service is up and terminating TLS with the minted
	// identity.
	capabilitiesPath = "/v1/capsules/capabilities"
)

// ErrWorkerNotReady classifies a Lease failure where the worker never
// reached StatusReady: either the Dispatcher gave up after ReadyTimeout, or
// the underlying Pool independently terminalized the worker (provisioning
// timeout, lost instance, ...) while the Dispatcher was waiting. Callers can
// match on this with errors.Is to distinguish an environmental/retryable
// failure (cloud provisioning slow or flaky) from a programming error (bad
// LeaseSpec, misconfigured Dispatcher).
var ErrWorkerNotReady = errors.New("vmpool: worker did not become ready")

// LeaseSpec configures one ephemeral worker lease for one job.
type LeaseSpec struct {
	JobID      string
	ListenPort int               // default DefaultLeaseListenPort
	Objects    objectstore.Store // optional: wired into SourceObjects
	// BucketURL, OutputsKeyEnv, and OutputsSecretEnv optionally configure
	// worker-side output mirroring: BucketURL is written verbatim into the
	// worker's env file as KITSOKI_WORKER_OUTPUTS_URL; OutputsKeyEnv and
	// OutputsSecretEnv name the *controller's* local environment variables
	// holding the actual bucket access key/secret, which Lease resolves and
	// embeds (single-job, destroyed with the droplet) under fixed env-file
	// keys, wiring KITSOKI_WORKER_OUTPUTS_KEY_ENV/_SECRET_ENV to point at
	// them. See cmd/kitsoki/capsule_worker_config.go (workerOutputsFromEnv)
	// for the worker-side consumer of this contract.
	BucketURL                       string
	OutputsKeyEnv, OutputsSecretEnv string
	Env                             map[string]string // extra env for the worker boot; may not redefine a reserved or outputs key
	PollInterval                    time.Duration     // default DefaultLeasePollInterval
	ReadyTimeout                    time.Duration     // default Config.ProvisionTimeout
}

// withDefaults fills zero fields with lease defaults, using cfg (the Pool's
// resolved Config) for ReadyTimeout.
func (s LeaseSpec) withDefaults(cfg Config) LeaseSpec {
	if s.ListenPort <= 0 {
		s.ListenPort = DefaultLeaseListenPort
	}
	if s.PollInterval <= 0 {
		s.PollInterval = DefaultLeasePollInterval
	}
	if s.ReadyTimeout <= 0 {
		s.ReadyTimeout = cfg.ProvisionTimeout
	}
	return s
}

// WorkerLease is a live, health-checked ephemeral worker bound to one job.
type WorkerLease struct {
	Worker   Worker                    // the pool record, as of the moment it became ready/running
	Endpoint string                    // https://<public-ip>:<port>
	Remote   executor.HTTPRemoteWorker // ready to use: credential + pinned CA + optional SourceObjects
	// Release destroys the droplet and marks the pool record terminal; safe
	// to call more than once (it delegates to Pool.Release, which is
	// idempotent) and safe to call even if Lease itself failed partway
	// through, since Lease already calls it on any failure path after the
	// worker record exists.
	Release func(ctx context.Context) error
}

// Dispatcher acquires leases from a Pool.
//
// Each Lease call builds its own shallow copy of *Pool with lease-scoped
// UserData and Health closures, and drives that copy exclusively — Dispatcher
// never mutates d.Pool itself. This is what makes concurrent Lease calls on
// one Dispatcher safe: Pool's mutable identity for a given lease (its
// UserData/Health func fields) is private to that call's stack, while the
// genuinely shared state (Store, backed by a lock file, and Provisioner,
// documented safe for concurrent use) is unaffected by the copy.
type Dispatcher struct {
	Pool *Pool
	// Sleep, when set, replaces the real timer used between readiness polls.
	// Tests inject an instant no-op (optionally scripting Provisioner state
	// changes from within it) so waiting for readiness costs no wall time.
	Sleep func(ctx context.Context, d time.Duration) error
	// HealthProbe, when set, replaces the default HTTPS capabilities probe
	// used to decide when a provisioning worker has become ready. Tests
	// inject a stub here rather than stand up a real TLS listener bound to
	// the minted per-lease identity.
	HealthProbe func(ctx context.Context, w Worker, client *http.Client, token string, port int) error
}

func (d *Dispatcher) sleep(ctx context.Context, dur time.Duration) error {
	if d.Sleep != nil {
		return d.Sleep(ctx, dur)
	}
	timer := time.NewTimer(dur)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (d *Dispatcher) healthProbe() func(ctx context.Context, w Worker, client *http.Client, token string, port int) error {
	if d.HealthProbe != nil {
		return d.HealthProbe
	}
	return healthProbe
}

// Lease acquires a fresh ephemeral droplet for spec.JobID, boots it with a
// freshly minted single-job bearer token and TLS server identity, waits for
// the worker service to report healthy, marks the pool record running, and
// returns a WorkerLease whose Remote is a ready-to-use
// executor.HTTPRemoteWorker. On any failure once the worker record exists,
// Lease destroys it (via Release) before returning the wrapped error, so a
// caller never has to separately clean up a failed acquisition.
func (d *Dispatcher) Lease(ctx context.Context, spec LeaseSpec) (*WorkerLease, error) {
	if d.Pool == nil {
		return nil, fmt.Errorf("vmpool: lease: dispatcher pool is required")
	}
	jobID := strings.TrimSpace(spec.JobID)
	if jobID == "" {
		return nil, fmt.Errorf("vmpool: lease: job id is required")
	}

	cfg := d.Pool.Config.WithDefaults()
	spec = spec.withDefaults(cfg)

	token, err := MintWorkerToken()
	if err != nil {
		return nil, fmt.Errorf("vmpool: lease: %w", err)
	}

	id := workerID(jobID)
	instanceName := cfg.NamePrefix + jobID

	// The droplet's public IP is not known until after Provisioner.Create
	// returns, but user data (which carries the TLS identity) must be
	// prepared before Create is called. So the identity cannot carry an IP
	// SAN. Instead its CommonName/DNS SAN is the (already-known) instance
	// name — a single literal hostname, no wildcard — and the controller's
	// http.Client both trusts the minted cert directly (via a CertPool
	// containing only it) and pins tls.Config.ServerName to that same name,
	// overriding the name the client would otherwise derive from the dial
	// address. This authenticates "holds the private key minted for this
	// lease" rather than "serves on this literal IP"; that is an acceptable
	// tradeoff here because the private key is single-job (minted fresh per
	// droplet, shipped only via that droplet's own user data, and never
	// reused), so proof of possession is equivalent to proof of identity for
	// the lifetime of one lease.
	identity, err := MintServerIdentity(instanceName, []string{instanceName}, identityTTL(cfg))
	if err != nil {
		return nil, fmt.Errorf("vmpool: lease: %w", err)
	}
	client, err := pinnedClient(identity)
	if err != nil {
		return nil, fmt.Errorf("vmpool: lease: %w", err)
	}

	env, err := buildOutputsEnv(spec)
	if err != nil {
		return nil, fmt.Errorf("vmpool: lease: %w", err)
	}
	for k, v := range spec.Env {
		env[k] = v
	}

	bootSpec := BootSpec{
		WorkerID:   id,
		JobID:      jobID,
		Token:      token,
		ListenAddr: fmt.Sprintf("0.0.0.0:%d", spec.ListenPort),
		Identity:   identity,
		Env:        env,
	}

	// A shallow copy of *d.Pool, scoped to this lease: UserData and Health
	// close over this lease's bootSpec/client/token, and are never written
	// back onto d.Pool. See the Dispatcher doc comment for why this makes
	// concurrent Lease calls race-free.
	leasePool := *d.Pool
	leasePool.UserData = func(Worker) (string, error) {
		return GenerateUserData(bootSpec)
	}
	probe := d.healthProbe()
	leasePool.Health = func(ctx context.Context, w Worker) error {
		return probe(ctx, w, client, token, spec.ListenPort)
	}

	worker, err := leasePool.Acquire(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("vmpool: lease: acquire: %w", err)
	}
	release := func(ctx context.Context) error {
		return leasePool.Release(ctx, worker.ID)
	}
	// In preserve mode a pre-ready failure keeps the instance for post-mortem
	// instead of destroying it; the durable record carries the reclaim hint.
	cleanup := release
	if leasePool.PreserveFailed {
		cleanup = func(ctx context.Context) error {
			current := worker
			if state, loadErr := leasePool.Store.Load(); loadErr == nil {
				if fresh, ok := state.WorkerByID(worker.ID); ok {
					current = fresh
				}
			}
			_, failErr := leasePool.destroyAndFail(ctx, current, "lease failed before ready")
			return failErr
		}
	}

	ready, err := d.waitReady(ctx, &leasePool, worker.ID, spec.PollInterval, spec.ReadyTimeout)
	if err != nil {
		return nil, failLease(ctx, cleanup, err)
	}

	if err := leasePool.MarkRunning(ctx, ready.ID, jobID); err != nil {
		return nil, failLease(ctx, cleanup, fmt.Errorf("vmpool: lease: mark running: %w", err))
	}
	// Best-effort read-back so WorkerLease.Worker reflects StatusRunning
	// rather than the pre-MarkRunning StatusReady snapshot; MarkRunning
	// already succeeded above, so a failure here is not itself fatal to the
	// lease.
	if workers, listErr := leasePool.List(); listErr == nil {
		for _, w := range workers {
			if w.ID == ready.ID {
				ready = w
				break
			}
		}
	}

	endpoint := fmt.Sprintf("https://%s:%d", ready.PublicIP, spec.ListenPort)
	remote := executor.HTTPRemoteWorker{
		Endpoint:   endpoint,
		Client:     client,
		Credential: staticCredential(token),
	}
	if spec.Objects != nil {
		remote.SourceObjects = bucketsource.Publisher{Store: spec.Objects}
	}

	return &WorkerLease{
		Worker:   ready,
		Endpoint: endpoint,
		Remote:   remote,
		Release:  release,
	}, nil
}

// waitReady polls pool until worker id reaches StatusReady, terminalizes, or
// spec's ReadyTimeout elapses. Elapsed time is tracked in poll-interval
// increments rather than by consulting a wall clock, so tests can inject an
// instant Sleep and exercise a timeout deterministically without either
// sleeping for real or coupling this loop to Pool.Now.
func (d *Dispatcher) waitReady(ctx context.Context, pool *Pool, id string, interval, timeout time.Duration) (Worker, error) {
	var elapsed time.Duration
	for {
		if err := ctx.Err(); err != nil {
			return Worker{}, err
		}
		workers, err := pool.Poll(ctx)
		if err != nil {
			return Worker{}, fmt.Errorf("vmpool: lease: poll: %w", err)
		}
		for _, w := range workers {
			if w.ID != id {
				continue
			}
			switch w.Status {
			case StatusReady:
				return w, nil
			case StatusFailed, StatusDestroyed:
				return Worker{}, fmt.Errorf("%w: worker %s terminalized while waiting: %s", ErrWorkerNotReady, id, w.Error)
			}
		}
		if elapsed >= timeout {
			return Worker{}, fmt.Errorf("%w: worker %s not ready after %s", ErrWorkerNotReady, id, timeout)
		}
		if err := d.sleep(ctx, interval); err != nil {
			return Worker{}, err
		}
		elapsed += interval
	}
}

// failLease releases a partially-acquired worker before propagating cause.
// If release itself fails, that failure is folded into the returned error
// text (so it is not silently lost) without displacing cause as the
// %w-wrapped root, keeping errors.Is(err, ErrWorkerNotReady) working through
// this path.
func failLease(ctx context.Context, release func(context.Context) error, cause error) error {
	if relErr := release(ctx); relErr != nil {
		return fmt.Errorf("%w (cleanup also failed: %v)", cause, relErr)
	}
	return cause
}

// staticCredential adapts a fixed bearer token to executor.Credential.
func staticCredential(token string) executor.Credential {
	return func(context.Context) (string, error) {
		return token, nil
	}
}

// identityTTL bounds the minted server identity's validity to comfortably
// outlive the longest a leased worker is allowed to live.
func identityTTL(cfg Config) time.Duration {
	ttl := cfg.MaxLifetime + time.Hour
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return ttl
}

// pinnedClient builds the controller-side http.Client for talking to one
// leased worker: it trusts only the certificate minted for this lease (via a
// CertPool containing exactly that certificate, nothing from the system
// trust store) and pins TLS ServerName to the certificate's CommonName. See
// the tradeoff note in Lease's doc comment for why ServerName is pinned
// rather than left to default from the dial address.
func pinnedClient(identity ServerIdentity) (*http.Client, error) {
	block, _ := pem.Decode(identity.CertPEM)
	if block == nil {
		return nil, fmt.Errorf("pinned client: no PEM block in minted certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("pinned client: parse minted certificate: %w", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    pool,
			ServerName: cert.Subject.CommonName,
		},
	}
	return &http.Client{Transport: transport}, nil
}

// healthProbe is the default Dispatcher readiness probe: an HTTPS GET of the
// worker's capabilities endpoint, authenticated with the minted bearer token,
// over client (already pinned to this lease's minted certificate). A worker
// with no public IP yet is reported not-ready without making a request.
func healthProbe(ctx context.Context, w Worker, client *http.Client, token string, port int) error {
	if w.PublicIP == "" {
		return fmt.Errorf("health probe: worker has no public ip yet")
	}
	endpoint := fmt.Sprintf("https://%s:%d%s", w.PublicIP, port, capabilitiesPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("health probe: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("health probe: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("health probe: status %s", resp.Status)
	}
	return nil
}

// buildOutputsEnv resolves spec's optional worker-output-mirroring
// configuration into env-file entries, reading the actual bucket credential
// values from the controller's own environment (spec.OutputsKeyEnv/
// OutputsSecretEnv name which controller env vars to read — the same
// indirection ci.SourceBucket uses) and embedding them under fixed,
// single-job env-file keys alongside the pointer names the worker resolves
// them by. It returns an empty, non-nil map when spec.BucketURL is unset.
func buildOutputsEnv(spec LeaseSpec) (map[string]string, error) {
	env := map[string]string{}
	if strings.TrimSpace(spec.BucketURL) == "" {
		return env, nil
	}
	if spec.OutputsKeyEnv == "" || spec.OutputsSecretEnv == "" {
		return nil, fmt.Errorf("BucketURL requires OutputsKeyEnv and OutputsSecretEnv")
	}
	accessKey := os.Getenv(spec.OutputsKeyEnv)
	if accessKey == "" {
		return nil, fmt.Errorf("%s is not set", spec.OutputsKeyEnv)
	}
	secretKey := os.Getenv(spec.OutputsSecretEnv)
	if secretKey == "" {
		return nil, fmt.Errorf("%s is not set", spec.OutputsSecretEnv)
	}
	env[workerEnvOutputsURL] = spec.BucketURL
	env[workerEnvOutputsKeyEnv] = workerEnvOutputsAccessKey
	env[workerEnvOutputsSecEnv] = workerEnvOutputsSecretKey
	env[workerEnvOutputsAccessKey] = accessKey
	env[workerEnvOutputsSecretKey] = secretKey
	return env, nil
}
