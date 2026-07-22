package workerserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/storydigest"
	"kitsoki/internal/capsule/workerserver"
	"kitsoki/internal/capsuletest"
)

// TestWorkerDetachedRunRegistersImmediatelyAndCompletesInBackground covers
// the detach=1 dispatch contract: the run endpoint answers 202 with a durably
// registered non-terminal record while the story executes in the background,
// and GET /executions/{id} converges on the terminal record.
func TestWorkerDetachedRunRegistersImmediatelyAndCompletesInBackground(t *testing.T) {
	project := capsuletest.Open(t, "clean-repo")
	storyRel := filepath.ToSlash(filepath.Join(".kitsoki", "stories", "ci", "app.yaml"))
	write(t, filepath.Join(project, storyRel), passingStory)
	envRel := writeEnvironment(t, project)
	git(t, project, "add", storyRel, envRel)
	git(t, project, "-c", "user.name=Capsule Test", "-c", "user.email=capsule@example.invalid", "commit", "-m", "Add no-LLM CI story")
	head := strings.TrimSpace(git(t, project, "rev-parse", "HEAD"))
	closure, err := storydigest.Compute(project, storyRel)
	if err != nil {
		t.Fatal(err)
	}
	envLock, err := (environment.Resolver{ProjectRoot: project}).Resolve(context.Background(), "ci")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := executor.GitBundle(context.Background(), project, head, 0)
	if err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	runner := func(ctx context.Context, workspace string, prepared executor.Prepared, _ string) (executor.Result, error) {
		// Block until the test has observed the non-terminal 202 record, so
		// the assertion cannot race a fast story.
		select {
		case <-release:
		case <-ctx.Done():
			return executor.Result{}, ctx.Err()
		}
		return executor.Result{ExitCode: 0, VerdictArtifact: "verdict:detached-test", VerdictJSON: []byte(`{"schema":"capsule-ci-verdict/v1"}`)}, nil
	}
	worker, err := workerserver.New(workerserver.Config{Root: t.TempDir(), Token: "test-token", RequireAuth: true, Capabilities: isolatedTestCapabilities(), Runner: runner, Environment: environment.Verifier{}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(worker.Handler())
	t.Cleanup(server.Close)
	client := server.Client()

	upload, _ := http.NewRequest(http.MethodPut, server.URL+"/v1/capsules/sources/"+head, bytes.NewReader(bundle.Data))
	upload.Header.Set("Authorization", "Bearer test-token")
	upload.Header.Set("X-Kitsoki-Bundle-Digest", bundle.Digest)
	uploadResponse, err := client.Do(upload)
	if err != nil {
		t.Fatal(err)
	}
	readAndClose(t, uploadResponse)
	if uploadResponse.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d", uploadResponse.StatusCode)
	}

	envelope, err := executor.Seal(executor.Envelope{
		JobID:            "job-detached-e2e",
		ProjectID:        "worker-test",
		DefinitionDigest: "sha256:def",
		Instance:         control.Handle{ID: "workspace", Generation: 1},
		SourceDigest:     head,
		StoryPath:        storyRel,
		StoryDigest:      closure.Digest,
		Environment:      envLock,
		Trigger:          map[string]any{"kind": "local", "requested_pipeline": "change"},
		Policy:           executor.Policy{Network: "none", ExternalWrite: "deny"},
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared := executor.Prepared{ID: "detached-e2e", Envelope: envelope, Placement: "remote", Applied: envelope.Policy}
	body, _ := json.Marshal(map[string]any{"prepared": prepared})
	runRequest, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/capsules/run?detach=1", bytes.NewReader(body))
	runRequest.Header.Set("Authorization", "Bearer test-token")
	runRequest.Header.Set("Content-Type", "application/json")
	runResponse, err := client.Do(runRequest)
	if err != nil {
		t.Fatal(err)
	}
	raw := readAndClose(t, runResponse)
	if runResponse.StatusCode != http.StatusAccepted {
		t.Fatalf("detached run status = %d: %s", runResponse.StatusCode, raw)
	}
	var accepted struct {
		Run workerserver.RunRecord `json:"run"`
	}
	if err := json.Unmarshal(raw, &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.Run.ExecutionID != prepared.ID || accepted.Run.Status != "running" || accepted.Run.Stage != "registered" {
		t.Fatalf("accepted run = %+v", accepted.Run)
	}

	// A duplicate detached dispatch while the first is active conflicts.
	dupRequest, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/capsules/run?detach=1", bytes.NewReader(body))
	dupRequest.Header.Set("Authorization", "Bearer test-token")
	dupRequest.Header.Set("Content-Type", "application/json")
	dupResponse, err := client.Do(dupRequest)
	if err != nil {
		t.Fatal(err)
	}
	dupRaw := readAndClose(t, dupResponse)
	if dupResponse.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate detached run status = %d: %s", dupResponse.StatusCode, dupRaw)
	}

	close(release)
	deadline := time.Now().Add(30 * time.Second)
	for {
		statusRequest, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/capsules/executions/"+prepared.ID, nil)
		statusRequest.Header.Set("Authorization", "Bearer test-token")
		statusResponse, err := client.Do(statusRequest)
		if err != nil {
			t.Fatal(err)
		}
		statusRaw := readAndClose(t, statusResponse)
		var current struct {
			Run workerserver.RunRecord `json:"run"`
		}
		if err := json.Unmarshal(statusRaw, &current); err != nil {
			t.Fatal(err)
		}
		if current.Run.Status == "completed" {
			if current.Run.Stage != "terminal" || current.Run.Result.ExecutionID != prepared.ID {
				t.Fatalf("terminal run = %+v", current.Run)
			}
			return
		}
		if current.Run.Status == "failed" || current.Run.Status == "cancelled" {
			t.Fatalf("detached run terminalized unexpectedly: %s", statusRaw)
		}
		if time.Now().After(deadline) {
			t.Fatalf("detached run never completed: %s", statusRaw)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestHTTPRemoteWorkerStartDetachedPublishesSourceAndReturnsRegistered
// exercises the controller-side transport for detached dispatch end to end:
// source publish via the SourceBundler, POST run?detach=1, and the normalized
// non-terminal execution status.
func TestHTTPRemoteWorkerStartDetachedPublishesSourceAndReturnsRegistered(t *testing.T) {
	project := capsuletest.Open(t, "clean-repo")
	storyRel := filepath.ToSlash(filepath.Join(".kitsoki", "stories", "ci", "app.yaml"))
	write(t, filepath.Join(project, storyRel), passingStory)
	envRel := writeEnvironment(t, project)
	git(t, project, "add", storyRel, envRel)
	git(t, project, "-c", "user.name=Capsule Test", "-c", "user.email=capsule@example.invalid", "commit", "-m", "Add no-LLM CI story")
	head := strings.TrimSpace(git(t, project, "rev-parse", "HEAD"))
	closure, err := storydigest.Compute(project, storyRel)
	if err != nil {
		t.Fatal(err)
	}
	envLock, err := (environment.Resolver{ProjectRoot: project}).Resolve(context.Background(), "ci")
	if err != nil {
		t.Fatal(err)
	}

	// The worker root must be created BEFORE the release/drain cleanup is
	// registered. t.Cleanup runs LIFO, so registering the drain afterwards is
	// what guarantees it runs BEFORE the TempDir RemoveAll. With the reverse
	// order the detached run is still extracting the source bundle into
	// <root>/runs/remote-detach/workspace/.git while RemoveAll walks it, and
	// cleanup fails with "directory not empty".
	root := t.TempDir()
	release := make(chan struct{})
	finished := make(chan struct{})
	runner := func(ctx context.Context, _ string, _ executor.Prepared, _ string) (executor.Result, error) {
		defer close(finished)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return executor.Result{ExitCode: 0}, nil
	}
	t.Cleanup(func() {
		close(release)
		// Wait for the detached run to actually return, not just to be
		// signalled — RemoveAll races the run's own writes, not the channel.
		select {
		case <-finished:
		case <-time.After(30 * time.Second):
			t.Error("detached runner did not finish before cleanup")
		}
	})
	worker, err := workerserver.New(workerserver.Config{Root: root, Token: "test-token", RequireAuth: true, Capabilities: isolatedTestCapabilities(), Runner: runner, Environment: environment.Verifier{}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(worker.Handler())
	t.Cleanup(server.Close)

	envelope, err := executor.Seal(executor.Envelope{
		JobID:            "job-remote-detach",
		ProjectID:        "worker-test",
		DefinitionDigest: "sha256:def",
		Instance:         control.Handle{ID: "workspace", Generation: 1},
		SourceDigest:     head,
		StoryPath:        storyRel,
		StoryDigest:      closure.Digest,
		Environment:      envLock,
		Trigger:          map[string]any{"kind": "local", "requested_pipeline": "change"},
		Policy:           executor.Policy{Network: "none", ExternalWrite: "deny"},
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared := executor.Prepared{ID: "remote-detach", Envelope: envelope, Placement: "remote", Applied: envelope.Policy}

	remote := executor.HTTPRemoteWorker{
		Endpoint:   server.URL,
		Client:     server.Client(),
		Credential: func(context.Context) (string, error) { return "test-token", nil },
		Source: executor.SourceBundlerFunc(func(ctx context.Context, e executor.Envelope) (executor.SourceBundle, error) {
			return executor.GitBundle(ctx, project, e.SourceDigest, 0)
		}),
	}
	status, err := remote.StartDetached(context.Background(), prepared, nil)
	if err != nil {
		t.Fatal(err)
	}
	if status.ExecutionID != prepared.ID || status.Status != "running" || status.Stage != "registered" {
		t.Fatalf("detached status = %+v", status)
	}
}
