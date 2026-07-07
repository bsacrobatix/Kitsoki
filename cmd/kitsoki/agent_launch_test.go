package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/host"
)

func TestAgentLaunchPlan_UsesStoryAgentAndHarnessProfile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(host.CodexBinEnv, "/bin/codex-test")
	t.Setenv("SYNTHETIC_API_KEY", "secret-token")
	appPath := filepath.Join(dir, "app.yaml")
	require.NoError(t, os.WriteFile(appPath, []byte(`
app: { id: launch-test, version: 0.1.0, title: Launch Test }
hosts: [host.agent.task]
agents:
  maker:
    system_prompt: "You are the maker."
    model: claude-sonnet-4-6
    tools: [Read, Write]
    cwd: work
world: {}
intents: {}
root: idle
states:
  idle: { view: "idle" }
`), 0644))
	require.NoError(t, os.Mkdir(filepath.Join(dir, "work"), 0755))
	cfgPath := filepath.Join(dir, ".kitsoki.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`
default_profile: synthetic-codex
harness_profiles:
  synthetic-codex:
    backend: codex
    model: hf:test/model
    env:
      OPENAI_BASE_URL: https://api.synthetic.new/openai/v1
      OPENAI_API_KEY: ${SYNTHETIC_API_KEY}
`), 0644))

	plan, err := buildAgentLaunchPlan(agentLaunchOptions{
		AppPath:    appPath,
		ConfigPath: cfgPath,
		AgentName:  "maker",
		Task:       "implement it",
	})
	require.NoError(t, err)
	for _, cleanup := range plan.cleanups {
		t.Cleanup(cleanup)
	}
	require.Equal(t, "synthetic-codex", plan.Profile)
	require.Equal(t, "codex", plan.Backend)
	require.Equal(t, "/bin/codex-test", plan.Binary)
	require.Equal(t, "hf:test/model", plan.Model, "profile model supersedes story-local Claude model")
	require.Equal(t, filepath.Join(dir, "work"), plan.WorkingDir)
	require.NotContains(t, plan.Stdin, "You are the maker.")
	require.Contains(t, plan.Stdin, "implement it")
	require.Contains(t, readCodexModelInstructionsFile(t, plan.Command), "You are the maker.")
	require.Equal(t, "<redacted>", plan.Env["OPENAI_API_KEY"])
	require.Equal(t, "https://api.synthetic.new/openai/v1", plan.Env["OPENAI_BASE_URL"])
	require.Contains(t, plan.Command, "exec")
	require.Contains(t, plan.Command, "-m")
	require.Contains(t, plan.Command, "hf:test/model")
	require.Contains(t, plan.Command, "-C")
	require.Contains(t, plan.Command, filepath.Join(dir, "work"))
}

func TestAgentLaunchPlan_ReadOnlyAgentDeniesMutationTools(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(host.AgentBinEnv, "/bin/claude-test")
	appPath := filepath.Join(dir, "app.yaml")
	require.NoError(t, os.WriteFile(appPath, []byte(`
app: { id: launch-test, version: 0.1.0, title: Launch Test }
hosts: [host.agent.ask]
agents:
  reviewer:
    system_prompt: "Review only."
    tools: [Read, Grep, Glob]
    external_side_effect: false
world: {}
intents: {}
root: idle
states:
  idle: { view: "idle" }
`), 0644))

	plan, err := buildAgentLaunchPlan(agentLaunchOptions{
		AppPath:    appPath,
		ConfigPath: filepath.Join(dir, ".kitsoki.yaml"),
		AgentName:  "reviewer",
		Task:       "look around",
	})
	require.NoError(t, err)
	require.Equal(t, "claude", plan.Backend)
	require.Equal(t, "/bin/claude-test", plan.Binary)
	require.Contains(t, plan.Command, "--permission-mode")
	require.Contains(t, plan.Command, "default")
	denied := flagValue(t, plan.Command, "--disallowedTools")
	require.Contains(t, denied, "Agent")
	require.Contains(t, denied, "AskUserQuestion")
	require.Contains(t, denied, "Bash")
	require.Contains(t, denied, "Write")
}

func TestAgentLaunchPlan_FreestandingCodexAgentAttachesMCP(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(host.CodexBinEnv, "/bin/codex-test")
	oldwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".codex", "agents"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".codex", "agents", "kitsoki-mcp-driver.toml"), []byte(`
name = "kitsoki-mcp-driver"
description = "Drive kitsoki through MCP."
developer_instructions = """
Use ONLY the kitsoki studio MCP.
Start with studio.ping.
"""
model = "gpt-5.5"
model_reasoning_effort = "medium"
sandbox_mode = "read-only"

[mcp_servers.kitsoki]
command = "kitsoki"
args = ["mcp", "--stories-dir", "stories"]
`), 0644))

	plan, err := buildAgentLaunchPlan(agentLaunchOptions{
		AgentName: "kitsoki-mcp-driver",
		Task:      "Call studio.ping and report the version.",
	})
	require.NoError(t, err)
	for _, cleanup := range plan.cleanups {
		t.Cleanup(cleanup)
	}
	require.False(t, plan.Interactive)
	require.Empty(t, plan.App)
	wantAgentFile, err := filepath.Abs(filepath.Join(dir, ".codex", "agents", "kitsoki-mcp-driver.toml"))
	require.NoError(t, err)
	wantAgentFile, err = filepath.EvalSymlinks(wantAgentFile)
	require.NoError(t, err)
	require.Equal(t, wantAgentFile, plan.AgentFile)
	require.Equal(t, "codex", plan.Backend)
	require.Equal(t, "/bin/codex-test", plan.Binary)
	require.Equal(t, "gpt-5.5", plan.Model)
	require.Equal(t, "medium", plan.Effort)
	wantWorkingDir, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	require.Equal(t, wantWorkingDir, plan.WorkingDir)
	require.Contains(t, plan.Stdin, "tool_search")
	require.Contains(t, plan.Stdin, "Call studio.ping")
	require.Contains(t, readCodexModelInstructionsFile(t, plan.Command), "Use ONLY the kitsoki studio MCP.")
	joined := strings.Join(plan.Command, " ")
	require.Contains(t, joined, "mcp_servers.kitsoki.command=\"kitsoki\"")
	require.Contains(t, joined, "mcp_servers.kitsoki.args=[\"mcp\",\"--stories-dir\",\"stories\"]")
	require.Contains(t, plan.Command, "--dangerously-bypass-approvals-and-sandbox")
}

func TestAgentLaunchPlan_FreestandingCodexAgentInteractive(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(host.CodexBinEnv, "/bin/codex-test")
	oldwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".codex", "agents"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".codex", "agents", "kitsoki-mcp-driver.toml"), []byte(`
name = "kitsoki-mcp-driver"
developer_instructions = "Use ONLY the kitsoki studio MCP."
model = "gpt-5.5"
model_reasoning_effort = "medium"
sandbox_mode = "read-only"

[mcp_servers.kitsoki]
command = "kitsoki"
args = ["mcp", "--stories-dir", "stories"]
`), 0644))

	plan, err := buildAgentLaunchPlan(agentLaunchOptions{
		AgentName: "kitsoki-mcp-driver",
	})
	require.NoError(t, err)
	require.True(t, plan.Interactive)
	require.Equal(t, "codex", plan.Backend)
	require.Equal(t, "/bin/codex-test", plan.Binary)
	require.Empty(t, plan.RunAsUser)
	require.Empty(t, plan.Stdin)
	require.NotContains(t, plan.Command, "exec", "interactive launch must use top-level codex, not codex exec")
	require.NotContains(t, plan.Command, "--dangerously-bypass-approvals-and-sandbox")
	require.Contains(t, plan.Command, "-m")
	require.Contains(t, plan.Command, "gpt-5.5")
	require.Contains(t, plan.Command, "--sandbox")
	require.Contains(t, plan.Command, "read-only")
	require.Contains(t, plan.Command, "model_reasoning_effort=\"medium\"")
	joined := strings.Join(plan.Command, " ")
	require.Contains(t, joined, "mcp_servers.kitsoki.command=\"kitsoki\"")
	require.Contains(t, joined, "mcp_servers.kitsoki.args=[\"mcp\",\"--stories-dir\",\"stories\"]")
	require.Contains(t, plan.Command[len(plan.Command)-1], "Use ONLY the kitsoki studio MCP.")
}

func TestAgentLaunchPlan_FreestandingCodexAgentInteractiveUsesRunAsUserWrapper(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(host.CodexBinEnv, "/bin/codex-test")
	oldwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".codex", "agents"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "agent-bin"), 0755))
	wrapper := filepath.Join(dir, "agent-bin", "codex")
	require.NoError(t, os.WriteFile(wrapper, []byte("#!/bin/sh\nexec /usr/bin/sudo -n -H -u kitsoki-agent /bin/codex \"$@\"\n"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kitsoki.yaml"), []byte("story_dirs: []\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kitsoki.local.yaml"), []byte(`
agent_user_delegation:
  enabled: true
  run_as_user: kitsoki-agent
  wrapper_bin: ./agent-bin
`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".codex", "agents", "kitsoki-mcp-driver.toml"), []byte(`
name = "kitsoki-mcp-driver"
developer_instructions = "Use ONLY the kitsoki studio MCP."
model = "gpt-5.5"
sandbox_mode = "read-only"

[mcp_servers.kitsoki]
command = "kitsoki"
args = ["mcp", "--stories-dir", "stories"]
`), 0644))

	plan, err := buildAgentLaunchPlan(agentLaunchOptions{
		ConfigPath: filepath.Join(dir, ".kitsoki.yaml"),
		AgentName:  "kitsoki-mcp-driver",
	})
	require.NoError(t, err)
	require.True(t, plan.Interactive)
	require.Equal(t, "kitsoki-agent", plan.RunAsUser)
	require.Equal(t, wrapper, plan.Binary)
	require.Equal(t, wrapper, plan.Command[0])
	require.Contains(t, plan.Command, "--sandbox")
	require.Contains(t, plan.Command, "read-only")
	require.Contains(t, plan.Command[len(plan.Command)-1], "Use ONLY the kitsoki studio MCP.")
}

func TestAgentLaunchPlan_FreestandingCodexBackendIgnoresMismatchedDefaultProfile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(host.CodexBinEnv, "/bin/codex-test")
	oldwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kitsoki.yaml"), []byte(`
default_profile: claude-native
harness_profiles:
  claude-native:
    backend: claude
    model: opus
    effort: high
`), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".codex", "agents"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".codex", "agents", "kitsoki-mcp-driver.toml"), []byte(`
name = "kitsoki-mcp-driver"
developer_instructions = "Use ONLY the kitsoki studio MCP."
model = "gpt-5.5"
model_reasoning_effort = "medium"
`), 0644))

	plan, err := buildAgentLaunchPlan(agentLaunchOptions{
		AgentName:   "kitsoki-mcp-driver",
		ConfigPath:  filepath.Join(dir, ".kitsoki.yaml"),
		Backend:     "codex",
		Interactive: true,
		WorkingDir:  ".",
	})
	require.NoError(t, err)
	require.True(t, plan.Interactive)
	require.Equal(t, "codex", plan.Backend)
	require.Empty(t, plan.Profile)
	require.Equal(t, "gpt-5.5", plan.Model)
	require.Equal(t, "medium", plan.Effort)
	joined := strings.Join(plan.Command, " ")
	require.NotContains(t, joined, "opus")
	require.NotContains(t, joined, "claude-native")
	require.Contains(t, plan.Command, "-m")
	require.Contains(t, plan.Command, "gpt-5.5")
}

func TestAgentLaunchPlan_FreestandingCodexInteractiveDropsClaudeAgentModel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(host.CodexBinEnv, "/bin/codex-test")
	oldwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".codex", "agents"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".codex", "agents", "kitsoki-mcp-driver.toml"), []byte(`
name = "kitsoki-mcp-driver"
developer_instructions = "Use ONLY the kitsoki studio MCP."
model = "opus"
`), 0644))

	plan, err := buildAgentLaunchPlan(agentLaunchOptions{
		AgentName:   "kitsoki-mcp-driver",
		Backend:     "codex",
		Interactive: true,
		WorkingDir:  ".",
	})
	require.NoError(t, err)
	require.True(t, plan.Interactive)
	require.Equal(t, "codex", plan.Backend)
	require.Empty(t, plan.Model)
	joined := strings.Join(plan.Command, " ")
	require.NotContains(t, joined, "opus")
	require.NotContains(t, plan.Command, "-m")
}

func TestAgentLaunchPlan_ExplicitProfileBackendConflict(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(host.CodexBinEnv, "/bin/codex-test")
	oldwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(dir))
	t.Cleanup(func() { _ = os.Chdir(oldwd) })
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".kitsoki.yaml"), []byte(`
default_profile: claude-native
harness_profiles:
  claude-native:
    backend: claude
    model: opus
`), 0644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".codex", "agents"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".codex", "agents", "kitsoki-mcp-driver.toml"), []byte(`
name = "kitsoki-mcp-driver"
developer_instructions = "Use ONLY the kitsoki studio MCP."
`), 0644))

	_, err = buildAgentLaunchPlan(agentLaunchOptions{
		AgentName:   "kitsoki-mcp-driver",
		ConfigPath:  filepath.Join(dir, ".kitsoki.yaml"),
		Profile:     "claude-native",
		Backend:     "codex",
		Interactive: true,
		WorkingDir:  ".",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), `--profile "claude-native" selects backend "claude", which conflicts with --backend "codex"`)
}

func TestAgentLaunchPlan_RawInteractiveNoAgentPrompt(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(host.CodexBinEnv, "/bin/codex-test")
	t.Setenv("RAW_OPENAI_API_KEY", "secret-token")
	cfgPath := filepath.Join(dir, ".kitsoki.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`
default_profile: desktop
harness_profiles:
  desktop:
    backend: codex
    model: gpt-5.5
    effort: medium
    env:
      OPENAI_API_KEY: ${RAW_OPENAI_API_KEY}
`), 0644))

	plan, err := buildAgentLaunchPlan(agentLaunchOptions{
		ConfigPath:     cfgPath,
		RawInteractive: true,
		WorkingDir:     dir,
		AddDirs:        []string{filepath.Join(dir, "extra")},
	})
	require.NoError(t, err)
	require.True(t, plan.Interactive)
	require.Equal(t, "raw", plan.Agent)
	require.Empty(t, plan.App)
	require.Empty(t, plan.AgentFile)
	require.Equal(t, "desktop", plan.Profile)
	require.Equal(t, "codex", plan.Backend)
	require.Equal(t, "/bin/codex-test", plan.Binary)
	require.Empty(t, plan.Stdin)
	require.Equal(t, "<redacted>", plan.Env["OPENAI_API_KEY"])
	require.Contains(t, plan.Command, "-m")
	require.Contains(t, plan.Command, "gpt-5.5")
	require.Contains(t, plan.Command, "-C")
	require.Contains(t, plan.Command, dir)
	require.Contains(t, plan.Command, "--add-dir")
	require.Contains(t, plan.Command, filepath.Join(dir, "extra"))
	joined := strings.Join(plan.Command, " ")
	require.NotContains(t, joined, "--dangerously-bypass-approvals-and-sandbox")
	require.NotContains(t, joined, "developer_instructions")
	require.NotContains(t, joined, "Use ONLY")
}

func TestAgentLaunchPlan_LaunchPolicyDeniesRawInteractive(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, ".kitsoki.yaml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(`
agent_launch_policy:
  enabled: true
  require_capsule: true
  protected_branches: [release]
`), 0644))

	_, err := buildAgentLaunchPlan(agentLaunchOptions{
		ConfigPath:     cfgPath,
		RawInteractive: true,
		WorkingDir:     dir,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "agent launch policy denied")
	require.Contains(t, err.Error(), "inside protected root")
}

func flagValue(t *testing.T, args []string, flag string) string {
	t.Helper()
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("flag %s not found in %v", flag, args)
	return ""
}

func readCodexModelInstructionsFile(t *testing.T, args []string) string {
	t.Helper()
	path := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-c" {
			continue
		}
		if v, ok := strings.CutPrefix(args[i+1], "model_instructions_file="); ok {
			path = strings.Trim(v, `"`)
			break
		}
	}
	require.NotEmpty(t, path, "codex command missing model_instructions_file override: %v", args)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(raw)
}
