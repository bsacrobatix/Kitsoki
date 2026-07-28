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

type fakeCapsuleClient struct {
	dispatched, polled int
	result             CapsuleResult
}

func (f *fakeCapsuleClient) Dispatch(context.Context, json.RawMessage) (CapsuleResult, error) {
	f.dispatched++
	return CapsuleResult{RunRef: "ci_1", ExecutionRef: "remote_1", Status: "running"}, nil
}
func (f *fakeCapsuleClient) Status(context.Context, string) (CapsuleResult, error) {
	f.polled++
	return f.result, nil
}

func TestCapsuleExecutorPersistsDispatchPollsTerminalBundleAndFencesReceipt(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	root := t.TempDir()
	body := []byte("portable work bundle")
	if err := os.WriteFile(filepath.Join(root, "result.tar"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	validator, err := NewFileBundleValidator(root, 1024)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteStore(db, WithBundleValidator(validator))
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.Enqueue(ctx, EnqueueRequest{ApplicationID: "pog", Queue: "bugfix", IdempotencyKey: "issue-1", Payload: []byte(`{"issue":"42"}`), MaxAttempts: 2, ProducesCode: true})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.ClaimNext(ctx, ClaimRequest{ApplicationID: "pog", Queue: "bugfix", WorkerID: "vm", AvailableCapacity: 1, LeaseDuration: time.Minute})
	if err != nil || lease == nil {
		t.Fatalf("claim: %v %#v", err, lease)
	}
	client := &fakeCapsuleClient{result: CapsuleResult{RunRef: "ci_1", ExecutionRef: "remote_1", Status: "passed", BundleRef: "result.tar", BundleDigest: "sha256:" + hex.EncodeToString(sum[:]), BundleKind: "patch"}}
	exec := CapsuleExecutor{Store: store, Client: client, Config: CapsuleExecutorConfig{ApplicationID: "pog", Queue: "bugfix", WorkerID: "vm", ProjectRoot: "/trusted/pog", WorkspaceID: "pog-bugfix", Pipeline: "bugfix", WorkerPolicy: "bugfix", InputSchema: map[string]InputField{"issue": {Type: "string", Required: true}}, MaxConcurrent: 1, LeaseDuration: time.Minute, PollInterval: time.Millisecond}}
	exec.runLease(ctx, lease.Job)
	got, err := store.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateSucceeded || got.Receipt == nil || got.Receipt.BundleRef != "result.tar" || !got.Receipt.Countable {
		t.Fatalf("terminal job = %#v", got)
	}
	if client.dispatched != 1 || client.polled == 0 {
		t.Fatalf("client calls dispatch=%d poll=%d", client.dispatched, client.polled)
	}
	if _, err := store.GetCapsuleDispatch(ctx, job.ID); err != ErrNotFound {
		t.Fatalf("dispatch should be removed after terminal completion: %v", err)
	}
}

func TestCapsuleExecutorResumesPersistedRunWithoutSecondDispatch(t *testing.T) {
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
	job, err := store.Enqueue(ctx, EnqueueRequest{ApplicationID: "app", Queue: "q", IdempotencyKey: "one", Payload: []byte(`{"issue":"42"}`), MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.ClaimNext(ctx, ClaimRequest{ApplicationID: "app", Queue: "q", WorkerID: "vm", AvailableCapacity: 1, LeaseDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutCapsuleDispatch(ctx, CapsuleDispatch{WorkRef: job.ID, RunRef: "ci_recovered", ExecutionRef: "remote", PayloadDigest: string(job.PayloadDigest)}); err != nil {
		t.Fatal(err)
	}
	client := &fakeCapsuleClient{result: CapsuleResult{RunRef: "ci_recovered", Status: "passed"}}
	exec := CapsuleExecutor{Store: store, Client: client, Config: CapsuleExecutorConfig{ApplicationID: "app", Queue: "q", WorkerID: "vm", ProjectRoot: "/trusted", WorkspaceID: "ws", Pipeline: "pipe", WorkerPolicy: "lane", InputSchema: map[string]InputField{"issue": {Type: "string", Required: true}}, MaxConcurrent: 1, LeaseDuration: time.Minute, PollInterval: time.Millisecond}}
	exec.runLease(ctx, lease.Job)
	if client.dispatched != 0 || client.polled != 1 {
		t.Fatalf("restart must poll durable run only: %#v", client)
	}
}
