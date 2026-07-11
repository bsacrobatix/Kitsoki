package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
)

func TestHTTPRemoteWorkerSendsSealedEnvelopeWithoutCredentialAndReturnsResult(t *testing.T) {
	var sawRun bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/capsules/capabilities":
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": Capabilities{ID: "remote", Placements: []string{"remote"}, Networks: []string{"none"}, Cancellable: true}})
		case "/v1/capsules/run":
			sawRun = true
			if got := r.Header.Get("X-Kitsoki-Request-ID"); !strings.HasPrefix(got, "req-") {
				t.Errorf("request id %q", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer token" {
				t.Errorf("authorization %q", got)
			}
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "token") {
				t.Fatal("credential leaked into remote payload")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": Result{ExitCode: 0, VerdictArtifact: "artifact:verdict", VerdictJSON: []byte(`{"schema":"capsule-ci-verdict/v1"}`), Artifacts: []string{"b", "a"}}})
		case "/v1/capsules/executions/run/1":
			if r.Method != http.MethodDelete {
				t.Errorf("method %s", r.Method)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	worker := HTTPRemoteWorker{Endpoint: "https://worker.invalid", Client: rewriteClient(t, server), Credential: func(context.Context) (string, error) { return "token", nil }}
	capabilities, err := worker.Describe(context.Background())
	if err != nil || capabilities.ID != "remote" {
		t.Fatalf("capabilities %#v: %v", capabilities, err)
	}
	envelope, err := Seal(Envelope{JobID: "job", ProjectID: "project", DefinitionDigest: "sha256:def", Instance: control.Handle{ID: "w", Generation: 1}, SourceDigest: "sha256:source", StoryDigest: "sha256:story", Environment: environment.Lock{Schema: environment.LockSchema, ID: "ci", Digest: "sha256:env"}, Policy: Policy{Network: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.Run(context.Background(), Prepared{ID: "run/1", Envelope: envelope}, func(context.Context, Prepared) (Result, error) {
		t.Fatal("remote worker must not run the local task")
		return Result{}, nil
	}, nil)
	if err != nil || !sawRun || result.ExecutionID != "run/1" || len(result.VerdictJSON) == 0 || result.Artifacts[0] != "a" {
		t.Fatalf("result %#v: %v", result, err)
	}
	if err := worker.Cancel(context.Background(), "run/1"); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPRemoteWorkerErrorIncludesBoundedRemoteDiagnostics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Kitsoki-Request-ID", "worker-request-1")
		http.Error(w, "worker failed token=super-secret because setup missing", http.StatusBadGateway)
	}))
	defer server.Close()
	worker := HTTPRemoteWorker{Endpoint: "https://worker.invalid", Client: rewriteClient(t, server)}
	envelope, err := Seal(Envelope{JobID: "job", ProjectID: "project", DefinitionDigest: "sha256:def", Instance: control.Handle{ID: "w", Generation: 1}, SourceDigest: "sha256:source", StoryDigest: "sha256:story", Environment: environment.Lock{Schema: environment.LockSchema, ID: "ci", Digest: "sha256:env"}, Policy: Policy{Network: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = worker.Run(context.Background(), Prepared{ID: "run/1", Envelope: envelope}, nil, nil)
	if err == nil {
		t.Fatal("expected remote error")
	}
	msg := err.Error()
	for _, want := range []string{"kind=status", "host=worker.invalid", "status=502 Bad Gateway", "request_id=worker-request-1", "body=worker failed token=<redacted>"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error missing %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "super-secret") {
		t.Fatalf("error leaked secret body: %s", msg)
	}
}

func rewriteClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		request.URL.Scheme = "http"
		request.URL.Host = strings.TrimPrefix(server.URL, "http://")
		return transport.RoundTrip(request)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
