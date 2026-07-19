package vmpool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrPoolFull is returned by Acquire when Config.MaxConcurrent non-terminal
// workers are already outstanding.
var ErrPoolFull = errors.New("vmpool: pool is full")

// ErrJobActive is returned by Acquire when the given job ID already has a
// non-terminal worker.
var ErrJobActive = errors.New("vmpool: job already has an active worker")

// ReconcileReport summarizes the result of a Reconcile pass.
type ReconcileReport struct {
	// Orphans are cloud instance IDs, tagged for this pool, that matched no
	// non-terminal durable worker record and were destroyed.
	Orphans []string
	// Lost are durable worker IDs whose cloud instance no longer exists;
	// they were marked StatusFailed.
	Lost []string
	// Active are cloud instance IDs that matched a non-terminal worker
	// record and required no action.
	Active []string
}

// Pool is the lifecycle engine for a bounded set of ephemeral VM workers. One
// VM serves one job: acquired from a base image, health-checked, dispatched,
// and destroyed. Durable state lives in Store; Provisioner is the
// cloud-provider seam. The store lock is held only around state mutations —
// never across a cloud call — so a slow Create/Destroy/Get never blocks
// unrelated pool operations.
type Pool struct {
	Store       *Store
	Provisioner Provisioner
	Config      Config

	// Now, when set, overrides time.Now for deterministic tests.
	Now func() time.Time

	// UserData renders the boot script for a new worker. Nil produces empty
	// user data.
	UserData func(Worker) (string, error)

	// Health probes the worker service on a provisioning instance. Nil
	// always reports healthy.
	Health func(context.Context, Worker) error

	// PreserveFailed keeps a failed worker's instance running (skipping the
	// automatic destroy) so its boot logs and state can be examined; the
	// operator reclaims it explicitly via Release or reap --repair. Cloud
	// spend continues while preserved — pair with MaxLifetime discipline.
	PreserveFailed bool
}

// first returns the first non-empty string.
func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (p *Pool) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

func (p *Pool) config() Config {
	return p.Config.WithDefaults()
}

// workerID deterministically derives the durable worker ID for a job.
func workerID(jobID string) string {
	return "vm-" + jobID
}

// Acquire admits a new worker for jobID, gated on Config.MaxConcurrent
// non-terminal workers. The durable record is created in StatusCreating
// before any cloud call is made, so a crash between record-creation and the
// provider Create call still leaves a durable, inspectable record rather
// than an orphaned instance with no local trace. On a Create failure the
// worker is marked StatusFailed with the error and that error is returned.
func (p *Pool) Acquire(ctx context.Context, jobID string) (Worker, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return Worker{}, fmt.Errorf("vmpool: job id is required")
	}
	cfg := p.config()
	id := workerID(jobID)
	created := p.now()

	worker := Worker{
		ID:           id,
		JobID:        jobID,
		InstanceName: cfg.NamePrefix + jobID,
		Status:       StatusCreating,
		Image:        cfg.Image,
		CreatedAt:    created,
	}

	if err := p.Store.Update(func(state *State) error {
		for _, w := range state.Workers {
			if w.JobID == jobID && !w.Status.Terminal() {
				return fmt.Errorf("vmpool: job %s already has an active worker %s: %w", jobID, w.ID, ErrJobActive)
			}
		}
		if state.ActiveCount() >= cfg.MaxConcurrent {
			return ErrPoolFull
		}
		state.Workers = append(state.Workers, worker)
		return nil
	}); err != nil {
		return Worker{}, err
	}

	var userData string
	if p.UserData != nil {
		var err error
		userData, err = p.UserData(worker)
		if err != nil {
			renderErr := fmt.Errorf("vmpool: render user data for %s: %w", id, err)
			p.fail(id, renderErr)
			return Worker{}, renderErr
		}
	}

	params := CreateParams{
		Name:      worker.InstanceName,
		Region:    cfg.Region,
		Size:      cfg.Size,
		Image:     cfg.Image,
		VPCUUID:   cfg.VPCUUID,
		UserData:  userData,
		SSHKeyIDs: cfg.SSHKeyIDs,
		Tags:      []string{cfg.Tag},
	}

	instance, err := p.Provisioner.Create(ctx, params)
	if err != nil {
		createErr := fmt.Errorf("vmpool: create instance for %s: %w", id, err)
		p.fail(id, createErr)
		return Worker{}, createErr
	}

	var final Worker
	err = p.Store.Update(func(state *State) error {
		idx := state.indexByID(id)
		if idx < 0 {
			return fmt.Errorf("vmpool: worker %s vanished before instance id could be recorded", id)
		}
		state.Workers[idx].InstanceID = instance.ID
		final = state.Workers[idx]
		return nil
	})
	if err != nil {
		return Worker{}, err
	}
	return final, nil
}

// fail marks the worker StatusFailed with cause, if it still exists and is
// not already terminal. Errors from the update itself are swallowed: fail is
// always called from a path that is already returning a more specific error
// to the caller, and a durable record that could not be updated is still
// preferable to panicking on a best-effort cleanup path.
func (p *Pool) fail(id string, cause error) {
	_ = p.Store.Update(func(state *State) error {
		idx := state.indexByID(id)
		if idx < 0 {
			return nil
		}
		w := &state.Workers[idx]
		if w.Status.Terminal() {
			return nil
		}
		w.Status = StatusFailed
		w.Error = cause.Error()
		w.TerminalAt = p.now()
		return nil
	})
}

// Poll advances every non-terminal worker one step and returns the updated
// set. Cloud calls (Get/Destroy/Health) happen unlocked; each resulting
// state change is applied under the store lock with its precondition
// re-verified against the freshly loaded record, so a concurrent Touch,
// MarkRunning, or Release is never silently clobbered by a stale decision.
func (p *Pool) Poll(ctx context.Context) ([]Worker, error) {
	cfg := p.config()
	state, err := p.Store.Load()
	if err != nil {
		return nil, err
	}
	now := p.now()

	out := make([]Worker, 0, len(state.Workers))
	for _, w := range state.Workers {
		if w.Status.Terminal() {
			continue
		}
		updated, err := p.pollWorker(ctx, cfg, w, now)
		if err != nil {
			return nil, err
		}
		out = append(out, updated)
	}
	return out, nil
}

func (p *Pool) pollWorker(ctx context.Context, cfg Config, w Worker, now time.Time) (Worker, error) {
	if reason, timedOut := timeoutReason(cfg, w, now); timedOut {
		return p.destroyAndFail(ctx, w, reason)
	}
	switch w.Status {
	case StatusCreating, StatusProvisioning:
		if w.InstanceID == "" {
			// Create has not completed yet (or Acquire crashed before
			// recording it); nothing to poll against the cloud yet.
			return w, nil
		}
		return p.pollProvisioning(ctx, w)
	default:
		return w, nil
	}
}

// timeoutReason reports the lifecycle-guard reason w should be destroyed and
// failed, if any: a status-specific timeout first (provisioning or
// activity), then the universal MaxLifetime bound.
func timeoutReason(cfg Config, w Worker, now time.Time) (string, bool) {
	switch w.Status {
	case StatusCreating, StatusProvisioning:
		if !w.CreatedAt.IsZero() && now.Sub(w.CreatedAt) > cfg.ProvisionTimeout {
			return "provisioning timed out", true
		}
	case StatusReady, StatusRunning:
		if !w.LastActivityAt.IsZero() && now.Sub(w.LastActivityAt) > cfg.ActivityTimeout {
			return "activity timed out", true
		}
	}
	if !w.CreatedAt.IsZero() && now.Sub(w.CreatedAt) > cfg.MaxLifetime {
		return "exceeded max lifetime", true
	}
	return "", false
}

// pollProvisioning advances a creating or provisioning worker by checking
// its instance with the provider.
func (p *Pool) pollProvisioning(ctx context.Context, w Worker) (Worker, error) {
	instance, found, err := p.Provisioner.Get(ctx, w.InstanceID)
	if err != nil {
		return Worker{}, fmt.Errorf("vmpool: get instance %s: %w", w.InstanceID, err)
	}
	if !found {
		return p.applyIf(w.ID,
			func(cur *Worker) bool { return !cur.Status.Terminal() },
			func(cur *Worker, now time.Time) {
				cur.Status = StatusFailed
				cur.Error = "instance disappeared"
				cur.TerminalAt = now
			})
	}

	switch w.Status {
	case StatusCreating:
		if instance.Status != "active" || instance.PublicIP == "" {
			return w, nil
		}
		return p.applyIf(w.ID,
			func(cur *Worker) bool { return cur.Status == StatusCreating },
			func(cur *Worker, now time.Time) {
				cur.Status = StatusProvisioning
				cur.PublicIP = instance.PublicIP
				cur.PrivateIP = instance.PrivateIP
			})
	case StatusProvisioning:
		if err := p.health(ctx, w); err != nil {
			// Not yet healthy; try again on the next poll.
			return w, nil
		}
		return p.applyIf(w.ID,
			func(cur *Worker) bool { return cur.Status == StatusProvisioning },
			func(cur *Worker, now time.Time) {
				cur.Status = StatusReady
				cur.ReadyAt = now
				cur.LastActivityAt = now
			})
	default:
		return w, nil
	}
}

func (p *Pool) health(ctx context.Context, w Worker) error {
	if p.Health == nil {
		return nil
	}
	return p.Health(ctx, w)
}

// destroyAndFail marks w StatusFailed with reason, provided it is still
// non-terminal at write time. Unless the pool preserves failures, the
// instance is destroyed (idempotent; a no-op if w has no instance yet); with
// PreserveFailed set the instance is kept running for post-mortem — the
// durable record notes the preservation and the operator reclaims it later
// via Release/reap, so a failure is never diagnosed blind.
func (p *Pool) destroyAndFail(ctx context.Context, w Worker, reason string) (Worker, error) {
	preserved := p.PreserveFailed && w.InstanceID != ""
	if w.InstanceID != "" && !preserved {
		if err := p.Provisioner.Destroy(ctx, w.InstanceID); err != nil {
			return Worker{}, fmt.Errorf("vmpool: destroy instance %s: %w", w.InstanceID, err)
		}
	}
	if preserved {
		reason += fmt.Sprintf(" [instance %s preserved for post-mortem: ssh root@%s, /var/log/kitsoki-worker/boot.log, cloud-init status; reclaim with vmpool release %s]", w.InstanceID, first(w.PublicIP, "<ip pending>"), w.ID)
	}
	return p.applyIf(w.ID,
		func(cur *Worker) bool { return !cur.Status.Terminal() },
		func(cur *Worker, now time.Time) {
			cur.Status = StatusFailed
			cur.Error = reason
			cur.Preserved = preserved
			cur.TerminalAt = now
		})
}

// applyIf mutates the current durable worker record for id under the store
// lock, but only if precondition still holds against the freshly loaded
// record — guarding against a concurrent change made between an unlocked
// cloud call and this write. If the precondition no longer holds, the
// current record is returned unchanged and no write occurs.
func (p *Pool) applyIf(id string, precondition func(*Worker) bool, mutate func(*Worker, time.Time)) (Worker, error) {
	var result Worker
	err := p.Store.Update(func(state *State) error {
		idx := state.indexByID(id)
		if idx < 0 {
			return fmt.Errorf("vmpool: worker %s not found", id)
		}
		cur := &state.Workers[idx]
		if precondition == nil || precondition(cur) {
			mutate(cur, p.now())
		}
		result = *cur
		return nil
	})
	return result, err
}

// Touch bumps LastActivityAt for workerID. Dispatchers call this while a job
// streams events, resetting the ActivityTimeout clock.
func (p *Pool) Touch(ctx context.Context, workerID string) error {
	return p.Store.Update(func(state *State) error {
		idx := state.indexByID(workerID)
		if idx < 0 {
			return fmt.Errorf("vmpool: worker %s not found", workerID)
		}
		w := &state.Workers[idx]
		if w.Status.Terminal() {
			return fmt.Errorf("vmpool: worker %s is terminal (%s)", workerID, w.Status)
		}
		w.LastActivityAt = p.now()
		return nil
	})
}

// MarkRunning transitions workerID from ready to running for jobID. It fails
// if the worker is not currently ready, or is bound to a different job.
func (p *Pool) MarkRunning(ctx context.Context, workerID, jobID string) error {
	return p.Store.Update(func(state *State) error {
		idx := state.indexByID(workerID)
		if idx < 0 {
			return fmt.Errorf("vmpool: worker %s not found", workerID)
		}
		w := &state.Workers[idx]
		if w.Status != StatusReady {
			return fmt.Errorf("vmpool: worker %s is %s, not ready", workerID, w.Status)
		}
		if w.JobID != jobID {
			return fmt.Errorf("vmpool: worker %s is bound to job %s, not %s", workerID, w.JobID, jobID)
		}
		w.Status = StatusRunning
		w.LastActivityAt = p.now()
		return nil
	})
}

// Release destroys workerID's instance (idempotent) and marks it
// StatusDestroyed. Releasing an already-terminal worker is a no-op.
func (p *Pool) Release(ctx context.Context, workerID string) error {
	state, err := p.Store.Load()
	if err != nil {
		return err
	}
	w, ok := state.WorkerByID(workerID)
	if !ok {
		return fmt.Errorf("vmpool: worker %s not found", workerID)
	}
	// A preserved failure is terminal but still owns a live instance; Release
	// is exactly its reclaim path.
	if w.Status.Terminal() && !w.Preserved {
		return nil
	}
	if w.InstanceID != "" {
		if err := p.Provisioner.Destroy(ctx, w.InstanceID); err != nil {
			return fmt.Errorf("vmpool: destroy instance %s: %w", w.InstanceID, err)
		}
	}
	return p.Store.Update(func(state *State) error {
		idx := state.indexByID(workerID)
		if idx < 0 {
			return fmt.Errorf("vmpool: worker %s not found", workerID)
		}
		cur := &state.Workers[idx]
		if cur.Status.Terminal() {
			cur.Preserved = false
			return nil
		}
		cur.Status = StatusDestroyed
		cur.TerminalAt = p.now()
		return nil
	})
}

// Reconcile is the tag-based orphan sweep: cloud instances tagged
// Config.Tag that match no non-terminal durable worker are destroyed as
// orphans, and non-terminal durable workers whose instance no longer exists
// are marked StatusFailed as lost.
func (p *Pool) Reconcile(ctx context.Context) (ReconcileReport, error) {
	cfg := p.config()
	state, err := p.Store.Load()
	if err != nil {
		return ReconcileReport{}, err
	}
	instances, err := p.Provisioner.ListByTag(ctx, cfg.Tag)
	if err != nil {
		return ReconcileReport{}, fmt.Errorf("vmpool: list instances by tag %s: %w", cfg.Tag, err)
	}

	knownInstance := make(map[string]bool, len(state.Workers))
	for _, w := range state.Workers {
		// A preserved failure's instance is intentionally kept for
		// post-mortem: it is known, not an orphan, until explicitly
		// released.
		if (!w.Status.Terminal() || w.Preserved) && w.InstanceID != "" {
			knownInstance[w.InstanceID] = true
		}
	}

	instanceExists := make(map[string]bool, len(instances))
	var report ReconcileReport
	for _, inst := range instances {
		instanceExists[inst.ID] = true
		if knownInstance[inst.ID] {
			report.Active = append(report.Active, inst.ID)
			continue
		}
		if err := p.Provisioner.Destroy(ctx, inst.ID); err != nil {
			return ReconcileReport{}, fmt.Errorf("vmpool: destroy orphan instance %s: %w", inst.ID, err)
		}
		report.Orphans = append(report.Orphans, inst.ID)
	}

	var lost []string
	for _, w := range state.Workers {
		if w.Status.Terminal() || w.InstanceID == "" {
			continue
		}
		if instanceExists[w.InstanceID] {
			continue
		}
		lost = append(lost, w.ID)
	}
	if len(lost) > 0 {
		lostSet := make(map[string]bool, len(lost))
		for _, id := range lost {
			lostSet[id] = true
		}
		if err := p.Store.Update(func(state *State) error {
			for i := range state.Workers {
				w := &state.Workers[i]
				if !lostSet[w.ID] || w.Status.Terminal() {
					continue
				}
				w.Status = StatusFailed
				w.Error = "instance lost"
				w.TerminalAt = p.now()
			}
			return nil
		}); err != nil {
			return ReconcileReport{}, err
		}
	}
	report.Lost = lost
	return report, nil
}

// List returns every durable worker record.
func (p *Pool) List() ([]Worker, error) {
	state, err := p.Store.Load()
	if err != nil {
		return nil, err
	}
	return state.Workers, nil
}
