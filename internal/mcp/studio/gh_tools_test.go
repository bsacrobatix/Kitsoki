package studio_test

import (
	"context"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gh_tools_test.go — verification for the gh.* surface. The happy paths call the
// real `gh` CLI (auth + network), so they are NOT exercised here; these tests
// pin the argument-validation guards that short-circuit BEFORE any gh
// invocation, which is the deterministic, offline-safe contract.

func TestGH_ArgValidation(t *testing.T) {
	ctx := context.Background()
	cs := newStudioNoWorkspace(ctx, t)

	cases := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"pr_view requires number", "gh.pr_view", map[string]any{}},
		{"comment requires number", "gh.comment", map[string]any{"body": "hi"}},
		{"comment requires body", "gh.comment", map[string]any{"number": 5}},
		{"comment rejects bad on", "gh.comment", map[string]any{"number": 5, "body": "hi", "on": "branch"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := callTool(ctx, cs, tc.tool, tc.args)
			assertRejected(t, res, err)
		})
	}
}

// TestGH_ReadOnlyOmitsComment confirms the mutating gh.comment is dropped from a
// read-only server while the read tools stay registered.
func TestGH_ReadOnlyOmitsComment(t *testing.T) {
	ctx := context.Background()
	cs := newStudioReadOnly(ctx, t)

	tools, err := cs.ListTools(ctx, &mcpsdk.ListToolsParams{})
	require.NoError(t, err)
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	assert.True(t, names["gh.issues"], "read tool gh.issues should be present")
	assert.True(t, names["gh.pr_view"], "read tool gh.pr_view should be present")
	assert.False(t, names["gh.comment"], "mutating gh.comment must be omitted read-only")
	// Spot-check the other families' read/write split too.
	assert.True(t, names["vcs.status"], "read tool vcs.status should be present")
	assert.False(t, names["vcs.integrate"], "mutating vcs.integrate must be omitted read-only")
	assert.True(t, names["trace.read"], "read tool trace.read should be present")
	assert.False(t, names["trace.to_flow"], "mutating trace.to_flow must be omitted read-only")
	assert.False(t, names["story.turn"], "story.turn must be omitted read-only")
}
