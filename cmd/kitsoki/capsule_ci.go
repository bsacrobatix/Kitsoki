package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/hygiene"
	"kitsoki/internal/capsule/record"
	"kitsoki/internal/capsule/storylauncher"
)

func capsuleCICmd() *cobra.Command {
	cmd := &cobra.Command{Use: "ci", Short: "Plan, run, and inspect story-native Capsule CI"}
	cmd.AddCommand(capsuleCIPlanCmd(), capsuleCIRunCmd(), capsuleCIStatusCmd(), capsuleCISummaryCmd(), capsuleCICancelCmd())
	return cmd
}
func ciInputs(ctx context.Context, project, workspace, pipeline string) (*control.Manager, control.Instance, ci.Pipeline, executor.Envelope, error) {
	root, err := filepath.Abs(project)
	if err != nil {
		return nil, control.Instance{}, ci.Pipeline{}, executor.Envelope{}, err
	}
	project = root
	m, err := capsuleWorkspaceManager(project)
	if err != nil {
		return nil, control.Instance{}, ci.Pipeline{}, executor.Envelope{}, err
	}
	in, err := m.Instances.Get(ctx, workspace)
	if err != nil {
		return nil, control.Instance{}, ci.Pipeline{}, executor.Envelope{}, err
	}
	cfg, err := ci.Load(project)
	if err != nil {
		return nil, control.Instance{}, ci.Pipeline{}, executor.Envelope{}, err
	}
	p, ok := cfg.Pipelines[pipeline]
	if !ok {
		return nil, control.Instance{}, ci.Pipeline{}, executor.Envelope{}, fmt.Errorf("capsule ci: pipeline %q not found", pipeline)
	}
	storyDigest, err := fileDigest(filepath.Join(project, p.Story))
	if err != nil {
		return nil, control.Instance{}, ci.Pipeline{}, executor.Envelope{}, err
	}
	service := ci.Service{ProjectRoot: project, Env: environment.Resolver{ProjectRoot: project, Probe: environment.HostProbe()}}
	_, envelope, err := service.Plan(ctx, ci.RunRequest{Pipeline: pipeline, Workspace: control.Handle{ID: in.ID, Generation: in.Generation}, DefinitionDigest: in.DefinitionDigest, SourceDigest: in.Head, StoryDigest: storyDigest, Trigger: ci.Trigger{Kind: "local", RequestedPipeline: pipeline}})
	return m, in, p, envelope, err
}
func capsuleCIPlanCmd() *cobra.Command {
	var project, workspace string
	var jsonOut bool
	cmd := &cobra.Command{Use: "plan <pipeline>", Args: cobra.ExactArgs(1), Short: "Build a sealed story-native CI envelope", RunE: func(cmd *cobra.Command, args []string) error {
		_, _, _, env, err := ciInputs(cmd.Context(), project, workspace, args[0])
		if err != nil {
			return err
		}
		return capsuleWorkspaceWrite(cmd, env, jsonOut)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&workspace, "workspace", "", "managed workspace id")
	cmd.Flags().BoolVar(&jsonOut, "json", true, "print JSON")
	_ = cmd.MarkFlagRequired("workspace")
	return cmd
}
func capsuleCIRunCmd() *cobra.Command {
	var project, workspace, verdictPath, fakeReceiptSigner string
	var jsonOut bool
	cmd := &cobra.Command{Use: "run <pipeline>", Args: cobra.ExactArgs(1), Short: "Run declared Capsule CI with a story-produced typed verdict", RunE: func(cmd *cobra.Command, args []string) error {
		m, in, p, _, err := ciInputs(cmd.Context(), project, workspace, args[0])
		if err != nil {
			return err
		}
		var launcher ci.Launcher
		if verdictPath != "" {
			raw, err := os.ReadFile(verdictPath)
			if err != nil {
				return fmt.Errorf("capsule ci: read verdict: %w", err)
			}
			var verdict ci.Verdict
			if err := json.Unmarshal(raw, &verdict); err != nil {
				return fmt.Errorf("capsule ci: parse verdict: %w", err)
			}
			launcher = ciLauncher{verdict: verdict}
		} else {
			launcher = storylauncher.Launcher{StoryPath: filepath.Join(project, p.Story)}
		}
		cfg, err := ci.Load(project)
		if err != nil {
			return err
		}
		service := ci.Service{ProjectRoot: project, Jobs: artifactjob.NewMemoryStore(), Env: environment.Resolver{ProjectRoot: project, Probe: environment.HostProbe()}, Executors: ci.NewConfiguredExecutors(cfg), Launcher: launcher, Hygiene: capsuleCIHygienePlanner(project)}
		result, err := service.Run(cmd.Context(), ci.RunRequest{Pipeline: args[0], Workspace: control.Handle{ID: in.ID, Generation: in.Generation}, DefinitionDigest: in.DefinitionDigest, SourceDigest: in.Head, StoryDigest: mustFileDigest(filepath.Join(project, p.Story)), Trigger: ci.Trigger{Kind: "local", RequestedPipeline: args[0]}})
		if err != nil {
			return persistCapsuleCIRunFailure(project, result, err)
		}
		signer, err := capsuleFakeReceiptSigner(fakeReceiptSigner)
		if err != nil {
			return err
		}
		stored, err := record.PersistWithOptions(project, result, record.PersistOptions{Signer: signer})
		if err != nil {
			return err
		}
		if err := (ci.FileRunStore{ProjectRoot: project}).Write(ci.RunRecord{JobID: string(result.Job.ID), Result: result, ReceiptID: stored.Receipt.ReceiptID, ReceiptVerification: stored.Verification.Status}); err != nil {
			return err
		}
		_ = m
		return capsuleWorkspaceWrite(cmd, result, jsonOut)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&workspace, "workspace", "", "managed workspace id")
	cmd.Flags().StringVar(&verdictPath, "verdict", "", "optional externally produced capsule-ci-verdict/v1 JSON; omit to drive the declared story engine")
	cmd.Flags().StringVar(&fakeReceiptSigner, "fake-receipt-signer", "", "deterministic local/test receipt signer id for projects requiring signed receipts")
	cmd.Flags().BoolVar(&jsonOut, "json", true, "print JSON")
	_ = cmd.MarkFlagRequired("workspace")
	return cmd
}

func persistCapsuleCIRunFailure(project string, result ci.RunResult, runErr error) error {
	if result.Job.ID == "" {
		return runErr
	}
	tracePath, traceErr := record.PersistTrace(project, result)
	writeErr := (ci.FileRunStore{ProjectRoot: project}).Write(ci.RunRecord{JobID: string(result.Job.ID), Result: result})
	if traceErr != nil && writeErr != nil {
		return fmt.Errorf("%w (also failed to persist capsule ci diagnostic trace: %v; run record: %v)", runErr, traceErr, writeErr)
	}
	if traceErr != nil {
		return fmt.Errorf("%w (also failed to persist capsule ci diagnostic trace: %v)", runErr, traceErr)
	}
	if writeErr != nil {
		return fmt.Errorf("%w (diagnostic trace: %s; also failed to persist run record: %v)", runErr, tracePath, writeErr)
	}
	return fmt.Errorf("%w (diagnostic trace: %s)", runErr, tracePath)
}

func capsuleCIHygienePlanner(project string) ci.HygienePlanner {
	return ci.HygienePlannerFunc(func(ctx context.Context, policy ci.CleanupPolicy) (ci.HygieneReport, error) {
		plan, err := hygiene.BuildPlan(ctx, hygiene.Options{ProjectRoot: project, KeepRuns: policy.KeepRuns, IncludeCapsuleCache: policy.IncludeCapsuleCache, IncludeGoBuildCache: policy.IncludeGoBuildCache})
		if err != nil {
			return ci.HygieneReport{}, err
		}
		return ci.HygieneReport{Schema: plan.Schema, Candidates: len(plan.Candidates), TotalBytes: plan.TotalBytes}, nil
	})
}
func capsuleCIStatusCmd() *cobra.Command {
	var project, job string
	var jsonOut bool
	cmd := &cobra.Command{Use: "status", Short: "Read persisted Capsule CI run records", RunE: func(cmd *cobra.Command, args []string) error {
		store := ci.FileRunStore{ProjectRoot: project}
		if job != "" {
			record, err := store.Get(job)
			if err != nil {
				return err
			}
			return capsuleWorkspaceWrite(cmd, record, jsonOut)
		}
		all, err := store.List()
		if err != nil {
			return err
		}
		index, err := store.Index()
		if err != nil {
			return err
		}
		if jsonOut {
			return capsuleWorkspaceWrite(cmd, index, jsonOut)
		}
		return capsuleWorkspaceWrite(cmd, map[string]any{"runs": all}, jsonOut)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&job, "job", "", "job id")
	cmd.Flags().BoolVar(&jsonOut, "json", true, "print JSON")
	return cmd
}
func capsuleCISummaryCmd() *cobra.Command {
	var project string
	var limit int
	var jsonOut bool
	cmd := &cobra.Command{Use: "summary", Short: "Project a provider-safe Capsule CI status summary", RunE: func(cmd *cobra.Command, args []string) error {
		summary, err := (ci.FileRunStore{ProjectRoot: project}).ProviderSummary(limit)
		if err != nil {
			return err
		}
		if jsonOut {
			return capsuleWorkspaceWrite(cmd, summary, true)
		}
		_, err = cmd.OutOrStdout().Write([]byte(summary.Markdown))
		return err
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().IntVar(&limit, "limit", 5, "number of latest runs to include")
	cmd.Flags().BoolVar(&jsonOut, "json", true, "print JSON")
	return cmd
}
func capsuleCICancelCmd() *cobra.Command {
	var project, job string
	var jsonOut bool
	cmd := &cobra.Command{Use: "cancel", Short: "Cancel a persisted running or parked Capsule CI job", RunE: func(cmd *cobra.Command, args []string) error {
		record, err := (ci.FileRunStore{ProjectRoot: project}).Cancel(job)
		if err != nil {
			return err
		}
		return capsuleWorkspaceWrite(cmd, record, jsonOut)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&job, "job", "", "job id")
	cmd.Flags().BoolVar(&jsonOut, "json", true, "print JSON")
	_ = cmd.MarkFlagRequired("job")
	return cmd
}

type ciLauncher struct{ verdict ci.Verdict }

func (l ciLauncher) Launch(context.Context, executor.Prepared) (ci.Verdict, error) {
	return l.verdict, nil
}
func fileDigest(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
func mustFileDigest(path string) string {
	v, err := fileDigest(path)
	if err != nil {
		panic(err)
	}
	return v
}
