package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/queue"
	"kitsoki/internal/capsule/reconcile"
	"kitsoki/internal/capsule/record"
	"kitsoki/internal/capsule/storydigest"
	"kitsoki/internal/capsule/storylauncher"
	"kitsoki/internal/host"
)

type devWorkspaceManifest struct {
	ID        string `json:"id"`
	Source    string `json:"source"`
	Target    string `json:"target"`
	Workspace string `json:"workspace"`
}

// Promote status values. Every runCapsulePromote return path resolves to one
// of these within a bound instead of hanging without a receipt: readiness
// and lock contention are reported as typed results, not opaque errors.
const (
	PromoteStatusNotReady  = "not_ready"
	PromoteStatusBusy      = "busy"
	PromoteStatusQueued    = "queued"
	PromoteStatusRetryWait = "retry_wait"
	PromoteStatusPromoted  = "promoted"
)

type capsulePromoteResult struct {
	Schema           string                `json:"schema"`
	Status           string                `json:"status"`
	ProjectRoot      string                `json:"project_root"`
	WorkspaceID      string                `json:"workspace_id"`
	CandidateSHA     string                `json:"candidate_sha"`
	ReceiptID        string                `json:"receipt_id"`
	Doctor           *ci.DoctorReport      `json:"doctor,omitempty"`
	QueueCandidate   queue.Candidate       `json:"queue_candidate"`
	QueueState       queue.State           `json:"queue_state,omitempty"`
	Plan             reconcile.Plan        `json:"plan,omitempty"`
	Promotion        reconcile.ApplyResult `json:"promotion,omitempty"`
	ProtectedMainSHA string                `json:"protected_main_sha,omitempty"`
}

func capsulePromoteCmd() *cobra.Command {
	var project, workspace, pipeline, target, gate, message, resolver, repair string
	var current, wait, jsonOut, skipTests bool
	cmd := &cobra.Command{
		Use:   "promote",
		Short: "Promote a receipt-bound Capsule candidate through queue and protected-main CAS",
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved, err := resolvePromoteWorkspace(project, workspace, current)
			if err != nil {
				return err
			}
			project = resolved.Source
			workspace = resolved.ID
			if target == "" {
				target = "main"
			}
			result, err := runCapsulePromote(cmd.Context(), capsulePromoteOptions{
				ProjectRoot:     project,
				WorkspaceID:     workspace,
				Pipeline:        pipeline,
				TargetRef:       target,
				GateCommand:     gate,
				Message:         message,
				ResolverCommand: resolver,
				RepairCommand:   repair,
				SkipTests:       skipTests,
				Wait:            wait,
			})
			if err != nil {
				return err
			}
			return capsuleWorkspaceWrite(cmd, result, jsonOut)
		},
	}
	cmd.Flags().StringVar(&project, "project", ".", "protected source project root")
	cmd.Flags().StringVar(&workspace, "workspace", "", "managed workspace id")
	cmd.Flags().BoolVar(&current, "current", false, "resolve project and workspace from .kitsoki-dev-workspace.json in the current directory")
	cmd.Flags().StringVar(&pipeline, "pipeline", "change", "Capsule CI pipeline to run")
	cmd.Flags().StringVar(&target, "target", "main", "protected source target branch")
	cmd.Flags().StringVar(&gate, "gate", "git diff --check", "deterministic queue gate command")
	cmd.Flags().StringVar(&message, "message", "capsule promote candidate", "snapshot commit message when the workspace is dirty")
	cmd.Flags().StringVar(&resolver, "resolver", "", "bounded project-owned resolver command for retained conflict continuations")
	cmd.Flags().StringVar(&repair, "repair", "", "bounded project-owned repair command for a red deterministic gate")
	cmd.Flags().BoolVar(&skipTests, "skip-tests", false, "emergency override: bypass Capsule CI receipt admission; recorded in the durable queue candidate")
	cmd.Flags().BoolVar(&wait, "wait", false, "process the local queue and apply protected-main CAS before returning")
	cmd.Flags().BoolVar(&jsonOut, "json", true, "print JSON")
	return cmd
}

type capsulePromoteOptions struct {
	ProjectRoot     string
	WorkspaceID     string
	Pipeline        string
	TargetRef       string
	GateCommand     string
	Message         string
	ResolverCommand string
	RepairCommand   string
	SkipTests       bool
	Wait            bool
}

func resolvePromoteWorkspace(project, workspace string, current bool) (devWorkspaceManifest, error) {
	if !current {
		if strings.TrimSpace(workspace) == "" {
			return devWorkspaceManifest{}, fmt.Errorf("capsule promote: --workspace is required without --current")
		}
		root, err := filepath.Abs(project)
		if err != nil {
			return devWorkspaceManifest{}, err
		}
		return devWorkspaceManifest{ID: workspace, Source: root}, nil
	}
	raw, err := os.ReadFile(".kitsoki-dev-workspace.json")
	if err != nil {
		return devWorkspaceManifest{}, fmt.Errorf("capsule promote --current: read .kitsoki-dev-workspace.json: %w", err)
	}
	var manifest devWorkspaceManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return devWorkspaceManifest{}, fmt.Errorf("capsule promote --current: parse manifest: %w", err)
	}
	if manifest.ID == "" || manifest.Source == "" {
		return devWorkspaceManifest{}, fmt.Errorf("capsule promote --current: manifest is missing workspace id or source")
	}
	source, err := filepath.Abs(manifest.Source)
	if err != nil {
		return devWorkspaceManifest{}, err
	}
	manifest.Source = source
	return manifest, nil
}

func runCapsulePromote(ctx context.Context, opts capsulePromoteOptions) (capsulePromoteResult, error) {
	if strings.TrimSpace(opts.Pipeline) == "" || strings.TrimSpace(opts.TargetRef) == "" {
		return capsulePromoteResult{}, fmt.Errorf("capsule promote: pipeline and target are required")
	}
	root, err := filepath.Abs(opts.ProjectRoot)
	if err != nil {
		return capsulePromoteResult{}, err
	}
	manager, err := capsuleWorkspaceManager(root)
	if err != nil {
		return capsulePromoteResult{}, err
	}
	instance, err := manager.Instances.Get(ctx, opts.WorkspaceID)
	if err != nil {
		return capsulePromoteResult{}, err
	}
	if err := manager.VerifyDevWorkspaceScriptInstance(ctx, instance); err != nil {
		return capsulePromoteResult{}, err
	}
	handle := control.Handle{ID: instance.ID, Generation: instance.Generation}
	workspacePath, err := manager.WorkspacePath(ctx, handle)
	if err != nil {
		return capsulePromoteResult{}, err
	}
	report, err := capsulePromoteDoctorCheck(ctx, root, opts.Pipeline, instance, workspacePath)
	if err != nil {
		return capsulePromoteResult{}, err
	}
	if !report.Ready {
		return capsulePromoteResult{Schema: "capsule-promote/v1", Status: PromoteStatusNotReady, ProjectRoot: root, WorkspaceID: instance.ID, Doctor: &report}, nil
	}
	dirty, err := gitDirty(ctx, workspacePath)
	if err != nil {
		return capsulePromoteResult{}, err
	}
	if dirty {
		handle, err = manager.CommitVCS(ctx, handle, opts.Message)
		if err != nil {
			return capsulePromoteResult{}, err
		}
		instance, err = manager.Instances.Get(ctx, handle.ID)
		if err != nil {
			return capsulePromoteResult{}, err
		}
	}
	var stored record.Stored
	candidateSHA := instance.Head
	if !opts.SkipTests {
		stored, err = runPromoteCI(ctx, root, instance, opts.Pipeline)
		if err != nil {
			return capsulePromoteResult{}, err
		}
		candidateSHA = stored.Receipt.Envelope.SourceDigest
	}
	// The candidate commit exists only in the managed dev-workspace clone at
	// this point. Publish it into the protected project root before the
	// queue admits it: queue.Store.Submit's own reachability check refuses
	// admission of a candidate whose commit isn't resolvable here, and a
	// worker preparing this candidate later fetches from the project root,
	// not from this workspace directly.
	if _, err := gitTrim(ctx, root, "fetch", "--no-tags", workspacePath, candidateSHA); err != nil {
		return capsulePromoteResult{}, fmt.Errorf("capsule promote: publish candidate %s into %s: %w", candidateSHA, root, err)
	}
	branch, err := gitTrim(ctx, workspacePath, "branch", "--show-current")
	if err != nil {
		return capsulePromoteResult{}, err
	}
	qstore := queue.Store{ProjectRoot: root}
	var qcandidate queue.Candidate
	if !opts.SkipTests {
		qcandidate, err = qstore.Submit(queue.Submit{
			Branch: branch, SHA: candidateSHA, Receipt: stored.Receipt, ReceiptRef: stored.ReceiptPath, Backend: "local", TargetRef: opts.TargetRef,
		})
	} else {
		qcandidate, err = qstore.Submit(queue.Submit{
			Branch: branch, SHA: candidateSHA, Admission: queue.EmergencySkipTestsAdmission, Backend: "local", TargetRef: opts.TargetRef,
		})
	}
	if err != nil {
		if errors.Is(err, queue.ErrBusy) {
			return capsulePromoteResult{Schema: "capsule-promote/v1", Status: PromoteStatusBusy, ProjectRoot: root, WorkspaceID: instance.ID, CandidateSHA: candidateSHA, ReceiptID: stored.Receipt.ReceiptID}, nil
		}
		return capsulePromoteResult{}, err
	}
	out := capsulePromoteResult{Schema: "capsule-promote/v1", Status: PromoteStatusQueued, ProjectRoot: root, WorkspaceID: instance.ID, CandidateSHA: candidateSHA, ReceiptID: stored.Receipt.ReceiptID, QueueCandidate: qcandidate}
	if !opts.Wait {
		return out, nil
	}
	var repairer queue.Repairer
	if strings.TrimSpace(opts.RepairCommand) != "" {
		repairer = queue.ShellRepairer{Command: opts.RepairCommand}
	}
	state, err := qstore.Process(ctx, queue.ProcessDeps{
		Integration: queue.ProtectedIntegration{ProjectRoot: root, TargetRef: opts.TargetRef, ResolverCommand: opts.ResolverCommand},
		Gate:        queue.ShellGate{Command: opts.GateCommand},
		Repairer:    repairer,
		Finalizer:   queue.ProtectedFinalizer{ProjectRoot: root, TargetRef: opts.TargetRef},
		GateVersion: opts.Pipeline + ":" + opts.GateCommand,
		TargetRef:   opts.TargetRef,
	})
	if err != nil {
		if errors.Is(err, queue.ErrBusy) {
			out.Status = PromoteStatusBusy
			return out, nil
		}
		return capsulePromoteResult{}, err
	}
	out.QueueState = state
	queued, ok := queueCandidate(state, qcandidate.ID)
	if !ok || queued.Status != queue.Landed {
		out.Status = PromoteStatusRetryWait
		return out, nil
	}
	if queued.ResultMainSHA == "" {
		return out, fmt.Errorf("capsule promote: landed queue candidate is missing protected CAS result")
	}
	out.ProtectedMainSHA = queued.ResultMainSHA
	out.Status = PromoteStatusPromoted
	return out, nil
}

// capsulePromoteDoctorCheck runs the same bounded, no-spend readiness
// preflight as `capsule ci doctor` (hygiene inventory bound by
// ci.Doctor.HygieneTimeout) so promote returns a typed not-ready result
// within a bound instead of proceeding into a run that may hang.
func capsulePromoteDoctorCheck(ctx context.Context, root, pipeline string, instance control.Instance, workspacePath string) (ci.DoctorReport, error) {
	cfg, err := ci.Load(workspacePath)
	if err != nil {
		cfg = ci.Config{}
	}
	executors := ci.NewConfiguredExecutors(cfg)
	executors.ProjectRoot = workspacePath
	doctor := ci.Doctor{ProjectRoot: workspacePath, Env: environment.Resolver{ProjectRoot: workspacePath, Probe: environment.HostProbe()}, Executors: executors, Hygiene: capsuleCIHygienePlanner(root), Workspace: ci.GitWorkspaceProbe{}}
	return doctor.Check(ctx, ci.DoctorRequest{Pipeline: pipeline, Workspace: instance, WorkspacePath: workspacePath})
}

func runPromoteCI(ctx context.Context, project string, instance control.Instance, pipeline string) (record.Stored, error) {
	cfg, err := ci.Load(instance.Path)
	if err != nil {
		return record.Stored{}, err
	}
	p, ok := cfg.Pipelines[pipeline]
	if !ok {
		return record.Stored{}, fmt.Errorf("capsule promote: pipeline %q not found", pipeline)
	}
	story, err := storydigestCompute(instance.Path, p.Story)
	if err != nil {
		return record.Stored{}, err
	}
	service := ci.Service{ProjectRoot: instance.Path, Env: environment.Resolver{ProjectRoot: instance.Path, Probe: environment.HostProbe()}}
	_, envelope, err := service.Plan(ctx, ci.RunRequest{Pipeline: pipeline, Workspace: control.Handle{ID: instance.ID, Generation: instance.Generation}, DefinitionDigest: instance.DefinitionDigest, SourceDigest: instance.Head, StoryDigest: story, Trigger: ci.Trigger{Kind: "local", RequestedPipeline: pipeline}})
	if err != nil {
		return record.Stored{}, err
	}
	var launcher ci.Launcher = storylauncher.Launcher{StoryPath: filepath.Join(instance.Path, p.Story), ProjectRoot: instance.Path, AgentLaunchPolicy: host.AgentLaunchPolicy{Enabled: true, AllowedRoots: []string{instance.Path}}}
	executors := ci.NewConfiguredExecutors(cfg)
	executors.ProjectRoot = instance.Path
	executors.Source = executor.SourceBundlerFunc(func(ctx context.Context, envelope executor.Envelope) (executor.SourceBundle, error) {
		return executor.GitBundle(ctx, instance.Path, envelope.SourceDigest, 0)
	})
	result, err := (ci.Service{ProjectRoot: instance.Path, Jobs: artifactjob.NewMemoryStore(), Env: environment.Resolver{ProjectRoot: instance.Path, Probe: environment.HostProbe()}, Executors: executors, Launcher: launcher, Hygiene: capsuleCIHygienePlanner(project), Observer: record.FileRunObserver{ProjectRoot: project}}).Run(ctx, ci.RunRequest{Pipeline: pipeline, Workspace: control.Handle{ID: instance.ID, Generation: instance.Generation}, DefinitionDigest: instance.DefinitionDigest, SourceDigest: instance.Head, StoryDigest: envelope.StoryDigest, Trigger: ci.Trigger{Kind: "local", RequestedPipeline: pipeline}})
	if err != nil {
		return record.Stored{}, persistCapsuleCIRunFailure(project, result, err)
	}
	stored, err := record.Persist(project, result)
	if err != nil {
		return record.Stored{}, err
	}
	if err := (ci.FileRunStore{ProjectRoot: project}).Write(ci.RunRecord{JobID: string(result.Job.ID), Result: result, ReceiptID: stored.Receipt.ReceiptID, ReceiptVerification: stored.Verification.Status}); err != nil {
		return record.Stored{}, err
	}
	return stored, nil
}

func storydigestCompute(root, storyPath string) (string, error) {
	story, err := storydigest.Compute(root, storyPath)
	if err != nil {
		return "", err
	}
	return story.Digest, nil
}

func queueCandidate(state queue.State, id string) (queue.Candidate, bool) {
	for _, candidate := range state.Candidates {
		if candidate.ID == id {
			return candidate, true
		}
	}
	return queue.Candidate{}, false
}

func gitDirty(ctx context.Context, dir string) (bool, error) {
	out, err := gitTrim(ctx, dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

func gitTrim(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
