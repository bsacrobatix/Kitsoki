package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"kitsoki/internal/capsule/bucketsource"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/vmpool"
	"kitsoki/internal/capsule/workerserver"
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
	name          string
	cfg           PoolExecutor
	projectRoot   string
	poolStateRoot string
	source        executor.SourceBundler

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
	return newPoolProviderWithPoolStateRoot(name, cfg, source, projectRoot, "")
}

func newPoolProviderWithPoolStateRoot(name string, cfg PoolExecutor, source executor.SourceBundler, projectRoot, poolStateRoot string) (executor.Provider, error) {
	if strings.TrimSpace(cfg.Image) == "" {
		return nil, fmt.Errorf("capsule ci: pool executor %q: image is required", name)
	}
	return &poolProvider{name: name, cfg: cfg, projectRoot: projectRoot, poolStateRoot: poolStateRoot, source: source, prepared: map[string]executor.Prepared{}}, nil
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
// ciPoolProvisionTimeout is the single source of truth for how long a
// leased worker gets to boot, both for the Pool's own provisioning-timeout
// lifecycle guard (buildPool's Config.ProvisionTimeout) and, via
// vmpool.LeaseSpec.withDefaults, for how long the Dispatcher's waitReady
// loop waits before giving up (leaseWorker used to hardcode a shorter,
// independent ReadyTimeout here — see leaseWorker's doc comment).
const ciPoolProvisionTimeout = 8 * time.Minute

// buildPool constructs the vmpool.Pool for this executor's configuration.
// PreserveFailed keeps a pre-ready lease failure's instance for post-mortem
// instead of destroying it blind; it defaults to off (see
// PoolExecutor.PreserveOnFailure's doc comment) so autonomous dispatch never
// silently accumulates billed droplets, and PreserveFailedTTL bounds how
// long a preserved instance survives before Reconcile reclaims it either
// way.
func (p *poolProvider) buildPool() (*vmpool.Pool, error) {
	provisioner, err := ciVMPoolNewProvisioner(p.cfg.TokenEnv)
	if err != nil {
		return nil, fmt.Errorf("capsule ci: pool executor %q: %w", p.name, err)
	}
	root := p.poolStateRoot
	if root == "" {
		root = p.projectRoot
	}
	if root == "" {
		root = "."
	}
	return &vmpool.Pool{
		Store:       &vmpool.Store{ProjectRoot: root, LockWait: ciVMPoolStoreLockWait},
		Provisioner: provisioner,
		Config: vmpool.Config{
			MaxConcurrent: p.cfg.MaxConcurrent,
			Region:        p.cfg.Region,
			Size:          p.cfg.Size,
			Image:         p.cfg.Image,
			VPCUUID:       p.cfg.VPCUUID,
			SSHKeyIDs:     append([]string(nil), p.cfg.SSHKeyIDs...),
			// POG fix: these were unset (zero), so timeoutReason() treated every
			// freshly-created worker as instantly "provisioning timed out" and the
			// reconcile destroyed it mid-boot. Budgets exceed the 3h pog-bugfix
			// command_timeout so a running agent loop is never lifecycle-killed.
			ProvisionTimeout:  ciPoolProvisionTimeout,
			ActivityTimeout:   4 * time.Hour,
			MaxLifetime:       5 * time.Hour,
			PreserveFailedTTL: p.cfg.PreserveFailedTTL,
		},
		PreserveFailed: p.cfg.PreserveOnFailure,
	}, nil
}

// leaseWorker acquires one fresh ephemeral worker for jobID with this
// executor's full boot configuration
// (pass_env/agent_backend/agent_model/user surface and optional bucket
// transport).
func (p *poolProvider) leaseWorker(ctx context.Context, jobID string) (*vmpool.WorkerLease, error) {
	pool, err := p.buildPool()
	if err != nil {
		return nil, err
	}
	dispatcher := &vmpool.Dispatcher{Pool: pool}
	if ciVMPoolDispatcherHook != nil {
		ciVMPoolDispatcherHook(dispatcher)
	}

	// ReadyTimeout is deliberately left zero here: vmpool.LeaseSpec.withDefaults
	// derives it from the Dispatcher's Pool.Config.ProvisionTimeout
	// (ciPoolProvisionTimeout, set in buildPool) when unset. This used to be
	// hardcoded shorter (6m) than ProvisionTimeout (8m), so a worker that
	// became ready between 6-8m into its boot was abandoned by waitReady with
	// the whole boot budget already spent — and, before PreserveOnFailure
	// defaulted off, silently preserved (billed) on top of that. Deriving
	// instead of duplicating keeps the two timeouts structurally impossible
	// to drift apart again.
	leaseSpec := vmpool.LeaseSpec{JobID: jobID, User: p.cfg.User, Env: map[string]string{}}
	if len(p.cfg.PassEnv) > 0 {
		// Names only: the worker resolves each value from its own process
		// environment (see cmd/kitsoki capsuleWorkerChildEnv); no credential
		// value ever transits the controller or the boot user data.
		leaseSpec.Env["KITSOKI_WORKER_PASS_ENV"] = strings.Join(p.cfg.PassEnv, ",")
	}
	if p.cfg.AgentBackend != "" {
		leaseSpec.Env["KITSOKI_WORKER_AGENT_BACKEND"] = p.cfg.AgentBackend
	}
	if p.cfg.AgentModel != "" {
		leaseSpec.Env["KITSOKI_WORKER_AGENT_MODEL"] = p.cfg.AgentModel
	}
	// Job-start preflight config (internal/capsule/workerserver.PreflightConfig):
	// written into the leased worker's boot env file so `capsule worker
	// serve` applies it to every job dispatched on this droplet, the same
	// contract AgentBackend above already uses.
	if p.cfg.PreflightSkip {
		leaseSpec.Env[workerserver.WorkerEnvPreflightSkip] = "1"
	}
	if p.cfg.PreflightDiskFloorBytes > 0 {
		leaseSpec.Env[workerserver.WorkerEnvPreflightDiskFloorBytes] = strconv.FormatInt(p.cfg.PreflightDiskFloorBytes, 10)
	}
	if p.cfg.PreflightLiveAuthProbe {
		leaseSpec.Env[workerserver.WorkerEnvPreflightLiveAuthProbe] = "1"
	}
	// POG fix: the leased worker runs the story (and the agent CLI it spawns)
	// as root; Claude Code refuses --dangerously-skip-permissions/bypassPermissions
	// as root unless IS_SANDBOX=1 marks the environment as an isolated sandbox,
	// which an ephemeral single-job pool droplet is. Without it every agent
	// acceptance attempt produces empty output and the bugfix loop never ships.
	leaseSpec.Env["IS_SANDBOX"] = "1"
	if tok := strings.TrimSpace(os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")); tok != "" {
		leaseSpec.Env["CLAUDE_CODE_OAUTH_TOKEN"] = tok
	}
	if p.cfg.SourceBucket != nil {
		store, err := newPoolObjectStore(p.name, *p.cfg.SourceBucket)
		if err != nil {
			return nil, err
		}
		leaseSpec.Objects = store
		leaseSpec.BucketURL = p.cfg.SourceBucket.URL
		leaseSpec.OutputsKeyEnv = p.cfg.SourceBucket.KeyEnv
		leaseSpec.OutputsSecretEnv = p.cfg.SourceBucket.SecretEnv
	}

	lease, err := dispatcher.Lease(ctx, leaseSpec)
	if err != nil {
		return nil, fmt.Errorf("capsule ci: pool executor %q: lease worker: %w", p.name, err)
	}
	return lease, nil
}

func (p *poolProvider) Run(ctx context.Context, prepared executor.Prepared, task executor.Task, sink executor.EventSink) (executor.Result, error) {
	validated, err := executor.ValidatePrepared(prepared)
	if err != nil {
		return executor.Result{}, err
	}
	prepared = validated

	lease, err := p.leaseWorker(ctx, prepared.Envelope.JobID)
	if err != nil {
		return executor.Result{}, err
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

// StartDetached leases a fresh ephemeral worker, publishes the sealed source,
// and dispatches the prepared execution asynchronously: the worker registers
// the run durably and this method returns its initial execution status
// without waiting for story terminal. Unlike Run, the leased droplet
// deliberately stays alive after this returns — it is executing the story —
// and is destroyed later by ReleaseDetached once `capsule ci status
// --refresh` reconciles a terminal state from the worker's bucket-mirrored
// run records. Detached dispatch therefore requires source_bucket: without
// output mirroring there would be no durable record to reconcile from after
// the worker host is gone.
func (p *poolProvider) StartDetached(ctx context.Context, prepared executor.Prepared, sink executor.EventSink) (executor.ExecutionStatus, error) {
	validated, err := executor.ValidatePrepared(prepared)
	if err != nil {
		return executor.ExecutionStatus{}, err
	}
	prepared = validated
	if p.cfg.SourceBucket == nil {
		return executor.ExecutionStatus{}, fmt.Errorf("capsule ci: pool executor %q: detached dispatch requires source_bucket (terminal state is reconciled from the worker's bucket run records)", p.name)
	}

	lease, err := p.leaseWorker(ctx, prepared.Envelope.JobID)
	if err != nil {
		return executor.ExecutionStatus{}, err
	}
	remote := lease.Remote
	remote.Source = p.source

	status, err := poolRemoteStartDetached(ctx, remote, prepared, sink)
	if err != nil {
		// The dispatch never started; release the droplet rather than leak it.
		if relErr := lease.Release(context.WithoutCancel(ctx)); relErr != nil {
			err = fmt.Errorf("%w (also failed to release worker: %v)", err, relErr)
		}
		return executor.ExecutionStatus{}, err
	}
	return status, nil
}

// poolRemoteStartDetached mirrors poolRemoteRun's test seam for the detached
// dispatch call (see poolRemoteRun's doc comment for why these are package
// vars rather than direct method calls).
var poolRemoteStartDetached = func(ctx context.Context, remote executor.HTTPRemoteWorker, prepared executor.Prepared, sink executor.EventSink) (executor.ExecutionStatus, error) {
	return remote.StartDetached(ctx, prepared, sink)
}

// Status implements executor.ExecutionController for detached pool
// executions by reading the worker's durable bucket run record
// (runs/<execution-id>/run.json) instead of a live worker endpoint: the
// droplet's minted single-job transport credential died with the dispatching
// process, and the droplet itself may already be gone.
func (p *poolProvider) Status(ctx context.Context, id string) (executor.ExecutionStatus, error) {
	if p.cfg.SourceBucket == nil {
		return executor.ExecutionStatus{}, fmt.Errorf("capsule ci: pool executor %q: status requires source_bucket", p.name)
	}
	store, err := newPoolObjectStore(p.name, *p.cfg.SourceBucket)
	if err != nil {
		return executor.ExecutionStatus{}, err
	}
	key := bucketsource.DefaultRunPrefix + "/" + id + "/run.json"
	rc, _, err := store.Get(ctx, key)
	if err != nil {
		return executor.ExecutionStatus{}, fmt.Errorf("capsule ci: pool executor %q: read durable run record %s: %w", p.name, key, err)
	}
	defer rc.Close()
	var record workerserver.RunRecord
	if err := json.NewDecoder(io.LimitReader(rc, 8<<20)).Decode(&record); err != nil {
		return executor.ExecutionStatus{}, fmt.Errorf("capsule ci: pool executor %q: decode durable run record %s: %w", p.name, key, err)
	}
	if record.Schema != workerserver.RunRecordSchema || record.ExecutionID != id {
		return executor.ExecutionStatus{}, fmt.Errorf("capsule ci: pool executor %q: durable run record %s does not describe execution %s", p.name, key, id)
	}
	return executor.ExecutionStatus{
		Schema:         executor.ExecutionStatusSchema,
		ExecutionID:    record.ExecutionID,
		EnvelopeDigest: record.EnvelopeDigest,
		RequestID:      record.RequestID,
		Status:         record.Status,
		Stage:          record.Stage,
		StartedAt:      record.StartedAt,
		UpdatedAt:      record.UpdatedAt,
		TerminalAt:     record.TerminalAt,
		Error:          record.Error,
		FailureClass:   record.FailureClass,
		Events:         record.Events,
		Result:         record.Result,
		Agent:          record.Agent,
		Cleanup:        record.Cleanup,
	}, nil
}

// RequestCancel is not supported for pool executions: after a detached
// dispatch the controller holds no live credential for the worker (see
// Status), so there is no authenticated path to request cancellation.
func (p *poolProvider) RequestCancel(context.Context, string) (executor.ExecutionStatus, error) {
	return executor.ExecutionStatus{}, fmt.Errorf("capsule ci: pool executor %q does not support out-of-band cancellation of a detached execution; the worker's max-lifetime reaper and ReleaseDetached bound its cost", p.name)
}

// ReleaseDetached destroys the ephemeral worker leased for jobID once its
// detached execution has been reconciled terminal. It is a no-op when the
// worker record is already terminal (or absent), so repeated reconciliation
// polls stay idempotent.
func (p *poolProvider) ReleaseDetached(ctx context.Context, jobID string) error {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return fmt.Errorf("capsule ci: pool executor %q: release: job id is required", p.name)
	}
	pool, err := p.buildPool()
	if err != nil {
		return err
	}
	workers, err := pool.List()
	if err != nil {
		return fmt.Errorf("capsule ci: pool executor %q: release: %w", p.name, err)
	}
	for _, w := range workers {
		if w.JobID != jobID || w.Status.Terminal() {
			continue
		}
		if err := pool.Release(ctx, w.ID); err != nil {
			return fmt.Errorf("capsule ci: pool executor %q: release worker %s: %w", p.name, w.ID, err)
		}
	}
	return nil
}

// newPoolObjectStore resolves a pool executor's source_bucket into a live
// objectstore.Store for vmpool.LeaseSpec.Objects and for reading back
// detached executions' durable run records. A package var so tests can
// substitute objectstore.NewFake without real bucket credentials. Unlike
// executors.go's newSourceObjects (which wraps the store in a
// bucketsource.Publisher configured with the remote's own prefix/TTL),
// vmpool.Dispatcher.Lease performs that wrapping itself with
// bucketsource's package defaults — see PoolExecutor.SourceBucket's doc
// comment for why prefix/presign_ttl overrides are rejected at validate
// time instead of silently ignored here.
var newPoolObjectStore = func(name string, sb SourceBucket) (objectstore.Store, error) {
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
var _ executor.DetachedStarter = (*poolProvider)(nil)
var _ executor.DetachedReleaser = (*poolProvider)(nil)
var _ executor.ExecutionController = (*poolProvider)(nil)
