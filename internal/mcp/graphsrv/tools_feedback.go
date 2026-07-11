package graphsrv

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerFeedbackTools registers feedback.report and feedback.list (plan
// §3.6, local sink only). Like registerGraphTools, this is a free function
// so P6's studio mount can reuse it directly.
func registerFeedbackTools(srv *mcpsdk.Server, deps *Deps) {
	registerFeedbackReportTool(srv, deps)
	registerFeedbackListTool(srv, deps)
}

// ─── feedback.report ───

const feedbackReportInputSchema = `{
  "type": "object",
  "properties": {
    "kind": {
      "type": "string",
      "enum": ["tool_gap", "data_gap", "doc_gap", "bug", "other"],
      "description": "What kind of friction this is. Defaults to tool_gap."
    },
    "severity": {"type": "string", "description": "Free-text severity (e.g. blocking, annoying, minor)."},
    "title": {"type": "string", "description": "Short one-line title. Used (case/whitespace-folded) for dedupe."},
    "goal": {"type": "string", "description": "What you were trying to accomplish."},
    "why_blocked": {"type": "string", "description": "What actually stopped you."},
    "attempted": {
      "type": "array",
      "description": "Tools you tried before filing this.",
      "items": {
        "type": "object",
        "properties": {
          "tool": {"type": "string", "description": "Tool name you called."},
          "args_summary": {"type": "string", "description": "Short human summary of the args (not the raw args)."},
          "result_code": {"type": "string", "description": "What it returned (e.g. ok, UNKNOWN_NODE)."}
        },
        "required": ["tool"],
        "additionalProperties": false
      }
    },
    "expected": {"type": "string", "description": "What you expected to happen instead."},
    "suggested_tool": {"type": "string", "description": "If you know what tool/capability would have unblocked you, name it here."},
    "anchor": {
      "type": "object",
      "description": "What this report is about.",
      "properties": {
        "catalog": {"type": "string", "description": "Bound catalog alias (defaults to the default catalog)."},
        "node": {"type": "string", "description": "Node id this report is about, if any."},
        "changeset": {"type": "string", "description": "Changeset id this report is about, if any."},
        "tool": {"type": "string", "description": "Tool this report is about, if any."}
      },
      "additionalProperties": false
    },
    "extra": {
      "type": "object",
      "description": "Freeform producer-owned extra context, string values only.",
      "additionalProperties": {"type": "string"}
    }
  },
  "required": ["title", "goal", "why_blocked", "expected"],
  "additionalProperties": false
}`

type feedbackReportAttempted struct {
	Tool        string `json:"tool"`
	ArgsSummary string `json:"args_summary,omitempty"`
	ResultCode  string `json:"result_code,omitempty"`
}

type feedbackReportAnchorArgs struct {
	Catalog   string `json:"catalog,omitempty"`
	Node      string `json:"node,omitempty"`
	Changeset string `json:"changeset,omitempty"`
	Tool      string `json:"tool,omitempty"`
}

type feedbackReportArgs struct {
	Kind          string                    `json:"kind,omitempty"`
	Severity      string                    `json:"severity,omitempty"`
	Title         string                    `json:"title"`
	Goal          string                    `json:"goal"`
	WhyBlocked    string                    `json:"why_blocked"`
	Attempted     []feedbackReportAttempted `json:"attempted,omitempty"`
	Expected      string                    `json:"expected"`
	SuggestedTool string                    `json:"suggested_tool,omitempty"`
	Anchor        *feedbackReportAnchorArgs `json:"anchor,omitempty"`
	Extra         map[string]string         `json:"extra,omitempty"`
}

type routingErrorEntry struct {
	Sink  string `json:"sink"`
	Error string `json:"error"`
}

type routedEntry struct {
	Sink string `json:"sink"`
	Ref  string `json:"ref"`
}

// feedbackReportOK is always returned with OK:true — feedback.report is
// contractually non-blocking; sink failures show up here as
// RoutingErrors, never as a tool error.
type feedbackReportOK struct {
	OK            bool                `json:"ok"`
	ReportID      string              `json:"report_id"`
	LocalPath     string              `json:"local_path,omitempty"`
	DuplicateOf   string              `json:"duplicate_of,omitempty"`
	Routed        []routedEntry       `json:"routed"`
	RoutingErrors []routingErrorEntry `json:"routing_errors,omitempty"`
}

const validKindsDefault = "tool_gap"

func registerFeedbackReportTool(srv *mcpsdk.Server, deps *Deps) {
	srv.AddTool(&mcpsdk.Tool{
		Name:        "feedback.report",
		Description: "File a durable, non-blocking report of friction (a tool gap, missing data, a doc gap, or a bug) so it survives after this session ends. Always returns ok:true; sink problems come back as routing_errors, never a tool error.",
		InputSchema: json.RawMessage(feedbackReportInputSchema),
	}, recorded(deps, "feedback.report", func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return handleFeedbackReport(ctx, deps, req)
	}))
}

func handleFeedbackReport(ctx context.Context, deps *Deps, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	var args feedbackReportArgs
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return errorResult(NewError(CodeValidation, "feedback.report: arguments are not valid JSON: "+err.Error(), "")), nil
		}
	}
	if args.Title == "" || args.Goal == "" || args.WhyBlocked == "" || args.Expected == "" {
		return errorResult(NewError(CodeValidation, "feedback.report: `title`, `goal`, `why_blocked`, and `expected` are all required", "")), nil
	}
	kind := args.Kind
	if kind == "" {
		kind = validKindsDefault
	}
	switch kind {
	case "tool_gap", "data_gap", "doc_gap", "bug", "other":
	default:
		return errorResult(NewError(CodeValidation, fmt.Sprintf("feedback.report: `kind` %q is not one of tool_gap|data_gap|doc_gap|bug|other", kind), "")), nil
	}

	now := timeNow(deps)
	reportID := newReportID(now)

	var routingErrors []routingErrorEntry
	var routed []routedEntry

	// --feedback-sink degrade warning: P2 only ever writes locally. If the
	// operator asked for catalog/github, record that the choice degraded
	// rather than silently ignoring it (plan §3.2).
	if deps.FeedbackSink != "" && deps.FeedbackSink != FeedbackSinkLocal {
		routingErrors = append(routingErrors, routingErrorEntry{
			Sink:  deps.FeedbackSink,
			Error: fmt.Sprintf("--feedback-sink=%s is not implemented yet (P6 work); this report was written to the local sink only", deps.FeedbackSink),
		})
	}

	// Resolve the anchor catalog to find the repo root to anchor into.
	// This never falls back to process cwd — see repoRootFor.
	anchorCatalogArg := ""
	var anchor feedbackReportAnchorArgs
	if args.Anchor != nil {
		anchor = *args.Anchor
		anchorCatalogArg = args.Anchor.Catalog
	}
	path, alias, errPayload := deps.Catalogs.Resolve(anchorCatalogArg)
	if errPayload != nil {
		routingErrors = append(routingErrors, routingErrorEntry{Sink: "local", Error: errPayload.Error})
		out := feedbackReportOK{OK: true, ReportID: reportID, Routed: routed, RoutingErrors: routingErrors}
		return okResult(out), nil
	}

	repoRoot, err := repoRootFor(path)
	if err != nil {
		routingErrors = append(routingErrors, routingErrorEntry{Sink: "local", Error: "resolve catalog repo root: " + err.Error()})
		out := feedbackReportOK{OK: true, ReportID: reportID, Routed: routed, RoutingErrors: routingErrors}
		return okResult(out), nil
	}
	sinkDir := feedbackSinkDir(repoRoot)

	dedupeKey := dedupeKeyFor(kind, args.Title)
	if existing, dupErr := findDuplicate(sinkDir, dedupeKey); dupErr == nil && existing != nil {
		out := feedbackReportOK{
			OK:          true,
			ReportID:    reportID,
			DuplicateOf: existing.ReportID,
			Routed:      []routedEntry{{Sink: "local", Ref: existing.ReportID}},
		}
		return okResult(out), nil
	}

	headSHA := ""
	if h, _ := graphHeadSHAFor(deps, ctx, path); h != "" {
		headSHA = h
	}

	attempted := make([]attemptEntry, 0, len(args.Attempted))
	for _, a := range args.Attempted {
		attempted = append(attempted, attemptEntry{Tool: a.Tool, ArgsSummary: a.ArgsSummary, ResultCode: a.ResultCode})
	}

	extra := map[string]any{
		"server_version": feedbackServerVersion,
		"mode":           deps.Mode,
		"actor":          deps.Actor,
	}
	for k, v := range args.Extra {
		extra[k] = v
	}

	ref := anchor.Node
	if ref == "" {
		ref = anchor.Changeset
	}
	step := anchor.Tool

	rec := storedFeedbackReport{
		ReportID:      reportID,
		Producer:      "kitsoki-graph-mcp",
		Kind:          kind,
		Severity:      args.Severity,
		Title:         args.Title,
		Goal:          args.Goal,
		WhyBlocked:    args.WhyBlocked,
		Attempted:     attempted,
		Expected:      args.Expected,
		SuggestedTool: args.SuggestedTool,
		Anchor: feedbackAnchor{
			Producer: "kitsoki-graph-mcp",
			Scope:    fmt.Sprintf("%s@%s", alias, headSHA),
			Step:     step,
			Ref:      ref,
			Extra:    extra,
		},
		Extra:     map[string]any{},
		CreatedAt: now,
		DedupeKey: dedupeKey,
		Evidence:  deps.Recorder.Snapshot(),
	}

	localPath, writeErr := appendFeedbackReport(sinkDir, rec)
	if writeErr != nil {
		routingErrors = append(routingErrors, routingErrorEntry{Sink: "local", Error: writeErr.Error()})
		out := feedbackReportOK{OK: true, ReportID: reportID, Routed: routed, RoutingErrors: routingErrors}
		return okResult(out), nil
	}

	routed = append(routed, routedEntry{Sink: "local", Ref: reportID})
	out := feedbackReportOK{
		OK:            true,
		ReportID:      reportID,
		LocalPath:     localPath,
		Routed:        routed,
		RoutingErrors: routingErrors,
	}
	return okResult(out), nil
}

// graphHeadSHAFor best-effort fetches the bound catalog's head rev via
// host.graph.open, for the anchor.scope "<alias>@<head-sha>" string. Never
// fails the report on error — an empty head sha is acceptable.
func graphHeadSHAFor(deps *Deps, ctx context.Context, path string) (string, error) {
	res, err := deps.Registry.Invoke(ctx, "host.graph.open", map[string]any{"catalog_path": path})
	if err != nil {
		return "", err
	}
	head, _ := res.Data["head"].(map[string]any)
	rev, _ := head["rev"].(string)
	return rev, nil
}

func timeNow(deps *Deps) time.Time {
	if deps.Clock != nil {
		return deps.Clock.Now()
	}
	return time.Now().UTC()
}

// ─── feedback.list ───

const feedbackListInputSchema = `{
  "type": "object",
  "properties": {
    "catalog": {"type": "string", "description": "Bound catalog alias to list feedback for (omit to use the default catalog)."},
    "limit": {"type": "integer", "minimum": 1, "description": "Max reports to return, newest first (default 25)."},
    "kind": {"type": "string", "enum": ["tool_gap", "data_gap", "doc_gap", "bug", "other"], "description": "Restrict to this kind."}
  },
  "additionalProperties": false
}`

type feedbackListArgs struct {
	Catalog string `json:"catalog,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	Kind    string `json:"kind,omitempty"`
}

type feedbackListRow struct {
	ReportID  string `json:"report_id"`
	Kind      string `json:"kind"`
	Severity  string `json:"severity,omitempty"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	LocalPath string `json:"local_path,omitempty"`
}

type feedbackListOK struct {
	OK      bool              `json:"ok"`
	Catalog string            `json:"catalog"`
	Total   int               `json:"total"`
	Reports []feedbackListRow `json:"reports"`
}

func registerFeedbackListTool(srv *mcpsdk.Server, deps *Deps) {
	srv.AddTool(&mcpsdk.Tool{
		Name:        "feedback.list",
		Description: "List previously filed feedback.report submissions for a catalog's local sink, newest first.",
		InputSchema: json.RawMessage(feedbackListInputSchema),
	}, recorded(deps, "feedback.list", func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return handleFeedbackList(ctx, deps, req)
	}))
}

func handleFeedbackList(ctx context.Context, deps *Deps, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
	var args feedbackListArgs
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return errorResult(NewError(CodeValidation, "feedback.list: arguments are not valid JSON: "+err.Error(), "")), nil
		}
	}
	if args.Kind != "" {
		switch args.Kind {
		case "tool_gap", "data_gap", "doc_gap", "bug", "other":
		default:
			return errorResult(NewError(CodeValidation, fmt.Sprintf("feedback.list: `kind` %q is not one of tool_gap|data_gap|doc_gap|bug|other", args.Kind), "")), nil
		}
	}

	path, alias, errPayload := deps.Catalogs.Resolve(args.Catalog)
	if errPayload != nil {
		return errorResult(errPayload), nil
	}

	repoRoot, err := repoRootFor(path)
	if err != nil {
		return errorResult(NewError(CodeValidation, "feedback.list: resolve catalog repo root: "+err.Error(), "")), nil
	}

	limit := args.Limit
	if limit <= 0 {
		limit = 25
	}
	reports, err := listFeedbackReports(feedbackSinkDir(repoRoot), args.Kind, limit)
	if err != nil {
		return errorResult(NewError(CodeValidation, "feedback.list: "+err.Error(), "")), nil
	}
	allForTotal, _ := listFeedbackReports(feedbackSinkDir(repoRoot), args.Kind, 0)

	rows := make([]feedbackListRow, 0, len(reports))
	for _, r := range reports {
		rows = append(rows, feedbackListRow{
			ReportID:  r.ReportID,
			Kind:      r.Kind,
			Severity:  r.Severity,
			Title:     r.Title,
			CreatedAt: r.CreatedAt.Format(time.RFC3339),
		})
	}

	out := feedbackListOK{OK: true, Catalog: alias, Total: len(allForTotal), Reports: rows}
	return okResult(out), nil
}
