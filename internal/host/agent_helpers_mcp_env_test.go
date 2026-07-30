package host

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriteMCPConfigTempfileExpandsEnvironment(t *testing.T) {
	t.Setenv("MCP_BRIDGE_TEST_TOKEN", "paired-value")
	path, cleanup, err := writeMCPConfigTempfile(map[string]any{
		"bridge": map[string]any{
			"command": "node",
			"args":    []any{"server.mjs", "--pairing-code", "${MCP_BRIDGE_TEST_TOKEN}"},
			"env":     map[string]any{"BRIDGE_TOKEN": "prefix-${MCP_BRIDGE_TEST_TOKEN}"},
		},
	}, "kitsoki-mcp-env-test")
	require.NoError(t, err)
	defer cleanup()

	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var config struct {
		MCPServers map[string]struct {
			Args []string          `json:"args"`
			Env  map[string]string `json:"env"`
		} `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal(raw, &config))
	require.Equal(t, "paired-value", config.MCPServers["bridge"].Args[2])
	require.Equal(t, "prefix-paired-value", config.MCPServers["bridge"].Env["BRIDGE_TOKEN"])
}

func TestWriteMCPConfigTempfileRejectsMissingEnvironment(t *testing.T) {
	_, cleanup, err := writeMCPConfigTempfile(map[string]any{
		"bridge": map[string]any{"args": []any{"${UNSET_MCP_BRIDGE_TEST_TOKEN}"}},
	}, "kitsoki-mcp-env-test")
	require.Error(t, err)
	require.Nil(t, cleanup)
	require.Contains(t, err.Error(), "UNSET_MCP_BRIDGE_TEST_TOKEN")
}

func TestExpandMCPServerEnvironmentPreservesUnmatchedToken(t *testing.T) {
	expanded, missing := ExpandMCPServerEnvironment(map[string]any{
		"bridge": map[string]any{"args": []any{"literal-${UNFINISHED"}},
	})
	require.Empty(t, missing)
	args := expanded["bridge"].(map[string]any)["args"].([]any)
	require.Equal(t, "literal-${UNFINISHED", args[0])
}
