package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"kitsoki/internal/app"
	"kitsoki/internal/host"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/webconfig"
)

type agentLaunchOptions struct {
	AppPath        string
	ConfigPath     string
	AgentName      string
	Profile        string
	Backend        string
	Model          string
	Effort         string
	WorkingDir     string
	Task           string
	TaskFile       string
	PermissionMode string
	AddDirs        []string
	Env            []string
	Exec           bool
}

type agentLaunchPlan struct {
	App         string            `json:"app,omitempty"`
	Agent       string            `json:"agent"`
	Profile     string            `json:"profile,omitempty"`
	Backend     string            `json:"backend"`
	Binary      string            `json:"binary"`
	WorkingDir  string            `json:"working_dir"`
	Model       string            `json:"model,omitempty"`
	Effort      string            `json:"effort,omitempty"`
	Tools       []string          `json:"tools,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Command     []string          `json:"command"`
	Stdin       string            `json:"stdin,omitempty"`
	DryRun      bool              `json:"dry_run"`
	FutureNotes []string          `json:"future_notes,omitempty"`

	providerEnv map[string]string
	claudeArgs  []string
}

func agentLaunchCmd() *cobra.Command {
	var opts agentLaunchOptions
	cmd := &cobra.Command{
		Use:   "launch --app <app.yaml> --agent <name> [--task <text>|--task-file <path>]",
		Short: "Launch a Claude/Codex CLI from a story agent definition",
		Long: `Resolve a reusable story agents: entry plus an optional harness profile
and turn it into a concrete Claude/Codex task-agent launch.

By default this prints a redacted JSON launch plan and does not call a provider.
Pass --exec to run the selected external CLI.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, err := buildAgentLaunchPlan(opts)
			if err != nil {
				return err
			}
			if !opts.Exec {
				plan.DryRun = true
				return writeAgentLaunchPlan(cmd.OutOrStdout(), plan)
			}
			ctx := context.Background()
			if len(plan.providerEnv) > 0 {
				ctx = host.WithAgentProviderEnv(ctx, plan.providerEnv)
			}
			ctx = host.WithAgentBackendNamed(ctx, plan.Backend)
			out, err := host.RunClaudeOneShotForHarness(ctx, plan.Binary, plan.claudeArgs, plan.Stdin, plan.WorkingDir)
			if err != nil {
				return err
			}
			if strings.TrimSpace(out) != "" {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), out)
			}
			return err
		},
	}

	cmd.Flags().StringVar(&opts.AppPath, "app", "", "story app.yaml whose top-level agents: block declares --agent (required)")
	cmd.Flags().StringVar(&opts.ConfigPath, "config", webconfig.DefaultConfigFile, "kitsoki config file for harness profiles")
	cmd.Flags().StringVar(&opts.AgentName, "agent", "", "agent name from the story agents: block (required)")
	cmd.Flags().StringVar(&opts.Profile, "profile", "", "harness profile name; defaults to config default_profile when set")
	cmd.Flags().StringVar(&opts.Backend, "backend", "", "override backend: claude|codex|copilot")
	cmd.Flags().StringVar(&opts.Model, "model", "", "override model for this task")
	cmd.Flags().StringVar(&opts.Effort, "effort", "", "override reasoning effort for this task")
	cmd.Flags().StringVar(&opts.WorkingDir, "working-dir", "", "override agent cwd; defaults to agent cwd then app directory")
	cmd.Flags().StringVar(&opts.Task, "task", "", "task instructions")
	cmd.Flags().StringVar(&opts.TaskFile, "task-file", "", "file containing task instructions")
	cmd.Flags().StringVar(&opts.PermissionMode, "permission-mode", "", "permission mode: ask|bypassPermissions|denyAll")
	cmd.Flags().StringArrayVar(&opts.AddDirs, "add-dir", nil, "additional directory made available to the launched agent")
	cmd.Flags().StringArrayVar(&opts.Env, "env", nil, "extra environment override KEY=VALUE; values are redacted from dry-run output")
	cmd.Flags().BoolVar(&opts.Exec, "exec", false, "actually run the external CLI; default is a no-provider dry run")

	_ = cmd.MarkFlagRequired("app")
	_ = cmd.MarkFlagRequired("agent")
	return cmd
}

func buildAgentLaunchPlan(opts agentLaunchOptions) (agentLaunchPlan, error) {
	if strings.TrimSpace(opts.Task) != "" && strings.TrimSpace(opts.TaskFile) != "" {
		return agentLaunchPlan{}, fmt.Errorf("use only one of --task or --task-file")
	}
	def, err := loadAppWithEnv(opts.AppPath)
	if err != nil {
		return agentLaunchPlan{}, err
	}
	decl, ok := def.Agents[opts.AgentName]
	if !ok || decl == nil {
		return agentLaunchPlan{}, fmt.Errorf("app %s has no agent %q", opts.AppPath, opts.AgentName)
	}
	profiles, defaultProfile, err := loadLaunchProfiles(opts.ConfigPath)
	if err != nil {
		return agentLaunchPlan{}, err
	}
	profileName := firstLaunchNonEmpty(opts.Profile, defaultProfile)
	profile, hasProfile := profiles[profileName]
	if profileName != "" && !hasProfile {
		return agentLaunchPlan{}, fmt.Errorf("unknown harness profile %q", profileName)
	}

	task, err := readLaunchTask(opts)
	if err != nil {
		return agentLaunchPlan{}, err
	}
	appDir := filepath.Dir(opts.AppPath)
	workingDir, err := resolveLaunchWorkingDir(firstLaunchNonEmpty(opts.WorkingDir, decl.Cwd, "."), appDir)
	if err != nil {
		return agentLaunchPlan{}, err
	}
	backend := firstLaunchNonEmpty(opts.Backend, profile.Backend, backendForAgentProvider(def, decl.Provider), "claude")
	if _, ok := host.ResolveAgentBackendName(backend); !ok && backend != "claude" {
		return agentLaunchPlan{}, fmt.Errorf("unknown backend %q", backend)
	}
	providerModel, providerEffort := providerDefaultsForAgent(def, decl.Provider)
	model := firstLaunchNonEmpty(opts.Model, profile.Model, decl.Model, providerModel)
	effort := firstLaunchNonEmpty(opts.Effort, profile.Effort, decl.Effort, providerEffort)
	providerEnv := map[string]string{}
	for k, v := range providerEnvForAgent(def, decl.Provider) {
		providerEnv[k] = v
	}
	for k, v := range profile.Env {
		providerEnv[k] = v
	}
	extraEnv, err := parseLaunchEnv(opts.Env)
	if err != nil {
		return agentLaunchPlan{}, err
	}
	for k, v := range extraEnv {
		providerEnv[k] = v
	}

	bin, err := resolveLaunchBin(backend, opts.Exec)
	if err != nil {
		return agentLaunchPlan{}, err
	}
	cliArgs := buildLaunchClaudeArgs(decl, model, effort, opts.PermissionMode, opts.AddDirs)
	planCtx := context.Background()
	if len(providerEnv) > 0 {
		planCtx = host.WithAgentProviderEnv(planCtx, providerEnv)
	}
	inv := host.TranslateAgentInvocationForBackend(planCtx, backend, cliArgs, task, workingDir)
	command := append([]string{bin}, inv.Args...)
	return agentLaunchPlan{
		App:         opts.AppPath,
		Agent:       opts.AgentName,
		Profile:     profileName,
		Backend:     backend,
		Binary:      bin,
		WorkingDir:  workingDir,
		Model:       model,
		Effort:      effort,
		Tools:       append([]string(nil), decl.Tools...),
		Env:         redactEnv(providerEnv),
		Command:     command,
		Stdin:       inv.Stdin,
		providerEnv: providerEnv,
		claudeArgs:  cliArgs,
		FutureNotes: []string{
			"OS sandbox policy is not applied by this command yet; pass --exec only in a trusted working tree.",
			"Extension overlays and HTTP rules should be modeled as future agent/profile fields and resolved into this launch plan.",
		},
	}, nil
}

func loadLaunchProfiles(configPath string) (map[string]orchestrator.HarnessProfile, string, error) {
	cfg, err := webconfig.Load(configPath)
	if err != nil {
		return nil, "", err
	}
	profiles, defaultProfile := harnessProfilesFromConfig(cfg)
	if profiles == nil {
		profiles = map[string]orchestrator.HarnessProfile{}
	}
	return profiles, defaultProfile, nil
}

func readLaunchTask(opts agentLaunchOptions) (string, error) {
	if strings.TrimSpace(opts.TaskFile) == "" {
		return opts.Task, nil
	}
	raw, err := os.ReadFile(opts.TaskFile)
	if err != nil {
		return "", fmt.Errorf("read --task-file: %w", err)
	}
	return string(raw), nil
}

func resolveLaunchWorkingDir(dir, appDir string) (string, error) {
	if filepath.IsAbs(dir) {
		return filepath.Clean(dir), nil
	}
	abs, err := filepath.Abs(filepath.Join(appDir, dir))
	if err != nil {
		return "", err
	}
	return abs, nil
}

func backendForAgentProvider(def *app.AppDef, provider string) string {
	if strings.TrimSpace(provider) == "" || def == nil || def.Providers == nil {
		return ""
	}
	p := def.Providers[provider]
	if p == nil {
		return ""
	}
	for k, v := range p.Env {
		if strings.EqualFold(k, "OPENAI_BASE_URL") || strings.EqualFold(k, "OPENAI_API_KEY") {
			_ = v
			return "codex"
		}
	}
	return ""
}

func providerEnvForAgent(def *app.AppDef, provider string) map[string]string {
	if strings.TrimSpace(provider) == "" || def == nil || def.Providers == nil {
		return nil
	}
	p := def.Providers[provider]
	if p == nil {
		return nil
	}
	return p.Env
}

func providerDefaultsForAgent(def *app.AppDef, provider string) (model, effort string) {
	if strings.TrimSpace(provider) == "" || def == nil || def.Providers == nil {
		return "", ""
	}
	p := def.Providers[provider]
	if p == nil {
		return "", ""
	}
	return p.Model, p.Effort
}

func buildLaunchClaudeArgs(decl *app.AgentDecl, model, effort, permissionMode string, addDirs []string) []string {
	args := []string{"-p", "--output-format", "stream-json", "--verbose"}
	args = append(args, "--setting-sources", "project,local")
	args = append(args, "--disable-slash-commands")
	args = append(args, "--strict-mcp-config")
	requestedPermissionMode := firstLaunchNonEmpty(permissionMode, launchDefaultPermissionMode(decl))
	permissionMode = normalizeLaunchPermissionMode(requestedPermissionMode)
	args = append(args, "--permission-mode", permissionMode)
	if strings.TrimSpace(decl.SystemPrompt) != "" {
		if decl.InheritClaudeDefault {
			args = append(args, "--append-system-prompt", decl.SystemPrompt)
		} else {
			args = append(args, "--system-prompt", decl.SystemPrompt)
		}
	}
	if strings.TrimSpace(model) != "" {
		args = append(args, "--model", model)
	}
	if strings.TrimSpace(effort) != "" {
		args = append(args, "--effort", effort)
	}
	if len(decl.Tools) > 0 {
		args = append(args, "--allowedTools", strings.Join(decl.Tools, ","))
	}
	denied := launchDeniedTools(decl, requestedPermissionMode)
	if len(denied) > 0 {
		args = append(args, "--disallowedTools", strings.Join(denied, ","))
	}
	for _, dir := range addDirs {
		if strings.TrimSpace(dir) != "" {
			args = append(args, "--add-dir", dir)
		}
	}
	return args
}

func launchDefaultPermissionMode(decl *app.AgentDecl) string {
	if decl != nil && decl.ExternalSideEffect != nil && !*decl.ExternalSideEffect {
		return "default"
	}
	return "bypassPermissions"
}

func launchDeniedTools(decl *app.AgentDecl, permissionMode string) []string {
	denied := []string{"AskUserQuestion", "Agent", "Task"}
	readOnly := permissionMode == "denyAll" || (decl != nil && decl.ExternalSideEffect != nil && !*decl.ExternalSideEffect)
	if readOnly {
		denied = append(denied, "Write", "Edit", "MultiEdit", "NotebookEdit", "Bash")
	}
	sort.Strings(denied)
	out := denied[:0]
	for _, tool := range denied {
		if len(out) == 0 || out[len(out)-1] != tool {
			out = append(out, tool)
		}
	}
	return out
}

func normalizeLaunchPermissionMode(mode string) string {
	switch mode {
	case "ask", "denyAll":
		return "default"
	case "":
		return "bypassPermissions"
	default:
		return mode
	}
}

func resolveLaunchBin(backend string, requireInstalled bool) (string, error) {
	binName := "claude"
	envName := host.AgentBinEnv
	switch backend {
	case "codex":
		if env := os.Getenv(host.CodexBinEnv); env != "" {
			return env, nil
		}
		binName = "codex"
		envName = host.CodexBinEnv
	case "copilot":
		if env := os.Getenv(host.CopilotBinEnv); env != "" {
			return env, nil
		}
		binName = "copilot"
		envName = host.CopilotBinEnv
	default:
		if env := os.Getenv(host.AgentBinEnv); env != "" {
			return env, nil
		}
	}
	path, err := exec.LookPath(binName)
	if err != nil {
		if requireInstalled {
			return "", fmt.Errorf("%s not found (set %s to override): %w", binName, envName, err)
		}
		return binName, nil
	}
	return path, nil
}

func parseLaunchEnv(entries []string) (map[string]string, error) {
	out := map[string]string{}
	for _, entry := range entries {
		k, v, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("--env must be KEY=VALUE, got %q", entry)
		}
		out[k] = v
	}
	return out, nil
}

func redactEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		if isSecretEnvKey(k) {
			out[k] = "<redacted>"
		} else {
			out[k] = env[k]
		}
	}
	return out
}

func isSecretEnvKey(k string) bool {
	upper := strings.ToUpper(k)
	return strings.Contains(upper, "KEY") || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET")
}

func writeAgentLaunchPlan(w interface{ Write([]byte) (int, error) }, plan agentLaunchPlan) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(plan)
}

func firstLaunchNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
