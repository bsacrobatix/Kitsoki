package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/queue"
	"kitsoki/internal/capsule/receipt"
	"kitsoki/internal/capsule/reconcile"
	"kitsoki/internal/capsule/record"
	"kitsoki/internal/capsule/storylauncher"
	"kitsoki/internal/host"
)

func capsulePromoteExistingCmd() *cobra.Command {
	var project, sourceTarget, sha, destinationTarget, pipeline, gate, definition string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:          "promote-existing",
		Short:        "Receipt-bind an exact landed target SHA and admit it to another protected queue",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := filepath.Abs(project)
			if err != nil {
				return err
			}
			request := queue.PromoteExistingRequest{
				SourceTarget: sourceTarget, LandedSHA: sha,
				DestinationTarget: destinationTarget, Pipeline: pipeline,
				GateCommand: gate,
			}
			authority := queue.PromoteExistingAuthority{
				ProjectRoot: root,
				Certifier: queue.ExistingSHACertifierFunc(func(ctx context.Context, in queue.ExistingSHACertification) (record.Stored, error) {
					return runPromoteExistingCI(ctx, root, definition, in)
				}),
			}
			result, err := authority.Promote(cmd.Context(), request)
			if err != nil {
				return err
			}
			return capsuleWorkspaceWrite(cmd, result, jsonOut)
		},
	}
	cmd.Flags().StringVar(&project, "project", ".", "protected project root")
	cmd.Flags().StringVar(&sourceTarget, "source-target", "", "source target whose landed queue result is authoritative")
	cmd.Flags().StringVar(&sha, "sha", "", "exact SHA currently landed on the source target")
	cmd.Flags().StringVar(&destinationTarget, "target", "main", "destination protected target")
	cmd.Flags().StringVar(&pipeline, "pipeline", "change", "Capsule CI pipeline to run against the exact SHA")
	cmd.Flags().StringVar(&gate, "gate", "git diff --check", "deterministic gate the destination queue worker must run")
	cmd.Flags().StringVar(&definition, "definition", "development", "Capsule definition identity for exact-source CI")
	cmd.Flags().BoolVar(&jsonOut, "json", true, "print JSON")
	_ = cmd.MarkFlagRequired("source-target")
	_ = cmd.MarkFlagRequired("sha")
	return cmd
}

func runPromoteExistingCI(ctx context.Context, project, definitionID string, in queue.ExistingSHACertification) (record.Stored, error) {
	if stored, ok, err := recoverPromoteExistingCI(ctx, project, in); err != nil {
		return record.Stored{}, err
	} else if ok {
		return stored, nil
	}

	checkout, err := os.MkdirTemp("", "kitsoki-promote-existing-")
	if err != nil {
		return record.Stored{}, err
	}
	if err := os.Remove(checkout); err != nil {
		return record.Stored{}, err
	}
	defer os.RemoveAll(checkout)
	if _, err := gitTrim(ctx, project, "clone", "--shared", "--no-checkout", project, checkout); err != nil {
		return record.Stored{}, fmt.Errorf("capsule promote-existing: materialize exact source: %w", err)
	}
	if _, err := gitTrim(ctx, checkout, "checkout", "--detach", in.Request.LandedSHA); err != nil {
		return record.Stored{}, fmt.Errorf("capsule promote-existing: checkout exact source: %w", err)
	}

	instanceID := strings.TrimPrefix(in.JobID, "promote-existing-")
	jobInputs := map[string]any{
		"promote_existing": map[string]any{
			"schema":                    queue.PromoteExistingSchema,
			"request_key":               in.Key,
			"source_target":             in.Request.SourceTarget,
			"landed_sha":                in.Request.LandedSHA,
			"destination_target":        in.Request.DestinationTarget,
			"source_landing_candidate":  in.SourceCandidate.ID,
			"source_landing_receipt_id": in.SourceLandingReceiptID,
		},
	}
	trigger := ci.Trigger{Kind: "local", Ref: "promote-existing/" + in.Key, RequestedPipeline: in.Request.Pipeline}
	_, instance, pipeline, planned, workspacePath, err := ciSourceInputs(ctx, project, checkout, in.Request.LandedSHA, instanceID, definitionID, in.Request.Pipeline, trigger, jobInputs)
	if err != nil {
		return record.Stored{}, err
	}
	cfg, err := ci.Load(workspacePath)
	if err != nil {
		return record.Stored{}, err
	}
	authority, err := ci.NewImmutableExecutorControlAuthority(cfg, in.Request.Pipeline, pipeline.Executor, pipeline.Result)
	if err != nil {
		return record.Stored{}, err
	}
	executors := ci.NewConfiguredExecutors(cfg)
	executors.ProjectRoot = workspacePath
	executors.PoolStateRoot = project
	executors.Source = executor.SourceBundlerFunc(func(ctx context.Context, envelope executor.Envelope) (executor.SourceBundle, error) {
		return executor.GitCommitBundle(ctx, workspacePath, envelope.SourceDigest, 0)
	})
	launcher := storylauncher.Launcher{
		StoryPath: filepath.Join(workspacePath, pipeline.Story), ProjectRoot: workspacePath,
		AgentLaunchPolicy: host.AgentLaunchPolicy{Enabled: true, AllowedRoots: []string{workspacePath}},
	}
	service := ci.Service{
		ProjectRoot: workspacePath,
		Jobs:        fixedArtifactJobStore{id: artifactjob.JobID(in.JobID), inner: artifactjob.NewMemoryStore()},
		Env:         environment.Resolver{ProjectRoot: workspacePath, Probe: environment.HostProbe()},
		Executors:   executors,
		Launcher:    launcher,
		Hygiene:     capsuleCIHygienePlanner(project),
		Observer:    record.FileRunObserver{ProjectRoot: project},
	}
	result, err := service.Run(ctx, ci.RunRequest{
		Pipeline:         in.Request.Pipeline,
		Workspace:        control.Handle{ID: instance.ID, Generation: instance.Generation},
		DefinitionDigest: instance.DefinitionDigest,
		SourceDigest:     instance.Head,
		StoryDigest:      planned.StoryDigest,
		JobInputs:        jobInputs,
		Trigger:          trigger,
		ExecutorControl:  &authority,
	})
	if err != nil {
		return record.Stored{}, persistCapsuleCIRunFailure(project, result, err)
	}
	stored, err := record.Persist(project, result)
	if err != nil {
		return record.Stored{}, err
	}
	if err := (ci.FileRunStore{ProjectRoot: project}).Write(ci.RunRecord{
		JobID: in.JobID, Result: result, ReceiptID: stored.Receipt.ReceiptID,
		ReceiptVerification: stored.Verification.Status,
	}); err != nil {
		return record.Stored{}, err
	}
	return stored, nil
}

func recoverPromoteExistingCI(ctx context.Context, project string, in queue.ExistingSHACertification) (record.Stored, bool, error) {
	store := ci.FileRunStore{ProjectRoot: project}
	run, err := store.Get(in.JobID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return record.Stored{}, false, nil
		}
		return record.Stored{}, false, err
	}
	if run.Result.Envelope.SourceDigest != "" && run.Result.Envelope.SourceDigest != in.Request.LandedSHA {
		return record.Stored{}, false, fmt.Errorf("capsule promote-existing: deterministic CI job %s is bound to source %s, not %s", in.JobID, run.Result.Envelope.SourceDigest, in.Request.LandedSHA)
	}
	if !run.Result.Terminal && run.Result.Execution.ExecutionID != "" && run.Result.ExecutorControl != nil {
		// A controller restart must reconcile the already-dispatched durable
		// executor run. Starting another worker attempt here would defeat the
		// deterministic job identity even if the final receipt filename were
		// eventually overwritten.
		command := &cobra.Command{}
		command.SetContext(ctx)
		command.SetOut(io.Discard)
		command.SetErr(io.Discard)
		run, err = capsuleCIRefreshJob(command, project, in.JobID, store, run)
		if err != nil {
			return record.Stored{}, false, fmt.Errorf("capsule promote-existing: reconcile existing exact-source CI job %s: %w", in.JobID, err)
		}
		if !run.Result.Terminal {
			return record.Stored{}, false, fmt.Errorf("capsule promote-existing: exact-source CI job %s remains %s", in.JobID, run.Result.Stage)
		}
	}
	if run.ReceiptID == "" {
		if !run.Result.Terminal || !run.Result.Verdict.PromotionEligible {
			return record.Stored{}, false, nil
		}
		stored, err := record.Persist(project, run.Result)
		if err != nil {
			return record.Stored{}, false, err
		}
		run.ReceiptID = stored.Receipt.ReceiptID
		run.ReceiptVerification = stored.Verification.Status
		if err := (ci.FileRunStore{ProjectRoot: project}).Write(run); err != nil {
			return record.Stored{}, false, err
		}
		return stored, true, nil
	}
	path := filepath.Join(project, ".capsules", "ci", in.JobID+".receipt.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return record.Stored{}, false, err
	}
	var persisted receipt.Receipt
	if err := json.Unmarshal(raw, &persisted); err != nil {
		return record.Stored{}, false, err
	}
	if persisted.ReceiptID != run.ReceiptID || persisted.Envelope.SourceDigest != in.Request.LandedSHA {
		return record.Stored{}, false, fmt.Errorf("capsule promote-existing: persisted CI receipt does not match deterministic job authority")
	}
	if err := (record.PromotionGate{ProjectRoot: project}).Verify(context.Background(), persisted.ReceiptID, reconcile.Plan{Candidate: in.Request.LandedSHA}); err != nil {
		return record.Stored{}, false, err
	}
	return record.Stored{Receipt: persisted, ReceiptPath: path, Verification: receipt.Verify(persisted, nil, false)}, true, nil
}

// fixedArtifactJobStore gives exact-SHA promotion a deterministic durable CI
// job identity. The wrapped registry is process-local; run checkpoints and the
// receipt remain project-local through FileRunObserver/FileRunStore.
type fixedArtifactJobStore struct {
	id    artifactjob.JobID
	inner *artifactjob.MemoryStore
}

func (s fixedArtifactJobStore) Register(ctx context.Context, req artifactjob.RegisterRequest) (artifactjob.Job, error) {
	req.ID = s.id
	return s.inner.Register(ctx, req)
}
func (s fixedArtifactJobStore) BindRun(ctx context.Context, id artifactjob.JobID, sessionID, runURL, tracePath string) (artifactjob.Job, error) {
	return s.inner.BindRun(ctx, id, sessionID, runURL, tracePath)
}
func (s fixedArtifactJobStore) Update(ctx context.Context, id artifactjob.JobID, update artifactjob.Update) (artifactjob.Job, error) {
	return s.inner.Update(ctx, id, update)
}
func (s fixedArtifactJobStore) Attach(ctx context.Context, id artifactjob.JobID, sessionID string) (artifactjob.Job, error) {
	return s.inner.Attach(ctx, id, sessionID)
}
func (s fixedArtifactJobStore) Get(ctx context.Context, id artifactjob.JobID) (artifactjob.Job, error) {
	return s.inner.Get(ctx, id)
}
func (s fixedArtifactJobStore) List(ctx context.Context, filter artifactjob.ListFilter) ([]artifactjob.Job, error) {
	return s.inner.List(ctx, filter)
}
func (s fixedArtifactJobStore) Archive(ctx context.Context, id artifactjob.JobID) (artifactjob.Job, error) {
	return s.inner.Archive(ctx, id)
}
func (s fixedArtifactJobStore) SweepInterrupted(ctx context.Context, sessionID string) (int64, error) {
	return s.inner.SweepInterrupted(ctx, sessionID)
}
