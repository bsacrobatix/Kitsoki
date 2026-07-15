package daemonfederation

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestHTTPClientListJobs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rpc" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":"federation","result":[{"job_id":"same","app_id":"bugfix","story":"stories/bugfix/app.yaml","status":"running","run_url":"/s/same","updated_at":"2026-07-15T00:00:00Z"}]}`)
	}))
	defer server.Close()

	jobs, err := (HTTPClient{}).ListJobs(context.Background(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].JobID != "same" {
		t.Fatalf("jobs = %#v", jobs)
	}
}

func TestHTTPClientRejectsRPCErrorAndMalformedResponse(t *testing.T) {
	for name, body := range map[string]string{
		"rpc error": `{"jsonrpc":"2.0","error":{"message":"not ready"}}`,
		"malformed": `{"result":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()
			if _, err := (HTTPClient{}).ListJobs(context.Background(), server.URL); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

type fakeClient struct {
	mu    sync.Mutex
	calls int
	fail  map[string]bool
}

func (f *fakeClient) ListJobs(_ context.Context, endpoint string) ([]Job, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if endpoint == "http://127.0.0.1:17778" || f.fail[endpoint] {
		return nil, errors.New("dial timeout with private detail that stays bounded")
	}
	return []Job{{JobID: "same", RunURL: "/s/same"}}, nil
}

func TestPoolRetainsKnownWorkerSnapshotAsDegraded(t *testing.T) {
	endpoint := "http://127.0.0.1:17777"
	client := &fakeClient{fail: map[string]bool{}}
	now := time.Date(2026, 7, 15, 1, 2, 3, 0, time.UTC)
	pool := &Pool{
		Workers: []Worker{{ID: "known", Label: "Known", Placement: PlacementThin, Endpoint: endpoint}},
		Client:  client, Now: func() time.Time { return now }, TTL: time.Second,
	}
	first := pool.Get(context.Background())
	client.fail[endpoint] = true
	now = now.Add(2 * time.Second)
	second := pool.Get(context.Background())
	if second.Workers[0].Health != HealthDegraded || second.Workers[0].LastSeen != first.Workers[0].LastSeen {
		t.Fatalf("worker = %#v", second.Workers[0])
	}
	if !reflect.DeepEqual(second.Jobs, first.Jobs) {
		t.Fatalf("degraded jobs = %#v, want %#v", second.Jobs, first.Jobs)
	}
}

func TestResolveOpenURLRejectsWorkerSuppliedAbsoluteURL(t *testing.T) {
	if got := resolveOpenURL("http://127.0.0.1:17777", "https://other.example/s/job"); got != "" {
		t.Fatalf("resolveOpenURL = %q", got)
	}
}

func TestPoolPartialOutageAndCachedSnapshot(t *testing.T) {
	client := &fakeClient{}
	now := time.Date(2026, 7, 15, 1, 2, 3, 0, time.UTC)
	pool := &Pool{
		Workers: []Worker{
			{ID: "thin-one", Label: "Thin one", Placement: PlacementThin, Endpoint: "http://127.0.0.1:17777"},
			{ID: "gpu-one", Label: "GPU one", Placement: PlacementLocalModel, Endpoint: "http://127.0.0.1:17778"},
		},
		Client: client,
		Now:    func() time.Time { return now },
		TTL:    time.Minute,
	}

	first := pool.Get(context.Background())
	second := pool.Get(context.Background())
	if client.calls != 2 {
		t.Fatalf("calls = %d, want one call per worker despite two snapshots", client.calls)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("cached snapshot changed: %#v != %#v", first, second)
	}
	if len(first.Jobs) != 1 || first.Jobs[0].JobID != "same" || first.Jobs[0].WorkerID != "thin-one" {
		t.Fatalf("jobs = %#v", first.Jobs)
	}
	if first.Jobs[0].OpenURL != "http://127.0.0.1:17777/s/same" {
		t.Fatalf("open_url = %q", first.Jobs[0].OpenURL)
	}
	if first.Workers[0].Health != "online" || first.Workers[1].Health != "offline" || first.Workers[1].LastError == "" {
		t.Fatalf("workers = %#v", first.Workers)
	}
}

func TestConfigValidateAndSSHArgs(t *testing.T) {
	config := Config{Workers: []Worker{{
		ID: "build-vm", Label: "Build VM", Placement: PlacementThin,
		Endpoint: "http://127.0.0.1:17777",
		Tunnel:   &Tunnel{Host: "worker.example", User: "kitsoki", LocalPort: 17777, RemoteHost: "127.0.0.1", RemotePort: 7777, IdentityFile: "keys/worker", KnownHostsFile: "keys/known_hosts"},
	}}}
	if err := config.Validate("/project/.kitsoki.local.yaml"); err != nil {
		t.Fatal(err)
	}
	got := config.Workers[0].Tunnel.SSHArgs()
	wantTail := []string{"-i", "/project/keys/worker", "-L", "127.0.0.1:17777:127.0.0.1:7777", "kitsoki@worker.example"}
	if !reflect.DeepEqual(got[len(got)-len(wantTail):], wantTail) {
		t.Fatalf("args tail = %#v", got)
	}
	for _, required := range []string{"BatchMode=yes", "ExitOnForwardFailure=yes", "StrictHostKeyChecking=yes", "UserKnownHostsFile=/project/keys/known_hosts"} {
		if !contains(got, required) {
			t.Fatalf("args missing %q: %#v", required, got)
		}
	}
}

func TestConfigRejectsDuplicateTunnelPort(t *testing.T) {
	worker := func(id string) Worker {
		return Worker{ID: id, Label: id, Placement: PlacementThin, Endpoint: "http://127.0.0.1:17777", Tunnel: &Tunnel{Host: "vm", LocalPort: 17777, RemoteHost: "127.0.0.1", RemotePort: 7777, IdentityFile: "/key", KnownHostsFile: "/known"}}
	}
	config := Config{Workers: []Worker{worker("one"), worker("two")}}
	if err := config.Validate("/project/.kitsoki.local.yaml"); err == nil {
		t.Fatal("expected duplicate port error")
	}
}

func TestConfigRejectsReservedLocalWorkerID(t *testing.T) {
	config := Config{Workers: []Worker{{
		ID: "local", Label: "Remote local", Placement: PlacementThin,
		Endpoint: "http://127.0.0.1:17777",
	}}}
	if err := config.Validate("/project/.kitsoki.local.yaml"); err == nil {
		t.Fatal("expected reserved local id error")
	}
}

type blockingClient struct {
	started chan struct{}
	release chan struct{}
	calls   int
	mu      sync.Mutex
}

func (c *blockingClient) ListJobs(context.Context, string) ([]Job, error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.mu.Unlock()
	if call == 1 {
		return []Job{{JobID: "cached", RunURL: "/s/cached"}}, nil
	}
	close(c.started)
	<-c.release
	return []Job{{JobID: "fresh", RunURL: "/s/fresh"}}, nil
}

func TestPoolReturnsStaleSnapshotWhileRefreshIsRunning(t *testing.T) {
	now := time.Date(2026, 7, 15, 1, 2, 3, 0, time.UTC)
	client := &blockingClient{started: make(chan struct{}), release: make(chan struct{})}
	pool := &Pool{
		Workers: []Worker{{ID: "worker", Label: "Worker", Placement: PlacementThin, Endpoint: "http://127.0.0.1:17777"}},
		Client:  client, Now: func() time.Time { return now }, TTL: time.Second,
	}
	first := pool.Get(context.Background())
	now = now.Add(2 * time.Second)
	refreshed := make(chan Snapshot, 1)
	go func() { refreshed <- pool.Get(context.Background()) }()
	<-client.started

	stale := pool.Get(context.Background())
	if !reflect.DeepEqual(stale, first) {
		t.Fatalf("stale snapshot = %#v, want %#v", stale, first)
	}
	close(client.release)
	if got := <-refreshed; len(got.Jobs) != 1 || got.Jobs[0].JobID != "fresh" {
		t.Fatalf("refreshed snapshot = %#v", got)
	}
}

type cancellationClient struct{}

func (cancellationClient) ListJobs(ctx context.Context, _ string) ([]Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []Job{{JobID: "shared", RunURL: "/s/shared"}}, nil
}

func TestPoolRefreshOutlivesInitiatingRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pool := &Pool{
		Workers: []Worker{{ID: "worker", Label: "Worker", Placement: PlacementThin, Endpoint: "http://127.0.0.1:17777"}},
		Client:  cancellationClient{},
	}
	snapshot := pool.Get(ctx)
	if len(snapshot.Jobs) != 1 || snapshot.Workers[0].Health != HealthOnline {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
