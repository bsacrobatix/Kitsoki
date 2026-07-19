package ci

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/vmpool"
	"kitsoki/internal/objectstore"
)

// ciVMPoolNewProvisioner constructs the vmpool.Provisioner backing every
// POOL-backed executor. Production resolves vmpool.NewDO from the
// configured token_env; tests override this package var to inject
// vmpool.NewFake() (mirroring cmd/kitsoki's vmpoolNewProvisioner pattern) so
// leasing/releasing exercises no real DigitalOcean account.
var ciVMPoolNewProvisioner = func(tokenEnv string) (vmpool.Provisioner, error) {
	token := strings.TrimSpace(os.Getenv(tokenEnv))
	if token == "" {
		return nil, fmt.Errorf("%s is not set", tokenEnv)
	}
	return vmpool.NewDO(token), nil
}

// ciVMPoolDispatcherHook, when non-nil, is called with each freshly
// constructed *vmpool.Dispatcher before poolProvider.Run leases a worker from
// it. Tests use this to inject a deterministic Sleep/HealthProbe (the same
// seam internal/capsule/vmpool's own dispatch_test.go uses) so waiting for
// readiness costs no wall time and depends on no real network.
var ciVMPoolDispatcherHook func(*vmpool.Dispatcher)

// ciVMPoolStoreLockWait bounds how long a Lease/Release call tolerates
// contention on the shared durable vmpool store (internal/capsule/queue can
// dispatch more than one gate concurrently against the same pool executor).
// A zero LockWait — cmd/kitsoki's vmpool CLI default — fails immediately on
// any contention, which is too strict for concurrent CI dispatch.
const ciVMPoolStoreLockWait = 5 * time.Second

// poolRemoteRun executes prepared against a leased worker's transport. It is
// a package var, not a direct lease.Remote.Run(...) call, because the
// minted per-lease TLS identity's private key never leaves the ephemeral
// worker's user data (see vmpool.Dispatcher.Lease): no local test process can
// stand up a TLS listener presenting a certificate the leased pinned client
// would trust. Tests substitute a stub here to exercise poolProvider's
// lease/run/release sequencing and error classification hermetically, while
// production always drives the real executor.HTTPRemoteWorker.Run — the same
// call vmpool's own dispatch tests and internal/capsule/executor's
// http_remote_test.go already cover independently.
var poolRemoteRun = func(ctx context.Context, remote executor.HTTPRemoteWorker, prepared executor.Prepared, task executor.Task, sink executor.EventSink) (executor.Result, error) {
	return remote.Run(ctx, prepared, task, sink)
}

// poolCapabilities is poolProvider.Describe's static answer. Probing a real
// lease to answer Describe would mean every doctor/no-spend preflight call
// provisions and destroys a droplet just to report capabilities that never
// change for this executor; a fixed, documented capability set is cheaper
// and keeps Describe (and therefore doctor) no-spend, at the cost of never
// reflecting transient DigitalOcean account/token health until Run actually
// leases a worker.
func poolCapabilities() executor.Capabilities {
	return executor.Capabilities{
		ID:          "vmpool-worker",
		Placements:  []string{"remote"},
		Isolation:   "vm",
		Networks:    []string{"live"},
		Cancellable: false,
	}
}

// poolProvider is the executor.Provider for a Remote.Pool executor. Unlike
// ConfiguredExecutors' fixed-endpoint remotes (a persistent HTTPRemoteWorker
// wrapped in executor.NewRemoteProvider), a pool executor has no standing
// endpoint: Prepare only validates the sealed envelope locally, and each Run
// leases a fresh ephemeral worker, drives the execution on it, and always
// releases (destroys) it before returning — success or failure alike.
type poolProvider struct {
	name        string
	cfg         PoolExecutor
	projectRoot string
	source      executor.SourceBundler

	mu       sync.Mutex
	prepared map[string]executor.Prepared
}

// newPoolProvider constructs the executor.Provider for a Remote.Pool
// executor. Image is checked here — at executor-selection time — rather than
// deferred to Run, matching newSourceObjects' rationale in executors.go: a
// missing configuration fact should surface as a clear executor-selection
// error naming the executor, not an opaque failure after a lease has already
// been attempted. Validate deliberately does not require Image (a
// still-to-be-published snapshot reference is a valid checked-in state), so
// this is the first point at which an unresolved Image actually blocks use.
func newPoolProvider(name string, cfg PoolExecutor, source executor.SourceBundler, projectRoot string) (executor.Provider, error) {
	if strings.TrimSpace(cfg.Image) == "" {
		return nil, fmt.Errorf("capsule ci: pool executor %q: image is required", name)
	}
	return &poolProvider{name: name, cfg: cfg, projectRoot: projectRoot, source: source, prepared: map[string]executor.Prepared{}}, nil
}

func (p *poolProvider) Describe(context.Context) (executor.Capabilities, error) {
	return poolCapabilities(), nil
}

// Prepare validates the sealed envelope against this executor's static
// capabilities without leasing a worker, so doctor's no-spend preflight
// (which calls Provider.Prepare but never Provider.Run) never creates a
// droplet for a pool executor.
func (p *poolProvider) Prepare(_ context.Context, e executor.Envelope) (executor.Prepared, error) {
	sealed, err := executor.Seal(e)
	if err != nil {
		return executor.Prepared{}, err
	}
	if err := executor.ValidateCapabilities(poolCapabilities(), sealed.Policy); err != nil {
		return executor.Prepared{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if out := p.prepared[sealed.Digest]; out.ID != "" {
		return out, nil
	}
	out := executor.Prepared{ID: "vmpool-" + sealed.Digest[len(sealed.Digest)-12:], Envelope: sealed, Placement: "remote", Applied: sealed.Policy}
	p.prepared[sealed.Digest] = out
	return out, nil
}

// AcceptPrepared implements the optional executor.PreparedAcceptor no-spend
// preflight seam: it validates the exact prepared execution locally, the
// same way Prepare does, without leasing a worker. Nothing in this repo's
// current call graph invokes Provider.(PreparedAcceptor) for a directly
// returned Provider (that hook is only exercised through
// executor.RemoteProvider wrapping a RemoteWorker) — poolProvider implements
// it anyway for symmetry with HTTPRemoteWorker and so a future no-spend
// caller that type-asserts for it gets the same guarantee.
func (p *poolProvider) AcceptPrepared(_ context.Context, prepared executor.Prepared) error {
	_, err := executor.ValidatePrepared(prepared)
	return err
}

// Run leases a fresh ephemeral worker for prepared.Envelope.JobID, wires the
// CI-provided SourceBundler onto it exactly as ConfiguredExecutors.Select
// wires a fixed-endpoint remote's Source, drives the execution, and always
// releases the worker afterward regardless of outcome. A lease failure
// (vmpool.ErrPoolFull, vmpool.ErrWorkerNotReady, or any wrapped provisioner
// error) is returned wrapped with %w, so errors.Is/As against those sentinels
// still succeeds; internal/capsule/queue.ExecutorGate classifies every
// non-nil ci.Service.Run error as environmental/retryable regardless of its
// concrete type (see executor_gate.go's runErr handling), so no further
// remote-call-error wrapping is needed for that classification to work.
func (p *poolProvider) Run(ctx context.Context, prepared executor.Prepared, task executor.Task, sink executor.EventSink) (executor.Result, error) {
	validated, err := executor.ValidatePrepared(prepared)
	if err != nil {
		return executor.Result{}, err
	}
	prepared = validated

	provisioner, err := ciVMPoolNewProvisioner(p.cfg.TokenEnv)
	if err != nil {
		return executor.Result{}, fmt.Errorf("capsule ci: pool executor %q: %w", p.name, err)
	}

	root := p.projectRoot
	if root == "" {
		root = "."
	}

	pool := &vmpool.Pool{
		Store:       &vmpool.Store{ProjectRoot: root, LockWait: ciVMPoolStoreLockWait},
		Provisioner: provisioner,
		Config: vmpool.Config{
			MaxConcurrent: p.cfg.MaxConcurrent,
			Region:        p.cfg.Region,
			Size:          p.cfg.Size,
			Image:         p.cfg.Image,
			VPCUUID:       p.cfg.VPCUUID,
			SSHKeyIDs:     append([]string(nil), p.cfg.SSHKeyIDs...),
		},
		// A pre-ready lease failure keeps its instance for post-mortem
		// instead of destroying it blind; a post-ready failure (this
		// method's own runErr below) is still always released explicitly —
		// see the Run doc comment.
		PreserveFailed: true,
	}
	dispatcher := &vmpool.Dispatcher{Pool: pool}
	if ciVMPoolDispatcherHook != nil {
		ciVMPoolDispatcherHook(dispatcher)
	}

	leaseSpec := vmpool.LeaseSpec{JobID: prepared.Envelope.JobID}
	if p.cfg.SourceBucket != nil {
		store, err := newPoolObjectStore(p.name, *p.cfg.SourceBucket)
		if err != nil {
			return executor.Result{}, err
		}
		leaseSpec.Objects = store
		leaseSpec.BucketURL = p.cfg.SourceBucket.URL
		leaseSpec.OutputsKeyEnv = p.cfg.SourceBucket.KeyEnv
		leaseSpec.OutputsSecretEnv = p.cfg.SourceBucket.SecretEnv
	}

	lease, err := dispatcher.Lease(ctx, leaseSpec)
	if err != nil {
		return executor.Result{}, fmt.Errorf("capsule ci: pool executor %q: lease worker: %w", p.name, err)
	}

	remote := lease.Remote
	remote.Source = p.source

	result, runErr := poolRemoteRun(ctx, remote, prepared, task, sink)

	// Release unconditionally, success or failure, using a context that has
	// already dropped cancellation/deadline: a run that failed because ctx
	// was cancelled must still get its ephemeral droplet destroyed rather
	// than leaking it.
	if relErr := lease.Release(context.WithoutCancel(ctx)); relErr != nil {
		if runErr == nil {
			runErr = fmt.Errorf("capsule ci: pool executor %q: release worker: %w", p.name, relErr)
		} else {
			runErr = fmt.Errorf("%w (also failed to release worker: %v)", runErr, relErr)
		}
	}
	return result, runErr
}

// Cancel is not supported: Run is a single synchronous lease/run/release
// call with no durable out-of-band handle to cancel afterward (matching
// Describe's Cancellable: false). Cancelling the context passed to Run is
// the supported way to stop an in-flight pool execution.
func (p *poolProvider) Cancel(context.Context, string) error {
	return fmt.Errorf("capsule ci: pool executor %q does not support out-of-band cancellation; cancel the run's context instead", p.name)
}

// newPoolObjectStore resolves a pool executor's source_bucket into a live
// objectstore.Store for vmpool.LeaseSpec.Objects. Unlike
// executors.go's newSourceObjects (which wraps the store in a
// bucketsource.Publisher configured with the remote's own prefix/TTL),
// vmpool.Dispatcher.Lease performs that wrapping itself with
// bucketsource's package defaults — see PoolExecutor.SourceBucket's doc
// comment for why prefix/presign_ttl overrides are rejected at validate
// time instead of silently ignored here.
func newPoolObjectStore(name string, sb SourceBucket) (objectstore.Store, error) {
	cfg, err := objectstore.ParseBucketURL(sb.URL)
	if err != nil {
		return nil, fmt.Errorf("capsule ci: pool executor %q source_bucket url: %w", name, err)
	}
	cfg.KeyEnv = sb.KeyEnv
	cfg.SecretEnv = sb.SecretEnv
	store, err := objectstore.NewSpaces(cfg, nil)
	if err != nil {
		return nil, fmt.Errorf("capsule ci: pool executor %q source bucket credentials (set %s and %s): %w", name, sb.KeyEnv, sb.SecretEnv, err)
	}
	return store, nil
}

var _ executor.Provider = (*poolProvider)(nil)
var _ executor.PreparedAcceptor = (*poolProvider)(nil)
