package applicationassurance

import (
	"context"
	"testing"
	"time"

	"kitsoki/internal/host"
	"kitsoki/internal/store"
)

func TestSQLiteStoreRetainsComplianceAndFlowReplay(t *testing.T) {
	sessionStore, err := store.Open(t.TempDir() + "/sessions.db")
	if err != nil {
		t.Fatal(err)
	}
	db := sessionStore.DB()
	evidence, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatal(err)
	}
	testStoresRoundTrip(t, evidence)
	restarted, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatal(err)
	}
	complianceRef, ok, err := (ComplianceStore{Store: restarted}).Get(
		context.Background(), "digest",
	)
	if err != nil || !ok || complianceRef != "kitsoki://compliance/sha256/digest" {
		t.Fatalf("restarted compliance = %q, %v, %v", complianceRef, ok, err)
	}
	flow, ok, err := (FlowEvidenceStore{Store: restarted}).LookupFlowEvidence(
		context.Background(), "flow-key",
	)
	if err != nil || !ok || flow.NodeID != "node-one" {
		t.Fatalf("restarted flow = %#v, %v, %v", flow, ok, err)
	}
}

func testStoresRoundTrip(t *testing.T, evidence *SQLStore) {
	t.Helper()
	ctx := context.Background()
	compliance := ComplianceStore{Store: evidence}
	ref, err := compliance.Put(ctx, "digest", []byte(`{"passed":true}`))
	if err != nil || ref != "kitsoki://compliance/sha256/digest" {
		t.Fatalf("compliance put = %q, %v", ref, err)
	}
	ref, err = compliance.Put(ctx, "digest", []byte(`{"passed":false}`))
	if err != nil || ref != "kitsoki://compliance/sha256/digest" {
		t.Fatalf("compliance replay = %q, %v", ref, err)
	}

	flowStore := FlowEvidenceStore{Store: evidence}
	first := host.FlowEvidenceRecord{
		Schema: "kitsoki.flow-evidence/v1", Key: "flow-key",
		EvidenceRef: "flow-evidence:one", ApplicationID: "app",
		Owner: "owner", AppRevision: "1", CatalogRevision: "catalog",
		NodeID: "node-one", Actor: "actor", Passed: true, RunCount: 1,
		RecordedAt: time.Unix(1, 0).UTC(),
	}
	stored, err := flowStore.PutFlowEvidenceIfAbsent(ctx, "flow-key", first)
	if err != nil || stored.NodeID != "node-one" {
		t.Fatalf("flow put = %#v, %v", stored, err)
	}
	second := first
	second.NodeID = "node-two"
	stored, err = flowStore.PutFlowEvidenceIfAbsent(ctx, "flow-key", second)
	if err != nil || stored.NodeID != "node-one" {
		t.Fatalf("flow replay = %#v, %v", stored, err)
	}
}
