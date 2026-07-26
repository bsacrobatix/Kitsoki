package graph

import (
	"context"
	"os"
	"testing"
)

// storeAddOp is a minimal valid "added" operation against the
// copySingleFileFixture catalog (testdata/good/minimal.yaml + a changeset
// type), matching the shape Propose's own tests commit.
func storeAddOp(id string) Operation {
	return Operation{
		Kind: OpAdded,
		Node: NodeID(id),
		After: map[string]any{
			"schema":     "graph/requirement/v0",
			"id":         id,
			"title":      "Store-committed requirement",
			"status":     "draft",
			"visibility": "internal",
		},
	}
}

func TestFileCatalogStore_LoadMatchesLoadCatalog(t *testing.T) {
	root := copySingleFileFixture(t)
	store := NewFileCatalogStore(root)
	if got := store.Ref(); got != root {
		t.Fatalf("Ref() = %q, want %q", got, root)
	}

	cat, rev, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	direct, err := LoadCatalog(root)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	if string(rev) != direct.ContentDigest {
		t.Errorf("rev = %q, want ContentDigest %q", rev, direct.ContentDigest)
	}
	if len(cat.Nodes) != len(direct.Nodes) {
		t.Errorf("store load has %d nodes, direct load has %d", len(cat.Nodes), len(direct.Nodes))
	}
}

func TestFileCatalogStore_CommitLandsAndAdvancesRev(t *testing.T) {
	root := copySingleFileFixture(t)
	store := NewFileCatalogStore(root)
	_, rev, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	res, err := store.Commit(context.Background(), rev, []Operation{storeAddOp("req-store")}, CommitOptions{})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if res.Rejected() {
		t.Fatalf("expected commit to land, got rejection: %+v", res)
	}
	if len(res.ChangedFiles) == 0 {
		t.Error("expected ChangedFiles to name the rewritten catalog file")
	}

	cat, newRev, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := cat.Nodes["req-store"]; !ok {
		t.Error("req-store not present after commit")
	}
	if newRev == rev {
		t.Error("expected the revision token to advance after a landed commit")
	}
}

func TestFileCatalogStore_CommitStaleBaseIsCASConflict(t *testing.T) {
	root := copySingleFileFixture(t)
	store := NewFileCatalogStore(root)
	_, rev, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// A concurrent writer lands between Load and Commit.
	raw, err := os.ReadFile(root)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(root, append(raw, []byte("\n# concurrent edit\n")...), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err = store.Commit(context.Background(), rev, []Operation{storeAddOp("req-stale")}, CommitOptions{})
	if err == nil {
		t.Fatal("expected a CAS conflict, got nil error")
	}
	if !IsCASConflict(err) {
		t.Fatalf("expected IsCASConflict(err), got: %v", err)
	}
}

func TestFileCatalogStore_DryRunPersistsNothing(t *testing.T) {
	root := copySingleFileFixture(t)
	store := NewFileCatalogStore(root)
	_, rev, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	before, err := os.ReadFile(root)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	res, err := store.Commit(context.Background(), rev, []Operation{storeAddOp("req-dry")}, CommitOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Commit dry-run: %v", err)
	}
	if res.Rejected() {
		t.Fatalf("expected dry-run to validate cleanly, got: %+v", res)
	}
	if len(res.ChangedFiles) == 0 {
		t.Error("expected dry-run ChangedFiles to preview the write")
	}

	after, err := os.ReadFile(root)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if string(before) != string(after) {
		t.Error("dry-run modified the catalog on disk")
	}
}

func TestFileCatalogStore_RejectionIsResultNotError(t *testing.T) {
	root := copySingleFileFixture(t)
	store := NewFileCatalogStore(root)
	_, rev, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	before, err := os.ReadFile(root)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// A modified op against a node that doesn't exist cannot be applied to
	// the scratch tree — a RejectReasons rejection, not a Go error.
	res, err := store.Commit(context.Background(), rev, []Operation{{
		Kind:    OpModified,
		Node:    "no-such-node",
		Changes: []FieldChange{{Path: []string{"status"}, After: "done"}},
	}}, CommitOptions{})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if len(res.RejectReasons) == 0 {
		t.Fatal("expected RejectReasons for an unappliable op")
	}

	after, err := os.ReadFile(root)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if string(before) != string(after) {
		t.Error("rejected commit modified the catalog on disk")
	}
}
