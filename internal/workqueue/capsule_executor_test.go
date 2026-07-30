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

type failOnceDispatchStore struct {
	Store
	CapsuleDispatchStore
	failed bool
}

func (s *failOnceDispatchStore) PutCapsuleDispatch(ctx context.Context, in CapsuleDispatch) error {
	if !s.failed {
		s.failed = true
		return context.DeadlineExceeded
	}
	return s.CapsuleDispatchStore.PutCapsuleDispatch(ctx, in)
}

type idempotentCapsuleClient struct {
	starts map[string]string
	calls  int
}

func (c *idempotentCapsuleClient) Dispatch(_ context.Context, key string, _ json.RawMessage) (CapsuleResult, error) {
	c.calls++
	if c.starts == nil {
		c.starts = map[string]string{}
	}
	run := c.starts[key]
	if run == "" {
		run = "provider-" + key
		c.starts[key] = run
	}
	return CapsuleResult{RunRef: run, ExecutionRef: run, Status: "passed"}, nil
}

func (c *idempotentCapsuleClient) Status(_ context.Context, run string) (CapsuleResult, error) {
	return CapsuleResult{RunRef: run, ExecutionRef: run, Status: "passed"}, nil
}

type fakeCapsuleClient struct {
	dispatched, polled int
	result             CapsuleResult
}

type fakePromotionSink struct {
	calls []CapsulePromotionRequest
	err   error
}

func (f *fakePromotionSink) Promote(_ context.Context, in CapsulePromotionRequest) (string, error) {
	f.calls = append(f.calls, in)
	return "queue-candidate-1", f.err
}

func (f *fakeCapsuleClient) Dispatch(context.Context, string, json.RawMessage) (CapsuleResult, error) {
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
	promoter := &fakePromotionSink{}
	exec := CapsuleExecutor{Store: store, Client: client, Promoter: promoter, Config: CapsuleExecutorConfig{ApplicationID: "pog", Queue: "bugfix", WorkerID: "vm", ProjectRoot: "/trusted/pog", WorkspaceID: "pog-bugfix", Pipeline: "bugfix", WorkerPolicy: "bugfix", InputSchema: map[string]InputField{"issue": {Type: "string", Required: true}}, MaxConcurrent: 1, LeaseDuration: time.Minute, PollInterval: time.Millisecond}}
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
	if len(promoter.calls) != 1 || promoter.calls[0].Result.BundleRef != "result.tar" {
		t.Fatalf("promotion calls=%#v", promoter.calls)
	}
	if !containsString(got.Receipt.ArtifactHandles, "capsule-queue:queue-candidate-1") {
		t.Fatalf("receipt missing durable queue identity: %#v", got.Receipt)
	}
	if _, err := store.GetCapsuleDispatch(ctx, job.ID); err != ErrNotFound {
		t.Fatalf("dispatch should be removed after terminal completion: %v", err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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

func TestCapsuleExecutorDispatchCrashReusesDeterministicProviderKey(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Unix(100, 0).UTC()
	base, err := NewSQLiteStore(db, WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	job, err := base.Enqueue(ctx, EnqueueRequest{ApplicationID: "app", Queue: "q", IdempotencyKey: "crash", Payload: []byte(`{"issue":"42"}`), MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	store := &failOnceDispatchStore{Store: base, CapsuleDispatchStore: base}
	client := &idempotentCapsuleClient{}
	config := CapsuleExecutorConfig{ApplicationID: "app", Queue: "q", WorkerID: "vm", ProjectRoot: "/trusted", WorkspaceID: "ws", Pipeline: "pipe", WorkerPolicy: "lane", InputSchema: map[string]InputField{"issue": {Type: "string", Required: true}}, MaxConcurrent: 1, LeaseDuration: time.Minute, PollInterval: time.Millisecond}
	first, err := store.ClaimNext(ctx, ClaimRequest{ApplicationID: "app", Queue: "q", WorkerID: "vm", AvailableCapacity: 1, LeaseDuration: time.Minute})
	if err != nil || first == nil {
		t.Fatalf("first claim=%#v err=%v", first, err)
	}
	(CapsuleExecutor{Store: store, Client: client, Config: config}).runLease(ctx, first.Job)
	now = now.Add(2 * time.Second)
	second, err := store.ClaimNext(ctx, ClaimRequest{ApplicationID: "app", Queue: "q", WorkerID: "vm", AvailableCapacity: 1, LeaseDuration: time.Minute})
	if err != nil || second == nil {
		t.Fatalf("second claim=%#v err=%v", second, err)
	}
	(CapsuleExecutor{Store: store, Client: client, Config: config}).runLease(ctx, second.Job)
	got, err := store.Get(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateSucceeded || len(client.starts) != 1 || client.calls != 2 {
		t.Fatalf("job=%#v starts=%v dispatch calls=%d", got, client.starts, client.calls)
	}
}
