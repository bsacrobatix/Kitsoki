package workqueue

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ApplicationJobResult is the daemon-private terminal projection required to
// turn one fixed Application Job into a fenced work-queue receipt.
type ApplicationJobResult struct {
	Ref, Status, Reason                 string
	Artifacts                           []string
	BundleRef, BundleDigest, BundleKind string
}

// ApplicationJobClient is deliberately narrower than the public host. The
// deployment binding fixes caller and template; queue payload is its only
// variable input.
type ApplicationJobClient interface {
	Submit(context.Context, string, string, json.RawMessage) (ApplicationJobResult, error)
	Status(context.Context, string, string) (ApplicationJobResult, error)
}

// ApplicationExecutorConfig is immutable daemon-owned execution policy for a
// single queue. It does not carry an endpoint, command, path, or provider.
type ApplicationExecutorConfig struct {
	ApplicationID, Queue, WorkerID string
	CallerApplication, Template    string
	Capabilities                   []string
	MaxConcurrent                  int
	LeaseDuration, PollInterval    time.Duration
}

func (c ApplicationExecutorConfig) valid() bool {
	return validIdentity(c.ApplicationID) && validIdentity(c.Queue) && validIdentity(c.WorkerID) &&
		validIdentity(c.CallerApplication) && validIdentity(c.Template) && c.MaxConcurrent > 0 &&
		c.LeaseDuration > 0 && c.PollInterval > 0
}

// ApplicationExecutor runs a configured queue adapter until its context is
// cancelled. Cancellation releases no lease; recovery remains lease expiry.
type ApplicationExecutor struct {
	Store  Store
	Jobs   ApplicationJobClient
	Config ApplicationExecutorConfig
}

func (e ApplicationExecutor) Run(ctx context.Context) error {
	if e.Store == nil || e.Jobs == nil || !e.Config.valid() {
		return fmt.Errorf("workqueue application executor is not configured")
	}
	var workers sync.WaitGroup
	for range e.Config.MaxConcurrent {
		workers.Add(1)
		go func() {
			defer workers.Done()
			e.loop(ctx)
		}()
	}
	workers.Wait()
	return nil
}

func (e ApplicationExecutor) loop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		lease, err := e.Store.ClaimNext(ctx, ClaimRequest{
			ApplicationID: e.Config.ApplicationID, Queue: e.Config.Queue,
			WorkerID: e.Config.WorkerID, Capabilities: e.Config.Capabilities,
			AvailableCapacity: e.Config.MaxConcurrent, LeaseDuration: e.Config.LeaseDuration,
		})
		if err != nil {
			if !wait(ctx, e.Config.PollInterval) {
				return
			}
			continue
		}
		if lease == nil {
			if !wait(ctx, e.Config.PollInterval) {
				return
			}
			continue
		}
		e.runLease(ctx, lease.Job)
	}
}

func (e ApplicationExecutor) runLease(ctx context.Context, job Job) {
	result, err := e.Jobs.Submit(ctx, e.Config.CallerApplication, e.Config.Template, json.RawMessage(job.Payload))
	if err != nil {
		e.fail(ctx, job, true, "application_job_submit_failed", ApplicationJobResult{})
		return
	}
	for {
		switch result.Status {
		case "done":
			receipt := e.receipt(job, result)
			if _, err := e.Store.Complete(ctx, job.ID, e.Config.WorkerID, job.Fence, receipt); err != nil {
				return // stale lease or failed evidence validation: never forge a fallback.
			}
			return
		case "failed", "interrupted":
			e.fail(ctx, job, true, "application_job_"+result.Status, result)
			return
		case "cancelled", "archived":
			e.fail(ctx, job, false, "application_job_"+result.Status, result)
			return
		}
		if !wait(ctx, e.Config.PollInterval) {
			return
		}
		if _, err := e.Store.Heartbeat(ctx, job.ID, e.Config.WorkerID, job.Fence, e.Config.LeaseDuration); err != nil {
			return
		}
		result, err = e.Jobs.Status(ctx, e.Config.CallerApplication, result.Ref)
		if err != nil {
			e.fail(ctx, job, true, "application_job_status_failed", ApplicationJobResult{})
			return
		}
	}
}

func (e ApplicationExecutor) receipt(job Job, result ApplicationJobResult) Receipt {
	return Receipt{Schema: ReceiptSchema, Outcome: "done", JobID: job.ID, Attempt: job.Attempts,
		Fence: job.Fence, TraceRef: result.Ref, ArtifactHandles: append([]string(nil), result.Artifacts...),
		BundleRef: result.BundleRef, BundleDigest: result.BundleDigest, BundleKind: result.BundleKind}
}

func (e ApplicationExecutor) fail(ctx context.Context, job Job, retryable bool, fallback string, result ApplicationJobResult) {
	reason := strings.TrimSpace(result.Reason)
	if reason == "" {
		reason = fallback
	}
	_, _ = e.Store.Fail(ctx, job.ID, e.Config.WorkerID, job.Fence, Failure{
		Retryable: retryable, Message: reason, Receipt: e.receipt(job, result),
	})
}

func wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
