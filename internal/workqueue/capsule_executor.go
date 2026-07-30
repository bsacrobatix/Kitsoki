package workqueue

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// CapsuleResult is the daemon-private projection of one sealed Capsule CI
// run. RunRef and ExecutionRef are durable provider identities; bundle fields
// are copied only after the configured client has verified the terminal CI
// result contract.
type CapsuleResult struct {
	RunRef, ExecutionRef, Status, Reason string
	Artifacts                            []string
	BundleRef, BundleDigest, BundleKind  string
}

// CapsuleClient starts and polls an allowlisted Capsule CI pipeline. The
// executor config owns project, workspace, pipeline, and worker policy; the
// queue supplies only a validated payload.
type CapsuleClient interface {
	Dispatch(context.Context, string, json.RawMessage) (CapsuleResult, error)
	Status(context.Context, string) (CapsuleResult, error)
}

// CapsulePromotionSink is the durable bridge from a passed code-producing
// work item into the protected merge queue. Promote is deliberately invoked
// before Store.Complete and is required to be idempotent for the exact
// (job, result, receipt) tuple. Therefore a daemon crash after admission but
// before fenced completion simply replays the same immutable admission.
//
// Implementations must not run a protected ref mutation here. They retain and
// verify the bundle, persist queue admission, and return its durable identity;
// the target-partitioned merge worker remains the sole finalizer.
type CapsulePromotionSink interface {
	Promote(context.Context, CapsulePromotionRequest) (string, error)
}

type CapsulePromotionRequest struct {
	Job     Job
	Result  CapsuleResult
	Receipt Receipt
}

// InputField is a bounded structural input contract. It intentionally covers
// the JSON values a story can consume without admitting arbitrary object
// schemas or a command/template language.
type InputField struct {
	Type     string `yaml:"type"`
	Required bool   `yaml:"required,omitempty"`
}

// CapsuleExecutorConfig is server-owned policy for one queue-to-Capsule CI
// binding. Project/workspace/pipeline/worker identifiers are recorded here for
// audit and validated by webconfig before daemon startup; this package uses the
// execution fields and typed payload schema only.
type CapsuleExecutorConfig struct {
	ApplicationID, Queue, WorkerID string
	ProjectRoot, WorkspaceID       string
	Pipeline, WorkerPolicy         string
	Capabilities                   []string
	InputSchema                    map[string]InputField
	MaxConcurrent                  int
	LeaseDuration, PollInterval    time.Duration
}

func (c CapsuleExecutorConfig) valid() bool {
	return validIdentity(c.ApplicationID) && validIdentity(c.Queue) && validIdentity(c.WorkerID) &&
		strings.TrimSpace(c.ProjectRoot) != "" && validIdentity(c.WorkspaceID) &&
		validIdentity(c.Pipeline) && validIdentity(c.WorkerPolicy) && c.MaxConcurrent > 0 &&
		c.LeaseDuration > 0 && c.PollInterval > 0 && validateInputSchema(c.InputSchema) == nil
}

// CapsuleExecutor never invokes a shell command or a Story Application event.
// A daemon crash merely stops heartbeats; the next lease holder reads the
// durable Capsule dispatch mapping and polls its provider-owned run instead of
// asserting that the old process resumed.
type CapsuleExecutor struct {
	Store    Store
	Client   CapsuleClient
	Promoter CapsulePromotionSink
	Config   CapsuleExecutorConfig
}

func (e CapsuleExecutor) Run(ctx context.Context) error {
	if e.Store == nil || e.Client == nil || !e.Config.valid() {
		return fmt.Errorf("workqueue capsule executor is not configured")
	}
	if _, ok := e.Store.(CapsuleDispatchStore); !ok {
		return fmt.Errorf("workqueue capsule executor requires durable dispatch storage")
	}
	var workers sync.WaitGroup
	for range e.Config.MaxConcurrent {
		workers.Add(1)
		go func() { defer workers.Done(); e.loop(ctx) }()
	}
	workers.Wait()
	return nil
}

func (e CapsuleExecutor) loop(ctx context.Context) {
	for ctx.Err() == nil {
		lease, err := e.Store.ClaimNext(ctx, ClaimRequest{ApplicationID: e.Config.ApplicationID, Queue: e.Config.Queue, WorkerID: e.Config.WorkerID, Capabilities: e.Config.Capabilities, AvailableCapacity: e.Config.MaxConcurrent, LeaseDuration: e.Config.LeaseDuration})
		if err != nil || lease == nil {
			if !capsuleWait(ctx, e.Config.PollInterval) {
				return
			}
			continue
		}
		e.runLease(ctx, lease.Job)
	}
}

func (e CapsuleExecutor) runLease(ctx context.Context, job Job) {
	dispatches := e.Store.(CapsuleDispatchStore)
	if err := validateCapsulePayload(job.Payload, e.Config.InputSchema); err != nil {
		e.fail(ctx, job, false, "capsule_ci_input_invalid", CapsuleResult{})
		return
	}
	dispatch, err := dispatches.GetCapsuleDispatch(ctx, job.ID)
	if err != nil && err != ErrNotFound {
		e.fail(ctx, job, true, "capsule_ci_dispatch_lookup_failed", CapsuleResult{})
		return
	}
	var result CapsuleResult
	if err == ErrNotFound {
		result, err = e.Client.Dispatch(ctx, job.ID, json.RawMessage(job.Payload))
		if err != nil || strings.TrimSpace(result.RunRef) == "" {
			e.fail(ctx, job, true, "capsule_ci_dispatch_failed", result)
			return
		}
		if err := dispatches.PutCapsuleDispatch(ctx, CapsuleDispatch{WorkRef: job.ID, RunRef: result.RunRef, ExecutionRef: result.ExecutionRef, PayloadDigest: string(job.PayloadDigest)}); err != nil {
			// The provider might be running, but without the durable identity we
			// cannot truthfully poll or recover it.
			e.fail(ctx, job, true, "capsule_ci_dispatch_persistence_failed", result)
			return
		}
	} else {
		if dispatch.PayloadDigest != string(job.PayloadDigest) {
			e.fail(ctx, job, false, "capsule_ci_dispatch_payload_mismatch", CapsuleResult{})
			return
		}
		result, err = e.Client.Status(ctx, dispatch.RunRef)
		if err != nil {
			e.fail(ctx, job, true, "capsule_ci_status_failed", CapsuleResult{RunRef: dispatch.RunRef})
			return
		}
	}
	for {
		switch result.Status {
		case "passed":
			completion := e.receipt(job, result)
			if job.ProducesCode {
				if e.Promoter == nil {
					e.fail(ctx, job, false, "capsule_ci_promotion_unconfigured", result)
					return
				}
				identity, promoteErr := e.Promoter.Promote(ctx, CapsulePromotionRequest{Job: job, Result: result, Receipt: completion})
				if promoteErr != nil {
					e.fail(ctx, job, true, "capsule_ci_promotion_failed", CapsuleResult{
						RunRef: result.RunRef, ExecutionRef: result.ExecutionRef,
						BundleRef: result.BundleRef, BundleDigest: result.BundleDigest, BundleKind: result.BundleKind,
						Reason: promoteErr.Error(),
					})
					return
				}
				completion.ArtifactHandles = append(completion.ArtifactHandles, "capsule-queue:"+identity)
			}
			if _, err := e.Store.Complete(ctx, job.ID, e.Config.WorkerID, job.Fence, completion); err == nil {
				_ = dispatches.DeleteCapsuleDispatch(ctx, job.ID)
			}
			return
		case "failed", "infra_failed", "cancelled", "interrupted":
			retry := result.Status == "infra_failed" || result.Status == "interrupted"
			e.fail(ctx, job, retry, "capsule_ci_"+result.Status, result)
			return
		case "running", "requested", "preparing", "":
		default:
			e.fail(ctx, job, true, "capsule_ci_unknown_status", result)
			return
		}
		if !capsuleWait(ctx, e.Config.PollInterval) {
			return
		}
		if _, err := e.Store.Heartbeat(ctx, job.ID, e.Config.WorkerID, job.Fence, e.Config.LeaseDuration); err != nil {
			return
		}
		result, err = e.Client.Status(ctx, result.RunRef)
		if err != nil {
			e.fail(ctx, job, true, "capsule_ci_status_failed", CapsuleResult{RunRef: result.RunRef})
			return
		}
	}
}

func (e CapsuleExecutor) receipt(job Job, result CapsuleResult) Receipt {
	return Receipt{Schema: ReceiptSchema, Outcome: "done", JobID: job.ID, Attempt: job.Attempts, Fence: job.Fence, TraceRef: result.RunRef, ArtifactHandles: append([]string(nil), result.Artifacts...), BundleRef: result.BundleRef, BundleDigest: result.BundleDigest, BundleKind: result.BundleKind}
}

func (e CapsuleExecutor) fail(ctx context.Context, job Job, retryable bool, fallback string, result CapsuleResult) {
	reason := strings.TrimSpace(result.Reason)
	if reason == "" {
		reason = fallback
	}
	_, _ = e.Store.Fail(ctx, job.ID, e.Config.WorkerID, job.Fence, Failure{Retryable: retryable, Message: reason, Receipt: e.receipt(job, result)})
}

func validateInputSchema(schema map[string]InputField) error {
	if len(schema) == 0 || len(schema) > 32 {
		return ErrInvalid
	}
	for key, field := range schema {
		if !validIdentity(key) {
			return ErrInvalid
		}
		switch field.Type {
		case "string", "integer", "number", "boolean", "object", "array":
		default:
			return ErrInvalid
		}
	}
	return nil
}

func validateCapsulePayload(raw []byte, schema map[string]InputField) error {
	if err := validateInputSchema(schema); err != nil {
		return err
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return ErrInvalid
	}
	for key := range value {
		if _, ok := schema[key]; !ok {
			return ErrInvalid
		}
	}
	for key, field := range schema {
		v, ok := value[key]
		if !ok {
			if field.Required {
				return ErrInvalid
			}
			continue
		}
		if !inputTypeMatches(v, field.Type) {
			return ErrInvalid
		}
	}
	return nil
}

func inputTypeMatches(value any, want string) bool {
	switch want {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		n, ok := value.(float64)
		return ok && n == float64(int64(n))
	}
	return false
}

func capsuleWait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
