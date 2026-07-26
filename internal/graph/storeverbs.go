package graph

// storeverbs.go: the lifecycle verbs routed through the CatalogStore seam —
// ProposeVia/AuthorizeVia/WithdrawVia/RebaseVia/ApplyVia mirror propose.go/
// apply.go's file verbs exactly (same validation order, same guard fills, same
// reject/lint/success ladder, same bounded CAS retry), but Load and Commit go
// through an injected CatalogStore instead of LoadCatalog +
// commitScratchOperations. Over a FileCatalogStore they are behaviorally
// identical to the file verbs (see storeverbs_test.go's parity test); over a
// non-file store (internal/graph/pgcatalog) they are the ONLY write path — a
// pg-backed catalog has no scratch tree to copy.
//
// The file verbs themselves are untouched: Propose(rootPath, ...) and friends
// still call commitScratchOperations directly, keeping the YAML path
// byte-for-byte unchanged. Retiring that duplication is a later phase.

import (
	"context"
	"fmt"
	"time"

	"kitsoki/internal/clock"
)

// ProposeVia is Propose routed through a CatalogStore: validate the payload
// against the store's current catalog, fill guards, mint a changeset id, and
// commit the changeset node through store.Commit under the Load's revision
// token. ValidateOnly maps to CommitOptions.DryRun.
func ProposeVia(ctx context.Context, store CatalogStore, input ProposeInput, actor string, clk clock.Clock) (*ProposeResult, error) {
	if clk == nil {
		clk = clock.Real()
	}
	for attempt := 1; attempt <= casMaxAttempts; attempt++ {
		res, retry, err := proposeViaOnce(ctx, store, input, actor, clk)
		if retry {
			continue
		}
		return res, err
	}
	return &ProposeResult{RejectReasons: []string{
		fmt.Sprintf("CONFLICT: %s changed concurrently during propose, exceeded %d retry attempts", store.Ref(), casMaxAttempts),
	}}, nil
}

func proposeViaOnce(ctx context.Context, store CatalogStore, input ProposeInput, actor string, clk clock.Clock) (res *ProposeResult, retry bool, err error) {
	cat, rev, err := store.Load(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("graph propose: load %s: %w", store.Ref(), err)
	}

	synthetic := &Node{
		ID:     "__propose-candidate__",
		TypeID: "changeset",
		Fields: map[string]any{"operations": rawOpsToAny(input.Operations)},
	}
	cs, err := ParseChangeset(synthetic)
	if err != nil {
		return &ProposeResult{RejectReasons: []string{err.Error()}}, false, nil
	}

	guardFills := fillGuards(cs, cat)

	if reasons := ValidateChangeset(cs, cat); len(reasons) > 0 {
		return &ProposeResult{RejectReasons: reasons}, false, nil
	}

	id, err := nextChangesetID(cat)
	if err != nil {
		return nil, false, fmt.Errorf("graph propose: %w", err)
	}
	visibility := input.Visibility
	if visibility == "" {
		visibility = "internal"
	}
	status := ChangesetStatusProposed
	if input.Provenance != nil && allOpsAutoAuthorizable(cs.Operations, writePolicyRootsSet(cat)) {
		status = ChangesetStatusAuthorized
	}

	filledOps := rawOpsToAny(opsToRaw(cs.Operations))
	nodeMap := map[string]any{
		"schema":     "graph/changeset/v1",
		"id":         string(id),
		"title":      input.Title,
		"status":     status,
		"visibility": visibility,
		"operations": filledOps,
		"created_at": clk.Now().UTC().Format(time.RFC3339),
	}
	if actor != "" {
		nodeMap["authored_by"] = actor
	}
	if input.Provenance != nil {
		nodeMap["provenance"] = input.Provenance
	}

	commit, err := store.Commit(ctx, rev, []Operation{{Kind: OpAdded, Node: id, After: nodeMap}}, CommitOptions{DryRun: input.ValidateOnly})
	if err != nil {
		if IsCASConflict(err) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("graph propose: %w", err)
	}
	if len(commit.RejectReasons) > 0 {
		return &ProposeResult{RejectReasons: commit.RejectReasons}, false, nil
	}
	if len(commit.LintIssues) > 0 && !input.ValidateOnly {
		return &ProposeResult{Lint: commit.LintIssues}, false, nil
	}

	return &ProposeResult{
		ChangesetID:        id,
		Status:             status,
		Lint:               commit.LintIssues,
		GuardFills:         guardFills,
		ValidatedOnly:      input.ValidateOnly,
		Canonicalized:      len(commit.CanonicalizedFiles) > 0,
		CanonicalizedFiles: commit.CanonicalizedFiles,
	}, false, nil
}

// commitResultApply renders a landed (non-conflict) CommitResult as the
// ApplyResult shape the lifecycle flips return — scratchCommit.applyResult's
// exported-seam twin.
func commitResultApply(commit CommitResult, applied bool) *ApplyResult {
	if len(commit.RejectReasons) > 0 {
		return &ApplyResult{RejectReasons: commit.RejectReasons}
	}
	if len(commit.LintIssues) > 0 {
		return &ApplyResult{LintIssues: commit.LintIssues}
	}
	return &ApplyResult{
		Applied:            applied,
		ChangedFiles:       commit.ChangedFiles,
		Canonicalized:      len(commit.CanonicalizedFiles) > 0,
		CanonicalizedFiles: commit.CanonicalizedFiles,
	}
}

// AuthorizeVia is Authorize routed through a CatalogStore.
func AuthorizeVia(ctx context.Context, store CatalogStore, changesetID NodeID, actor string, clk clock.Clock) (*ApplyResult, error) {
	if clk == nil {
		clk = clock.Real()
	}
	for attempt := 1; attempt <= casMaxAttempts; attempt++ {
		res, retry, err := authorizeViaOnce(ctx, store, changesetID, actor, clk)
		if retry {
			continue
		}
		return res, err
	}
	return &ApplyResult{RejectReasons: []string{
		fmt.Sprintf("CONFLICT: %s changed concurrently during authorize, exceeded %d retry attempts", store.Ref(), casMaxAttempts),
	}}, nil
}

func authorizeViaOnce(ctx context.Context, store CatalogStore, changesetID NodeID, actor string, clk clock.Clock) (res *ApplyResult, retry bool, err error) {
	cat, rev, err := store.Load(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("graph authorize: load %s: %w", store.Ref(), err)
	}
	node, ok := cat.Nodes[changesetID]
	if !ok {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("graph authorize: changeset %q not found in catalog", changesetID),
		}}, false, nil
	}
	if node.TypeID != "changeset" {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("graph authorize: node %q is type %q, not changeset", changesetID, node.TypeID),
		}}, false, nil
	}
	if node.Status != ChangesetStatusProposed {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("graph authorize: changeset %q has status %q, must be %q to authorize", changesetID, node.Status, ChangesetStatusProposed),
		}}, false, nil
	}

	changes := []FieldChange{
		{Path: []string{"status"}, Before: ChangesetStatusProposed, After: ChangesetStatusAuthorized},
		{Path: []string{"fields", "authorized_at"}, After: clk.Now().UTC().Format(time.RFC3339)},
	}
	if actor != "" {
		changes = append(changes, FieldChange{Path: []string{"fields", "authorized_by"}, After: actor})
	}
	commit, err := store.Commit(ctx, rev, []Operation{{Kind: OpModified, Node: changesetID, Changes: changes}}, CommitOptions{})
	if err != nil {
		if IsCASConflict(err) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("graph authorize: %w", err)
	}
	return commitResultApply(commit, true), false, nil
}

// WithdrawVia is Withdraw routed through a CatalogStore. actor and clk are
// accepted for seam consistency and unused, exactly like Withdraw.
func WithdrawVia(ctx context.Context, store CatalogStore, changesetID NodeID, actor string, clk clock.Clock) (*ApplyResult, error) {
	_, _ = actor, clk
	for attempt := 1; attempt <= casMaxAttempts; attempt++ {
		res, retry, err := withdrawViaOnce(ctx, store, changesetID)
		if retry {
			continue
		}
		return res, err
	}
	return &ApplyResult{RejectReasons: []string{
		fmt.Sprintf("CONFLICT: %s changed concurrently during withdraw, exceeded %d retry attempts", store.Ref(), casMaxAttempts),
	}}, nil
}

func withdrawViaOnce(ctx context.Context, store CatalogStore, changesetID NodeID) (res *ApplyResult, retry bool, err error) {
	cat, rev, err := store.Load(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("graph withdraw: load %s: %w", store.Ref(), err)
	}
	node, ok := cat.Nodes[changesetID]
	if !ok {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("graph withdraw: changeset %q not found in catalog", changesetID),
		}}, false, nil
	}
	if node.TypeID != "changeset" {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("graph withdraw: node %q is type %q, not changeset", changesetID, node.TypeID),
		}}, false, nil
	}
	if node.Status != ChangesetStatusProposed && node.Status != ChangesetStatusAuthorized {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("graph withdraw: changeset %q has status %q, must be %q or %q to withdraw", changesetID, node.Status, ChangesetStatusProposed, ChangesetStatusAuthorized),
		}}, false, nil
	}

	commit, err := store.Commit(ctx, rev, []Operation{{
		Kind: OpModified,
		Node: changesetID,
		Changes: []FieldChange{
			{Path: []string{"status"}, Before: node.Status, After: ChangesetStatusWithdrawn},
		},
	}}, CommitOptions{})
	if err != nil {
		if IsCASConflict(err) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("graph withdraw: %w", err)
	}
	return commitResultApply(commit, true), false, nil
}

// RebaseVia is Rebase routed through a CatalogStore. actor and clk are
// accepted for seam consistency and unused, exactly like Rebase.
func RebaseVia(ctx context.Context, store CatalogStore, changesetID NodeID, actor string, clk clock.Clock) (*ApplyResult, error) {
	_, _ = actor, clk
	for attempt := 1; attempt <= casMaxAttempts; attempt++ {
		res, retry, err := rebaseViaOnce(ctx, store, changesetID)
		if retry {
			continue
		}
		return res, err
	}
	return &ApplyResult{RejectReasons: []string{
		fmt.Sprintf("CONFLICT: %s changed concurrently during rebase, exceeded %d retry attempts", store.Ref(), casMaxAttempts),
	}}, nil
}

func rebaseViaOnce(ctx context.Context, store CatalogStore, changesetID NodeID) (res *ApplyResult, retry bool, err error) {
	cat, rev, err := store.Load(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("graph rebase: load %s: %w", store.Ref(), err)
	}
	node, ok := cat.Nodes[changesetID]
	if !ok {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("graph rebase: changeset %q not found in catalog", changesetID),
		}}, false, nil
	}
	if node.TypeID != "changeset" {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("graph rebase: node %q is type %q, not changeset", changesetID, node.TypeID),
		}}, false, nil
	}
	if node.Status != ChangesetStatusProposed {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("graph rebase: changeset %q has status %q, must be %q to rebase", changesetID, node.Status, ChangesetStatusProposed),
		}}, false, nil
	}

	rawOps, ok := node.Fields["operations"].([]any)
	if !ok {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("graph rebase: changeset %q missing operations", changesetID),
		}}, false, nil
	}

	cs, err := ParseChangeset(node)
	if err != nil {
		return &ApplyResult{RejectReasons: []string{err.Error()}}, false, nil
	}

	if !refreshStaleGuards(cs, cat) {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("graph rebase: changeset %q has no stale Before guards to refresh", changesetID),
		}}, false, nil
	}

	if reasons := ValidateChangeset(cs, cat); len(reasons) > 0 {
		return &ApplyResult{RejectReasons: reasons}, false, nil
	}

	newRawOps := rawOpsToAny(opsToRaw(cs.Operations))
	commit, err := store.Commit(ctx, rev, []Operation{{
		Kind: OpModified,
		Node: changesetID,
		Changes: []FieldChange{
			{Path: []string{"fields", "operations"}, Before: rawOps, After: newRawOps},
		},
	}}, CommitOptions{})
	if err != nil {
		if IsCASConflict(err) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("graph rebase: %w", err)
	}
	return commitResultApply(commit, true), false, nil
}

// ApplyVia is Apply routed through a CatalogStore: the changeset's own
// operations plus the same-commit "notified" flip (skipped on dryRun, exactly
// like applyOnce), committed under the Load's revision token.
func ApplyVia(ctx context.Context, store CatalogStore, changesetID NodeID, dryRun bool, actor string, clk clock.Clock) (*ApplyResult, error) {
	_ = actor
	if clk == nil {
		clk = clock.Real()
	}
	for attempt := 1; attempt <= casMaxAttempts; attempt++ {
		res, retry, err := applyViaOnce(ctx, store, changesetID, dryRun, clk)
		if retry {
			continue
		}
		return res, err
	}
	return &ApplyResult{RejectReasons: []string{
		fmt.Sprintf("CONFLICT: %s changed concurrently during apply, exceeded %d retry attempts", store.Ref(), casMaxAttempts),
	}}, nil
}

func applyViaOnce(ctx context.Context, store CatalogStore, changesetID NodeID, dryRun bool, clk clock.Clock) (res *ApplyResult, retry bool, err error) {
	cat, rev, err := store.Load(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("graph apply: load %s: %w", store.Ref(), err)
	}
	csNode, ok := cat.Nodes[changesetID]
	if !ok {
		return nil, false, fmt.Errorf("graph apply: changeset %q not found in catalog", changesetID)
	}
	cs, err := ParseChangeset(csNode)
	if err != nil {
		return nil, false, fmt.Errorf("graph apply: %w", err)
	}
	if !dryRun && cs.Status != ChangesetStatusAuthorized {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("changeset %q has status %q, must be %q to apply (pass dryRun to preview an unauthorized changeset)", cs.NodeID, cs.Status, ChangesetStatusAuthorized),
		}}, false, nil
	}
	if reasons := ValidateChangeset(cs, cat); len(reasons) > 0 {
		return &ApplyResult{RejectReasons: reasons}, false, nil
	}

	ops := cs.Operations
	if !dryRun {
		ops = append(append([]Operation{}, cs.Operations...), Operation{
			Kind: OpModified,
			Node: changesetID,
			Changes: []FieldChange{
				{Path: []string{"status"}, Before: ChangesetStatusAuthorized, After: ChangesetStatusNotified},
				{Path: []string{"fields", "applied_at"}, After: clk.Now().UTC().Format(time.RFC3339)},
			},
		})
	}
	commit, err := store.Commit(ctx, rev, ops, CommitOptions{DryRun: dryRun})
	if err != nil {
		if IsCASConflict(err) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("graph apply: %w", err)
	}
	return commitResultApply(commit, !dryRun), false, nil
}
