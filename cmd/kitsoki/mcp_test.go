package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMCPAttachEntry_RoundTrips covers slice task 2.4: the emitted .mcp.json
// entry resolves command/args to this binary and round-trips through JSON. It
// mirrors the writeMCPConfigTempfile shape: {"mcpServers":{"kitsoki":{"command":
// ...,"args":["mcp","--stories-dir",...]}}}.
func TestMCPAttachEntry_RoundTrips(t *testing.T) {
	b, err := mcpAttachJSON("/usr/local/bin/kitsoki", "/work/stories")
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got), "emitted entry must be valid JSON")

	servers, ok := got["mcpServers"].(map[string]any)
	require.True(t, ok, "mcpServers object present")
	entry, ok := servers["kitsoki"].(map[string]any)
	require.True(t, ok, "kitsoki server entry present")

	assert.Equal(t, "/usr/local/bin/kitsoki", entry["command"], "command resolves to this binary")

	args, ok := entry["args"].([]any)
	require.True(t, ok, "args is an array")
	assert.Equal(t, []any{"mcp", "--stories-dir", "/work/stories"}, args,
		"args invoke `mcp` with the stories dir")
}

// TestMCPAttachEntry_OmitsEmptyStoriesDir confirms the --stories-dir flag is
// dropped when no dir is given (the server resolves stories elsewhere).
func TestMCPAttachEntry_OmitsEmptyStoriesDir(t *testing.T) {
	entry := mcpAttachEntry("kitsoki", "")
	servers := entry["mcpServers"].(map[string]any)
	kit := servers["kitsoki"].(map[string]any)
	assert.Equal(t, []any{"mcp"}, kit["args"], "no --stories-dir when dir is empty")
}

// TestMCPCmd_Registered confirms `mcp` is wired into the root command tree and
// declares its documented flags (--stories-dir, --db, --harness, --workspace)
// with the no-LLM replay default.
func TestMCPCmd_Registered(t *testing.T) {
	root := newRootCmd()
	var mcp *cobraCommandStub
	for _, c := range root.Commands() {
		if c.Name() == "mcp" {
			mcp = &cobraCommandStub{}
			// Verify the documented flags exist.
			for _, f := range []string{"stories-dir", "db", "harness", "workspace"} {
				require.NotNil(t, c.Flags().Lookup(f), "mcp must declare --%s", f)
			}
			// No-LLM default: --harness defaults to replay.
			h := c.Flags().Lookup("harness")
			require.NotNil(t, h)
			assert.Equal(t, "replay", h.DefValue, "--harness defaults to the no-LLM replay harness")
			break
		}
	}
	require.NotNil(t, mcp, "root command tree must include `mcp`")
}

// cobraCommandStub is a presence marker for the registration test (the real
// *cobra.Command is exercised directly above).
type cobraCommandStub struct{}
