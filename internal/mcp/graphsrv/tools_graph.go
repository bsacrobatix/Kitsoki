package graphsrv

import (
	"context"
	"encoding/json"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerGraphTools registers the seven-tool read family (plan §3.3)
// against srv, closing over deps. mode gates future write-tool
// registration (P4) — today it's a no-op since no write tools exist yet,
// but the parameter and the switch below are the forward-compatible gating
// point P4 hangs propose/authorize/apply/withdraw/changeset-mutate off of.
//
// This function (and registerFeedbackTools) is deliberately a free function
// taking (srv, deps, mode) rather than a Server method, so P6's studio
// server can call it directly against its own mcpsdk.Server + Deps without
// depending on this package's Server/NewServer/cobra plumbing.
func registerGraphTools(srv *mcpsdk.Server, deps *Deps, mode string) {
	registerGraphLintTool(srv, deps)

	// TODO(P2 next step): graph.open — new host.graph.open engine op,
	// catalog overview (head/rev/dirty, node_count, per-type census,
	// edge vocabulary, lint summary, changeset lifecycle counts, feedback
	// pending count, guide string). Budget: ≤2KB (BudgetGraphOpen).
	// TODO(P2 next step): graph.get — wraps host.graph.get. missing[]
	// nearest-id suggestions via internal/graph/neighbors.go's
	// NearestIDs. Per-field 2KB cap (BudgetGraphGetField), single-field
	// refetch 32KB cap (BudgetGraphGetSingle), ≤24KB/call overall
	// (BudgetGraphGetTotal).
	// TODO(P2 next step): graph.find — wraps host.graph.find; add cursor
	// support (offset + filter-hash + catalog-hash) on top of P1's
	// limit/offset; catalog_changed:true if the catalog hash moved since
	// the cursor was minted. ≤8KB/page (BudgetGraphFindPage).
	// TODO(P2 next step): graph.neighbors — wraps host.graph.neighbors.
	// ≤10KB (BudgetGraphNeighbors).
	// TODO(P2 next step): graph.type — wraps host.graph.query
	// (mode:"explain-type"); one-line type list when type_id omitted.
	// TODO(P2 next step): graph.impact — wraps host.graph.query
	// (mode:"impact"); description notes "Call before retype/remove;
	// propose will reject what this predicts."

	switch mode {
	case ModeRead, ModePropose, ModeSteward:
		// No write tools exist in P2 in any mode. P4 adds a branch here
		// that skips propose/authorize/apply/withdraw/changeset-mutate
		// registration when mode == ModeRead.
	}
}

// registerFeedbackTools registers feedback.report and feedback.list (plan
// §3.6, local sink only). Like registerGraphTools, this is a free function
// so P6's studio mount can reuse it directly.
func registerFeedbackTools(srv *mcpsdk.Server, deps *Deps) {
	// TODO(P2 next step): feedback.report — local JSONL + markdown bundle
	// under <bound-catalog-repo-root>/.artifacts/graph-mcp/ (git
	// toplevel walk from the catalog path, NOT process cwd — see plan's
	// red-team amendment #7). ULID report_id (check go.mod for an
	// existing dependency before adding github.com/oklog/ulid/v2).
	// (kind, normalized-title) server-side dedupe. Always returns
	// ok:true; sink failures surface as routing_errors, never a tool
	// error.
	// TODO(P2 next step): feedback.list — {limit?, kind?} listing from
	// the same local sink.
	_ = srv
	_ = deps
}

const graphLintInputSchema = `{
  "type": "object",
  "properties": {
    "catalog": {
      "type": "string",
      "description": "Bound catalog alias (omit to use the default catalog)."
    },
    "max": {
      "type": "integer",
      "minimum": 1,
      "description": "Maximum number of issues to return (default 50)."
    }
  },
  "additionalProperties": false
}`

// graphLintArgs is the input to graph.lint.
type graphLintArgs struct {
	Catalog string `json:"catalog,omitempty"`
	Max     int    `json:"max,omitempty"`
}

// graphLintOK is graph.lint's successful result shape: {ok, clean,
// issue_count, issues}.
type graphLintOK struct {
	OK         bool   `json:"ok"`
	Catalog    string `json:"catalog"`
	Clean      bool   `json:"clean"`
	IssueCount int    `json:"issue_count"`
	Issues     []any  `json:"issues"`
	Truncated  bool   `json:"truncated,omitempty"`
}

const graphLintDefaultMax = 50

// registerGraphLintTool wires graph.lint, the one read tool built end-to-end
// in the P2 foundation step (chosen as the simplest wrapper: it's a direct
// pass-through of the pre-existing host.graph.lint op with no
// catalog-overview composition and no cursor/truncation-budget math beyond
// a flat issue-count cap).
func registerGraphLintTool(srv *mcpsdk.Server, deps *Deps) {
	srv.AddTool(&mcpsdk.Tool{
		Name:        "graph.lint",
		Description: "Report catalog lint issues (dangling edges, missing required fields, etc). Wraps the existing lint engine op.",
		InputSchema: json.RawMessage(graphLintInputSchema),
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return handleGraphLint(ctx, deps, req)
	})
}

func handleGraphLint(ctx context.Context, deps *Deps, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	var args graphLintArgs
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return errorResult(NewError(CodeValidation, "graph.lint: arguments are not valid JSON: "+err.Error(), "")), nil
		}
	}

	path, alias, errPayload := deps.Catalogs.Resolve(args.Catalog)
	if errPayload != nil {
		return errorResult(errPayload), nil
	}

	res, err := deps.Registry.Invoke(ctx, "host.graph.lint", map[string]any{"catalog_path": path})
	if err != nil {
		return errorResult(NewError(CodeValidation, "graph.lint: "+err.Error(), "check that the bound catalog path is a valid catalog file or bundle")), nil
	}
	if res.Error != "" {
		return errorResult(NewError(CodeValidation, "graph.lint: "+res.Error, "")), nil
	}

	issues, _ := res.Data["issues"].([]any)
	max := args.Max
	if max <= 0 {
		max = graphLintDefaultMax
	}
	kept, truncated := TruncateSlice(issues, max)

	clean, _ := res.Data["clean"].(bool)
	issueCount, _ := res.Data["issue_count"].(int)

	out := graphLintOK{
		OK:         true,
		Catalog:    alias,
		Clean:      clean,
		IssueCount: issueCount,
		Issues:     kept,
		Truncated:  truncated,
	}
	return okResult(out), nil
}
