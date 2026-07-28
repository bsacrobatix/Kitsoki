package workqueue

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

type applicationJobClientFunc struct {
	result ApplicationJobResult
	err    error
}

func (f applicationJobClientFunc) Submit(context.Context, string, string, json.RawMessage) (ApplicationJobResult, error) {
	return f.result, f.err
}
func (f applicationJobClientFunc) Status(context.Context, string, string) (ApplicationJobResult, error) {
	return f.result, f.err
}

func TestApplicationExecutorCompletesFencedReceipt(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewSQLiteStore(db)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(ctx, EnqueueRequest{ApplicationID: "pog", Queue: "feedback", IdempotencyKey: "one", Payload: []byte(`{"feedback_ref":"f1"}`), MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.ClaimNext(ctx, ClaimRequest{ApplicationID: "pog", Queue: "feedback", WorkerID: "runner", AvailableCapacity: 1, LeaseDuration: time.Minute})
	if err != nil || lease == nil {
		t.Fatalf("claim = %#v, %v", lease, err)
	}
	executor := ApplicationExecutor{Store: store, Jobs: applicationJobClientFunc{result: ApplicationJobResult{Ref: "aj_123", Status: "done", Artifacts: []string{"bundle:abc"}}}, Config: ApplicationExecutorConfig{ApplicationID: "pog", Queue: "feedback", WorkerID: "runner", CallerApplication: "queue-adapter", Template: "pog-bugfix", MaxConcurrent: 1, LeaseDuration: time.Minute, PollInterval: time.Second}}
	executor.runLease(ctx, lease.Job)
	done, err := store.Get(ctx, job.ID)
	if err != nil || done.State != StateSucceeded || done.Receipt == nil || done.Receipt.TraceRef != "aj_123" || len(done.Receipt.ArtifactHandles) != 1 {
		t.Fatalf("done = %#v, %v", done, err)
	}
}

func TestApplicationExecutorRequiresConfiguredBundleForCodeReceipt(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	bundle := []byte("recoverable bundle")
	if err := os.WriteFile(filepath.Join(root, "work.bundle"), bundle, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(bundle)
	validator, err := NewFileBundleValidator(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := NewSQLiteStore(db, WithBundleValidator(validator))
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(ctx, EnqueueRequest{ApplicationID: "pog", Queue: "bugfix", IdempotencyKey: "one", Payload: []byte(`{}`), ProducesCode: true, MaxAttempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.ClaimNext(ctx, ClaimRequest{ApplicationID: "pog", Queue: "bugfix", WorkerID: "runner", AvailableCapacity: 1, LeaseDuration: time.Minute})
	if err != nil || lease == nil {
		t.Fatalf("claim = %#v, %v", lease, err)
	}
	executor := ApplicationExecutor{Store: store, Jobs: applicationJobClientFunc{result: ApplicationJobResult{Ref: "aj_123", Status: "done", BundleRef: "work.bundle", BundleDigest: "sha256:" + hex.EncodeToString(sum[:]), BundleKind: "patch"}}, Config: ApplicationExecutorConfig{ApplicationID: "pog", Queue: "bugfix", WorkerID: "runner", CallerApplication: "queue-adapter", Template: "pog-bugfix", MaxConcurrent: 1, LeaseDuration: time.Minute, PollInterval: time.Second}}
	executor.runLease(ctx, lease.Job)
	done, err := store.Get(ctx, job.ID)
	if err != nil || done.State != StateSucceeded || done.Receipt == nil || !done.Receipt.Countable || done.Receipt.BundleRef != "work.bundle" {
		t.Fatalf("done = %#v, %v", done, err)
	}
}
