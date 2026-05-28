package app

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOffPath_AgentSlot asserts that off_path.agent: loads onto
// OffPathDef.Agent when the referenced agent exists. The runtime that
// consumes this slot is not yet wired (orchestrator.ModeOffPath is
// stubbed), but the YAML surface ships now so authors can declare it.
func TestOffPath_AgentSlot(t *testing.T) {
	yaml := `app:
  id: offpath-agent
  version: 0.1.0
root: foyer
states:
  foyer:
    view: "Foyer."
agents:
  my-agent:
    system_prompt: "fixture"
off_path:
  trigger: help
  banner: "*** off path ***"
  agent: my-agent
`
	def, err := LoadBytes([]byte(yaml))
	require.NoError(t, err)
	require.NotNil(t, def.OffPath)
	require.Equal(t, "help", def.OffPath.Trigger)
	require.Equal(t, "my-agent", def.OffPath.Agent)
}

// TestOffPath_NoAgent asserts that off_path.agent is optional: an
// off_path block with no agent: field loads cleanly and OffPathDef.Agent
// is the empty string (so the cross-reference validator skips it).
func TestOffPath_NoAgent(t *testing.T) {
	yaml := `app:
  id: offpath-no-agent
  version: 0.1.0
root: foyer
states:
  foyer:
    view: "Foyer."
off_path:
  trigger: help
`
	def, err := LoadBytes([]byte(yaml))
	require.NoError(t, err)
	require.NotNil(t, def.OffPath)
	require.Equal(t, "", def.OffPath.Agent)
}
