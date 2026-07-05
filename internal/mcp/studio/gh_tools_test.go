package studio_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/host"
	studio "kitsoki/internal/mcp/studio"
)

// gh_tools_test.go — verification for the gh.* surface. gh.issues is native and
// covered with a fake GitHub HTTP API; the remaining gh.pr_view/comment happy
// paths call the real `gh` CLI, so these tests pin their offline-safe validation
// guards instead.

func TestGHIssuesUsesNativeTicketProvider(t *testing.T) {
	ctx := context.Background()
	cs := newStudioNoWorkspace(ctx, t)

	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("PATH", t.TempDir())

	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/search/issues", r.URL.Path)
		assert.Equal(t, "2", r.URL.Query().Get("per_page"))
		gotQuery = r.URL.Query().Get("q")
		writeGHToolsJSON(t, w, map[string]any{"items": []map[string]any{{
			"number":    12,
			"title":     "Native issue",
			"state":     "open",
			"html_url":  "https://github.com/acme/repo/issues/12",
			"assignees": []map[string]any{{"login": "brad"}},
			"labels":    []map[string]any{{"name": "bug"}},
		}}})
	}))
	defer srv.Close()
	restoreAPI := host.SetGitHubAPIForTest(srv.URL, srv.Client())
	defer restoreAPI()

	res, err := callTool(ctx, cs, "gh.issues", map[string]any{
		"repo":     "acme/repo",
		"state":    "open",
		"assignee": "@me",
		"search":   "label:bug",
		"limit":    2,
	})
	require.NoError(t, err)
	require.False(t, res.IsError, "gh.issues: %s", contentText(res))
	require.Contains(t, gotQuery, "repo:acme/repo")
	require.Contains(t, gotQuery, "is:issue")
	require.Contains(t, gotQuery, "is:open")
	require.Contains(t, gotQuery, "assignee:@me")
	require.Contains(t, gotQuery, "label:bug")

	var out studio.GHIssuesOK
	require.NoError(t, json.Unmarshal([]byte(contentText(res)), &out))
	require.True(t, out.OK)
	rawIssues, ok := out.Issues.([]any)
	require.True(t, ok, "issues should decode as an array: %#v", out.Issues)
	require.Len(t, rawIssues, 1)
	issue := rawIssues[0].(map[string]any)
	assert.Equal(t, "12", issue["id"])
	assert.Equal(t, "Native issue", issue["title"])
	assert.Equal(t, "github", issue["source"])
}

func writeGHToolsJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(v))
}

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
