package graph

import "testing"

func TestPropose_AppendsChangesetNode(t *testing.T) {
	root := copyBundleFixture(t)
	res, err := Propose(root, ProposeInput{
		Title: "Add requirement two",
		Operations: []map[string]any{
			{
				"kind": "added",
				"after": map[string]any{
					"schema":     "graph/requirement/v0",
					"id":         "req-two",
					"title":      "Requirement two",
					"status":     "draft",
					"visibility": "public",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(res.RejectReasons) > 0 {
		t.Fatalf("expected propose to succeed, got reject reasons: %v", res.RejectReasons)
	}
	if res.ChangesetID == "" {
		t.Fatal("expected a changeset id")
	}
	if res.Status != ChangesetStatusProposed {
		t.Errorf("status = %q, want %q (no provenance -> always human-reviewed)", res.Status, ChangesetStatusProposed)
	}

	cat, err := LoadCatalog(root)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	node, ok := cat.Nodes[res.ChangesetID]
	if !ok {
		t.Fatalf("changeset node %q not found in catalog", res.ChangesetID)
	}
	if node.TypeID != "changeset" {
		t.Errorf("node type = %q, want changeset", node.TypeID)
	}
	if node.Status != ChangesetStatusProposed {
		t.Errorf("node status = %q, want proposed", node.Status)
	}
	// The proposed operations themselves must NOT have been applied yet —
	// only the changeset node was written.
	if _, ok := cat.Nodes["req-two"]; ok {
		t.Fatal("req-two should not exist yet: propose must not apply its payload")
	}
}

func TestPropose_RejectsImmediateStaleBefore(t *testing.T) {
	root := copyBundleFixture(t)
	res, err := Propose(root, ProposeInput{
		Title: "Bad edit",
		Operations: []map[string]any{
			{
				"kind": "modified",
				"node": "req-one",
				"changes": []any{
					map[string]any{"path": []any{"status"}, "before": "satisfied", "after": "closed"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(res.RejectReasons) == 0 {
		t.Fatal("expected an immediate Before stale-check rejection (catalog has draft, not satisfied)")
	}
	if res.ChangesetID != "" {
		t.Fatal("a rejected propose must not assign/write a changeset id")
	}

	cat, err := LoadCatalog(root)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, node := range cat.Nodes {
		if node.TypeID == "changeset" {
			t.Fatal("a rejected propose must not touch the catalog at all")
		}
	}
}

func TestPropose_RejectsAddingExistingNode(t *testing.T) {
	root := copyBundleFixture(t)
	res, err := Propose(root, ProposeInput{
		Title: "Duplicate",
		Operations: []map[string]any{
			{"kind": "added", "after": map[string]any{"schema": "graph/requirement/v0", "id": "req-one", "title": "dup", "status": "draft", "visibility": "public"}},
		},
	})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(res.RejectReasons) == 0 {
		t.Fatal("expected a rejection: req-one already exists")
	}
}

func TestAuthorize_FlipsProposedToAuthorized(t *testing.T) {
	root := copyBundleFixture(t)
	writeChangeset(t, root, "change-auth", ChangesetStatusProposed, `    - kind: modified
      node: req-one
      changes:
        - path: [status]
          before: draft
          after: satisfied
`)
	res, err := Authorize(root, "change-auth")
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if res.Rejected() {
		t.Fatalf("expected authorize to succeed, got rejected: %+v", res)
	}
	if !res.Applied {
		t.Fatal("expected Applied=true")
	}

	cat, err := LoadCatalog(root)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cat.Nodes["change-auth"].Status != ChangesetStatusAuthorized {
		t.Errorf("status = %q, want authorized", cat.Nodes["change-auth"].Status)
	}
	// The changeset's own operations must still be un-applied — authorize
	// is a lifecycle flip only, never an apply.
	if cat.Nodes["req-one"].Status != "draft" {
		t.Errorf("req-one status = %q, want still draft (authorize must not apply operations)", cat.Nodes["req-one"].Status)
	}
}

func TestAuthorize_RejectsNonProposedStatus(t *testing.T) {
	root := copyBundleFixture(t)
	writeChangeset(t, root, "change-already-auth", ChangesetStatusAuthorized, `    - kind: modified
      node: req-one
      changes:
        - path: [status]
          before: draft
          after: satisfied
`)
	res, err := Authorize(root, "change-already-auth")
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !res.Rejected() {
		t.Fatal("expected a rejection: changeset is already authorized, not proposed")
	}
}

func TestAuthorize_RejectsUnknownChangeset(t *testing.T) {
	root := copyBundleFixture(t)
	res, err := Authorize(root, "does-not-exist")
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !res.Rejected() {
		t.Fatal("expected a rejection for an unknown changeset id")
	}
}

func TestApply_MarksNotifiedInSameCommit(t *testing.T) {
	root := copyBundleFixture(t)
	writeChangeset(t, root, "change-notify", ChangesetStatusAuthorized, `    - kind: modified
      node: req-one
      changes:
        - path: [status]
          before: draft
          after: satisfied
`)
	res, err := Apply(root, "change-notify", false)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Rejected() {
		t.Fatalf("expected apply to succeed, got rejected: %+v", res)
	}

	cat, err := LoadCatalog(root)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cat.Nodes["change-notify"].Status != ChangesetStatusNotified {
		t.Errorf("changeset status = %q, want notified (apply must mark notified in the same commit)", cat.Nodes["change-notify"].Status)
	}
	if cat.Nodes["req-one"].Status != "satisfied" {
		t.Errorf("req-one status = %q, want satisfied", cat.Nodes["req-one"].Status)
	}

	// Re-applying a now-"notified" (not "authorized") changeset must be
	// rejected — this is what makes the review queue able to tell applied
	// from pending.
	res2, err := Apply(root, "change-notify", false)
	if err != nil {
		t.Fatalf("re-Apply: %v", err)
	}
	if !res2.Rejected() {
		t.Fatal("expected a re-apply of a notified changeset to be rejected")
	}
}

func TestWithdraw_FlipsProposedToWithdrawn(t *testing.T) {
	root := copyBundleFixture(t)
	writeChangeset(t, root, "change-withdraw", ChangesetStatusProposed, `    - kind: modified
      node: req-one
      changes:
        - path: [status]
          before: draft
          after: satisfied
`)
	res, err := Withdraw(root, "change-withdraw")
	if err != nil {
		t.Fatalf("Withdraw: %v", err)
	}
	if res.Rejected() {
		t.Fatalf("expected withdraw to succeed, got rejected: %+v", res)
	}

	cat, err := LoadCatalog(root)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if cat.Nodes["change-withdraw"].Status != ChangesetStatusWithdrawn {
		t.Errorf("status = %q, want withdrawn", cat.Nodes["change-withdraw"].Status)
	}

	// A withdrawn changeset must not be authorizable or appliable.
	if res2, _ := Authorize(root, "change-withdraw"); !res2.Rejected() {
		t.Error("expected authorize on a withdrawn changeset to be rejected")
	}
}

func TestWithdraw_RejectsNotifiedChangeset(t *testing.T) {
	root := copyBundleFixture(t)
	writeChangeset(t, root, "change-notified", ChangesetStatusNotified, `    - kind: modified
      node: req-one
      changes:
        - path: [status]
          before: draft
          after: satisfied
`)
	res, err := Withdraw(root, "change-notified")
	if err != nil {
		t.Fatalf("Withdraw: %v", err)
	}
	if !res.Rejected() {
		t.Fatal("expected withdraw of an already-notified (applied) changeset to be rejected")
	}
}

func TestPropose_SystemAuthoredAllowlistedFieldAutoAuthorizes(t *testing.T) {
	root := copyBundleFixture(t)
	res, err := Propose(root, ProposeInput{
		Title: "materialize write-back",
		Operations: []map[string]any{
			{
				"kind": "modified",
				"node": "feature-one",
				"changes": []any{
					map[string]any{"path": []any{"fields", "materialization"}, "before": nil, "after": map[string]any{"stage": "done"}},
				},
			},
		},
		Provenance: map[string]any{"job_id": "job-1", "story": "materialize-work-item"},
	})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if len(res.RejectReasons) > 0 {
		t.Fatalf("expected propose to succeed, got: %v", res.RejectReasons)
	}
	if res.Status != ChangesetStatusAuthorized {
		t.Errorf("status = %q, want authorized (system-authored + allowlisted field -> auto-authorize)", res.Status)
	}
}

func TestPropose_SystemAuthoredNonAllowlistedFieldStaysProposed(t *testing.T) {
	root := copyBundleFixture(t)
	res, err := Propose(root, ProposeInput{
		Title: "system-authored content edit",
		Operations: []map[string]any{
			{
				"kind": "modified",
				"node": "feature-one",
				"changes": []any{
					map[string]any{"path": []any{"title"}, "before": "Feature one", "after": "Renamed"},
				},
			},
		},
		Provenance: map[string]any{"job_id": "job-1", "story": "materialize-work-item"},
	})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if res.Status != ChangesetStatusProposed {
		t.Errorf("status = %q, want proposed (title is not an allowlisted auto-authorize field)", res.Status)
	}
}
