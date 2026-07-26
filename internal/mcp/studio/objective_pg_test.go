package studio

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"kitsoki/internal/dbruntime/pgtest"
)

func openPGObjectiveStore(t *testing.T) *PostgresObjectiveStore {
	t.Helper()
	db := pgtest.Open(t)
	store, err := NewPostgresObjectiveStore(db)
	if err != nil {
		t.Fatalf("NewPostgresObjectiveStore: %v", err)
	}
	return store
}

func pgTestObjective(id string) Objective {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	return Objective{
		SchemaVersion: ObjectiveSchemaVersion, ID: id, Goal: "durable receipts",
		Acceptance: []string{"receipts survive restart"},
		Policy:     PolicyProfile{Name: "strict", Strict: true},
		Status:     ObjectiveOpen, Revision: 1, OpenedAt: now, UpdatedAt: now,
	}
}

func pgTestReceipt(objectiveID string, callerSequence int) Receipt {
	return Receipt{
		SchemaVersion: ReceiptSchemaVersion, Sequence: callerSequence,
		Type: ReceiptEvidenceRecorded, ObjectiveID: objectiveID, ObjectiveRevision: 1,
		PolicyHash: "hash", RecordedAt: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
		Attributes: map[string]string{"n": fmt.Sprint(callerSequence)},
	}
}

func TestPostgresObjectiveStoreCRUDRoundTrip(t *testing.T) {
	t.Parallel()
	store := openPGObjectiveStore(t)
	ctx := context.Background()
	objective := pgTestObjective("obj-crud")

	if err := store.Create(ctx, objective); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.Create(ctx, objective); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate Create: want already-exists error, got %v", err)
	}
	got, err := store.Get(ctx, objective.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Goal != objective.Goal || got.Policy.Name != "strict" || !got.OpenedAt.Equal(objective.OpenedAt) {
		t.Fatalf("Get round-trip mismatch: %+v", got)
	}

	got.Revision = 2
	got.Goal = "updated goal"
	if err := store.Save(ctx, got); err != nil {
		t.Fatalf("Save: %v", err)
	}
	again, err := store.Get(ctx, objective.ID)
	if err != nil {
		t.Fatalf("Get after Save: %v", err)
	}
	if again.Revision != 2 || again.Goal != "updated goal" {
		t.Fatalf("Save not persisted: %+v", again)
	}

	if _, err := store.Get(ctx, "missing"); !errors.Is(err, ErrObjectiveNotFound) {
		t.Fatalf("Get missing: want ErrObjectiveNotFound, got %v", err)
	}
	if err := store.Save(ctx, pgTestObjective("missing")); !errors.Is(err, ErrObjectiveNotFound) {
		t.Fatalf("Save missing: want ErrObjectiveNotFound, got %v", err)
	}
	if err := store.AppendReceipt(ctx, pgTestReceipt("missing", 1)); !errors.Is(err, ErrObjectiveNotFound) {
		t.Fatalf("AppendReceipt missing: want ErrObjectiveNotFound, got %v", err)
	}
	if _, err := store.ListReceipts(ctx, "missing"); !errors.Is(err, ErrObjectiveNotFound) {
		t.Fatalf("ListReceipts missing: want ErrObjectiveNotFound, got %v", err)
	}
}

func TestPostgresObjectiveStoreAppendListOrdering(t *testing.T) {
	t.Parallel()
	store := openPGObjectiveStore(t)
	ctx := context.Background()
	if err := store.Create(ctx, pgTestObjective("obj-order")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Caller-supplied sequences are deliberately wrong/duplicated: the store
	// must assign the authoritative dense sequence itself.
	for _, bogus := range []int{99, 99, 0} {
		stored, err := store.AppendReceiptAssign(ctx, pgTestReceipt("obj-order", bogus))
		if err != nil {
			t.Fatalf("AppendReceiptAssign: %v", err)
		}
		if stored.Sequence == bogus && bogus != 0 {
			t.Fatalf("store did not reassign caller sequence %d", bogus)
		}
	}
	receipts, err := store.ListReceipts(ctx, "obj-order")
	if err != nil {
		t.Fatalf("ListReceipts: %v", err)
	}
	if len(receipts) != 3 {
		t.Fatalf("want 3 receipts, got %d", len(receipts))
	}
	for i, receipt := range receipts {
		if receipt.Sequence != i+1 {
			t.Fatalf("receipt %d: want sequence %d, got %d", i, i+1, receipt.Sequence)
		}
		if receipt.Type != ReceiptEvidenceRecorded || receipt.ObjectiveID != "obj-order" {
			t.Fatalf("receipt %d round-trip mismatch: %+v", i, receipt)
		}
	}
}

func TestPostgresObjectiveStorePerObjectiveSequenceIsolation(t *testing.T) {
	t.Parallel()
	store := openPGObjectiveStore(t)
	ctx := context.Background()
	for _, id := range []string{"obj-a", "obj-b"} {
		if err := store.Create(ctx, pgTestObjective(id)); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
	}
	// Interleave appends across the two objectives.
	for i := 0; i < 3; i++ {
		for _, id := range []string{"obj-a", "obj-b"} {
			if err := store.AppendReceipt(ctx, pgTestReceipt(id, 0)); err != nil {
				t.Fatalf("AppendReceipt %s: %v", id, err)
			}
		}
	}
	for _, id := range []string{"obj-a", "obj-b"} {
		receipts, err := store.ListReceipts(ctx, id)
		if err != nil {
			t.Fatalf("ListReceipts %s: %v", id, err)
		}
		if len(receipts) != 3 {
			t.Fatalf("%s: want 3 receipts, got %d", id, len(receipts))
		}
		for i, receipt := range receipts {
			if receipt.Sequence != i+1 {
				t.Fatalf("%s receipt %d: want sequence %d, got %d", id, i, i+1, receipt.Sequence)
			}
		}
	}
}

func TestPostgresObjectiveStoreConcurrentAppendSequenceIntegrity(t *testing.T) {
	t.Parallel()
	store := openPGObjectiveStore(t)
	ctx := context.Background()
	const writers = 16
	ids := []string{"obj-conc-a", "obj-conc-b"}
	for _, id := range ids {
		if err := store.Create(ctx, pgTestObjective(id)); err != nil {
			t.Fatalf("Create %s: %v", id, err)
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, writers*len(ids))
	for _, id := range ids {
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				if _, err := store.AppendReceiptAssign(ctx, pgTestReceipt(id, 0)); err != nil {
					errs <- err
				}
			}(id)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent append: %v", err)
	}
	for _, id := range ids {
		receipts, err := store.ListReceipts(ctx, id)
		if err != nil {
			t.Fatalf("ListReceipts %s: %v", id, err)
		}
		if len(receipts) != writers {
			t.Fatalf("%s: want %d receipts, got %d", id, writers, len(receipts))
		}
		for i, receipt := range receipts {
			if receipt.Sequence != i+1 {
				t.Fatalf("%s: sequence gap/dup at %d: got %d", id, i, receipt.Sequence)
			}
		}
	}
}

func TestPostgresObjectiveStoreReceiptsAreImmutable(t *testing.T) {
	t.Parallel()
	db := pgtest.Open(t)
	store, err := NewPostgresObjectiveStore(db)
	if err != nil {
		t.Fatalf("NewPostgresObjectiveStore: %v", err)
	}
	ctx := context.Background()
	if err := store.Create(ctx, pgTestObjective("obj-immutable")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := store.AppendReceipt(ctx, pgTestReceipt("obj-immutable", 0)); err != nil {
		t.Fatalf("AppendReceipt: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE studio.objective_receipts SET type = 'forged'`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("UPDATE receipts: want append-only rejection, got %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM studio.objective_receipts`); err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("DELETE receipts: want append-only rejection, got %v", err)
	}
	receipts, err := store.ListReceipts(ctx, "obj-immutable")
	if err != nil {
		t.Fatalf("ListReceipts: %v", err)
	}
	if len(receipts) != 1 || receipts[0].Type != ReceiptEvidenceRecorded {
		t.Fatalf("receipt mutated or lost: %+v", receipts)
	}
}

// TestPostgresObjectiveServiceLifecycle drives the real ObjectiveService over
// the durable store: every lifecycle receipt must carry the store-assigned
// sequence, and reopening the store over the same database must see them.
func TestPostgresObjectiveServiceLifecycle(t *testing.T) {
	t.Parallel()
	db := pgtest.Open(t)
	store, err := NewPostgresObjectiveStore(db)
	if err != nil {
		t.Fatalf("NewPostgresObjectiveStore: %v", err)
	}
	service, err := NewObjectiveService(store, nil)
	if err != nil {
		t.Fatalf("NewObjectiveService: %v", err)
	}
	ctx := context.Background()
	_, opened, err := service.Open(ctx, OpenObjectiveInput{
		ID: "obj-service", Goal: "durable lifecycle", Acceptance: []string{"done"},
		Policy: PolicyProfile{Name: "lenient"},
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if opened.Sequence != 1 {
		t.Fatalf("open receipt: want sequence 1, got %d", opened.Sequence)
	}
	_, evidenced, err := service.RecordEvidence(ctx, "obj-service", RecordEvidenceInput{ID: "ev-1", Kind: "test", Summary: "green"})
	if err != nil {
		t.Fatalf("RecordEvidence: %v", err)
	}
	if evidenced.Sequence != 2 {
		t.Fatalf("evidence receipt: want sequence 2, got %d", evidenced.Sequence)
	}
	_, closed, err := service.Close(ctx, "obj-service")
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if closed.Sequence != 3 {
		t.Fatalf("close receipt: want sequence 3, got %d", closed.Sequence)
	}

	// A second store over the same database (a "restarted process") sees the
	// same durable history — the property MemoryObjectiveStore cannot provide.
	reopened, err := NewPostgresObjectiveStore(db)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	receipts, err := reopened.ListReceipts(ctx, "obj-service")
	if err != nil {
		t.Fatalf("ListReceipts after reopen: %v", err)
	}
	if len(receipts) != 3 {
		t.Fatalf("want 3 durable receipts, got %d", len(receipts))
	}
	wantTypes := []ReceiptType{ReceiptObjectiveOpened, ReceiptEvidenceRecorded, ReceiptObjectiveClosed}
	for i, receipt := range receipts {
		if receipt.Type != wantTypes[i] || receipt.Sequence != i+1 {
			t.Fatalf("receipt %d: got type %s sequence %d", i, receipt.Type, receipt.Sequence)
		}
	}
}
