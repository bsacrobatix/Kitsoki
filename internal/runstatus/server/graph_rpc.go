// graph_rpc.go — the bare "graph.propose"/"graph.authorize" JSON-RPC methods
// (A1, use-case-loop-plan §3.3): thin wire adapters over
// internal/graph.Propose/Authorize, wired directly into Server.dispatch
// rather than through internal/kitendpoint (see server.go's dispatch-switch
// comment on the "graph.*" case for why).
package server

import (
	objectgraph "kitsoki/internal/graph"
)

func graphStringParam(params map[string]any, key string) string {
	s, _ := params[key].(string)
	return s
}

func graphMapParam(params map[string]any, key string) map[string]any {
	m, _ := params[key].(map[string]any)
	return m
}

func graphBoolParam(params map[string]any, key string) bool {
	b, _ := params[key].(bool)
	return b
}

// graphProposeRPC: {catalog_path, title, operations[, visibility, provenance]}
// -> {changeset_id, status, lint, rejected, reject_reasons}. See
// internal/host/graph_handlers.go's graphProposeOp for the sibling
// host.graph.propose adapter this mirrors (kept in sync deliberately —
// same wire shape, different transport).
func graphProposeRPC(params map[string]any) (any, *rpcError) {
	catalogPath := graphStringParam(params, "catalog_path")
	if catalogPath == "" {
		return nil, &rpcError{Code: codeServerError, Message: "graph.propose: missing 'catalog_path'"}
	}
	rawOps, _ := params["operations"].([]any)
	ops := make([]map[string]any, 0, len(rawOps))
	for _, r := range rawOps {
		if m, ok := r.(map[string]any); ok {
			ops = append(ops, m)
		}
	}
	res, err := objectgraph.Propose(catalogPath, objectgraph.ProposeInput{
		Title:      graphStringParam(params, "title"),
		Visibility: graphStringParam(params, "visibility"),
		Operations: ops,
		Provenance: graphMapParam(params, "provenance"),
	})
	if err != nil {
		return nil, &rpcError{Code: codeServerError, Message: "graph.propose: " + err.Error()}
	}
	lint := make([]any, len(res.Lint))
	for i, iss := range res.Lint {
		lint[i] = iss.Error()
	}
	rejectReasons := make([]any, len(res.RejectReasons))
	for i, r := range res.RejectReasons {
		rejectReasons[i] = r
	}
	return map[string]any{
		"changeset_id":   string(res.ChangesetID),
		"status":         res.Status,
		"lint":           lint,
		"rejected":       len(res.RejectReasons) > 0 || len(res.Lint) > 0,
		"reject_reasons": rejectReasons,
	}, nil
}

// graphAuthorizeRPC: {catalog_path, changeset_id} -> {rejected,
// reject_reasons, lint_issues, changed_files} — the same result shape
// graph.apply/host.graph.apply already use, for a consistent client
// contract across the two lifecycle-writing ops.
func graphAuthorizeRPC(params map[string]any) (any, *rpcError) {
	catalogPath := graphStringParam(params, "catalog_path")
	changesetID := graphStringParam(params, "changeset_id")
	if catalogPath == "" || changesetID == "" {
		return nil, &rpcError{Code: codeServerError, Message: "graph.authorize: requires both 'catalog_path' and 'changeset_id'"}
	}
	res, err := objectgraph.Authorize(catalogPath, objectgraph.NodeID(changesetID))
	if err != nil {
		return nil, &rpcError{Code: codeServerError, Message: "graph.authorize: " + err.Error()}
	}
	rejectReasons := make([]any, len(res.RejectReasons))
	for i, r := range res.RejectReasons {
		rejectReasons[i] = r
	}
	lintIssues := make([]any, len(res.LintIssues))
	for i, iss := range res.LintIssues {
		lintIssues[i] = iss.Error()
	}
	changedFiles := make([]any, len(res.ChangedFiles))
	for i, f := range res.ChangedFiles {
		changedFiles[i] = f
	}
	return map[string]any{
		"rejected":       res.Rejected(),
		"reject_reasons": rejectReasons,
		"lint_issues":    lintIssues,
		"changed_files":  changedFiles,
	}, nil
}

// graphWithdrawRPC: {catalog_path, changeset_id} -> the bare sibling of
// graphAuthorizeRPC for the review queue's "withdraw" action
// (internal/graph.Withdraw).
func graphWithdrawRPC(params map[string]any) (any, *rpcError) {
	catalogPath := graphStringParam(params, "catalog_path")
	changesetID := graphStringParam(params, "changeset_id")
	if catalogPath == "" || changesetID == "" {
		return nil, &rpcError{Code: codeServerError, Message: "graph.withdraw: requires both 'catalog_path' and 'changeset_id'"}
	}
	res, err := objectgraph.Withdraw(catalogPath, objectgraph.NodeID(changesetID))
	if err != nil {
		return nil, &rpcError{Code: codeServerError, Message: "graph.withdraw: " + err.Error()}
	}
	rejectReasons := make([]any, len(res.RejectReasons))
	for i, r := range res.RejectReasons {
		rejectReasons[i] = r
	}
	lintIssues := make([]any, len(res.LintIssues))
	for i, iss := range res.LintIssues {
		lintIssues[i] = iss.Error()
	}
	changedFiles := make([]any, len(res.ChangedFiles))
	for i, f := range res.ChangedFiles {
		changedFiles[i] = f
	}
	return map[string]any{
		"rejected":       res.Rejected(),
		"reject_reasons": rejectReasons,
		"lint_issues":    lintIssues,
		"changed_files":  changedFiles,
	}, nil
}

// graphRebaseRPC: {catalog_path, changeset_id} -> the bare sibling of
// graphWithdrawRPC for the review queue's "rebase" action
// (internal/graph.Rebase) — refreshes non-conflicting stale Before guards on
// a proposed changeset and re-validates, per §3.3.
func graphRebaseRPC(params map[string]any) (any, *rpcError) {
	catalogPath := graphStringParam(params, "catalog_path")
	changesetID := graphStringParam(params, "changeset_id")
	if catalogPath == "" || changesetID == "" {
		return nil, &rpcError{Code: codeServerError, Message: "graph.rebase: requires both 'catalog_path' and 'changeset_id'"}
	}
	res, err := objectgraph.Rebase(catalogPath, objectgraph.NodeID(changesetID))
	if err != nil {
		return nil, &rpcError{Code: codeServerError, Message: "graph.rebase: " + err.Error()}
	}
	rejectReasons := make([]any, len(res.RejectReasons))
	for i, r := range res.RejectReasons {
		rejectReasons[i] = r
	}
	lintIssues := make([]any, len(res.LintIssues))
	for i, iss := range res.LintIssues {
		lintIssues[i] = iss.Error()
	}
	changedFiles := make([]any, len(res.ChangedFiles))
	for i, f := range res.ChangedFiles {
		changedFiles[i] = f
	}
	return map[string]any{
		"rejected":       res.Rejected(),
		"reject_reasons": rejectReasons,
		"lint_issues":    lintIssues,
		"changed_files":  changedFiles,
	}, nil
}

// graphApplyRPC: {catalog_path, changeset_id[, dry_run]} -> the bare
// "graph.apply" sibling of graph.propose/graph.authorize, so a portal review
// queue can re-validate (dry_run) and commit an authorized changeset without
// going through the kit dispatch surface — same wire shape as
// host.graph.apply (internal/host/graph_handlers.go's graphApplyOp).
func graphApplyRPC(params map[string]any) (any, *rpcError) {
	catalogPath := graphStringParam(params, "catalog_path")
	changesetID := graphStringParam(params, "changeset_id")
	if catalogPath == "" || changesetID == "" {
		return nil, &rpcError{Code: codeServerError, Message: "graph.apply: requires both 'catalog_path' and 'changeset_id'"}
	}
	res, err := objectgraph.Apply(catalogPath, objectgraph.NodeID(changesetID), graphBoolParam(params, "dry_run"))
	if err != nil {
		return nil, &rpcError{Code: codeServerError, Message: "graph.apply: " + err.Error()}
	}
	rejectReasons := make([]any, len(res.RejectReasons))
	for i, r := range res.RejectReasons {
		rejectReasons[i] = r
	}
	lintIssues := make([]any, len(res.LintIssues))
	for i, iss := range res.LintIssues {
		lintIssues[i] = iss.Error()
	}
	changedFiles := make([]any, len(res.ChangedFiles))
	for i, f := range res.ChangedFiles {
		changedFiles[i] = f
	}
	return map[string]any{
		"rejected":       res.Rejected(),
		"reject_reasons": rejectReasons,
		"lint_issues":    lintIssues,
		"changed_files":  changedFiles,
	}, nil
}
