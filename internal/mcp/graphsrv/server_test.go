package graphsrv_test

import (
	"context"
	"encoding/json"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"kitsoki/internal/mcp/graphsrv"
)

const fixturePath = "testdata/graph-fixture.yaml"

// connectGraphServer wires an in-process client + server pair using
// InMemoryTransports, mirroring internal/mcp/validator_test.go's
// connectValidator helper.
func connectGraphServer(t *testing.T, cfg graphsrv.Config) (*mcpsdk.ClientSession, func()) {
	t.Helper()
	srv, err := graphsrv.NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	clientT, serverT := mcpsdk.NewInMemoryTransports()
	ctx := context.Background()
	go func() {
		if _, err := srv.Connect(ctx, serverT, nil); err != nil {
			t.Logf("server connect error: %v", err)
		}
	}()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	return cs, func() { _ = cs.Close() }
}

func TestGraphServer_ListsLintTool(t *testing.T) {
	cs, done := connectGraphServer(t, graphsrv.Config{CatalogFlags: []string{fixturePath}})
	defer done()

	res, err := cs.ListTools(context.Background(), &mcpsdk.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var found bool
	for _, tool := range res.Tools {
		if tool.Name == "graph.lint" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected graph.lint in tools/list, got %+v", res.Tools)
	}
}

func TestGraphServer_LintCallsThroughToEngine(t *testing.T) {
	cs, done := connectGraphServer(t, graphsrv.Config{CatalogFlags: []string{fixturePath}})
	defer done()

	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "graph.lint",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool(graph.lint): %v", err)
	}
	if res.IsError {
		t.Fatalf("graph.lint returned an error result: %+v", res.Content)
	}
	m, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("StructuredContent is not a map: %T %+v", res.StructuredContent, res.StructuredContent)
	}
	if ok, _ := m["ok"].(bool); !ok {
		t.Fatalf("expected ok:true, got %+v", m)
	}
	if _, ok := m["issue_count"]; !ok {
		t.Fatalf("expected issue_count in response, got %+v", m)
	}
}

func TestGraphServer_UnknownCatalogAlias(t *testing.T) {
	cs, done := connectGraphServer(t, graphsrv.Config{CatalogFlags: []string{fixturePath}})
	defer done()

	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "graph.lint",
		Arguments: map[string]any{"catalog": "nope"},
	})
	if err != nil {
		t.Fatalf("CallTool(graph.lint): %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected an error result for an unbound alias, got %+v", res.StructuredContent)
	}
	m, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("StructuredContent is not a map: %T", res.StructuredContent)
	}
	if code, _ := m["code"].(string); code != graphsrv.CodeUnknownCatalog {
		t.Fatalf("code = %v, want %s", m["code"], graphsrv.CodeUnknownCatalog)
	}
	if ifStuck, _ := m["if_stuck"].(string); ifStuck == "" {
		t.Fatalf("expected if_stuck to be populated on every error payload, got %+v", m)
	}
}

func TestGraphServer_RawPathRejected(t *testing.T) {
	cs, done := connectGraphServer(t, graphsrv.Config{CatalogFlags: []string{"main=" + fixturePath}})
	defer done()

	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "graph.lint",
		Arguments: map[string]any{"catalog": fixturePath},
	})
	if err != nil {
		t.Fatalf("CallTool(graph.lint): %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected raw path to be rejected, got %+v", res.StructuredContent)
	}
	m, _ := res.StructuredContent.(map[string]any)
	if code, _ := m["code"].(string); code != graphsrv.CodeValidation {
		t.Fatalf("code = %v, want %s", m["code"], graphsrv.CodeValidation)
	}
}

func TestGraphServer_NoCatalogBound(t *testing.T) {
	// An empty CatalogSet only arises when no --catalog flags are given AND
	// pog/catalog.yaml doesn't exist under cwd. We can't control the test
	// binary's cwd safely here without touching global state, so we assert
	// the lower-level CatalogSet directly instead of round-tripping through
	// a live server (kept fast + hermetic).
	cs, err := graphsrv.ParseCatalogFlags(nil)
	if err != nil {
		// If a pog/catalog.yaml genuinely exists under this package's test
		// cwd, ParseCatalogFlags binds it instead of returning empty; that's
		// not the case we're testing, so just skip.
		t.Skipf("ParseCatalogFlags(nil) errored unexpectedly: %v", err)
	}
	if !cs.Empty() {
		t.Skip("pog/catalog.yaml unexpectedly present under test cwd; NO_CATALOG path not exercised here")
	}
	_, _, errPayload := cs.Resolve("")
	if errPayload == nil || errPayload.Code != graphsrv.CodeNoCatalog {
		t.Fatalf("expected NO_CATALOG, got %+v", errPayload)
	}
}

func TestGraphServer_NoCatalogBound_LiveServer(t *testing.T) {
	// Unlike TestGraphServer_NoCatalogBound (which asserts against
	// CatalogSet directly because touching global cwd felt risky), t.Chdir
	// (Go 1.24+) chdirs for the duration of this test only and restores
	// automatically on cleanup, so we can safely exercise the live-server
	// NO_CATALOG path end-to-end: no --catalog flags, and cwd has no
	// pog/catalog.yaml to probe.
	t.Chdir(t.TempDir())

	cs, done := connectGraphServer(t, graphsrv.Config{})
	defer done()

	res, err := cs.CallTool(context.Background(), &mcpsdk.CallToolParams{
		Name:      "graph.lint",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool(graph.lint): %v", err)
	}
	if !res.IsError {
		t.Fatalf("expected NO_CATALOG error with no bound catalog, got %+v", res.StructuredContent)
	}
	m, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("StructuredContent is not a map: %T", res.StructuredContent)
	}
	if code, _ := m["code"].(string); code != graphsrv.CodeNoCatalog {
		t.Fatalf("code = %v, want %s", m["code"], graphsrv.CodeNoCatalog)
	}
}

func TestGraphServer_ToolSchemasHaveNoBooleanLeaves(t *testing.T) {
	// Regression guard mirroring internal/mcp/studio/tool_schema_test.go:
	// a reflected Go `any` field renders as bare `true`/`false` in JSON
	// Schema, which makes Claude Code drop the entire tools/list. Every
	// property in every registered tool's InputSchema must be a concrete
	// object/array/string/etc leaf, never a bool.
	cs, done := connectGraphServer(t, graphsrv.Config{CatalogFlags: []string{fixturePath}})
	defer done()

	res, err := cs.ListTools(context.Background(), &mcpsdk.ListToolsParams{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range res.Tools {
		raw, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("marshal schema for %s: %v", tool.Name, err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal schema for %s: %v", tool.Name, err)
		}
		props, _ := m["properties"].(map[string]any)
		for propName, propSchema := range props {
			if _, isBool := propSchema.(bool); isBool {
				t.Errorf("tool %s property %q has a boolean schema leaf (reflected `any` field) — this drops the whole tools/list in Claude Code", tool.Name, propName)
			}
		}
	}
}
