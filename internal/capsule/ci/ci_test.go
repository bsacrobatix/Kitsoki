package ci

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
)

type launcher func(context.Context, executor.Prepared) (Verdict, error)

func (f launcher) Launch(ctx context.Context, p executor.Prepared) (Verdict, error) { return f(ctx, p) }

type fixedJobStore struct {
	id    artifactjob.JobID
	inner *artifactjob.MemoryStore
}

func newFixedJobStore(id string) fixedJobStore {
	return fixedJobStore{id: artifactjob.JobID(id), inner: artifactjob.NewMemoryStore()}
}

func (s fixedJobStore) Register(ctx context.Context, req artifactjob.RegisterRequest) (artifactjob.Job, error) {
	req.ID = s.id
	return s.inner.Register(ctx, req)
}
func (s fixedJobStore) BindRun(ctx context.Context, id artifactjob.JobID, sessionID string, runURL string, tracePath string) (artifactjob.Job, error) {
	return s.inner.BindRun(ctx, id, sessionID, runURL, tracePath)
}
func (s fixedJobStore) Update(ctx context.Context, id artifactjob.JobID, update artifactjob.Update) (artifactjob.Job, error) {
	return s.inner.Update(ctx, id, update)
}
func (s fixedJobStore) Attach(ctx context.Context, id artifactjob.JobID, sessionID string) (artifactjob.Job, error) {
	return s.inner.Attach(ctx, id, sessionID)
}
func (s fixedJobStore) Get(ctx context.Context, id artifactjob.JobID) (artifactjob.Job, error) {
	return s.inner.Get(ctx, id)
}
func (s fixedJobStore) List(ctx context.Context, filter artifactjob.ListFilter) ([]artifactjob.Job, error) {
	return s.inner.List(ctx, filter)
}
func (s fixedJobStore) Archive(ctx context.Context, id artifactjob.JobID) (artifactjob.Job, error) {
	return s.inner.Archive(ctx, id)
}
func (s fixedJobStore) SweepInterrupted(ctx context.Context, sessionID string) (int64, error) {
	return s.inner.SweepInterrupted(ctx, sessionID)
}

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

func TestValidateVerdictRejectsDigestMismatchAndCallerControlledPromotion(t *testing.T) {
	envelope, _ := executor.Seal(executor.Envelope{JobID: "job", ProjectID: "project", DefinitionDigest: "sha256:def", Instance: control.Handle{ID: "w", Generation: 1}, SourceDigest: "sha256:source", StoryPath: "story", StoryDigest: "sha256:story", Environment: environment.Lock{Schema: environment.LockSchema, ID: "ci", Digest: "sha256:env"}, Policy: executor.Policy{Network: "none"}})
	valid := Verdict{Schema: VerdictSchema, Pipeline: "change", Outcome: "passed", Checks: []Check{{ID: "test", Kind: "deterministic", Outcome: "passed", Evidence: []string{"artifact:test"}}}, PromotionEligible: true, SourceDigest: envelope.SourceDigest, StoryDigest: envelope.StoryDigest, EnvironmentDigest: envelope.Environment.Digest, EnvelopeDigest: envelope.Digest}
	if err := ValidateVerdict(valid, envelope, ResultContract{}); err != nil {
		t.Fatal(err)
	}
	mismatch := valid
	mismatch.SourceDigest = "sha256:other"
	if err := ValidateVerdict(mismatch, envelope, ResultContract{}); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected digest mismatch, got %v", err)
	}
	controlled := valid
	controlled.Checks[0].Outcome = "failed"
	if err := ValidateVerdict(controlled, envelope, ResultContract{}); err == nil || !strings.Contains(err.Error(), "derived") {
		t.Fatalf("expected derived promotion eligibility error, got %v", err)
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

func TestServiceRunFailureReturnsFailedJobAndExecutorEvents(t *testing.T) {
	root := t.TempDir()
	requireFiles(t, root)
	service := Service{ProjectRoot: root, Jobs: artifactjob.NewMemoryStore(), Env: environment.Resolver{Probe: environment.ToolProbeFunc(func(context.Context, string) (string, error) { return "go1.25", nil })}, Executors: NewBuiltinExecutors(), Launcher: launcher(func(context.Context, executor.Prepared) (Verdict, error) {
		return Verdict{}, io.ErrUnexpectedEOF
	})}
	result, err := service.Run(context.Background(), RunRequest{Pipeline: "change", Workspace: control.Handle{ID: "w", Generation: 1}, DefinitionDigest: "sha256:def", SourceDigest: "sha256:source", StoryDigest: "sha256:story", Trigger: Trigger{Kind: "local"}})
	if err == nil {
		t.Fatal("expected run error")
	}
	if result.Job.Status != artifactjob.StatusFailed {
		t.Fatalf("status=%s result=%#v", result.Job.Status, result)
	}
	if len(result.Events) != 2 || result.Events[0].Kind != "capsule.executor.started" || result.Events[1].Kind != "capsule.executor.failed" {
		t.Fatalf("events %#v", result.Events)
	}
}

func TestServiceSelectsInjectedContainerExecutor(t *testing.T) {
	root := t.TempDir()
	requireFiles(t, root)
	raw, err := os.ReadFile(filepath.Join(root, ".kitsoki", "ci.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("\n    executor: container\n")...)
	if err := os.WriteFile(filepath.Join(root, ".kitsoki", "ci.yaml"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	builtins := NewBuiltinExecutors()
	builtins.Container = executor.NewContainerProvider(executor.NewFakeContainerBackend())
	service := Service{ProjectRoot: root, Jobs: artifactjob.NewMemoryStore(), Env: environment.Resolver{Probe: environment.ToolProbeFunc(func(context.Context, string) (string, error) { return "go1.25", nil })}, Executors: builtins, Launcher: launcher(func(_ context.Context, p executor.Prepared) (Verdict, error) {
		return Verdict{Schema: VerdictSchema, Pipeline: "change", Outcome: "passed", Checks: []Check{{ID: "test", Kind: "deterministic", Outcome: "passed", Evidence: []string{"artifact:test"}}}, PromotionEligible: true, SourceDigest: p.Envelope.SourceDigest, StoryDigest: p.Envelope.StoryDigest, EnvironmentDigest: p.Envelope.Environment.Digest, EnvelopeDigest: p.Envelope.Digest}, nil
	})}
	result, err := service.Run(context.Background(), RunRequest{Pipeline: "change", Workspace: control.Handle{ID: "w", Generation: 1}, DefinitionDigest: "sha256:def", SourceDigest: "sha256:source", StoryDigest: "sha256:story", Trigger: Trigger{Kind: "local"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Execution.ExecutionID == "" || result.Execution.ExecutionID[:10] != "container-" {
		t.Fatalf("selected execution %#v", result.Execution)
	}
	if result.Execution.Provider["completion_state_outcome"] != "passed" {
		t.Fatalf("container provider facts %#v", result.Execution.Provider)
	}
}

func TestServiceHostAndFakeRemoteProduceEquivalentStoryRun(t *testing.T) {
	hostRoot := filepath.Join(t.TempDir(), "project")
	requireFiles(t, hostRoot)
	remoteRoot := filepath.Join(t.TempDir(), "project")
	requireFiles(t, remoteRoot)
	raw, err := os.ReadFile(filepath.Join(remoteRoot, ".kitsoki", "ci.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("\n    executor: remote-fake\n")...)
	if err := os.WriteFile(filepath.Join(remoteRoot, ".kitsoki", "ci.yaml"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	launch := launcher(func(_ context.Context, p executor.Prepared) (Verdict, error) {
		return Verdict{Schema: VerdictSchema, Pipeline: "change", Outcome: "passed", Summary: "equivalent", Checks: []Check{{ID: "test", Kind: "deterministic", Outcome: "passed", Evidence: []string{"artifact:test"}}}, PromotionEligible: true, SourceDigest: p.Envelope.SourceDigest, StoryDigest: p.Envelope.StoryDigest, EnvironmentDigest: p.Envelope.Environment.Digest, EnvelopeDigest: p.Envelope.Digest}, nil
	})
	run := func(root string) RunResult {
		t.Helper()
		service := Service{ProjectRoot: root, Jobs: newFixedJobStore("job-equivalence"), Env: environment.Resolver{Probe: environment.ToolProbeFunc(func(context.Context, string) (string, error) { return "go1.25", nil })}, Executors: NewBuiltinExecutors(), Launcher: launch}
		result, err := service.Run(context.Background(), RunRequest{Pipeline: "change", Workspace: control.Handle{ID: "w", Generation: 1}, DefinitionDigest: "sha256:def", SourceDigest: "sha256:source", StoryDigest: "sha256:story", Trigger: Trigger{Kind: "local"}})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	host := normalizeEquivalentRun(run(hostRoot))
	remote := normalizeEquivalentRun(run(remoteRoot))
	if string(host.Job.Status) != string(artifactjob.StatusDone) || string(remote.Job.Status) != string(artifactjob.StatusDone) {
		t.Fatalf("statuses host=%s remote=%s", host.Job.Status, remote.Job.Status)
	}
	if string(host.Verdict.Outcome) != "passed" || !host.Verdict.PromotionEligible {
		t.Fatalf("host verdict %#v", host.Verdict)
	}
	if string(remote.Verdict.Outcome) != "passed" || !remote.Verdict.PromotionEligible {
		t.Fatalf("remote verdict %#v", remote.Verdict)
	}
	if !reflect.DeepEqual(host.Verdict, remote.Verdict) || !reflect.DeepEqual(host.Execution, remote.Execution) || host.Envelope.Digest != remote.Envelope.Digest {
		t.Fatalf("host=%#v\nremote=%#v", host, remote)
	}
}

func TestServiceAddsRequiredHygieneCheckToVerdict(t *testing.T) {
	root := t.TempDir()
	requireFiles(t, root)
	raw, err := os.ReadFile(filepath.Join(root, ".kitsoki", "ci.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("\ncleanup:\n  keep_runs: 10\n  require_hygiene_check: true\n  max_reclaimable_bytes: 100\n")...)
	if err := os.WriteFile(filepath.Join(root, ".kitsoki", "ci.yaml"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	service := Service{
		ProjectRoot: root,
		Jobs:        artifactjob.NewMemoryStore(),
		Env:         environment.Resolver{Probe: environment.ToolProbeFunc(func(context.Context, string) (string, error) { return "go1.25", nil })},
		Executors:   NewBuiltinExecutors(),
		Launcher: launcher(func(_ context.Context, p executor.Prepared) (Verdict, error) {
			return Verdict{Schema: VerdictSchema, Pipeline: "change", Outcome: "passed", Checks: []Check{{ID: "test", Kind: "deterministic", Outcome: "passed", Evidence: []string{"artifact:test"}}}, PromotionEligible: true, SourceDigest: p.Envelope.SourceDigest, StoryDigest: p.Envelope.StoryDigest, EnvironmentDigest: p.Envelope.Environment.Digest, EnvelopeDigest: p.Envelope.Digest}, nil
		}),
		Hygiene: HygienePlannerFunc(func(_ context.Context, policy CleanupPolicy) (HygieneReport, error) {
			if policy.KeepRuns != 10 || policy.MaxReclaimableBytes != 100 {
				t.Fatalf("policy %#v", policy)
			}
			return HygieneReport{Schema: "capsule-hygiene-plan/v1", Candidates: 2, TotalBytes: 50, EvidenceRef: "artifact:hygiene-plan"}, nil
		}),
	}
	result, err := service.Run(context.Background(), RunRequest{Pipeline: "change", Workspace: control.Handle{ID: "w", Generation: 1}, DefinitionDigest: "sha256:def", SourceDigest: "sha256:source", StoryDigest: "sha256:story", Trigger: Trigger{Kind: "local"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verdict.PromotionEligible {
		t.Fatalf("verdict %#v", result.Verdict)
	}
	got := checkByID(result.Verdict.Checks, "capsule-hygiene")
	if got == nil || got.Outcome != "passed" || len(got.Evidence) != 1 || got.Evidence[0] != "artifact:hygiene-plan" {
		t.Fatalf("hygiene check %#v", got)
	}
}

func TestServiceHygieneDebtBlocksPromotion(t *testing.T) {
	root := t.TempDir()
	requireFiles(t, root)
	raw, err := os.ReadFile(filepath.Join(root, ".kitsoki", "ci.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("\ncleanup:\n  require_hygiene_check: true\n  max_reclaimable_bytes: 100\n")...)
	if err := os.WriteFile(filepath.Join(root, ".kitsoki", "ci.yaml"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	service := Service{
		ProjectRoot: root,
		Jobs:        artifactjob.NewMemoryStore(),
		Env:         environment.Resolver{Probe: environment.ToolProbeFunc(func(context.Context, string) (string, error) { return "go1.25", nil })},
		Executors:   NewBuiltinExecutors(),
		Launcher: launcher(func(_ context.Context, p executor.Prepared) (Verdict, error) {
			return Verdict{Schema: VerdictSchema, Pipeline: "change", Outcome: "passed", Checks: []Check{{ID: "test", Kind: "deterministic", Outcome: "passed", Evidence: []string{"artifact:test"}}}, PromotionEligible: true, SourceDigest: p.Envelope.SourceDigest, StoryDigest: p.Envelope.StoryDigest, EnvironmentDigest: p.Envelope.Environment.Digest, EnvelopeDigest: p.Envelope.Digest}, nil
		}),
		Hygiene: HygienePlannerFunc(func(context.Context, CleanupPolicy) (HygieneReport, error) {
			return HygieneReport{Schema: "capsule-hygiene-plan/v1", Candidates: 3, TotalBytes: 101, EvidenceRef: "artifact:hygiene-plan"}, nil
		}),
	}
	result, err := service.Run(context.Background(), RunRequest{Pipeline: "change", Workspace: control.Handle{ID: "w", Generation: 1}, DefinitionDigest: "sha256:def", SourceDigest: "sha256:source", StoryDigest: "sha256:story", Trigger: Trigger{Kind: "local"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict.Outcome != "failed" || result.Verdict.PromotionEligible {
		t.Fatalf("verdict %#v", result.Verdict)
	}
	got := checkByID(result.Verdict.Checks, "capsule-hygiene")
	if got == nil || got.Outcome != "failed" {
		t.Fatalf("hygiene check %#v", got)
	}
}

func TestPipelineCleanupPolicyOverridesGlobalRetention(t *testing.T) {
	root := t.TempDir()
	requireFiles(t, root)
	raw := []byte("schema: capsule-ci/v1\ndefault_environment: ci\ncleanup:\n  keep_runs: 20\n  require_hygiene_check: true\n  max_reclaimable_bytes: 100\npipelines:\n  change:\n    story: .kitsoki/stories/ci/app.yaml\n    triggers: [local]\n    cleanup:\n      keep_runs: 5\n      max_reclaimable_bytes: 10\n    result:\n      schema: capsule-ci-verdict/v1\n")
	if err := os.WriteFile(filepath.Join(root, ".kitsoki", "ci.yaml"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	service := Service{ProjectRoot: root, Jobs: artifactjob.NewMemoryStore(), Env: environment.Resolver{Probe: environment.ToolProbeFunc(func(context.Context, string) (string, error) { return "go1.25", nil })}}
	pipeline, _, err := service.Plan(context.Background(), RunRequest{Pipeline: "change", Workspace: control.Handle{ID: "w", Generation: 1}, DefinitionDigest: "sha256:def", SourceDigest: "sha256:source", StoryDigest: "sha256:story", Trigger: Trigger{Kind: "local"}})
	if err != nil {
		t.Fatal(err)
	}
	if pipeline.Cleanup.KeepRuns != 5 || pipeline.Cleanup.MaxReclaimableBytes != 10 || !pipeline.Cleanup.RequireHygieneCheck {
		t.Fatalf("cleanup policy %#v", pipeline.Cleanup)
	}
}

func normalizeEquivalentRun(result RunResult) RunResult {
	result.Execution.ExecutionID = ""
	result.Job.ID = ""
	result.Job.CreatedAt = result.Job.CreatedAt.UTC()
	result.Job.UpdatedAt = result.Job.CreatedAt
	return result
}

func checkByID(checks []Check, id string) *Check {
	for i := range checks {
		if checks[i].ID == id {
			return &checks[i]
		}
	}
	return nil
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

func TestValidateAllowedAgentsRequireBudgetAndFallback(t *testing.T) {
	root := t.TempDir()
	requireFiles(t, root)
	raw, err := os.ReadFile(filepath.Join(root, ".kitsoki", "ci.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, []byte("\n    agents:\n      policy: allow\n      profiles: [local]\n")...)
	if err := os.WriteFile(filepath.Join(root, ".kitsoki", "ci.yaml"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "allowed agents require profiles, budget, and fallback") {
		t.Fatalf("expected budget/fallback policy error, got %v", err)
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
