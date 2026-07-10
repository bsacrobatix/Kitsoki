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
)

func capsuleCICmd() *cobra.Command {
	cmd := &cobra.Command{Use: "ci", Short: "Plan, run, and inspect story-native Capsule CI"}
	cmd.AddCommand(capsuleCIPlanCmd(), capsuleCIRunCmd(), capsuleCIStatusCmd())
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
	var project, workspace, verdictPath string
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
			launcher = ci.EngineLauncher{StoryPath: filepath.Join(project, p.Story)}
		}
		service := ci.Service{ProjectRoot: project, Jobs: artifactjob.NewMemoryStore(), Env: environment.Resolver{ProjectRoot: project, Probe: environment.HostProbe()}, Provider: executor.NewFakeProvider("local"), Launcher: launcher}
		result, err := service.Run(cmd.Context(), ci.RunRequest{Pipeline: args[0], Workspace: control.Handle{ID: in.ID, Generation: in.Generation}, DefinitionDigest: in.DefinitionDigest, SourceDigest: in.Head, StoryDigest: mustFileDigest(filepath.Join(project, p.Story)), Trigger: ci.Trigger{Kind: "local", RequestedPipeline: args[0]}})
		if err != nil {
			return err
		}
		if err := (ci.FileRunStore{ProjectRoot: project}).Write(ci.RunRecord{JobID: string(result.Job.ID), Result: result}); err != nil {
			return err
		}
		_ = m
		return capsuleWorkspaceWrite(cmd, result, jsonOut)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&workspace, "workspace", "", "managed workspace id")
	cmd.Flags().StringVar(&verdictPath, "verdict", "", "optional externally produced capsule-ci-verdict/v1 JSON; omit to drive the declared story engine")
	cmd.Flags().BoolVar(&jsonOut, "json", true, "print JSON")
	_ = cmd.MarkFlagRequired("workspace")
	return cmd
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
		return capsuleWorkspaceWrite(cmd, map[string]any{"runs": all}, jsonOut)
	}}
	cmd.Flags().StringVar(&project, "project", ".", "project root")
	cmd.Flags().StringVar(&job, "job", "", "job id")
	cmd.Flags().BoolVar(&jsonOut, "json", true, "print JSON")
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
