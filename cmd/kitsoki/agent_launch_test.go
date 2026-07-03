package main

import (
	"os"
	"path/filepath"
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
	require.Equal(t, "synthetic-codex", plan.Profile)
	require.Equal(t, "codex", plan.Backend)
	require.Equal(t, "/bin/codex-test", plan.Binary)
	require.Equal(t, "hf:test/model", plan.Model, "profile model supersedes story-local Claude model")
	require.Equal(t, filepath.Join(dir, "work"), plan.WorkingDir)
	require.Contains(t, plan.Stdin, "You are the maker.")
	require.Contains(t, plan.Stdin, "implement it")
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
