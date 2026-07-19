package workerserver_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/capsule/workerserver"
	"kitsoki/internal/capsuletest"
)

// TestWorkerSourceFetchByReference covers POST /v1/capsules/sources/{head}:
// the worker downloads from the referenced URL via the injected fetcher, and
// accepts the source only when the fetched bytes verify against the
// referenced digest and size.
func TestWorkerSourceFetchByReference(t *testing.T) {
	project := capsuletest.Open(t, "clean-repo")
	head := strings.TrimSpace(git(t, project, "rev-parse", "HEAD"))
	bundle, err := executor.GitBundle(context.Background(), project, head, 0)
	if err != nil {
		t.Fatal(err)
	}

	objects := map[string][]byte{"good": bundle.Data, "truncated": bundle.Data[:len(bundle.Data)-10]}
	fetcher := func(_ context.Context, url string, max int64) ([]byte, error) {
		data, ok := objects[strings.TrimPrefix(url, "stub://")]
		if !ok {
			return nil, fmt.Errorf("stub fetch failed for %s", url)
		}
		return data, nil
	}
	worker, err := workerserver.New(workerserver.Config{
		Root:         t.TempDir(),
		Token:        "test-token",
		RequireAuth:  true,
		Capabilities: isolatedTestCapabilities(),
		Runner: func(context.Context, string, executor.Prepared, string) (executor.Result, error) {
			return executor.Result{}, nil
		},
		Environment:   environment.Verifier{},
		SourceFetcher: fetcher,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(worker.Handler())
	t.Cleanup(server.Close)
	client := server.Client()

	post := func(head string, request executor.SourceFetchRequest) *http.Response {
		t.Helper()
		body, _ := json.Marshal(request)
		req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/capsules/sources/"+head, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer test-token")
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	good := executor.SourceFetchRequest{Schema: executor.SourceFetchSchema, URL: "stub://good", Digest: bundle.Digest, Size: bundle.Size}

	if response := post(head, executor.SourceFetchRequest{Schema: "bogus/v1", URL: "stub://good", Digest: bundle.Digest, Size: bundle.Size}); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad schema status = %d", readStatus(t, response))
	}
	if response := post(head, executor.SourceFetchRequest{Schema: executor.SourceFetchSchema, URL: "stub://missing", Digest: bundle.Digest, Size: bundle.Size}); response.StatusCode != http.StatusBadGateway {
		t.Fatalf("fetch failure status = %d", readStatus(t, response))
	}
	if response := post(head, executor.SourceFetchRequest{Schema: executor.SourceFetchSchema, URL: "stub://truncated", Digest: bundle.Digest, Size: bundle.Size}); response.StatusCode != http.StatusBadGateway {
		t.Fatalf("size mismatch status = %d", readStatus(t, response))
	}
	wrongDigest := good
	wrongDigest.Digest = "sha256:" + strings.Repeat("0", 64)
	if response := post(head, wrongDigest); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("digest mismatch status = %d", readStatus(t, response))
	}

	response := post(head, good)
	raw := readAndClose(t, response)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("fetch status = %d: %s", response.StatusCode, raw)
	}
	var meta workerserver.SourceMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Head != head || meta.BundleDigest != bundle.Digest {
		t.Fatalf("source meta = %+v", meta)
	}

	// The stored source is now a cache hit for the controller's HEAD probe.
	headReq, _ := http.NewRequest(http.MethodHead, server.URL+"/v1/capsules/sources/"+head, nil)
	headReq.Header.Set("Authorization", "Bearer test-token")
	headResponse, err := client.Do(headReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = headResponse.Body.Close()
	if headResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("head-after-fetch status = %d", headResponse.StatusCode)
	}
}

func readStatus(t *testing.T, response *http.Response) int {
	t.Helper()
	readAndClose(t, response)
	return response.StatusCode
}
