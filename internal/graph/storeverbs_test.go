package graph

import (
	"context"
	"testing"

	"kitsoki/internal/clock"
)

// TestStoreVerbs_FileStoreLifecycleParity drives the full propose →
// authorize → apply lifecycle through the *Via verbs over a FileCatalogStore
// and asserts the exact same end state the file verbs produce (see
// TestPropose_SingleFileCatalogCommitsCleanly): the store-routed verbs are a
// second entry point to the same semantics, not a second implementation of
// them.
func TestStoreVerbs_FileStoreLifecycleParity(t *testing.T) {
	root := copySingleFileFixture(t)
	store := NewFileCatalogStore(root)
	ctx := context.Background()

	res, err := ProposeVia(ctx, store, ProposeInput{
		Title: "Add a requirement",
		Operations: []map[string]any{
			{"kind": "added", "after": map[string]any{"schema": "graph/requirement/v0", "id": "req-new", "title": "New requirement", "status": "draft", "visibility": "internal"}},
		},
	}, "tester", clock.Real())
	if err != nil {
		t.Fatalf("ProposeVia: %v", err)
	}
	if len(res.RejectReasons) > 0 {
		t.Fatalf("expected propose to succeed, got: %v", res.RejectReasons)
	}
	if res.Status != ChangesetStatusProposed {
		t.Fatalf("status = %q, want %q", res.Status, ChangesetStatusProposed)
	}

	cat, err := LoadCatalog(root)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	csNode, ok := cat.Nodes[res.ChangesetID]
	if !ok {
		t.Fatalf("changeset node %q not found after ProposeVia", res.ChangesetID)
	}
	if got, _ := csNode.Fields["authored_by"].(string); got != "tester" {
		t.Fatalf("authored_by = %q, want %q", got, "tester")
	}

	authRes, err := AuthorizeVia(ctx, store, res.ChangesetID, "steward", clock.Real())
	if err != nil {
		t.Fatalf("AuthorizeVia: %v", err)
	}
	if authRes.Rejected() {
		t.Fatalf("expected authorize to succeed, got rejected: %+v", authRes)
	}

	applyRes, err := ApplyVia(ctx, store, res.ChangesetID, false, "", clock.Real())
	if err != nil {
		t.Fatalf("ApplyVia: %v", err)
	}
	if applyRes.Rejected() {
		t.Fatalf("expected apply to succeed, got rejected: %+v", applyRes)
	}
	if !applyRes.Applied {
		t.Fatal("expected Applied=true on a real apply")
	}

	final, err := LoadCatalog(root)
	if err != nil {
		t.Fatalf("final reload: %v", err)
	}
	if _, ok := final.Nodes["req-new"]; !ok {
		t.Error("req-new was not applied through the store verbs")
	}
	if got := final.Nodes[res.ChangesetID].Status; got != ChangesetStatusNotified {
		t.Errorf("changeset status = %q, want %q", got, ChangesetStatusNotified)
	}
	if got, _ := final.Nodes[res.ChangesetID].Fields["authorized_by"].(string); got != "steward" {
		t.Errorf("authorized_by = %q, want %q", got, "steward")
	}
}

// TestStoreVerbs_WithdrawAndDryRun covers the two remaining verb behaviors on
// the store path: WithdrawVia flips proposed→withdrawn, and an ApplyVia dry
// run previews an unauthorized changeset without persisting anything.
func TestStoreVerbs_WithdrawAndDryRun(t *testing.T) {
	root := copySingleFileFixture(t)
	store := NewFileCatalogStore(root)
	ctx := context.Background()

	res, err := ProposeVia(ctx, store, ProposeInput{
		Title: "Doomed proposal",
		Operations: []map[string]any{
			{"kind": "added", "after": map[string]any{"schema": "graph/requirement/v0", "id": "req-doomed", "title": "Doomed", "status": "draft", "visibility": "internal"}},
		},
	}, "", clock.Real())
	if err != nil {
		t.Fatalf("ProposeVia: %v", err)
	}
	if len(res.RejectReasons) > 0 {
		t.Fatalf("expected propose to succeed, got: %v", res.RejectReasons)
	}

	dryRes, err := ApplyVia(ctx, store, res.ChangesetID, true, "", clock.Real())
	if err != nil {
		t.Fatalf("ApplyVia dry-run: %v", err)
	}
	if dryRes.Rejected() {
		t.Fatalf("expected dry-run to succeed on an unauthorized changeset, got: %+v", dryRes)
	}
	if dryRes.Applied {
		t.Fatal("dry-run must report Applied=false")
	}
	cat, err := LoadCatalog(root)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := cat.Nodes["req-doomed"]; ok {
		t.Fatal("dry-run persisted req-doomed")
	}

	wRes, err := WithdrawVia(ctx, store, res.ChangesetID, "", clock.Real())
	if err != nil {
		t.Fatalf("WithdrawVia: %v", err)
	}
	if wRes.Rejected() {
		t.Fatalf("expected withdraw to succeed, got: %+v", wRes)
	}
	final, err := LoadCatalog(root)
	if err != nil {
		t.Fatalf("final reload: %v", err)
	}
	if got := final.Nodes[res.ChangesetID].Status; got != ChangesetStatusWithdrawn {
		t.Errorf("changeset status = %q, want %q", got, ChangesetStatusWithdrawn)
	}
}
