// Capsule MCP is the least-authority coding-agent surface. It deliberately
// accepts opaque workspace handles and project-relative paths only: no tool can
// name an arbitrary host directory, add an executor, or widen its startup grant.
package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"kitsoki/internal/capsule/control"
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
}

func NewCapsuleServer(cfg CapsuleConfig) (*CapsuleServer, error) {
	if cfg.Manager == nil {
		return nil, fmt.Errorf("capsule mcp: manager is required")
	}
	if strings.TrimSpace(cfg.Owner) == "" {
		return nil, fmt.Errorf("capsule mcp: owner is required")
	}
	s := &CapsuleServer{manager: cfg.Manager, owner: cfg.Owner, projectID: cfg.ProjectID}
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
func capsuleErr(err error) *mcpsdk.CallToolResult {
	return &mcpsdk.CallToolResult{IsError: true, StructuredContent: capsuleError{OK: false, Error: err.Error()}, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: fmt.Sprintf(`{"ok":false,"error":%q}`, err.Error())}}}
}
