package graph

import (
	"fmt"
	"os"
)

// ProposeInput is the payload for Propose: a candidate changeset's title and
// raw operations — the same wire shape ParseChangeset expects off a
// changeset node's Fields["operations"] (a list of {"kind": ..., ...}
// mappings) — plus optional system-authorship provenance.
type ProposeInput struct {
	Title      string
	Visibility string // defaults to "internal"
	Operations []map[string]any

	// Provenance, when non-nil, marks this as a system-authored changeset
	// (materialization write-back, derived status flips, ...) and typically
	// carries {"job_id": ..., "story": ...}. Combined with the D9
	// auto-authorize allowlist (autoAuthorizeFieldRoots), it determines
	// whether Propose immediately authorizes the changeset (§3.3
	// "machine-write policy: system-authored changesets ... auto-authorized
	// for an allowlisted field set") instead of leaving it "proposed" for
	// human review.
	Provenance map[string]any
}

// ProposeResult reports the outcome of Propose.
type ProposeResult struct {
	// ChangesetID is set only when the changeset node was successfully
	// appended to the catalog.
	ChangesetID NodeID
	// Status is the changeset's resulting lifecycle status ("proposed", or
	// "authorized" when Provenance qualified for auto-authorize).
	Status string
	// Lint is the (normally empty) result of linting the catalog with the
	// new changeset node appended — appending a changeset node never
	// changes any other node, so this is mostly a sanity signal, but is
	// still gate-worthy per the op's contract (§3.3: "return
	// {changeset_id, lint}").
	Lint []LintIssue
	// RejectReasons is non-empty when the payload's operations failed
	// ValidateChangeset against the current catalog (including the
	// immediate Before stale-check) — the real catalog was never touched.
	RejectReasons []string
}

// autoAuthorizeFieldRoots is the D9 allowlist: a system-authored changeset
// (one carrying Provenance) whose every "modified" operation only ever
// touches paths rooted at one of these — or the bare ["status"] path, for
// derived status flips — self-authorizes instead of waiting in the human
// review queue. Any other operation kind, or any field outside this set,
// keeps the changeset "proposed" regardless of provenance. Human-authored
// content changesets (no Provenance) always stay "proposed".
var autoAuthorizeFieldRoots = map[string]bool{
	"materialization": true,
	"evidence":        true,
}

func isAutoAuthorizablePath(path []string) bool {
	if len(path) == 1 && path[0] == "status" {
		return true
	}
	if len(path) == 2 && path[0] == "fields" {
		return autoAuthorizeFieldRoots[path[1]]
	}
	return false
}

// allOpsAutoAuthorizable reports whether every operation in ops is a
// "modified" op whose every field change targets an allowlisted path (D9).
func allOpsAutoAuthorizable(ops []Operation) bool {
	if len(ops) == 0 {
		return false
	}
	for _, op := range ops {
		if op.Kind != OpModified {
			return false
		}
		for _, ch := range op.Changes {
			if !isAutoAuthorizablePath(ch.Path) {
				return false
			}
		}
	}
	return true
}

// rawOpsToAny adapts the wire shape ([]map[string]any) Propose's callers
// hand in to the []any ParseChangeset expects off Fields["operations"] (it
// type-asserts raw.([]any), then each element to map[string]any).
func rawOpsToAny(ops []map[string]any) []any {
	out := make([]any, len(ops))
	for i, op := range ops {
		out[i] = op
	}
	return out
}

// nextChangesetID picks a fresh, collision-free "cs-<n>" id — the simplest
// deterministic scheme that survives concurrent-ish propose calls within a
// single process (each call reloads the catalog fresh).
func nextChangesetID(cat *Catalog) NodeID {
	n := 0
	for _, node := range cat.Nodes {
		if node.TypeID == "changeset" {
			n++
		}
	}
	for {
		n++
		candidate := NodeID(fmt.Sprintf("cs-%d", n))
		if _, exists := cat.Nodes[candidate]; !exists {
			return candidate
		}
	}
}

// Propose validates a changeset payload's operations against rootPath's
// current catalog — via ParseChangeset + ValidateChangeset, exactly the
// checks Apply itself runs, so a Before stale-check or a "node already
// exists" problem surfaces immediately, not at authorize/apply time — then
// assigns a fresh changeset id and appends the changeset node itself
// (comment-preserving, via the same scratch-copy + applyOperations + Lint
// machinery Apply uses) to the catalog. A rejected propose never touches
// rootPath.
//
// Nothing else may write a changeset node into the catalog: Apply requires
// it to already exist (apply.go's Apply loads csNode and errors if absent),
// and flipping a changeset's own status via a changeset would be infinite
// regress — so Propose is the sole writer of new changeset nodes, and
// Authorize (below) is the sole writer of the proposed->authorized flip.
func Propose(rootPath string, input ProposeInput) (*ProposeResult, error) {
	cat, err := LoadCatalog(rootPath)
	if err != nil {
		return nil, fmt.Errorf("graph propose: load %s: %w", rootPath, err)
	}

	synthetic := &Node{
		ID:     "__propose-candidate__",
		TypeID: "changeset",
		Fields: map[string]any{"operations": rawOpsToAny(input.Operations)},
	}
	cs, err := ParseChangeset(synthetic)
	if err != nil {
		return &ProposeResult{RejectReasons: []string{err.Error()}}, nil
	}
	if reasons := ValidateChangeset(cs, cat); len(reasons) > 0 {
		return &ProposeResult{RejectReasons: reasons}, nil
	}

	id := nextChangesetID(cat)
	visibility := input.Visibility
	if visibility == "" {
		visibility = "internal"
	}
	status := ChangesetStatusProposed
	if input.Provenance != nil && allOpsAutoAuthorizable(cs.Operations) {
		status = ChangesetStatusAuthorized
	}

	nodeMap := map[string]any{
		"schema":     "graph/changeset/v1",
		"id":         string(id),
		"title":      input.Title,
		"status":     status,
		"visibility": visibility,
		"operations": rawOpsToAny(input.Operations),
	}
	if input.Provenance != nil {
		nodeMap["provenance"] = input.Provenance
	}

	changedFiles, issues, rejectReasons, err := commitScratchOperations(cat, []Operation{
		{Kind: OpAdded, Node: id, After: nodeMap},
	})
	if err != nil {
		return nil, fmt.Errorf("graph propose: %w", err)
	}
	if len(rejectReasons) > 0 {
		return &ProposeResult{RejectReasons: rejectReasons}, nil
	}
	if len(issues) > 0 {
		return &ProposeResult{Lint: issues}, nil
	}
	_ = changedFiles

	return &ProposeResult{ChangesetID: id, Status: status, Lint: issues}, nil
}

// Authorize flips a changeset from "proposed" to "authorized" — the only
// mutation lifecycle flips make (§3.3: "content writes are changesets;
// lifecycle flips are blessed direct writes with provenance"). It writes
// directly rather than through another changeset: flipping a changeset's
// own status via a changeset would be infinite regress (see Propose's doc
// comment). On an unauthenticated localhost /rpc this is honor-system.
func Authorize(rootPath string, changesetID NodeID) (*ApplyResult, error) {
	cat, err := LoadCatalog(rootPath)
	if err != nil {
		return nil, fmt.Errorf("graph authorize: load %s: %w", rootPath, err)
	}
	node, ok := cat.Nodes[changesetID]
	if !ok {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("graph authorize: changeset %q not found in catalog", changesetID),
		}}, nil
	}
	if node.TypeID != "changeset" {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("graph authorize: node %q is type %q, not changeset", changesetID, node.TypeID),
		}}, nil
	}
	if node.Status != ChangesetStatusProposed {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("graph authorize: changeset %q has status %q, must be %q to authorize", changesetID, node.Status, ChangesetStatusProposed),
		}}, nil
	}

	changedFiles, issues, rejectReasons, err := commitScratchOperations(cat, []Operation{
		{
			Kind: OpModified,
			Node: changesetID,
			Changes: []FieldChange{
				{Path: []string{"status"}, Before: ChangesetStatusProposed, After: ChangesetStatusAuthorized},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("graph authorize: %w", err)
	}
	if len(rejectReasons) > 0 {
		return &ApplyResult{RejectReasons: rejectReasons}, nil
	}
	if len(issues) > 0 {
		return &ApplyResult{LintIssues: issues}, nil
	}
	return &ApplyResult{Applied: true, ChangedFiles: changedFiles}, nil
}

// Withdraw flips a "proposed" or "authorized" (but not yet applied/
// "notified") changeset to "withdrawn" — the review queue's "clean up a
// rejected proposal" action (§3.3). Like Authorize, this is a direct write,
// not a second changeset.
func Withdraw(rootPath string, changesetID NodeID) (*ApplyResult, error) {
	cat, err := LoadCatalog(rootPath)
	if err != nil {
		return nil, fmt.Errorf("graph withdraw: load %s: %w", rootPath, err)
	}
	node, ok := cat.Nodes[changesetID]
	if !ok {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("graph withdraw: changeset %q not found in catalog", changesetID),
		}}, nil
	}
	if node.TypeID != "changeset" {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("graph withdraw: node %q is type %q, not changeset", changesetID, node.TypeID),
		}}, nil
	}
	if node.Status != ChangesetStatusProposed && node.Status != ChangesetStatusAuthorized {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("graph withdraw: changeset %q has status %q, must be %q or %q to withdraw", changesetID, node.Status, ChangesetStatusProposed, ChangesetStatusAuthorized),
		}}, nil
	}

	changedFiles, issues, rejectReasons, err := commitScratchOperations(cat, []Operation{
		{
			Kind: OpModified,
			Node: changesetID,
			Changes: []FieldChange{
				{Path: []string{"status"}, Before: node.Status, After: ChangesetStatusWithdrawn},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("graph withdraw: %w", err)
	}
	if len(rejectReasons) > 0 {
		return &ApplyResult{RejectReasons: rejectReasons}, nil
	}
	if len(issues) > 0 {
		return &ApplyResult{LintIssues: issues}, nil
	}
	return &ApplyResult{Applied: true, ChangedFiles: changedFiles}, nil
}

// Rebase refreshes a "proposed" changeset's stale Before guards (§3.3's
// review-queue "rebase" action, cut from A1) against the catalog's current
// state, then re-validates. For every "modified" operation's field changes,
// a Before that no longer matches the live catalog is refreshed to the live
// value; the changeset is only actually rewritten if, after refreshing,
// ValidateChangeset reports zero remaining reasons — i.e. the refresh was
// non-conflicting (nothing else about the operation, e.g. a missing node,
// was wrong). If a genuine conflict remains, Rebase leaves the changeset
// untouched and reports the reasons, exactly like a rejected Propose. This
// intentionally does not touch "removed"/"retyped"/other op kinds' guards —
// only "modified" field-level Before values, which is what the review queue
// scenario (someone else edited an unrelated field while your changeset sat
// in the queue) actually needs.
func Rebase(rootPath string, changesetID NodeID) (*ApplyResult, error) {
	cat, err := LoadCatalog(rootPath)
	if err != nil {
		return nil, fmt.Errorf("graph rebase: load %s: %w", rootPath, err)
	}
	node, ok := cat.Nodes[changesetID]
	if !ok {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("graph rebase: changeset %q not found in catalog", changesetID),
		}}, nil
	}
	if node.TypeID != "changeset" {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("graph rebase: node %q is type %q, not changeset", changesetID, node.TypeID),
		}}, nil
	}
	if node.Status != ChangesetStatusProposed {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("graph rebase: changeset %q has status %q, must be %q to rebase", changesetID, node.Status, ChangesetStatusProposed),
		}}, nil
	}

	rawOpsAny, ok := node.Fields["operations"]
	if !ok {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("graph rebase: changeset %q missing operations", changesetID),
		}}, nil
	}
	rawOps, ok := rawOpsAny.([]any)
	if !ok {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("graph rebase: changeset %q operations must be a list", changesetID),
		}}, nil
	}

	cs, err := ParseChangeset(node)
	if err != nil {
		return &ApplyResult{RejectReasons: []string{err.Error()}}, nil
	}

	newRawOps := make([]any, len(rawOps))
	changedAny := false
	for i, rawOp := range rawOps {
		opMap, ok := rawOp.(map[string]any)
		if !ok || i >= len(cs.Operations) || cs.Operations[i].Kind != OpModified {
			newRawOps[i] = rawOp
			continue
		}
		op := cs.Operations[i]
		target, exists := cat.Nodes[op.Node]
		if !exists {
			newRawOps[i] = rawOp
			continue
		}
		rawChanges, _ := opMap["changes"].([]any)
		newRawChanges := make([]any, len(rawChanges))
		for j, rc := range rawChanges {
			cm, ok := rc.(map[string]any)
			if !ok || j >= len(op.Changes) {
				newRawChanges[j] = rc
				continue
			}
			ch := op.Changes[j]
			current := readNodePath(target, ch.Path)
			if ch.Before != nil && !valuesEqual(current, ch.Before) {
				refreshed := map[string]any{}
				for k, v := range cm {
					refreshed[k] = v
				}
				refreshed["before"] = current
				newRawChanges[j] = refreshed
				changedAny = true
			} else {
				newRawChanges[j] = rc
			}
		}
		newOpMap := map[string]any{}
		for k, v := range opMap {
			newOpMap[k] = v
		}
		newOpMap["changes"] = newRawChanges
		newRawOps[i] = newOpMap
	}

	if !changedAny {
		return &ApplyResult{RejectReasons: []string{
			fmt.Sprintf("graph rebase: changeset %q has no stale Before guards to refresh", changesetID),
		}}, nil
	}

	// Re-validate the refreshed operations before committing anything —
	// only a fully non-conflicting refresh is applied.
	synthetic := &Node{ID: node.ID, TypeID: "changeset", Fields: map[string]any{"operations": newRawOps}}
	refreshedCS, err := ParseChangeset(synthetic)
	if err != nil {
		return &ApplyResult{RejectReasons: []string{err.Error()}}, nil
	}
	if reasons := ValidateChangeset(refreshedCS, cat); len(reasons) > 0 {
		return &ApplyResult{RejectReasons: reasons}, nil
	}

	changedFiles, issues, rejectReasons, err := commitScratchOperations(cat, []Operation{
		{
			Kind: OpModified,
			Node: changesetID,
			Changes: []FieldChange{
				{Path: []string{"fields", "operations"}, Before: rawOps, After: newRawOps},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("graph rebase: %w", err)
	}
	if len(rejectReasons) > 0 {
		return &ApplyResult{RejectReasons: rejectReasons}, nil
	}
	if len(issues) > 0 {
		return &ApplyResult{LintIssues: issues}, nil
	}
	return &ApplyResult{Applied: true, ChangedFiles: changedFiles}, nil
}

// commitScratchOperations is Apply's dry-run-first scratch-copy machinery,
// factored out so Propose and Authorize can commit a small synthetic
// operation set (appending a changeset node, or flipping its status) with
// the exact same comment-preserving-rewrite-then-lint-then-copy-back
// guarantee Apply gives ordinary changesets — a rejected candidate never
// touches rootPath.
func commitScratchOperations(cat *Catalog, ops []Operation) (changedFiles []string, lintIssues []LintIssue, rejectReasons []string, err error) {
	tmpDir, err := os.MkdirTemp("", "kitsoki-graph-commit-*")
	if err != nil {
		return nil, nil, nil, fmt.Errorf("mktemp: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	scratchRoot, err := copyCatalogTree(cat.RootPath, tmpDir, cat)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("copy scratch tree: %w", err)
	}

	changed, err := applyOperations(cat, ops, scratchRoot)
	if err != nil {
		return nil, nil, []string{err.Error()}, nil
	}

	candidate, err := LoadCatalog(scratchRoot)
	if err != nil {
		return nil, nil, []string{fmt.Sprintf("candidate catalog failed to load: %v", err)}, nil
	}
	// Error-severity only, same rationale as Apply's gate (apply.go):
	// advisory warnings must not block a propose/commit.
	if issues := ErrorIssues(Lint(candidate)); len(issues) > 0 {
		return nil, issues, nil, nil
	}

	if err := commitChangedFiles(cat.RootPath, scratchRoot, changed); err != nil {
		return nil, nil, nil, err
	}
	return changed, nil, nil, nil
}
