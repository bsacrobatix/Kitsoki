package ci

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
)

type launcher func(context.Context, executor.Prepared) (Verdict, error)

func (f launcher) Launch(ctx context.Context, p executor.Prepared) (Verdict, error) { return f(ctx, p) }
func TestServiceRunsTypedStoryVerdictWithFakeExecutor(t *testing.T) {
	root := t.TempDir()
	requireFiles(t, root)
	service := Service{ProjectRoot: root, Jobs: artifactjob.NewMemoryStore(), Env: environment.Resolver{Probe: environment.ToolProbeFunc(func(context.Context, string) (string, error) { return "go1.25", nil })}, Provider: executor.NewFakeProvider("fake"), Launcher: launcher(func(_ context.Context, p executor.Prepared) (Verdict, error) {
		return Verdict{Schema: VerdictSchema, Pipeline: "change", Outcome: "passed", Checks: []Check{{ID: "test", Kind: "deterministic", Outcome: "passed", Evidence: []string{"artifact:test"}}}, PromotionEligible: true, SourceDigest: p.Envelope.SourceDigest, StoryDigest: p.Envelope.StoryDigest, EnvironmentDigest: p.Envelope.Environment.Digest, EnvelopeDigest: p.Envelope.Digest}, nil
	})}
	result, err := service.Run(context.Background(), RunRequest{Pipeline: "change", Workspace: control.Handle{ID: "w", Generation: 1}, DefinitionDigest: "sha256:def", SourceDigest: "sha256:source", StoryDigest: "sha256:story", Trigger: Trigger{Kind: "local"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Job.Status != artifactjob.StatusDone || !result.Verdict.PromotionEligible {
		t.Fatalf("result %#v", result)
	}
}

func TestServiceSelectsTheDeclaredPipelineExecutor(t *testing.T) {
	root := t.TempDir()
	requireFiles(t, root)
	raw, err := os.ReadFile(filepath.Join(root, ".kitsoki", "ci.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("\n    executor: remote-fake\n")...)
	if err := os.WriteFile(filepath.Join(root, ".kitsoki", "ci.yaml"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	service := Service{ProjectRoot: root, Jobs: artifactjob.NewMemoryStore(), Env: environment.Resolver{Probe: environment.ToolProbeFunc(func(context.Context, string) (string, error) { return "go1.25", nil })}, Executors: NewBuiltinExecutors(), Launcher: launcher(func(_ context.Context, p executor.Prepared) (Verdict, error) {
		return Verdict{Schema: VerdictSchema, Pipeline: "change", Outcome: "passed", Checks: []Check{{ID: "test", Kind: "deterministic", Outcome: "passed", Evidence: []string{"artifact:test"}}}, PromotionEligible: true, SourceDigest: p.Envelope.SourceDigest, StoryDigest: p.Envelope.StoryDigest, EnvironmentDigest: p.Envelope.Environment.Digest, EnvelopeDigest: p.Envelope.Digest}, nil
	})}
	result, err := service.Run(context.Background(), RunRequest{Pipeline: "change", Workspace: control.Handle{ID: "w", Generation: 1}, DefinitionDigest: "sha256:def", SourceDigest: "sha256:source", StoryDigest: "sha256:story", Trigger: Trigger{Kind: "local"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Execution.ExecutionID == "" || result.Execution.ExecutionID[:7] != "remote-" {
		t.Fatalf("selected execution %#v", result.Execution)
	}
}

func TestServiceAcceptsTypedVerdictFromRemoteWorkerWithoutLocalLauncher(t *testing.T) {
	root := t.TempDir()
	requireFiles(t, root)
	raw, err := os.ReadFile(filepath.Join(root, ".kitsoki", "ci.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("\n    executor: remote\nremotes:\n  remote:\n    endpoint: https://worker.invalid\n    credential_env: KITSOKI_TEST_REMOTE_TOKEN\n")...)
	if err := os.WriteFile(filepath.Join(root, ".kitsoki", "ci.yaml"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KITSOKI_TEST_REMOTE_TOKEN", "secret-token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/capsules/capabilities":
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": executor.Capabilities{ID: "remote", Placements: []string{"remote"}, Networks: []string{"none"}, Cancellable: true}})
		case "/v1/capsules/run":
			if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
				t.Errorf("authorization %q", got)
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "secret-token") {
				t.Fatal("credential leaked into remote payload")
			}
			var req struct {
				Prepared executor.Prepared `json:"prepared"`
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Fatal(err)
			}
			verdict := Verdict{Schema: VerdictSchema, Pipeline: "change", Outcome: "passed", Checks: []Check{{ID: "test", Kind: "deterministic", Outcome: "passed", Evidence: []string{"artifact:test"}}}, PromotionEligible: true, SourceDigest: req.Prepared.Envelope.SourceDigest, StoryDigest: req.Prepared.Envelope.StoryDigest, EnvironmentDigest: req.Prepared.Envelope.Environment.Digest, EnvelopeDigest: req.Prepared.Envelope.Digest}
			raw, _ := json.Marshal(verdict)
			_ = json.NewEncoder(w).Encode(map[string]any{"result": executor.Result{VerdictArtifact: "artifact:verdict", VerdictJSON: raw}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	service := Service{ProjectRoot: root, Jobs: artifactjob.NewMemoryStore(), Env: environment.Resolver{Probe: environment.ToolProbeFunc(func(context.Context, string) (string, error) { return "go1.25", nil })}, Executors: ConfiguredExecutors{Builtins: NewBuiltinExecutors(), Remotes: cfg.Remotes, Client: ciRewriteClient(t, server)}}
	result, err := service.Run(context.Background(), RunRequest{Pipeline: "change", Workspace: control.Handle{ID: "w", Generation: 1}, DefinitionDigest: "sha256:def", SourceDigest: "sha256:source", StoryDigest: "sha256:story", Trigger: Trigger{Kind: "local"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict.Outcome != "passed" || result.Execution.ExecutionID == "" {
		t.Fatalf("remote result %#v", result)
	}
}

func TestValidateRejectsUndeclaredRemoteExecutor(t *testing.T) {
	root := t.TempDir()
	requireFiles(t, root)
	raw, err := os.ReadFile(filepath.Join(root, ".kitsoki", "ci.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("\n    executor: remote\n")...)
	if err := os.WriteFile(filepath.Join(root, ".kitsoki", "ci.yaml"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "executor \"remote\" is not configured") {
		t.Fatalf("expected undeclared executor error, got %v", err)
	}
}

func ciRewriteClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &http.Client{Transport: ciRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		request.URL.Scheme = "http"
		request.URL.Host = strings.TrimPrefix(server.URL, "http://")
		return transport.RoundTrip(request)
	})}
}

type ciRoundTripFunc func(*http.Request) (*http.Response, error)

func (f ciRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }
func requireFiles(t *testing.T, root string) {
	t.Helper()
	for path, raw := range map[string]string{".kitsoki/environments/ci.yaml": "schema: capsule-environment/v1\nid: ci\nsource:\n  host_probe: true\ntoolchains:\n  go: '1.25'\n", ".kitsoki/ci.yaml": "schema: capsule-ci/v1\ndefault_environment: ci\npipelines:\n  change:\n    story: .kitsoki/stories/ci/app.yaml\n    triggers: [local]\n    result:\n      schema: capsule-ci-verdict/v1\n", ".kitsoki/stories/ci/app.yaml": "app:\n  id: ci\nrooms:\n  idle:\n    view: ok\n"} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFileRunStorePersistsCompletedRun(t *testing.T) {
	store := FileRunStore{ProjectRoot: t.TempDir()}
	want := RunRecord{JobID: "job-1", Result: RunResult{Job: artifactjob.Job{ID: "job-1"}, Verdict: Verdict{Schema: VerdictSchema, Outcome: "passed"}}}
	if err := store.Write(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("job-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Result.Verdict.Outcome != "passed" {
		t.Fatalf("record %#v", got)
	}
}

func TestFileRunStoreListSkipsReceiptSidecars(t *testing.T) {
	root := t.TempDir()
	store := FileRunStore{ProjectRoot: root}
	if err := store.Write(RunRecord{JobID: "job", Result: RunResult{Job: artifactjob.Job{ID: "job"}}}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".capsules", "ci")
	if err := os.WriteFile(filepath.Join(dir, "job.receipt.json"), []byte(`{"job_id":"job"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].JobID != "job" {
		t.Fatalf("records %#v", got)
	}
}

func TestFileRunStoreCancelParksOrRunningJob(t *testing.T) {
	store := FileRunStore{ProjectRoot: t.TempDir()}
	if err := store.Write(RunRecord{JobID: "job-cancel", Result: RunResult{Job: artifactjob.Job{ID: "job-cancel", Status: artifactjob.StatusAwaitingInput}, Verdict: Verdict{Outcome: "needs_input"}}}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Cancel("job-cancel")
	if err != nil {
		t.Fatal(err)
	}
	if got.Result.Job.Status != artifactjob.StatusCancelled || got.Result.Verdict.Outcome != "cancelled" {
		t.Fatalf("cancel %#v", got)
	}
}
