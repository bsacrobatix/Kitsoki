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

func TestValidateRequiresReceiptSignerWhenSignaturesAreRequired(t *testing.T) {
	root := t.TempDir()
	requireFiles(t, root)
	raw, err := os.ReadFile(filepath.Join(root, ".kitsoki", "ci.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("\nreceipt:\n  require_signature: true\n")...)
	if err := os.WriteFile(filepath.Join(root, ".kitsoki", "ci.yaml"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "receipt signer is required") {
		t.Fatalf("expected signer policy error, got %v", err)
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

func TestFileRunStoreIndexProjectsReceiptAndDigestSummary(t *testing.T) {
	root := t.TempDir()
	store := FileRunStore{ProjectRoot: root}
	job := artifactjob.Job{ID: "job", Status: artifactjob.StatusDone, Story: "stories/capsule-ci/app.yaml", WorkspaceInstanceID: "workspace-1"}
	result := RunResult{
		Job: job,
		Envelope: executor.Envelope{
			Digest:       "sha256:envelope",
			SourceDigest: "sha256:source",
			StoryDigest:  "sha256:story",
			Environment:  environment.Lock{Digest: "sha256:env"},
		},
		Verdict: Verdict{Pipeline: "change", Outcome: "passed", PromotionEligible: true},
	}
	if err := store.Write(RunRecord{JobID: "job", Result: result, ReceiptID: "sha256:receipt", ReceiptVerification: "valid"}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".capsules", "ci")
	if err := os.WriteFile(filepath.Join(dir, "job.receipt.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "job.trace.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	index, err := store.Index()
	if err != nil {
		t.Fatal(err)
	}
	if index.Schema != RunIndexSchema || len(index.Runs) != 1 {
		t.Fatalf("index %#v", index)
	}
	run := index.Runs[0]
	if run.ReceiptID != "sha256:receipt" || run.ReceiptVerification != "valid" || !run.PromotionEligible {
		t.Fatalf("receipt projection %#v", run)
	}
	if run.SourceDigest != "sha256:source" || run.StoryDigest != "sha256:story" || run.EnvironmentDigest != "sha256:env" || run.EnvelopeDigest != "sha256:envelope" {
		t.Fatalf("digest projection %#v", run)
	}
	if run.TracePath != ".capsules/ci/job.trace.json" || run.ReceiptPath != ".capsules/ci/job.receipt.json" {
		t.Fatalf("sidecar projection %#v", run)
	}
}

func TestFileRunStoreProviderSummaryUsesRunIndexEvidence(t *testing.T) {
	root := t.TempDir()
	store := FileRunStore{ProjectRoot: root}
	writeRun := func(id, outcome, receiptStatus string, eligible bool) {
		t.Helper()
		result := RunResult{
			Job:      artifactjob.Job{ID: artifactjob.JobID(id), Status: artifactjob.StatusDone},
			Envelope: executor.Envelope{Digest: "sha256:env-" + id, SourceDigest: "sha256:source-" + id, StoryDigest: "sha256:story-" + id, Environment: environment.Lock{Digest: "sha256:environment-" + id}},
			Verdict:  Verdict{Pipeline: "change", Outcome: outcome, PromotionEligible: eligible},
		}
		if err := store.Write(RunRecord{JobID: id, Result: result, ReceiptID: "sha256:receipt-" + id, ReceiptVerification: receiptStatus}); err != nil {
			t.Fatal(err)
		}
	}
	writeRun("b", "failed", "invalid", false)
	writeRun("a", "passed", "valid", true)
	dir := filepath.Join(root, ".capsules", "ci")
	if err := os.WriteFile(filepath.Join(dir, "a.trace.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	summary, err := store.ProviderSummary(1)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Schema != ProviderSummarySchema || summary.Total != 2 || summary.Passed != 1 || summary.Failed != 1 || summary.PromotionEligible != 1 {
		t.Fatalf("summary %#v", summary)
	}
	if len(summary.Latest) != 1 || summary.Latest[0].JobID != "b" {
		t.Fatalf("latest %#v", summary.Latest)
	}
	if !strings.Contains(summary.Markdown, "Capsule CI: 2 run(s)") || !strings.Contains(summary.Markdown, "| b | change | failed | invalid |") {
		t.Fatalf("markdown %s", summary.Markdown)
	}
	for _, field := range summary.ProviderSafeFields {
		if strings.Contains(field, "secret") {
			t.Fatalf("unsafe provider field %q", field)
		}
	}
}

func TestFileRunStoreIndexEmptyRunsSlice(t *testing.T) {
	index, err := (FileRunStore{ProjectRoot: t.TempDir()}).Index()
	if err != nil {
		t.Fatal(err)
	}
	if index.Runs == nil || len(index.Runs) != 0 {
		t.Fatalf("runs %#v", index.Runs)
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
