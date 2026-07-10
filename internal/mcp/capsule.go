// Capsule MCP is the least-authority coding-agent surface. It deliberately
// accepts opaque workspace handles and project-relative paths only: no tool can
// name an arbitrary host directory, add an executor, or widen its startup grant.
package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/hygiene"
	"kitsoki/internal/capsule/receipt"
	"kitsoki/internal/capsule/reconcile"
	"kitsoki/internal/capsule/record"
)

type CapsuleConfig struct {
	Manager    *control.Manager
	Owner      string
	ProjectID  string
	CILauncher func(string) ci.Launcher
	Signer     receipt.Signer
}
type CapsuleServer struct {
	mcpSrv           *mcpsdk.Server
	manager          *control.Manager
	owner, projectID string
	plans            map[string]capsuleSyncPlan
	ciLauncher       func(string) ci.Launcher
	signer           receipt.Signer
	mu               sync.Mutex
}
type capsuleSyncPlan struct {
	plan   reconcile.Plan
	handle control.Handle
}

func NewCapsuleServer(cfg CapsuleConfig) (*CapsuleServer, error) {
	if cfg.Manager == nil {
		return nil, fmt.Errorf("capsule mcp: manager is required")
	}
	if strings.TrimSpace(cfg.Owner) == "" {
		return nil, fmt.Errorf("capsule mcp: owner is required")
	}
	s := &CapsuleServer{manager: cfg.Manager, owner: cfg.Owner, projectID: cfg.ProjectID, plans: map[string]capsuleSyncPlan{}, ciLauncher: cfg.CILauncher, signer: cfg.Signer}
	s.mcpSrv = mcpsdk.NewServer(&mcpsdk.Implementation{Name: "kitsoki-capsule", Version: "v1"}, nil)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.project.describe", Description: "Describe this already-scoped Capsule project and its fixed capabilities. No machine paths or secret values are returned."}, s.describe)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.definition.inspect", Description: "Inspect an allowed immutable Capsule definition."}, s.definition)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.workspace.create", Description: "Create or reacquire an opaque, lease-bound workspace handle for an allowed definition."}, s.create)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.workspace.list", Description: "List opaque Capsule workspace handles owned by this server grant."}, s.list)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.workspace.status", Description: "Read a handle-scoped Capsule workspace status."}, s.status)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.workspace.close", Description: "Close a handle-scoped Capsule workspace."}, s.close)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.fs.read", Description: "Read one existing project-relative file below a Capsule workspace."}, s.read)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.fs.write", Description: "Write one project-relative file below a Capsule workspace. Returns a fresh generation handle."}, s.write)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.fs.search", Description: "Search agent-visible files below a Capsule workspace; verifier-only assets are excluded."}, s.search)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.exec.run", Description: "Run a declared no-shell command inside a Capsule workspace. Raw argv is available only when the immutable grant and definition allow it."}, s.run)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.vcs.status", Description: "Read local Git status for a Capsule workspace."}, s.vcsStatus)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.vcs.diff", Description: "Read the local Git diff for a Capsule workspace."}, s.vcsDiff)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.vcs.commit", Description: "Commit local Capsule workspace changes. This does not publish or update a remote."}, s.vcsCommit)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.sync.plan", Description: "Observe a handle-scoped workspace and create an immutable, stale-safe local reconciliation plan. It never publishes remotely."}, s.syncPlan)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.sync.conflicts", Description: "Materialize project-scoped structured conflict inputs for a diverged server-owned reconciliation plan. Returns project-relative artifact paths only."}, s.syncConflicts)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.sync.integration", Description: "Materialize a project-scoped managed integration instance for a diverged server-owned reconciliation plan. Returns project-relative paths only."}, s.syncIntegration)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.sync.continue", Description: "Apply a resolved managed integration instance after resolver, independent lost-work review, and validation evidence are supplied."}, s.syncContinue)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.sync.abort", Description: "Abort a managed sync continuation and optionally preserve a project-relative patch artifact before cleanup."}, s.syncAbort)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.sync.apply", Description: "Apply a previously returned reconciliation plan only if all observed refs and the workspace generation are unchanged. Required CI gate evidence is checked before promotion."}, s.syncApply)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.ci.plan", Description: "Build the sealed environment and story envelope for an allowed project pipeline and workspace handle."}, s.ciPlan)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.ci.run", Description: "Run the declared project CI story through the Capsule executor and return its typed verdict. A story can pass, fail, or park; it cannot self-authorize promotion."}, s.ciRun)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.ci.status", Description: "Read persisted Capsule CI run records without host paths or raw secrets."}, s.ciStatus)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.ci.summary", Description: "Read a provider-safe Capsule CI summary derived from the canonical run index and receipt projections."}, s.ciSummary)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.ci.cancel", Description: "Cancel a persisted running or parked Capsule CI job. Remote workers receive the same cancellation contract when configured."}, s.ciCancel)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.cleanup.plan", Description: "Plan project-scoped Capsule disk hygiene using project-relative paths only. This never deletes files or host-global caches."}, s.cleanupPlan)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.cleanup.apply", Description: "Apply project-scoped Capsule disk hygiene when the startup grant includes the cleanup effect. Host-global caches are not deleted through MCP."}, s.cleanupApply)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.env.resolve", Description: "Resolve a declared environment using probes only; it never installs host tools or returns secrets."}, s.envResolve)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.env.lock", Description: "Resolve and persist a reviewable environment lock when this immutable grant allows environment writes."}, s.envLock)
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.env.verify", Description: "Verify a saved environment lock against current probes without modifying the host."}, s.envVerify)
	return s, nil
}
func (s *CapsuleServer) Run(ctx context.Context) error {
	return s.mcpSrv.Run(ctx, &mcpsdk.StdioTransport{})
}
func (s *CapsuleServer) Connect(ctx context.Context, t mcpsdk.Transport, o *mcpsdk.ServerSessionOptions) (*mcpsdk.ServerSession, error) {
	return s.mcpSrv.Connect(ctx, t, o)
}

type capsuleHandleArgs struct {
	Workspace control.Handle `json:"workspace"`
}
type capsuleDefinitionArgs struct {
	Definition string `json:"definition"`
}
type capsuleCreateArgs struct {
	ID         string `json:"id"`
	Definition string `json:"definition"`
	Executor   string `json:"executor,omitempty"`
}
type capsulePathArgs struct {
	Workspace control.Handle `json:"workspace"`
	Path      string         `json:"path"`
}
type capsuleWriteArgs struct {
	Workspace control.Handle `json:"workspace"`
	Path      string         `json:"path"`
	Contents  string         `json:"contents"`
}
type capsuleSearchArgs struct {
	Workspace control.Handle `json:"workspace"`
	Query     string         `json:"query"`
	Limit     int            `json:"limit,omitempty"`
}
type capsuleRunArgs struct {
	Workspace control.Handle `json:"workspace"`
	CommandID string         `json:"command_id,omitempty"`
	Argv      []string       `json:"argv,omitempty"`
	Timeout   string         `json:"timeout,omitempty"`
}
type capsuleCommitArgs struct {
	Workspace control.Handle `json:"workspace"`
	Message   string         `json:"message"`
}
type capsuleSyncPlanArgs struct {
	Workspace    control.Handle      `json:"workspace"`
	Operation    reconcile.Operation `json:"operation"`
	Target       string              `json:"target"`
	RequiredGate string              `json:"required_gate,omitempty"`
}
type capsuleSyncApplyArgs struct {
	PlanDigest  string `json:"plan_digest"`
	GateReceipt string `json:"gate_receipt,omitempty"`
}
type capsuleSyncConflictsArgs struct {
	PlanDigest string `json:"plan_digest"`
}
type capsuleSyncContinueArgs struct {
	PlanDigest        string `json:"plan_digest"`
	ResolverDecision  string `json:"resolver_decision"`
	LostWorkReview    string `json:"lost_work_review"`
	ValidationReceipt string `json:"validation_receipt"`
}
type capsuleSyncAbortArgs struct {
	PlanDigest string `json:"plan_digest"`
	Preserve   bool   `json:"preserve"`
}
type capsuleCIArgs struct {
	Workspace control.Handle `json:"workspace"`
	Pipeline  string         `json:"pipeline"`
}
type capsuleCIStatusArgs struct {
	Job string `json:"job,omitempty"`
}
type capsuleCISummaryArgs struct {
	Limit int `json:"limit,omitempty"`
}
type capsuleCleanupArgs struct {
	KeepRuns            int  `json:"keep_runs,omitempty"`
	IncludeCapsuleCache bool `json:"include_capsule_cache,omitempty"`
}
type capsuleEnvArgs struct {
	ID string `json:"id"`
}
type capsuleError struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

func (s *CapsuleServer) describe(ctx context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, any, error) {
	defs, err := s.manager.Definitions.List(ctx)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	allowed := map[string]bool{}
	for _, id := range s.manager.Grant.Definitions {
		allowed[id] = true
	}
	var out []map[string]string
	for _, def := range defs {
		if allowed[def.ID] || allowed["*"] {
			out = append(out, map[string]string{"id": def.ID, "digest": def.Digest, "source": string(def.Source.Kind)})
		}
	}
	return nil, map[string]any{"ok": true, "project": s.projectID, "definitions": out, "executors": s.manager.Grant.Executors, "effects": s.manager.Grant.Effects}, nil
}
func (s *CapsuleServer) definition(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsuleDefinitionArgs) (*mcpsdk.CallToolResult, any, error) {
	def, err := s.manager.Definition(ctx, a.Definition)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": true, "definition": def}, nil
}
func (s *CapsuleServer) create(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsuleCreateArgs) (*mcpsdk.CallToolResult, any, error) {
	h, err := s.manager.Create(ctx, control.CreateRequest{ID: a.ID, DefinitionID: a.Definition, Owner: s.owner, Provider: a.Executor})
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": true, "workspace": h}, nil
}
func (s *CapsuleServer) list(ctx context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, any, error) {
	all, err := s.manager.List(ctx)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	out := make([]control.Instance, 0, len(all))
	for _, in := range all {
		if in.Lease.Owner == s.owner {
			out = append(out, in)
		}
	}
	return nil, map[string]any{"ok": true, "workspaces": out}, nil
}
func (s *CapsuleServer) status(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsuleHandleArgs) (*mcpsdk.CallToolResult, any, error) {
	in, err := s.manager.Status(ctx, a.Workspace)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	if in.Lease.Owner != s.owner {
		return capsuleErr(control.ErrDenied), nil, nil
	}
	return nil, map[string]any{"ok": true, "workspace": in}, nil
}
func (s *CapsuleServer) close(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsuleHandleArgs) (*mcpsdk.CallToolResult, any, error) {
	if err := s.manager.Close(ctx, a.Workspace, s.owner); err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": true}, nil
}
func (s *CapsuleServer) read(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsulePathArgs) (*mcpsdk.CallToolResult, any, error) {
	raw, err := s.manager.ReadFile(ctx, a.Workspace, a.Path)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": true, "contents": string(raw)}, nil
}
func (s *CapsuleServer) write(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsuleWriteArgs) (*mcpsdk.CallToolResult, any, error) {
	h, err := s.manager.WriteFile(ctx, a.Workspace, a.Path, []byte(a.Contents))
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": true, "workspace": h}, nil
}
func (s *CapsuleServer) search(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsuleSearchArgs) (*mcpsdk.CallToolResult, any, error) {
	matches, err := s.manager.SearchFiles(ctx, a.Workspace, a.Query, a.Limit)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": true, "matches": matches}, nil
}
func (s *CapsuleServer) run(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsuleRunArgs) (*mcpsdk.CallToolResult, any, error) {
	var timeout time.Duration
	var err error
	if a.Timeout != "" {
		timeout, err = time.ParseDuration(a.Timeout)
		if err != nil {
			return capsuleErr(fmt.Errorf("capsule exec: timeout: %w", err)), nil, nil
		}
	}
	res, err := s.manager.RunCommand(ctx, a.Workspace, a.CommandID, a.Argv, timeout)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": res.ExitCode == 0, "result": res}, nil
}
func (s *CapsuleServer) vcsStatus(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsuleHandleArgs) (*mcpsdk.CallToolResult, any, error) {
	out, err := s.manager.StatusVCS(ctx, a.Workspace)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": true, "status": out}, nil
}
func (s *CapsuleServer) vcsDiff(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsuleHandleArgs) (*mcpsdk.CallToolResult, any, error) {
	out, err := s.manager.DiffVCS(ctx, a.Workspace)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": true, "diff": out}, nil
}
func (s *CapsuleServer) vcsCommit(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsuleCommitArgs) (*mcpsdk.CallToolResult, any, error) {
	h, err := s.manager.CommitVCS(ctx, a.Workspace, a.Message)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": true, "workspace": h}, nil
}
func (s *CapsuleServer) syncPlan(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsuleSyncPlanArgs) (*mcpsdk.CallToolResult, any, error) {
	if !reconcile.ValidOperation(a.Operation) {
		return capsuleErr(fmt.Errorf("capsule sync: unsupported operation %q", a.Operation)), nil, nil
	}
	requiredEffect := reconcile.RequiredEffect(a.Operation)
	if !s.manager.Grant.Allows("effect", requiredEffect) {
		return capsuleErr(fmt.Errorf("%w: %s", control.ErrDenied, requiredEffect)), nil, nil
	}
	if !s.manager.Grant.Allows("branch", a.Target) {
		return capsuleErr(fmt.Errorf("%w: branch %q", control.ErrDenied, a.Target)), nil, nil
	}
	in, err := s.manager.Status(ctx, a.Workspace)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	if in.Lease.Owner != s.owner {
		return capsuleErr(control.ErrDenied), nil, nil
	}
	path, err := s.manager.WorkspacePath(ctx, a.Workspace)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	p, err := (reconcile.Reconciler{VCS: reconcile.Git{}}).Plan(ctx, reconcile.PlanRequest{Workspace: path, TargetRef: a.Target, Operation: a.Operation, Generation: a.Workspace.Generation, RequiredGate: a.RequiredGate})
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	s.mu.Lock()
	s.plans[p.Digest] = capsuleSyncPlan{plan: p, handle: a.Workspace}
	s.mu.Unlock()
	return nil, map[string]any{"ok": true, "plan": p}, nil
}
func (s *CapsuleServer) syncApply(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsuleSyncApplyArgs) (*mcpsdk.CallToolResult, any, error) {
	s.mu.Lock()
	storedPlan, ok := s.plans[a.PlanDigest]
	s.mu.Unlock()
	if !ok {
		return capsuleErr(fmt.Errorf("capsule sync: plan %q not found", a.PlanDigest)), nil, nil
	}
	in, err := s.manager.Status(ctx, storedPlan.handle)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	if in.Lease.Owner != s.owner {
		return capsuleErr(control.ErrDenied), nil, nil
	}
	gate := record.PromotionGate{ProjectRoot: s.manager.Grant.ProjectRoot}
	result, err := (reconcile.Reconciler{VCS: reconcile.Git{}, Gates: gate}).Apply(ctx, storedPlan.plan, a.GateReceipt)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	next, err := s.manager.MarkIntegrated(ctx, storedPlan.handle)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": true, "result": result, "workspace": next}, nil
}
func (s *CapsuleServer) syncConflicts(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsuleSyncConflictsArgs) (*mcpsdk.CallToolResult, any, error) {
	s.mu.Lock()
	storedPlan, ok := s.plans[a.PlanDigest]
	s.mu.Unlock()
	if !ok {
		return capsuleErr(fmt.Errorf("capsule sync: plan %q not found", a.PlanDigest)), nil, nil
	}
	in, err := s.manager.Status(ctx, storedPlan.handle)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	if in.Lease.Owner != s.owner {
		return capsuleErr(control.ErrDenied), nil, nil
	}
	artifact, path, err := (reconcile.Reconciler{VCS: reconcile.Git{}}).MaterializeConflictArtifact(ctx, storedPlan.plan, s.manager.Grant.ProjectRoot)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	rel, err := filepath.Rel(s.manager.Grant.ProjectRoot, path)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": true, "artifact": artifact, "path": filepath.ToSlash(rel)}, nil
}
func (s *CapsuleServer) syncIntegration(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsuleSyncConflictsArgs) (*mcpsdk.CallToolResult, any, error) {
	s.mu.Lock()
	storedPlan, ok := s.plans[a.PlanDigest]
	s.mu.Unlock()
	if !ok {
		return capsuleErr(fmt.Errorf("capsule sync: plan %q not found", a.PlanDigest)), nil, nil
	}
	in, err := s.manager.Status(ctx, storedPlan.handle)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	if in.Lease.Owner != s.owner {
		return capsuleErr(control.ErrDenied), nil, nil
	}
	instance, path, err := (reconcile.Reconciler{VCS: reconcile.Git{}}).MaterializeIntegrationInstance(ctx, storedPlan.plan, s.manager.Grant.ProjectRoot)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	rel, err := filepath.Rel(s.manager.Grant.ProjectRoot, path)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": true, "instance": instance, "path": filepath.ToSlash(rel)}, nil
}
func (s *CapsuleServer) syncContinue(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsuleSyncContinueArgs) (*mcpsdk.CallToolResult, any, error) {
	s.mu.Lock()
	storedPlan, ok := s.plans[a.PlanDigest]
	s.mu.Unlock()
	if !ok {
		return capsuleErr(fmt.Errorf("capsule sync: plan %q not found", a.PlanDigest)), nil, nil
	}
	in, err := s.manager.Status(ctx, storedPlan.handle)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	if in.Lease.Owner != s.owner {
		return capsuleErr(control.ErrDenied), nil, nil
	}
	gate := record.PromotionGate{ProjectRoot: s.manager.Grant.ProjectRoot}
	result, err := (reconcile.Reconciler{VCS: reconcile.Git{}, Gates: gate}).ApplyContinuation(ctx, reconcile.ContinuationApplyRequest{Plan: storedPlan.plan, ProjectRoot: s.manager.Grant.ProjectRoot, ResolverDecision: a.ResolverDecision, LostWorkReview: a.LostWorkReview, ValidationReceipt: a.ValidationReceipt})
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	next, err := s.manager.MarkIntegrated(ctx, storedPlan.handle)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": true, "result": result, "workspace": next}, nil
}
func (s *CapsuleServer) syncAbort(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsuleSyncAbortArgs) (*mcpsdk.CallToolResult, any, error) {
	s.mu.Lock()
	storedPlan, ok := s.plans[a.PlanDigest]
	s.mu.Unlock()
	if !ok {
		return capsuleErr(fmt.Errorf("capsule sync: plan %q not found", a.PlanDigest)), nil, nil
	}
	in, err := s.manager.Status(ctx, storedPlan.handle)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	if in.Lease.Owner != s.owner {
		return capsuleErr(control.ErrDenied), nil, nil
	}
	result, err := (reconcile.Reconciler{VCS: reconcile.Git{}}).AbortContinuation(ctx, reconcile.AbortContinuationRequest{Plan: storedPlan.plan, ProjectRoot: s.manager.Grant.ProjectRoot, Preserve: a.Preserve})
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": true, "result": result}, nil
}
func (s *CapsuleServer) ciPlan(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsuleCIArgs) (*mcpsdk.CallToolResult, any, error) {
	_, _, envelope, err := s.planCI(ctx, a.Workspace, a.Pipeline)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": true, "envelope": envelope}, nil
}
func (s *CapsuleServer) ciRun(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsuleCIArgs) (*mcpsdk.CallToolResult, any, error) {
	in, pipeline, _, err := s.planCI(ctx, a.Workspace, a.Pipeline)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	project := s.manager.Grant.ProjectRoot
	if s.ciLauncher == nil {
		return capsuleErr(fmt.Errorf("capsule ci: no story launcher is configured")), nil, nil
	}
	cfg, err := ci.Load(project)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	service := ci.Service{ProjectRoot: project, Jobs: artifactjob.NewMemoryStore(), Env: environment.Resolver{ProjectRoot: project, Probe: environment.HostProbe()}, Executors: ci.NewConfiguredExecutors(cfg), Launcher: s.ciLauncher(filepath.Join(project, pipeline.Story)), Hygiene: capsuleCIHygienePlanner(project)}
	digest, err := capsuleStoryDigest(filepath.Join(project, pipeline.Story))
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	result, err := service.Run(ctx, ci.RunRequest{Pipeline: a.Pipeline, Workspace: a.Workspace, DefinitionDigest: in.DefinitionDigest, SourceDigest: in.Head, StoryDigest: digest, Trigger: ci.Trigger{Kind: "local", RequestedPipeline: a.Pipeline}})
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	stored, err := record.PersistWithOptions(project, result, record.PersistOptions{Signer: s.signer})
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	if err := (ci.FileRunStore{ProjectRoot: project}).Write(ci.RunRecord{JobID: string(result.Job.ID), Result: result, ReceiptID: stored.Receipt.ReceiptID, ReceiptVerification: stored.Verification.Status}); err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": result.Verdict.Outcome == "passed", "result": result}, nil
}
func (s *CapsuleServer) ciStatus(_ context.Context, _ *mcpsdk.CallToolRequest, a capsuleCIStatusArgs) (*mcpsdk.CallToolResult, any, error) {
	store := ci.FileRunStore{ProjectRoot: s.manager.Grant.ProjectRoot}
	if a.Job != "" {
		record, err := store.Get(a.Job)
		if err != nil {
			return capsuleErr(err), nil, nil
		}
		return nil, map[string]any{"ok": true, "run": record}, nil
	}
	all, err := store.List()
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	index, err := store.Index()
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": true, "index": index, "runs": index.Runs, "records": len(all)}, nil
}
func (s *CapsuleServer) ciCancel(_ context.Context, _ *mcpsdk.CallToolRequest, a capsuleCIStatusArgs) (*mcpsdk.CallToolResult, any, error) {
	if a.Job == "" {
		return capsuleErr(fmt.Errorf("capsule ci: job is required")), nil, nil
	}
	record, err := (ci.FileRunStore{ProjectRoot: s.manager.Grant.ProjectRoot}).Cancel(a.Job)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": true, "run": record}, nil
}
func (s *CapsuleServer) ciSummary(_ context.Context, _ *mcpsdk.CallToolRequest, a capsuleCISummaryArgs) (*mcpsdk.CallToolResult, any, error) {
	summary, err := (ci.FileRunStore{ProjectRoot: s.manager.Grant.ProjectRoot}).ProviderSummary(a.Limit)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": true, "summary": summary}, nil
}
func capsuleCIHygienePlanner(project string) ci.HygienePlanner {
	return ci.HygienePlannerFunc(func(ctx context.Context, policy ci.CleanupPolicy) (ci.HygieneReport, error) {
		plan, err := hygiene.BuildPlan(ctx, hygiene.Options{ProjectRoot: project, KeepRuns: policy.KeepRuns, IncludeCapsuleCache: policy.IncludeCapsuleCache})
		if err != nil {
			return ci.HygieneReport{}, err
		}
		return ci.HygieneReport{Schema: plan.Schema, Candidates: len(plan.Candidates), TotalBytes: plan.TotalBytes}, nil
	})
}
func (s *CapsuleServer) cleanupPlan(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsuleCleanupArgs) (*mcpsdk.CallToolResult, any, error) {
	plan, err := hygiene.BuildPlan(ctx, hygiene.Options{ProjectRoot: s.manager.Grant.ProjectRoot, KeepRuns: a.KeepRuns, IncludeCapsuleCache: a.IncludeCapsuleCache})
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	out, err := s.projectCleanupPlan(plan)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": true, "plan": out}, nil
}
func (s *CapsuleServer) cleanupApply(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsuleCleanupArgs) (*mcpsdk.CallToolResult, any, error) {
	if !s.manager.Grant.Allows("effect", "cleanup") {
		return capsuleErr(fmt.Errorf("%w: cleanup", control.ErrDenied)), nil, nil
	}
	result, err := hygiene.Apply(ctx, hygiene.Options{ProjectRoot: s.manager.Grant.ProjectRoot, KeepRuns: a.KeepRuns, IncludeCapsuleCache: a.IncludeCapsuleCache})
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	plan, err := s.projectCleanupPlan(result.Plan)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	removed, err := s.projectCleanupCandidates(result.Removed)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": true, "result": map[string]any{"plan": plan, "removed": removed, "total_bytes": result.TotalBytes}}, nil
}
func (s *CapsuleServer) envResolve(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsuleEnvArgs) (*mcpsdk.CallToolResult, any, error) {
	r := environment.Resolver{ProjectRoot: s.manager.Grant.ProjectRoot, Probe: environment.HostProbe()}
	lock, err := r.Resolve(ctx, a.ID)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": true, "lock": lock}, nil
}
func (s *CapsuleServer) envLock(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsuleEnvArgs) (*mcpsdk.CallToolResult, any, error) {
	if !s.manager.Grant.Allows("effect", "env_write") {
		return capsuleErr(fmt.Errorf("%w: env_write", control.ErrDenied)), nil, nil
	}
	r := environment.Resolver{ProjectRoot: s.manager.Grant.ProjectRoot, Probe: environment.HostProbe()}
	lock, err := r.Resolve(ctx, a.ID)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	path, err := environment.WriteLock(r.ProjectRoot, lock)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": true, "lock": lock, "path": filepath.Base(path)}, nil
}
func (s *CapsuleServer) envVerify(ctx context.Context, _ *mcpsdk.CallToolRequest, a capsuleEnvArgs) (*mcpsdk.CallToolResult, any, error) {
	r := environment.Resolver{ProjectRoot: s.manager.Grant.ProjectRoot, Probe: environment.HostProbe()}
	path := filepath.Join(r.ProjectRoot, ".kitsoki", "environments", a.ID+".lock.json")
	saved, err := environment.ReadLock(path)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	current, err := r.Resolve(ctx, a.ID)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	if saved.Digest != current.Digest {
		return capsuleErr(fmt.Errorf("capsule environment %s: lock mismatch", a.ID)), nil, nil
	}
	return nil, map[string]any{"ok": true, "lock": saved}, nil
}
func (s *CapsuleServer) planCI(ctx context.Context, h control.Handle, name string) (control.Instance, ci.Pipeline, executor.Envelope, error) {
	in, err := s.manager.Status(ctx, h)
	if err != nil {
		return control.Instance{}, ci.Pipeline{}, executor.Envelope{}, err
	}
	if in.Lease.Owner != s.owner {
		return control.Instance{}, ci.Pipeline{}, executor.Envelope{}, control.ErrDenied
	}
	project := s.manager.Grant.ProjectRoot
	cfg, err := ci.Load(project)
	if err != nil {
		return control.Instance{}, ci.Pipeline{}, executor.Envelope{}, err
	}
	pipeline, ok := cfg.Pipelines[name]
	if !ok {
		return control.Instance{}, ci.Pipeline{}, executor.Envelope{}, fmt.Errorf("capsule ci: pipeline %q not found", name)
	}
	digest, err := capsuleStoryDigest(filepath.Join(project, pipeline.Story))
	if err != nil {
		return control.Instance{}, ci.Pipeline{}, executor.Envelope{}, err
	}
	service := ci.Service{ProjectRoot: project, Env: environment.Resolver{ProjectRoot: project, Probe: environment.HostProbe()}}
	_, envelope, err := service.Plan(ctx, ci.RunRequest{Pipeline: name, Workspace: h, DefinitionDigest: in.DefinitionDigest, SourceDigest: in.Head, StoryDigest: digest, Trigger: ci.Trigger{Kind: "local", RequestedPipeline: name}})
	return in, pipeline, envelope, err
}

type capsuleCleanupCandidate struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	Reason string `json:"reason"`
	Safe   bool   `json:"safe"`
}
type capsuleCleanupPlan struct {
	Schema     string                    `json:"schema"`
	Project    string                    `json:"project"`
	KeepRuns   int                       `json:"keep_runs"`
	Candidates []capsuleCleanupCandidate `json:"candidates"`
	TotalBytes int64                     `json:"total_bytes"`
}

func (s *CapsuleServer) projectCleanupPlan(plan hygiene.Plan) (capsuleCleanupPlan, error) {
	candidates, err := s.projectCleanupCandidates(plan.Candidates)
	if err != nil {
		return capsuleCleanupPlan{}, err
	}
	project := s.projectID
	if project == "" {
		project = filepath.Base(s.manager.Grant.ProjectRoot)
	}
	return capsuleCleanupPlan{Schema: plan.Schema, Project: project, KeepRuns: plan.KeepRuns, Candidates: candidates, TotalBytes: plan.TotalBytes}, nil
}
func (s *CapsuleServer) projectCleanupCandidates(in []hygiene.Candidate) ([]capsuleCleanupCandidate, error) {
	out := make([]capsuleCleanupCandidate, 0, len(in))
	root := s.manager.Grant.ProjectRoot
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	for _, c := range in {
		path := c.Path
		if real, err := filepath.EvalSymlinks(path); err == nil {
			path = real
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("capsule cleanup: refusing to expose path outside project: %s", c.Path)
		}
		out = append(out, capsuleCleanupCandidate{ID: c.ID, Kind: c.Kind, Path: filepath.ToSlash(rel), Bytes: c.Bytes, Reason: c.Reason, Safe: c.Safe})
	}
	return out, nil
}
func capsuleStoryDigest(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
func capsuleErr(err error) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{IsError: true, StructuredContent: capsuleError{OK: false, Error: err.Error()}, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: fmt.Sprintf(`{"ok":false,"error":%q}`, err.Error())}}}
}
