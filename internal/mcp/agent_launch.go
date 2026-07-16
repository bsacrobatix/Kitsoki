package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const agentLaunchInputSchema = `{"type":"object","properties":{"agent":{"type":"string"},"task":{"type":"string"},"working_dir":{"type":"string"},"profile":{"type":"string"},"backend":{"type":"string","enum":["codex","claude"]},"mode":{"type":"string","enum":["normal","codeact"]}},"required":["agent","task"]}`

type AgentLaunchConfig struct {
	AllowedAgents []string
	ConfigPath    string
	Binary        string
	Timeout       time.Duration
}

type AgentLaunchServer struct {
	mcpSrv *mcpsdk.Server
	cfg    AgentLaunchConfig
	allow  map[string]bool
}

type AgentLaunchRequest struct {
	Agent      string `json:"agent"`
	Task       string `json:"task"`
	WorkingDir string `json:"working_dir,omitempty"`
	Profile    string `json:"profile,omitempty"`
	Backend    string `json:"backend,omitempty"`
	Mode       string `json:"mode,omitempty"`
}

func NewAgentLaunchServer(cfg AgentLaunchConfig) (*AgentLaunchServer, error) {
	allow := make(map[string]bool, len(cfg.AllowedAgents))
	for _, name := range cfg.AllowedAgents {
		if name = strings.TrimSpace(name); name != "" {
			allow[name] = true
		}
	}
	if len(allow) == 0 {
		return nil, fmt.Errorf("agent-launch mcp: at least one --allow-agent is required")
	}
	if cfg.Binary == "" {
		cfg.Binary = os.Args[0]
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Minute
	}
	s := &AgentLaunchServer{cfg: cfg, allow: allow}
	s.mcpSrv = mcpsdk.NewServer(&mcpsdk.Implementation{Name: "kitsoki-agent-launch", Version: "0.1.0"}, nil)
	for _, name := range []string{"agent.launch_plan", "agent.launch"} {
		s.mcpSrv.AddTool(&mcpsdk.Tool{Name: name, Description: "Launch an explicitly allowlisted Kitsoki agent through launch policy.", InputSchema: json.RawMessage(agentLaunchInputSchema)}, s.handle(name == "agent.launch"))
	}
	return s, nil
}

func (s *AgentLaunchServer) handle(execute bool) func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	return func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		var input AgentLaunchRequest
		if err := json.Unmarshal(req.Params.Arguments, &input); err != nil {
			return nil, fmt.Errorf("agent-launch: invalid arguments: %w", err)
		}
		if !s.allow[strings.TrimSpace(input.Agent)] {
			return nil, fmt.Errorf("agent-launch: agent %q is not allowlisted", input.Agent)
		}
		if strings.TrimSpace(input.Task) == "" {
			return nil, fmt.Errorf("agent-launch: task is required")
		}
		args := []string{"agent", "launch", "--agent", input.Agent, "--task", input.Task}
		if input.WorkingDir != "" {
			args = append(args, "--working-dir", input.WorkingDir)
		}
		if input.Profile != "" {
			args = append(args, "--profile", input.Profile)
		}
		if input.Backend != "" {
			args = append(args, "--backend", input.Backend)
		}
		if input.Mode != "" {
			args = append(args, "--mode", input.Mode)
		}
		if s.cfg.ConfigPath != "" {
			args = append(args, "--config", s.cfg.ConfigPath)
		}
		if execute {
			args = append(args, "--exec")
		}
		callCtx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
		defer cancel()
		cmd := exec.CommandContext(callCtx, s.cfg.Binary, args...)
		if input.WorkingDir != "" {
			cmd.Dir = input.WorkingDir
		}
		out, err := cmd.CombinedOutput()
		result := map[string]any{"ok": err == nil, "agent": input.Agent, "executed": execute, "output": string(out)}
		if err != nil {
			result["error"] = err.Error()
		}
		encoded, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return nil, marshalErr
		}
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: string(encoded)}}, IsError: err != nil}, nil
	}
}

func (s *AgentLaunchServer) Run(ctx context.Context) error {
	return s.mcpSrv.Run(ctx, &mcpsdk.StdioTransport{})
}
func (s *AgentLaunchServer) Connect(ctx context.Context, t mcpsdk.Transport, opts *mcpsdk.ServerSessionOptions) (*mcpsdk.ServerSession, error) {
	return s.mcpSrv.Connect(ctx, t, opts)
}
