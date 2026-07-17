// agents_rpc_test.go — the runstatus.agents.list RPC (agent mode).
//
// The catalog is served through the optional [server.AgentLister] provider
// extension, mirroring WorkerProvider's shape: a provider that implements it
// returns the merged agent catalog; one that doesn't yields an empty list
// (never an error) so read-only and legacy surfaces stay untouched.
package server_test

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/runstatus/server"
)

// agentStubProvider extends stubProvider with the AgentLister capability.
type agentStubProvider struct {
	*stubProvider
	agents []server.AgentInfo
}

func (p *agentStubProvider) ListAgents() ([]server.AgentInfo, error) {
	return append([]server.AgentInfo(nil), p.agents...), nil
}

// TestMulti_AgentsList proves agents.list round-trips the provider's catalog
// rows — including the ready-to-use story_path the SPA feeds straight into
// session.new — over the wire unchanged.
func TestMulti_AgentsList(t *testing.T) {
	t.Parallel()
	p := &agentStubProvider{
		stubProvider: newStubProvider(),
		agents: []server.AgentInfo{
			{
				Name:        "demo",
				Source:      "project",
				Description: "Demo read-only agent",
				Effect:      "read",
				StoryPath:   "agent:demo",
			},
			{
				Name:      "kitsoki-engineer",
				Source:    "builtin",
				Effect:    "write",
				Shadows:   []string{"library"},
				StoryPath: "agent:kitsoki-engineer",
			},
		},
	}
	ts := httptest.NewServer(server.NewMulti(p).Handler())
	defer ts.Close()

	var rows []server.AgentInfo
	rpcCall(t, ts, "runstatus.agents.list", map[string]any{}, &rows)
	require.Len(t, rows, 2)
	assert.Equal(t, "demo", rows[0].Name)
	assert.Equal(t, "project", rows[0].Source)
	assert.Equal(t, "read", rows[0].Effect)
	assert.Equal(t, "agent:demo", rows[0].StoryPath)
	assert.Equal(t, []string{"library"}, rows[1].Shadows)
}

// TestMulti_AgentsListWithoutProviderIsEmpty proves a provider without the
// AgentLister extension answers with an empty catalog, not an error.
func TestMulti_AgentsListWithoutProviderIsEmpty(t *testing.T) {
	t.Parallel()
	p := newStubProvider()
	ts := httptest.NewServer(server.NewMulti(p).Handler())
	defer ts.Close()

	var rows []server.AgentInfo
	rpcCall(t, ts, "runstatus.agents.list", map[string]any{}, &rows)
	assert.Empty(t, rows)
}
