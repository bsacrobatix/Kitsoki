package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/atomicfile"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/headroom"
	"kitsoki/internal/capsule/queue"
	"kitsoki/internal/capsule/receipt"
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
	Schema           string                 `json:"schema"`
	Status           string                 `json:"status"`
	ProjectRoot      string                 `json:"project_root"`
	WorkspaceID      string                 `json:"workspace_id"`
	CandidateSHA     string                 `json:"candidate_sha"`
	ReceiptID        string                 `json:"receipt_id"`
	Doctor           *ci.DoctorReport       `json:"doctor,omitempty"`
	QueueCandidate   queue.Candidate        `json:"queue_candidate"`
	QueueState       queue.State            `json:"queue_state,omitempty"`
	RemoteAdmission  *remoteAdmissionResult `json:"remote_admission,omitempty"`
	Plan             reconcile.Plan         `json:"plan,omitempty"`
	Promotion        reconcile.ApplyResult  `json:"promotion,omitempty"`
	ProtectedMainSHA string                 `json:"protected_main_sha,omitempty"`
}

func capsulePromoteCmd() *cobra.Command {
	var project, queueRoot, workspace, pipeline, target, gate, message, resolver, repair, repairReview, repairerID, reviewerID, reviewPolicyDigest string
	var remoteURL, remoteTokenEnv, remoteBucketURL, remoteKeyEnv, remoteSecretEnv, remoteTargetBaseSHA, remoteTrain, remoteStatusCommand string
	var gateTimeout time.Duration
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
				ProjectRoot:         project,
				QueueRoot:           queueRoot,
				WorkspaceID:         workspace,
				Pipeline:            pipeline,
				TargetRef:           target,
				GateCommand:         gate,
				Message:             message,
				ResolverCommand:     resolver,
				RepairCommand:       repair,
				RepairReviewCommand: repairReview,
				RepairerID:          repairerID,
				ReviewerID:          reviewerID,
				ReviewPolicyDigest:  reviewPolicyDigest,
				GateTimeout:         gateTimeout,
				SkipTests:           skipTests,
				Wait:                wait,
				RemoteAdmission:     remoteAdmissionOptions{URL: remoteURL, TokenEnv: remoteTokenEnv, BucketURL: remoteBucketURL, KeyEnv: remoteKeyEnv, SecretEnv: remoteSecretEnv, TargetBaseSHA: remoteTargetBaseSHA, TrainID: remoteTrain, StatusCommand: remoteStatusCommand},
			})
			if err != nil {
				return err
			}
			return capsuleWorkspaceWrite(cmd, result, jsonOut)
		},
	}
	cmd.Flags().StringVar(&project, "project", ".", "protected source project root")
	cmd.Flags().StringVar(&queueRoot, "queue-root", "", "exact external queue authority directory (default <project>/.capsules/queue)")
	cmd.Flags().StringVar(&workspace, "workspace", "", "managed workspace id")
	cmd.Flags().BoolVar(&current, "current", false, "resolve project and workspace from .kitsoki-dev-workspace.json in the current directory")
	cmd.Flags().StringVar(&pipeline, "pipeline", "change", "Capsule CI pipeline to run")
	cmd.Flags().StringVar(&target, "target", "main", "protected source target branch")
	cmd.Flags().StringVar(&gate, "gate", "git diff --check", "deterministic queue gate command")
	cmd.Flags().StringVar(&message, "message", "capsule promote candidate", "snapshot commit message when the workspace is dirty")
	cmd.Flags().StringVar(&resolver, "resolver", "", "bounded project-owned resolver command for retained conflict continuations")
	cmd.Flags().StringVar(&repair, "repair", "", "bounded project-owned repair command for a red deterministic gate")
	cmd.Flags().StringVar(&repairReview, "repair-review", "", "independent anti-weakening review command required with --repair")
	cmd.Flags().StringVar(&repairerID, "repairer-id", "", "stable repair agent identity required with --repair")
	cmd.Flags().StringVar(&reviewerID, "reviewer-id", "", "stable independent reviewer identity required with --repair")
	cmd.Flags().StringVar(&reviewPolicyDigest, "review-policy-digest", "", "deterministic anti-weakening policy digest required with --repair")
	cmd.Flags().DurationVar(&gateTimeout, "gate-timeout", queue.DefaultStageTimeout, "hard timeout applied independently to gate, repair, and anti-weakening review stages")
	cmd.Flags().BoolVar(&skipTests, "skip-tests", false, "emergency override: bypass Capsule CI receipt admission; recorded in the durable queue candidate")
	cmd.Flags().BoolVar(&wait, "wait", false, "process the local queue and apply protected-main CAS before returning")
	cmd.Flags().BoolVar(&jsonOut, "json", true, "print JSON")
	cmd.Flags().StringVar(&remoteURL, "remote-admission-url", "", "authenticated external admission service base URL; enables remote Capsule promotion")
	cmd.Flags().StringVar(&remoteTokenEnv, "remote-admission-token-env", "KITSOKI_QUEUE_ADMISSION_TOKEN", "environment variable holding the remote admission bearer token")
	cmd.Flags().StringVar(&remoteBucketURL, "remote-bucket-url", "", "Spaces/S3 bucket URL for sealed remote promotion objects")
	cmd.Flags().StringVar(&remoteKeyEnv, "remote-bucket-key-env", "KITSOKI_WORKER_OUTPUTS_ACCESS_KEY", "environment variable holding the bucket access key")
	cmd.Flags().StringVar(&remoteSecretEnv, "remote-bucket-secret-env", "KITSOKI_WORKER_OUTPUTS_SECRET_KEY", "environment variable holding the bucket secret")
	cmd.Flags().StringVar(&remoteTargetBaseSHA, "remote-target-base-sha", "", "exact integration target base SHA bound into remote admission")
	cmd.Flags().StringVar(&remoteTrain, "remote-train", "", "immutable integration train identity required for remote admission")
	cmd.Flags().StringVar(&remoteStatusCommand, "remote-status-command", "", "exact hosted queue-status command; printed with the durable remote candidate identity and never executed locally")
	return cmd
}

type capsulePromoteOptions struct {
	ProjectRoot         string
	QueueRoot           string
	WorkspaceID         string
	Pipeline            string
	TargetRef           string
	GateCommand         string
	Message             string
	ResolverCommand     string
	RepairCommand       string
	RepairReviewCommand string
	RepairerID          string
	ReviewerID          string
	ReviewPolicyDigest  string
	GateTimeout         time.Duration
	SkipTests           bool
	Wait                bool
	RemoteAdmission     remoteAdmissionOptions
}

// promoteReceiptReuse is the durable hand-off between Capsule CI and queue
// admission. Queue contention is expected to be temporary; it must not cause
// a second expensive CI run for an identical promotion attempt.  The record is
// deliberately more specific than a receipt: it binds the receipt to the
// workspace generation, branch, queue target, and deterministic queue gate
// that the caller asked to promote.
const promoteReceiptReuseSchema = "capsule-promote-receipt-reuse/v1"

// errPromoteReceiptNotEligible marks a verified, exact prior CI result that
// cannot authorize this promotion. It is deliberately distinct from malformed
// or substituted provenance: callers may run fresh CI for this case, but must
// fail closed for every evidence-integrity error.
var errPromoteReceiptNotEligible = errors.New("capsule promote: reusable receipt is not promotion eligible")

type promoteReceiptReuse struct {
	Schema            string `json:"schema"`
	ProjectRoot       string `json:"project_root"`
	QueueRoot         string `json:"queue_root"`
	WorkspaceID       string `json:"workspace_id"`
	WorkspaceGen      uint64 `json:"workspace_generation"`
	Branch            string `json:"branch"`
	CandidateSHA      string `json:"candidate_sha"`
	Pipeline          string `json:"pipeline"`
	TargetRef         string `json:"target_ref"`
	GateCommand       string `json:"gate_command"`
	ReceiptID         string `json:"receipt_id"`
	ReceiptPath       string `json:"receipt_path"`
	RunID             string `json:"run_id"`
	EnvelopeDigest    string `json:"envelope_digest"`
	StoryDigest       string `json:"story_digest"`
	EnvironmentDigest string `json:"environment_digest"`
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
	if strings.TrimSpace(opts.RemoteAdmission.URL) != "" && opts.SkipTests {
		return capsulePromoteResult{}, fmt.Errorf("capsule promote: remote admission never permits --skip-tests")
	}
	if strings.TrimSpace(opts.RemoteAdmission.URL) != "" && opts.Wait {
		return capsulePromoteResult{}, fmt.Errorf("capsule promote: --wait only processes the local queue; inspect the remote queue status using the returned admission identity")
	}
	if strings.TrimSpace(opts.RepairCommand) != "" {
		if !opts.Wait {
			return capsulePromoteResult{}, fmt.Errorf("capsule promote: --repair requires --wait")
		}
		if strings.TrimSpace(opts.RepairReviewCommand) == "" || strings.TrimSpace(opts.RepairerID) == "" ||
			strings.TrimSpace(opts.ReviewerID) == "" || strings.TrimSpace(opts.ReviewPolicyDigest) == "" {
			return capsulePromoteResult{}, fmt.Errorf("capsule promote: --repair requires --repair-review, --repairer-id, --reviewer-id, and --review-policy-digest")
		}
		if strings.TrimSpace(opts.RepairerID) == strings.TrimSpace(opts.ReviewerID) {
			return capsulePromoteResult{}, fmt.Errorf("capsule promote: repairer and reviewer identities must differ")
		}
	}
	if strings.TrimSpace(opts.RemoteAdmission.URL) != "" {
		if err := validateRemoteAdmissionOptions(opts); err != nil {
			return capsulePromoteResult{}, err
		}
	}
	root, err := filepath.Abs(opts.ProjectRoot)
	if err != nil {
		return capsulePromoteResult{}, err
	}
	if strings.TrimSpace(opts.QueueRoot) != "" {
		opts.QueueRoot, err = filepath.Abs(opts.QueueRoot)
		if err != nil {
			return capsulePromoteResult{}, err
		}
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
	// Absorb ordinary `git commit` work before readiness is judged. Agents in a
	// workspace commit with git — that is the natural thing to do — and the
	// registered head must follow git rather than the other way round. Adoption
	// is strictly a fast-forward of provenance: the tree must be clean and the
	// live HEAD must be a strict descendant of the registered head, so nothing
	// unvalidated and nothing unregistered-by-rewrite can enter here. The CI
	// receipt below is still required and is computed over the adopted head.
	adoption, err := capsulePromoteReconcileHead(ctx, manager, handle)
	if err != nil {
		return capsulePromoteResult{}, err
	}
	if adoption.Adopted {
		instance, err = manager.Instances.Get(ctx, adoption.Handle.ID)
		if err != nil {
			return capsulePromoteResult{}, err
		}
		handle = control.Handle{ID: instance.ID, Generation: instance.Generation}
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
	branch, err := gitTrim(ctx, workspacePath, "branch", "--show-current")
	if err != nil {
		return capsulePromoteResult{}, err
	}
	var stored record.Stored
	candidateSHA := instance.Head
	if !opts.SkipTests {
		stored, _, err = promoteReceiptForAttempt(ctx, root, instance, branch, opts, func(ctx context.Context) (record.Stored, error) {
			return runPromoteCI(ctx, root, instance, opts.Pipeline)
		})
		if err != nil {
			return capsulePromoteResult{}, err
		}
		candidateSHA = stored.Receipt.Envelope.SourceDigest
	}
	if strings.TrimSpace(opts.RemoteAdmission.URL) != "" {
		if opts.SkipTests { // defensive: this branch is unreachable after validation above.
			return capsulePromoteResult{}, fmt.Errorf("capsule promote: remote admission requires a receipt")
		}
		admitted, err := remoteCapsuleAdmission(ctx, opts, instance, workspacePath, branch, stored)
		if err != nil {
			return capsulePromoteResult{}, err
		}
		return capsulePromoteResult{Schema: "capsule-promote/v1", Status: "remote_admitted", ProjectRoot: root, WorkspaceID: instance.ID, CandidateSHA: candidateSHA, ReceiptID: stored.Receipt.ReceiptID, RemoteAdmission: &admitted}, nil
	}
	// The candidate commit exists only in the managed dev-workspace clone at
	// this point. Publish it into the protected project root before the
	// queue admits it: queue.Store.Submit's own reachability check refuses
	// admission of a candidate whose commit isn't resolvable here, and a
	// worker preparing this candidate later fetches from the project root,
	// not from this workspace directly.
	if _, err := gitTrim(ctx, root, "fetch", "--no-tags", "--no-write-fetch-head", workspacePath, candidateSHA); err != nil {
		return capsulePromoteResult{}, fmt.Errorf("capsule promote: publish candidate %s into %s: %w", candidateSHA, root, err)
	}
	qstore := queue.Store{ProjectRoot: root, QueueRoot: opts.QueueRoot}
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
	var reviewer queue.RepairReviewer
	if strings.TrimSpace(opts.RepairCommand) != "" {
		repairer = queue.ShellRepairer{Command: opts.RepairCommand}
		reviewer = queue.ShellRepairReviewer{Command: opts.RepairReviewCommand, ReviewerID: opts.ReviewerID}
	}
	state, err := qstore.Process(ctx, queue.ProcessDeps{
		Integration:        queue.ProtectedIntegration{ProjectRoot: root, QueueRoot: opts.QueueRoot, TargetRef: opts.TargetRef, ResolverCommand: opts.ResolverCommand, Headroom: headroom.Default()},
		Gate:               queue.ShellGate{Command: opts.GateCommand},
		Repairer:           repairer,
		RepairReviewer:     reviewer,
		RepairerID:         opts.RepairerID,
		ReviewPolicyDigest: opts.ReviewPolicyDigest,
		Finalizer:          queue.ProtectedFinalizer{ProjectRoot: root, QueueRoot: opts.QueueRoot, TargetRef: opts.TargetRef},
		GateVersion:        opts.Pipeline + ":" + opts.GateCommand,
		GateTier:           queue.RequiredGateTierForTarget(opts.TargetRef),
		GateTimeout:        opts.GateTimeout,
		TargetRef:          opts.TargetRef,
		GateMemo:           queue.FileGateMemo{ProjectRoot: root, QueueRoot: opts.QueueRoot},
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

// capsulePromoteReconcileHead adopts a workspace head that advanced through
// raw git, so promotion admits the work an agent actually did.
//
// It deliberately does not weaken any admission property:
//   - a dirty tree is NOT adopted here; it falls through to the doctor, which
//     still refuses it, so a partially-committed workspace can never promote;
//   - only a strict fast-forward is adopted, so no registered commit is ever
//     silently dropped;
//   - a rewrite (behind, diverged, or a registered head missing from the object
//     database) fails loudly and names the divergence — that case needs a human;
//   - the receipt/gate requirements downstream are untouched, and CI runs over
//     the adopted head.
func capsulePromoteReconcileHead(ctx context.Context, manager *control.Manager, handle control.Handle) (control.AdoptHeadResult, error) {
	drift, err := manager.InspectHead(ctx, handle)
	if err != nil {
		return control.AdoptHeadResult{}, err
	}
	switch {
	case drift.Relation == control.HeadInSync:
		return control.AdoptHeadResult{Drift: drift, Summary: drift.Explain()}, nil
	case !drift.Relation.Adoptable():
		return control.AdoptHeadResult{}, fmt.Errorf("capsule promote: refusing to promote: %s; %s", drift.Explain(), drift.Next())
	case drift.Dirty:
		// Leave the typed not-ready doctor report to explain this.
		return control.AdoptHeadResult{Drift: drift, Summary: drift.Explain()}, nil
	}
	return manager.AdoptHead(ctx, handle)
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
	executors.PoolStateRoot = root
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
	// Hosted artifact promotion tears down instance.Path after CI. Keep the
	// project-check evidence under the controller-owned project state instead
	// of the disposable materialization, while leaving ordinary local Capsule
	// runs unchanged.
	var launcher ci.Launcher = storylauncher.Launcher{
		StoryPath: filepath.Join(instance.Path, p.Story), ProjectRoot: instance.Path,
		AgentLaunchPolicy: host.AgentLaunchPolicy{Enabled: true, AllowedRoots: []string{instance.Path}},
		ConfigureHosts: func(reg *host.Registry) error {
			if ok := reg.Replace("host.capsule_ci.project_checks", host.NewCapsuleCIProjectChecksHandlerWithEvidenceDestination(nil, host.CapsuleCIEvidenceDestination{
				Root: filepath.Join(project, ".capsules", "ci", "evidence"), ReferencePrefix: "file:.capsules/ci/evidence",
			})); !ok {
				return fmt.Errorf("capsule promote: project check host is not registered")
			}
			return nil
		},
	}
	executors := ci.NewConfiguredExecutors(cfg)
	executors.ProjectRoot = instance.Path
	executors.PoolStateRoot = project
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

// promoteReceiptForAttempt returns a previous Capsule-CI receipt only when its
// durable continuation and the receipt/run pair still describe this exact
// promotion. A stale continuation (for example after a workspace commit or a
// different target/gate request) is not an error: it simply cannot authorize
// reuse and a fresh CI run is required. A matching continuation with malformed
// or substituted evidence is an error and never falls through to a blind rerun.
func promoteReceiptForAttempt(ctx context.Context, root string, instance control.Instance, branch string, opts capsulePromoteOptions, run func(context.Context) (record.Stored, error)) (record.Stored, bool, error) {
	stored, found, err := loadPromoteReceiptReuse(ctx, root, instance, branch, opts)
	if err != nil {
		return record.Stored{}, false, err
	}
	if found {
		return stored, true, nil
	}
	stored, err = run(ctx)
	if err != nil {
		return record.Stored{}, false, err
	}
	if err := persistPromoteReceiptReuse(root, instance, branch, opts, stored); err != nil {
		return record.Stored{}, false, err
	}
	return stored, false, nil
}

func loadPromoteReceiptReuse(ctx context.Context, root string, instance control.Instance, branch string, opts capsulePromoteOptions) (record.Stored, bool, error) {
	path, err := promoteReceiptReusePath(root, instance.ID)
	if err != nil {
		return record.Stored{}, false, err
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return record.Stored{}, false, nil
	}
	if err != nil {
		return record.Stored{}, false, fmt.Errorf("capsule promote: read receipt reuse record: %w", err)
	}
	var reuse promoteReceiptReuse
	if err := json.Unmarshal(raw, &reuse); err != nil {
		return record.Stored{}, false, fmt.Errorf("capsule promote: parse receipt reuse record: %w", err)
	}
	if !reuseMatchesAttempt(reuse, root, instance, branch, opts) {
		return record.Stored{}, false, nil
	}
	stored, err := verifyPromoteReceiptReuse(ctx, root, instance, reuse)
	if errors.Is(err, errPromoteReceiptNotEligible) {
		return record.Stored{}, false, nil
	}
	if err != nil {
		return record.Stored{}, false, err
	}
	return stored, true, nil
}

func reuseMatchesAttempt(reuse promoteReceiptReuse, root string, instance control.Instance, branch string, opts capsulePromoteOptions) bool {
	return reuse.Schema == promoteReceiptReuseSchema &&
		reuse.ProjectRoot == root &&
		reuse.QueueRoot == opts.QueueRoot &&
		reuse.WorkspaceID == instance.ID &&
		reuse.WorkspaceGen == instance.Generation &&
		reuse.Branch == branch &&
		reuse.CandidateSHA == instance.Head &&
		reuse.Pipeline == opts.Pipeline &&
		reuse.TargetRef == opts.TargetRef &&
		reuse.GateCommand == opts.GateCommand
}

func verifyPromoteReceiptReuse(ctx context.Context, root string, instance control.Instance, reuse promoteReceiptReuse) (record.Stored, error) {
	if reuse.ReceiptID == "" || reuse.RunID == "" || reuse.EnvelopeDigest == "" || reuse.StoryDigest == "" || reuse.EnvironmentDigest == "" {
		return record.Stored{}, fmt.Errorf("capsule promote: receipt reuse record is missing receipt, run, or envelope provenance")
	}
	wantPath := filepath.Join(root, ".capsules", "ci", reuse.RunID+".receipt.json")
	if reuse.ReceiptPath != wantPath {
		return record.Stored{}, fmt.Errorf("capsule promote: receipt reuse record has an unexpected receipt path")
	}
	raw, err := os.ReadFile(wantPath)
	if err != nil {
		return record.Stored{}, fmt.Errorf("capsule promote: read reusable receipt: %w", err)
	}
	var r receipt.Receipt
	if err := json.Unmarshal(raw, &r); err != nil {
		return record.Stored{}, fmt.Errorf("capsule promote: parse reusable receipt: %w", err)
	}
	verification := receipt.Verify(r, nil, false)
	if verification.Status != "valid" {
		return record.Stored{}, fmt.Errorf("capsule promote: reusable receipt provenance: receipt integrity is invalid")
	}
	run, err := (ci.FileRunStore{ProjectRoot: root}).Get(reuse.RunID)
	if err != nil {
		return record.Stored{}, fmt.Errorf("capsule promote: read reusable CI run: %w", err)
	}
	if r.ReceiptID != reuse.ReceiptID || r.JobID != reuse.RunID ||
		r.Envelope.SourceDigest != instance.Head ||
		r.Envelope.Instance.ID != instance.ID || r.Envelope.Instance.Generation != instance.Generation ||
		r.Envelope.Digest != reuse.EnvelopeDigest || r.Envelope.StoryDigest != reuse.StoryDigest ||
		r.Envelope.Environment.Digest != reuse.EnvironmentDigest ||
		r.Verdict.Pipeline != reuse.Pipeline || run.JobID != reuse.RunID ||
		run.ReceiptID != reuse.ReceiptID || run.ReceiptVerification != "valid" ||
		!reflect.DeepEqual(run.Result.Envelope, r.Envelope) || !reflect.DeepEqual(run.Result.Verdict, r.Verdict) {
		return record.Stored{}, fmt.Errorf("capsule promote: reusable receipt provenance does not match this workspace, pipeline, envelope, or run")
	}
	if !verification.PromotionEligible {
		return record.Stored{}, errPromoteReceiptNotEligible
	}
	// PromotionGate binds receipt content, promotion eligibility, and its
	// persisted run projection to the exact current candidate. It also applies
	// the project signature policy if one exists.
	if err := (record.PromotionGate{ProjectRoot: root}).Verify(ctx, reuse.ReceiptID, reconcile.Plan{Candidate: instance.Head, ReceiptCandidate: instance.Head}); err != nil {
		return record.Stored{}, fmt.Errorf("capsule promote: reusable receipt provenance: %w", err)
	}
	return record.Stored{Receipt: r, Verification: verification, ReceiptPath: wantPath}, nil
}

func persistPromoteReceiptReuse(root string, instance control.Instance, branch string, opts capsulePromoteOptions, stored record.Stored) error {
	if stored.Receipt.ReceiptID == "" || stored.Receipt.JobID == "" || stored.Receipt.Envelope.SourceDigest != instance.Head || stored.Receipt.Verdict.Pipeline != opts.Pipeline {
		return fmt.Errorf("capsule promote: CI result cannot be reused because its receipt does not match the requested workspace and pipeline")
	}
	path, err := promoteReceiptReusePath(root, instance.ID)
	if err != nil {
		return err
	}
	reuse := promoteReceiptReuse{
		Schema: promoteReceiptReuseSchema, ProjectRoot: root, QueueRoot: opts.QueueRoot, WorkspaceID: instance.ID, WorkspaceGen: instance.Generation,
		Branch: branch, CandidateSHA: instance.Head, Pipeline: opts.Pipeline, TargetRef: opts.TargetRef, GateCommand: opts.GateCommand,
		ReceiptID: stored.Receipt.ReceiptID, ReceiptPath: filepath.Join(root, ".capsules", "ci", stored.Receipt.JobID+".receipt.json"), RunID: stored.Receipt.JobID,
		EnvelopeDigest: stored.Receipt.Envelope.Digest, StoryDigest: stored.Receipt.Envelope.StoryDigest, EnvironmentDigest: stored.Receipt.Envelope.Environment.Digest,
	}
	raw, err := json.MarshalIndent(reuse, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(path, append(raw, '\n'), 0o600, 0o755); err != nil {
		return fmt.Errorf("capsule promote: persist receipt reuse record: %w", err)
	}
	return nil
}

func promoteReceiptReusePath(root, workspaceID string) (string, error) {
	if workspaceID == "" || filepath.Base(workspaceID) != workspaceID || workspaceID == "." || workspaceID == ".." {
		return "", fmt.Errorf("capsule promote: invalid workspace id for receipt reuse")
	}
	return filepath.Join(root, ".capsules", "promotions", workspaceID+".receipt-reuse.json"), nil
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
