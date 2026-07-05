package server_test

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	appgraph "kitsoki/internal/app/graph"
	"kitsoki/internal/runstatus/server"
)

func newObjectGraphServer(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(server.NewMulti(newStubProvider()).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func TestObjectGraph_LoadSeedCatalog(t *testing.T) {
	ts := newObjectGraphServer(t)
	var out appgraph.KitsokiGraph
	rpcCall(t, ts, "runstatus.objectgraph.load", map[string]any{
		"catalog_path": "../../../docs/proposals/project-object-graph/seed-objects.yaml",
	}, &out)

	assert.Equal(t, appgraph.SchemaV1, out.Schema)
	assert.Equal(t, "object-graph", out.Kind)
	assert.True(t, out.Directed)
	require.NotEmpty(t, out.Nodes)
	require.NotEmpty(t, out.Edges)

	found := false
	for _, n := range out.Nodes {
		if n.ID == "feature-operator-ask" {
			found = true
			assert.Equal(t, "feature", n.Kind)
			assert.Equal(t, "Operator questions forwarded from headless agents", n.Label)
			assert.Equal(t, "shipped", n.Status)
			assert.Equal(t, "public", n.Attrs["visibility"])
		}
	}
	assert.True(t, found, "expected feature-operator-ask among the loaded nodes")
}

func TestObjectGraph_LoadMissingCatalogPathErrors(t *testing.T) {
	ts := newObjectGraphServer(t)
	code, msg := rpcCallExpectError(t, ts, "runstatus.objectgraph.load", map[string]any{})
	assert.NotEqual(t, 0, code)
	assert.Contains(t, msg, "catalog_path")
}

func TestObjectGraph_DiffBadgesTheOverlayAddition(t *testing.T) {
	ts := newObjectGraphServer(t)
	var out appgraph.KitsokiGraph
	rpcCall(t, ts, "runstatus.objectgraph.diff", map[string]any{
		"catalog_path": "../../../docs/proposals/project-object-graph/seed-objects.yaml",
		"overlay_path": "../../../docs/proposals/project-object-graph/seed-objects.overlay-ui-declutter.yaml",
	}, &out)

	assert.Equal(t, "object-graph-diff", out.Kind)
	require.NotEmpty(t, out.Nodes)

	found := false
	for _, n := range out.Nodes {
		if n.ID == "evidence-object-graph-ui-persona-review" {
			found = true
			assert.Equal(t, "added", n.Attrs["diff_kind"])
		} else {
			assert.Equal(t, "unchanged", n.Attrs["diff_kind"], "node %s", n.ID)
		}
	}
	assert.True(t, found, "expected the overlay's added evidence node in the diff graph")
}

func TestObjectGraph_DiffMissingOverlayPathErrors(t *testing.T) {
	ts := newObjectGraphServer(t)
	code, msg := rpcCallExpectError(t, ts, "runstatus.objectgraph.diff", map[string]any{
		"catalog_path": "../../../docs/proposals/project-object-graph/seed-objects.yaml",
	})
	assert.NotEqual(t, 0, code)
	assert.Contains(t, msg, "overlay_path")
}
