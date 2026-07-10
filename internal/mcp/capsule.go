// Capsule MCP is the least-authority coding-agent surface. It deliberately
// accepts opaque workspace handles and project-relative paths only: no tool can
// name an arbitrary host directory, add an executor, or widen its startup grant.
package mcp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/reconcile"
)

type CapsuleConfig struct {
	Manager   *control.Manager
	Owner     string
	ProjectID string
}
type CapsuleServer struct {
	mcpSrv           *mcpsdk.Server
	manager          *control.Manager
	owner, projectID string
	plans            map[string]capsuleSyncPlan
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
	s := &CapsuleServer{manager: cfg.Manager, owner: cfg.Owner, projectID: cfg.ProjectID, plans: map[string]capsuleSyncPlan{}}
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
	mcpsdk.AddTool(s.mcpSrv, &mcpsdk.Tool{Name: "capsule.sync.apply", Description: "Apply a previously returned reconciliation plan only if all observed refs and the workspace generation are unchanged. Required CI gate evidence is checked before promotion."}, s.syncApply)
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
	if !s.manager.Grant.Allows("effect", "local_reconcile") {
		return capsuleErr(fmt.Errorf("%w: local_reconcile", control.ErrDenied)), nil, nil
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
	record, ok := s.plans[a.PlanDigest]
	s.mu.Unlock()
	if !ok {
		return capsuleErr(fmt.Errorf("capsule sync: plan %q not found", a.PlanDigest)), nil, nil
	}
	in, err := s.manager.Status(ctx, record.handle)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	if in.Lease.Owner != s.owner {
		return capsuleErr(control.ErrDenied), nil, nil
	}
	gate := reconcile.GateVerifierFunc(func(_ context.Context, receipt string, plan reconcile.Plan) error {
		if plan.RequiredGate != "" && strings.TrimSpace(receipt) == "" {
			return fmt.Errorf("capsule sync: required gate receipt is missing")
		}
		return nil
	})
	result, err := (reconcile.Reconciler{VCS: reconcile.Git{}, Gates: gate}).Apply(ctx, record.plan, a.GateReceipt)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	next, err := s.manager.MarkIntegrated(ctx, record.handle)
	if err != nil {
		return capsuleErr(err), nil, nil
	}
	return nil, map[string]any{"ok": true, "result": result, "workspace": next}, nil
}
func capsuleErr(err error) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{IsError: true, StructuredContent: capsuleError{OK: false, Error: err.Error()}, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: fmt.Sprintf(`{"ok":false,"error":%q}`, err.Error())}}}
}
