package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/queue"
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
		client := capsuleQueueClient{binding: binding, bundleRoot: r.cfg.WorkQueueBundleRoot}
		promoter := capsuleQueuePromoter{binding: binding, bundleRoot: r.cfg.WorkQueueBundleRoot}
		exec := workqueue.CapsuleExecutor{Store: store, Client: client, Config: workqueue.CapsuleExecutorConfig{
			ApplicationID: binding.TargetApplication, Queue: binding.Queue, WorkerID: binding.WorkerID,
			ProjectRoot: binding.ProjectRoot, WorkspaceID: binding.WorkspaceID, Pipeline: binding.Pipeline, WorkerPolicy: binding.WorkerPolicy,
			Capabilities: workerregistry.CapabilityLabels(worker), InputSchema: binding.InputSchema,
			MaxConcurrent: binding.MaxConcurrent, LeaseDuration: time.Duration(binding.LeaseSeconds) * time.Second, PollInterval: time.Duration(binding.PollSeconds) * time.Second,
		}, Promoter: promoter}
		go func() { _ = exec.Run(runCtx) }()
	}
	return cancel, nil
}

// capsuleQueuePromoter is the concrete durable bridge: it re-verifies the
// daemon-owned retained bytes and cheap content hash, then imports the bundle
// through the existing idempotent external queue admission. It never executes
// a gate or mutates a protected ref.
type capsuleQueuePromoter struct {
	binding    webconfig.WorkQueueExecutorBinding
	bundleRoot string
}

func (p capsuleQueuePromoter) Promote(ctx context.Context, in workqueue.CapsulePromotionRequest) (string, error) {
	if !in.Job.ProducesCode {
		return "", fmt.Errorf("capsule queue promotion requires a code-producing job")
	}
	validator, err := workqueue.NewFileBundleValidator(p.bundleRoot, queue.DefaultMaxExternalBundle)
	if err != nil {
		return "", err
	}
	ref, err := validator.ValidateBundle(ctx, in.Result.BundleRef, in.Result.BundleDigest)
	if err != nil {
		return "", fmt.Errorf("capsule queue promotion retained bundle: %w", err)
	}
	bundlePath := filepath.Join(p.bundleRoot, ref)
	run, err := (ci.FileRunStore{ProjectRoot: p.binding.ProjectRoot}).Get(in.Result.RunRef)
	if err != nil {
		return "", err
	}
	if !run.Result.Terminal || run.Result.Verdict.Outcome != "passed" ||
		run.Result.Envelope.SourceDigest == "" {
		return "", fmt.Errorf("capsule queue promotion run is not terminal and passed")
	}
	candidateSHA, selectedBundle, cleanup, err := retainedBundleCandidate(ctx, bundlePath, run.Result.Envelope.SourceDigest)
	if err != nil {
		return "", err
	}
	defer cleanup()
	selectedBytes, err := os.ReadFile(selectedBundle)
	if err != nil {
		return "", err
	}
	selectedSum := sha256.Sum256(selectedBytes)
	selectedDigest := "sha256:" + hex.EncodeToString(selectedSum[:])
	target := p.binding.PromotionTarget
	tier := strings.TrimSpace(p.binding.PromotionTier)
	if tier == "" {
		tier = queue.RequiredGateTierForTarget(target)
	}
	baseSHA := run.Result.Envelope.SourceDigest
	targetSHA, err := gitTrim(ctx, p.binding.ProjectRoot, "rev-parse", "--verify", target+"^{commit}")
	if err != nil {
		return "", err
	}
	if candidateSHA == baseSHA {
		return "", fmt.Errorf("capsule queue promotion candidate contains no work beyond its target base")
	}
	policyRaw, _ := json.Marshal(struct {
		Target, TargetPolicy, FinalizationPolicy, Tier, Pipeline string
	}{target, p.binding.PromotionTargetPolicy, p.binding.PromotionFinalizationPolicy, tier, p.binding.Pipeline})
	policySum := sha256.Sum256(policyRaw)
	manifest := "sha256:" + hex.EncodeToString(policySum[:])
	gateSum := sha256.Sum256([]byte(candidateSHA + "\n" + manifest + "\n" + selectedDigest))
	admissionID := "bundle-" + hex.EncodeToString(gateSum[:16])
	branch := "workqueue/" + in.Job.ID
	executionID := in.Result.ExecutionRef
	if executionID == "" {
		executionID = in.Result.RunRef
	}
	submit := queue.Submit{
		Branch: branch, SHA: candidateSHA, TargetRef: target,
		TargetBaseSHAAtAdmission: targetSHA,
		TargetPolicy:             queue.TargetPolicy(p.binding.PromotionTargetPolicy),
		FinalizationPolicy:       queue.FinalizationPolicy(p.binding.PromotionFinalizationPolicy),
		RequiredGateTier:         tier,
		Admission:                queue.DurableBundleAdmission,
		AdmissionID:              admissionID,
		Backend:                  "workqueue-capsule-ci",
		ManifestDigest:           manifest,
	}
	result := queue.ExternalWorkerResult{
		Schema: queue.ExternalWorkerResultSchema, ExecutionID: executionID,
		JobID: in.Job.ID, TrainID: in.Job.ApplicationID + "/" + in.Job.Queue,
		ManifestDigest: manifest, Branch: branch, CandidateSHA: candidateSHA,
		BaseSHA: baseSHA, TargetRef: target, ReceiptID: admissionID,
		BundleDigest: selectedDigest, BundleBytes: int64(len(selectedBytes)),
	}
	candidate, _, err := (queue.Store{
		ProjectRoot: p.binding.ProjectRoot, QueueRoot: p.binding.PromotionQueueRoot,
		LockWait: 2 * time.Second,
	}).AdmitExternalBundle(ctx, queue.ExternalBundleSubmission{Result: result, BundlePath: selectedBundle, Submit: submit})
	if err != nil {
		return "", err
	}
	return candidate.ID, nil
}

// retainedBundleCandidate imports authority-owned bundle bytes into a private
// temporary repository and chooses the single maximal commit beyond the sealed
// pre-work source. Story output fields never participate. Multiple unrelated
// tips are ambiguous and fail closed instead of guessing which work to land.
func retainedBundleCandidate(ctx context.Context, bundlePath, sealedSHA string) (string, string, func(), error) {
	root, err := os.MkdirTemp("", "kitsoki-retained-output-*")
	if err != nil {
		return "", "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	if _, err := gitTrim(ctx, root, "init"); err != nil {
		cleanup()
		return "", "", func() {}, err
	}
	heads, err := gitTrim(ctx, root, "bundle", "list-heads", bundlePath)
	if err != nil {
		cleanup()
		return "", "", func() {}, err
	}
	unique := map[string]struct{}{}
	for index, line := range strings.Split(heads, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if _, imported := unique[fields[0]]; !imported {
			if _, err := gitTrim(ctx, root, "fetch", "--no-tags", bundlePath, fields[0]+":refs/retained/import-"+fmt.Sprint(index)); err != nil {
				cleanup()
				return "", "", func() {}, fmt.Errorf("import retained bundle head %s: %w", fields[0], err)
			}
		}
		if fields[0] == sealedSHA {
			continue
		}
		if _, ancestorErr := gitTrim(ctx, root, "merge-base", "--is-ancestor", sealedSHA, fields[0]); ancestorErr == nil {
			unique[fields[0]] = struct{}{}
		}
	}
	var maximal []string
	for candidate := range unique {
		isAncestor := false
		for other := range unique {
			if candidate == other {
				continue
			}
			if _, err := gitTrim(ctx, root, "merge-base", "--is-ancestor", candidate, other); err == nil {
				isAncestor = true
				break
			}
		}
		if !isAncestor {
			maximal = append(maximal, candidate)
		}
	}
	if len(maximal) != 1 {
		cleanup()
		return "", "", func() {}, fmt.Errorf("retained bundle has %d unambiguous maximal output heads beyond sealed source", len(maximal))
	}
	if _, err := gitTrim(ctx, root, "checkout", "--detach", maximal[0]); err != nil {
		cleanup()
		return "", "", func() {}, err
	}
	selected := filepath.Join(root, "selected.bundle")
	if _, err := gitTrim(ctx, root, "bundle", "create", selected, "HEAD"); err != nil {
		cleanup()
		return "", "", func() {}, err
	}
	return maximal[0], selected, cleanup, nil
}

type capsuleQueueClient struct {
	binding    webconfig.WorkQueueExecutorBinding
	bundleRoot string
	readWIP    func(context.Context, ci.Config, string, string) ([]byte, error)
}

func (c capsuleQueueClient) Dispatch(ctx context.Context, idempotencyKey string, raw json.RawMessage) (workqueue.CapsuleResult, error) {
	if existing, err := (ci.FileRunStore{ProjectRoot: c.binding.ProjectRoot}).Get(idempotencyKey); err == nil {
		return c.project(existing.Result)
	}
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
	result, err := service.Run(ctx, ci.RunRequest{JobID: artifactjob.JobID(idempotencyKey), Pipeline: c.binding.Pipeline, Workspace: control.Handle{ID: instance.ID, Generation: instance.Generation}, DefinitionDigest: instance.DefinitionDigest, SourceDigest: instance.Head, StoryDigest: planned.StoryDigest, JobInputs: inputs, Trigger: ci.Trigger{Kind: "local", RequestedPipeline: c.binding.Pipeline}, ExecutorOverride: c.binding.WorkerID, Detach: true})
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
	cfg, err := ci.Load(c.binding.ProjectRoot)
	if err != nil {
		return workqueue.CapsuleResult{}, err
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
		executionID := status.ExecutionID
		if executionID == "" {
			executionID = status.Result.ExecutionID
		}
		if err := c.projectRetainedWIP(ctx, cfg, run.Result.Executor, executionID, &verdict); err != nil {
			return workqueue.CapsuleResult{}, err
		}
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

// projectRetainedWIP replaces any Story-provided bundle claims with the bytes
// that the worker has already durably mirrored under its execution identity.
// The subsequent fenced queue completion runs the normal bundle validator,
// which retains/canonicalizes this daemon-owned intake file (including the
// PostgreSQL shared-retention path) before it becomes countable.
func (c capsuleQueueClient) projectRetainedWIP(ctx context.Context, cfg ci.Config, executorName, executionID string, verdict *ci.Verdict) error {
	if verdict == nil || verdict.Outcome != "passed" {
		return nil
	}
	if strings.TrimSpace(c.binding.BundleRefOutput) == "" || strings.TrimSpace(c.binding.BundleDigestOutput) == "" {
		return fmt.Errorf("capsule ci terminal bundle output names are not configured")
	}
	root := filepath.Clean(strings.TrimSpace(c.bundleRoot))
	if !filepath.IsAbs(root) {
		return fmt.Errorf("capsule ci retained WIP intake root is not absolute")
	}
	read := c.readWIP
	if read == nil {
		read = ci.ReadRetainedWIP
	}
	data, err := read(ctx, cfg, executorName, executionID)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(data)
	ref, err := writeRetainedWIP(root, executionID, data)
	if err != nil {
		return err
	}
	if verdict.Outputs == nil {
		verdict.Outputs = map[string]string{}
	}
	verdict.Outputs[c.binding.BundleRefOutput] = ref
	verdict.Outputs[c.binding.BundleDigestOutput] = "sha256:" + hex.EncodeToString(digest[:])
	if c.binding.BundleKindOutput != "" {
		verdict.Outputs[c.binding.BundleKindOutput] = "git-bundle"
	}
	return nil
}

func writeRetainedWIP(root, executionID string, data []byte) (string, error) {
	if strings.TrimSpace(executionID) == "" || strings.ContainsAny(executionID, "/\\") || executionID == "." || executionID == ".." || len(data) == 0 {
		return "", fmt.Errorf("capsule ci retained WIP identity or bytes are unsafe")
	}
	dir := filepath.Join(root, "capsule-ci")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create retained WIP intake directory: %w", err)
	}
	path := filepath.Join(dir, executionID+".bundle")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write retained WIP intake: %w", err)
	}
	return filepath.ToSlash(filepath.Join("capsule-ci", executionID+".bundle")), nil
}
