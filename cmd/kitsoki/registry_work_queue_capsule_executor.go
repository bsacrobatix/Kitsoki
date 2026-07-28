package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/record"
	"kitsoki/internal/capsule/storylauncher"
	"kitsoki/internal/host"
	"kitsoki/internal/webconfig"
	"kitsoki/internal/workerregistry"
	"kitsoki/internal/workqueue"
)

// StartWorkQueueCapsuleExecutors starts only deployment-configured direct
// Capsule-CI adapters. Unlike application jobs this path has no session/event
// dispatch: it seals the queue payload into the configured CI pipeline and
// relies on the pipeline's durable executor status for recovery.
func (r *SessionRegistry) StartWorkQueueCapsuleExecutors(ctx context.Context) (func(), error) {
	r.mu.Lock()
	store, bindings, workers := r.workQueueStore, r.cfg.WorkQueueExecutors, append([]workerregistry.Entry(nil), r.cfg.Workers...)
	r.mu.Unlock()
	if len(bindings) == 0 {
		return func() {}, nil
	}
	if store == nil {
		return nil, fmt.Errorf("work queue capsule executors require daemon queue storage")
	}
	runCtx, cancel := context.WithCancel(ctx)
	for id, binding := range bindings {
		worker, ok := configuredQueueWorker(workers, binding.WorkerID)
		if !ok {
			cancel()
			return nil, fmt.Errorf("work queue capsule executor %q worker %q is unavailable", id, binding.WorkerID)
		}
		client := capsuleQueueClient{binding: binding}
		exec := workqueue.CapsuleExecutor{Store: store, Client: client, Config: workqueue.CapsuleExecutorConfig{
			ApplicationID: binding.TargetApplication, Queue: binding.Queue, WorkerID: binding.WorkerID,
			ProjectRoot: binding.ProjectRoot, WorkspaceID: binding.WorkspaceID, Pipeline: binding.Pipeline, WorkerPolicy: binding.WorkerPolicy,
			Capabilities: workerregistry.CapabilityLabels(worker), InputSchema: binding.InputSchema,
			MaxConcurrent: binding.MaxConcurrent, LeaseDuration: time.Duration(binding.LeaseSeconds) * time.Second, PollInterval: time.Duration(binding.PollSeconds) * time.Second,
		}}
		go func() { _ = exec.Run(runCtx) }()
	}
	return cancel, nil
}

type capsuleQueueClient struct {
	binding webconfig.WorkQueueExecutorBinding
}

func (c capsuleQueueClient) Dispatch(ctx context.Context, raw json.RawMessage) (workqueue.CapsuleResult, error) {
	var inputs map[string]any
	if err := json.Unmarshal(raw, &inputs); err != nil {
		return workqueue.CapsuleResult{}, err
	}
	m, instance, pipeline, planned, workspace, err := ciInputs(ctx, c.binding.ProjectRoot, c.binding.WorkspaceID, c.binding.Pipeline, ci.Trigger{Kind: "local", RequestedPipeline: c.binding.Pipeline}, inputs)
	if err != nil {
		return workqueue.CapsuleResult{}, err
	}
	_ = m
	if err := checkCapsuleCIPlacement(c.binding.ProjectRoot, c.binding.Pipeline, c.binding.WorkerPolicy, c.binding.WorkerID); err != nil {
		return workqueue.CapsuleResult{}, err
	}
	cfg, err := ci.Load(workspace)
	if err != nil {
		return workqueue.CapsuleResult{}, err
	}
	executors := ci.NewConfiguredExecutors(cfg)
	executors.ProjectRoot, executors.PoolStateRoot = workspace, c.binding.ProjectRoot
	executors.Source = executor.SourceBundlerFunc(func(ctx context.Context, envelope executor.Envelope) (executor.SourceBundle, error) {
		return executor.GitBundle(ctx, workspace, envelope.SourceDigest, 0)
	})
	launcher := storylauncher.Launcher{StoryPath: filepath.Join(workspace, pipeline.Story), ProjectRoot: workspace, AgentLaunchPolicy: host.AgentLaunchPolicy{Enabled: true, AllowedRoots: []string{workspace}}}
	service := ci.Service{ProjectRoot: workspace, Jobs: artifactjob.NewMemoryStore(), Env: environment.Resolver{ProjectRoot: workspace, Probe: environment.HostProbe()}, Executors: executors, Launcher: launcher, Hygiene: capsuleCIHygienePlanner(c.binding.ProjectRoot), Observer: record.FileRunObserver{ProjectRoot: c.binding.ProjectRoot}}
	result, err := service.Run(ctx, ci.RunRequest{Pipeline: c.binding.Pipeline, Workspace: control.Handle{ID: instance.ID, Generation: instance.Generation}, DefinitionDigest: instance.DefinitionDigest, SourceDigest: instance.Head, StoryDigest: planned.StoryDigest, JobInputs: inputs, Trigger: ci.Trigger{Kind: "local", RequestedPipeline: c.binding.Pipeline}, ExecutorOverride: c.binding.WorkerID, Detach: true})
	if err != nil {
		_ = persistCapsuleCIRunFailure(c.binding.ProjectRoot, result, err)
		return workqueue.CapsuleResult{}, err
	}
	if err := (ci.FileRunStore{ProjectRoot: c.binding.ProjectRoot}).Write(ci.RunRecord{JobID: string(result.Job.ID), Result: result}); err != nil {
		return workqueue.CapsuleResult{}, err
	}
	return c.project(result)
}

func (c capsuleQueueClient) Status(ctx context.Context, runRef string) (workqueue.CapsuleResult, error) {
	store := ci.FileRunStore{ProjectRoot: c.binding.ProjectRoot}
	run, err := store.Get(runRef)
	if err != nil {
		return workqueue.CapsuleResult{}, err
	}
	if run.Result.Terminal {
		return c.project(run.Result)
	}
	controller, pipeline, authority, err := capsuleCIExecutionController(ctx, c.binding.ProjectRoot, run)
	if err != nil {
		return workqueue.CapsuleResult{}, err
	}
	if run.Result.ExecutorControl == nil && authority != nil {
		run.Result.ExecutorControl = authority
		if err := store.Write(run); err != nil {
			return workqueue.CapsuleResult{}, err
		}
	}
	status, err := controller.Status(ctx, run.Result.Execution.ExecutionID)
	if err != nil {
		return workqueue.CapsuleResult{}, err
	}
	run, err = store.RecordExecutorStatus(runRef, status)
	if err != nil {
		return workqueue.CapsuleResult{}, err
	}
	if status.Status == "completed" && !run.Result.Terminal && len(status.Result.VerdictJSON) > 0 {
		var verdict ci.Verdict
		if err := json.Unmarshal(status.Result.VerdictJSON, &verdict); err != nil {
			return workqueue.CapsuleResult{}, err
		}
		verdict = ci.NormalizeVerdict(verdict)
		if err := ci.ValidateVerdict(verdict, run.Result.Envelope, pipeline.Result); err != nil {
			return workqueue.CapsuleResult{}, err
		}
		run, err = store.FinalizeCollected(runRef, verdict, status.Result)
		if err != nil {
			return workqueue.CapsuleResult{}, err
		}
		stored, err := record.PersistWithOptions(c.binding.ProjectRoot, run.Result, record.PersistOptions{})
		if err != nil {
			return workqueue.CapsuleResult{}, err
		}
		run.ReceiptID, run.ReceiptVerification = stored.Receipt.ReceiptID, stored.Verification.Status
		if err := store.Write(run); err != nil {
			return workqueue.CapsuleResult{}, err
		}
	}
	return c.project(run.Result)
}

func (c capsuleQueueClient) project(result ci.RunResult) (workqueue.CapsuleResult, error) {
	status := result.Verdict.Outcome
	if status == "" && !result.Terminal {
		status = "running"
	}
	out := workqueue.CapsuleResult{RunRef: string(result.Job.ID), ExecutionRef: result.Execution.ExecutionID, Status: status, Reason: result.Verdict.Summary}
	if status != "passed" {
		return out, nil
	}
	ref, refOK := result.Verdict.Outputs[c.binding.BundleRefOutput]
	digest, digestOK := result.Verdict.Outputs[c.binding.BundleDigestOutput]
	if !refOK || !digestOK {
		return workqueue.CapsuleResult{}, fmt.Errorf("capsule ci terminal bundle outputs are missing")
	}
	out.BundleRef, out.BundleDigest, out.BundleKind = ref, digest, result.Verdict.Outputs[c.binding.BundleKindOutput]
	return out, nil
}
