package orchestrator_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/chathost"
	"kitsoki/internal/chats"
	"kitsoki/internal/host"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/store"
)

// TestAskOffPath_PersonaReachesOracle asserts that when OffPathDef.Persona
// is set on the AppDef, AskOffPath forwards it as a `system_prompt` arg into
// host.oracle.talk, which in turn passes it as --append-system-prompt to the
// claude binary. We use the shared fake-oracle.sh which echoes the system
// prompt back in its answer (when present) so the assertion can ride the
// existing test-binary contract without a new mock layer.
func TestAskOffPath_PersonaReachesOracle(t *testing.T) {
	t.Setenv(host.OracleBinEnv, fakeOraclePath(t))

	const persona = "speak like a frontier scout"

	def := minimalOffPathApp()
	def.OffPath = &app.OffPathDef{
		Trigger: "/freeform",
		Banner:  "*** off-trail ***",
		Return:  "/onpath",
		Persona: persona,
	}

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	rawChatStore, err := chats.NewStore(s.DB())
	require.NoError(t, err)

	orch := orchestrator.New(def, m, s, noopHarness{},
		orchestrator.WithChatStore(chathost.NewAdapter(rawChatStore)),
	)

	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	answer, err := orch.AskOffPath(ctx, sid, "where to camp?")
	require.NoError(t, err)
	require.True(t, strings.Contains(answer, "system=["+persona+"]"),
		"persona must reach the oracle binary via --append-system-prompt; got answer=%q", answer)
}

// TestAskOffPath_NoPersona_NoSystemPromptArg asserts the legacy path: when no
// Persona is configured on OffPathDef, AskOffPath must NOT pass any
// system_prompt arg, so the binary command-line stays clean and apps without
// a persona block (cloak, dev-story) keep their pre-existing behaviour.
func TestAskOffPath_NoPersona_NoSystemPromptArg(t *testing.T) {
	// Reuse the standard setup which builds a minimalOffPathApp() without a
	// Persona — that's exactly the no-persona case we want to assert.
	orch, _, _, sid := setupOffPathOrch(t)
	ctx := context.Background()

	answer, err := orch.AskOffPath(ctx, sid, "anything")
	require.NoError(t, err)
	require.False(t, strings.Contains(answer, "system=["),
		"no persona was configured but --append-system-prompt leaked into the call: %q", answer)
}

// TestAskOffPath_AgentRefReachesOracle asserts the generalised path: when
// OffPathDef.Agent names an entry in AppDef.Agents (instead of OffPathDef.
// Persona being set inline), AskOffPath resolves the agent and threads its
// SystemPrompt through host.oracle.talk via the new agents-context shim.
// This proves the new primitive round-trips the same way the back-compat
// Persona shortcut does.
func TestAskOffPath_AgentRefReachesOracle(t *testing.T) {
	t.Setenv(host.OracleBinEnv, fakeOraclePath(t))

	const agentSystemPrompt = "speak like a wise frontier guide"

	def := minimalOffPathApp()
	def.OffPath = &app.OffPathDef{
		Trigger: "/freeform",
		Banner:  "*** off-trail ***",
		Return:  "/onpath",
		Agent:   "frontier_guide",
	}
	def.Agents = map[string]*app.AgentDecl{
		"frontier_guide": {SystemPrompt: agentSystemPrompt},
	}

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	rawChatStore, err := chats.NewStore(s.DB())
	require.NoError(t, err)

	orch := orchestrator.New(def, m, s, noopHarness{},
		orchestrator.WithChatStore(chathost.NewAdapter(rawChatStore)),
	)

	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	answer, err := orch.AskOffPath(ctx, sid, "where to camp?")
	require.NoError(t, err)
	require.Contains(t, answer, "system=["+agentSystemPrompt+"]",
		"agent-resolved system_prompt must reach the oracle binary: %q", answer)
}

// TestAskOffPath_PersonaWinsOverAgent asserts the priority rule: when both
// Persona and Agent are set on OffPathDef, the inline Persona wins (it's
// the back-compat shortcut and stays authoritative when present).
func TestAskOffPath_PersonaWinsOverAgent(t *testing.T) {
	t.Setenv(host.OracleBinEnv, fakeOraclePath(t))

	const inlinePersona = "INLINE wins"
	const agentPrompt = "agent LOSES"

	def := minimalOffPathApp()
	def.OffPath = &app.OffPathDef{
		Trigger: "/freeform",
		Banner:  "*** off-trail ***",
		Return:  "/onpath",
		Persona: inlinePersona,
		Agent:   "frontier_guide",
	}
	def.Agents = map[string]*app.AgentDecl{
		"frontier_guide": {SystemPrompt: agentPrompt},
	}

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	rawChatStore, err := chats.NewStore(s.DB())
	require.NoError(t, err)

	orch := orchestrator.New(def, m, s, noopHarness{},
		orchestrator.WithChatStore(chathost.NewAdapter(rawChatStore)),
	)

	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	answer, err := orch.AskOffPath(ctx, sid, "anything")
	require.NoError(t, err)
	require.Contains(t, answer, "system=["+inlinePersona+"]")
	require.NotContains(t, answer, agentPrompt)
}
