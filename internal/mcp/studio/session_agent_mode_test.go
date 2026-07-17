package studio_test

// session_agent_mode_test.go — MCP studio coverage for the `agent:<name>`
// virtual story path (agent mode, .context/agent-mode-design.md §1/§2.1).
// newSessionRuntime carries its own agent-scheme head-check
// (session_runtime.go: agentroot.Resolve + Synthesize with the runtime's
// carried launch policy), independent wiring from the tested cmd/kitsoki and
// testrunner heads — so this file proves the studio branch directly:
//
//   1. session.new {story_path: "agent:<name>"} over a hermetic project TOML
//      lands the session in the synthesized `agent` room;
//   2. an unknown agent name surfaces a clean typed error (mirrors a bad
//      story path, never a panic or an opaque load failure);
//   3. the runtime's SetAgentLaunchPolicy policy reaches Synthesize's
//      write/external preflight — a denying policy rejects a write agent
//      with the auditable preflight denial BEFORE any session exists.
//
// Hermeticity: the studio branch resolves through agentroot.Sources{} (zero
// value = real chain), which has no injection seam here, so — exactly like
// cmd/kitsoki's agent_mode_test.go and testrunner's TestRunFlows_AgentScheme —
// the test isolates resolution by chdir'ing into a temp project carrying the
// TOMLs and redirecting HOME away from the developer's real ~/.codex/agents.
// No LLM anywhere: replay harness mode, no cassette, no drive of the write
// agent's dispatching shape.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/agentroot"
	"kitsoki/internal/host"
	studio "kitsoki/internal/mcp/studio"
)

// withStudioProjectAgents isolates the process in a temp project defining two
// TOML agents under .kitsoki/agents: "studio-demo" (read-only, conversational
// shape — no preflight, no on_enter dispatch) and "studio-scribe" (write shape
// — subject to the launch-policy preflight). HOME is redirected so the
// developer's real ~/.codex/agents and default trace/cache homes never leak.
func withStudioProjectAgents(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	require.NoError(t, os.MkdirAll(home, 0o755))
	t.Setenv("HOME", home)

	agentsDir := filepath.Join(dir, ".kitsoki", "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))
	const demoTOML = `name = "studio-demo"
description = "Studio read-only demo agent"
developer_instructions = "You answer questions about the project."
sandbox_mode = "read-only"
`
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "studio-demo.toml"), []byte(demoTOML), 0o600))
	const scribeTOML = `name = "studio-scribe"
description = "Studio write agent"
developer_instructions = "You make the requested change."
sandbox_mode = "workspace-write"
`
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "studio-scribe.toml"), []byte(scribeTOML), 0o600))

	t.Chdir(dir)
}

// openAgentSession issues session.new over the agent:<name> scheme and returns
// the raw result (the caller asserts success or the expected typed error).
func openAgentSession(ctx context.Context, t *testing.T, cs *mcpsdk.ClientSession, name string) (*mcpsdk.CallToolResult, error) {
	t.Helper()
	return callTool(ctx, cs, "session.new", map[string]any{
		"story_path": "agent:" + name,
		"harness":    "replay",
		"trace":      filepath.Join(t.TempDir(), "trace.jsonl"),
	})
}

// TestSessionNew_AgentScheme_LandsInAgentRoom proves the studio head accepts
// the agent:<name> scheme end-to-end: the synthesized one-room root loads, a
// session is created, and the open reply reports the session resting in the
// `agent` room (agentroot.RoomName) under replay mode.
func TestSessionNew_AgentScheme_LandsInAgentRoom(t *testing.T) {
	withStudioProjectAgents(t)
	ctx := context.Background()
	srv, _ := newReplayServer(t)
	cs := connectInProcess(ctx, t, srv)

	res, err := openAgentSession(ctx, t, cs, "studio-demo")
	require.NoError(t, err)
	require.False(t, res.IsError, "session.new over agent:<name> must succeed: %s", contentText(res))

	var ok studio.SessionOpenOK
	require.NoError(t, json.Unmarshal([]byte(contentText(res)), &ok))
	require.True(t, ok.OK)
	require.Equal(t, agentroot.RoomName, ok.State,
		"an agent session must land in the synthesized %q room", agentroot.RoomName)
	require.Equal(t, "replay", ok.Mode, "no-cassette agent session stays on the no-LLM replay path")
	require.NotEmpty(t, ok.Handle)
}

// TestSessionNew_AgentScheme_UnknownNameCleanError proves an unknown agent
// name fails session.new with a clean typed error naming the resolution
// failure — the same UX contract as a bad story path — instead of opening a
// broken handle or panicking.
func TestSessionNew_AgentScheme_UnknownNameCleanError(t *testing.T) {
	withStudioProjectAgents(t)
	ctx := context.Background()
	srv, _ := newReplayServer(t)
	cs := connectInProcess(ctx, t, srv)

	res, err := openAgentSession(ctx, t, cs, "definitely-not-a-known-agent")
	require.NoError(t, err, "an unknown agent is a tool-level error, not a transport failure")
	require.True(t, res.IsError, "session.new must reject an unknown agent name")
	require.Contains(t, contentText(res), "not found",
		"the error must surface the agentroot resolution failure verbatim")
}

// TestSessionNew_AgentScheme_LaunchPolicyPreflightDenies proves the policy
// pass-through that ONLY this head carries for MCP-created sessions: the
// StudioSession's SetAgentLaunchPolicy policy must reach
// agentroot.Synthesize's write/external preflight, so a denying policy
// rejects a write agent at session.new time with the auditable preflight
// denial (design §2.1 "fail fast before any session exists"). Dropping the
// LaunchPolicy pass-through in session_runtime.go makes this test fail: the
// zero policy allows everything and the denial message never appears.
func TestSessionNew_AgentScheme_LaunchPolicyPreflightDenies(t *testing.T) {
	withStudioProjectAgents(t)
	ctx := context.Background()
	srv, sess := newReplayServer(t)
	// RequireCapsule denies the bare temp cwd (not an opened capsule) — the
	// same denying posture internal/host's policy tests use.
	sess.SetAgentLaunchPolicy(host.AgentLaunchPolicy{Enabled: true, RequireCapsule: true})
	cs := connectInProcess(ctx, t, srv)

	res, err := openAgentSession(ctx, t, cs, "studio-scribe")
	require.NoError(t, err)
	require.True(t, res.IsError, "a denying launch policy must reject the write agent at session.new")
	require.Contains(t, contentText(res), "agent mode preflight",
		"the rejection must be the synthesis-time preflight denial, not a later dispatch error")
}
