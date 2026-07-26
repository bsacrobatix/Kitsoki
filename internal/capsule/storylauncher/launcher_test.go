package storylauncher

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
	"kitsoki/internal/host"
)

func TestLauncherUsesStoryTerminalVerdict(t *testing.T) {
	dir := t.TempDir()
	story := filepath.Join(dir, "app.yaml")
	raw := `app:
  id: engine-ci
  version: 0.1.0
  title: Engine CI
  author: Test
  license: CC0
world:
  ci_job_id: { type: string, default: "" }
  ci_pipeline: { type: string, default: "" }
  ci_trigger: { type: object, default: {} }
  ci_source: { type: object, default: {} }
  ci_workspace: { type: object, default: {} }
  ci_environment: { type: object, default: {} }
  ci_policy: { type: object, default: {} }
  ci_verdict: { type: object, default: {} }
intents:
  run: { description: run, examples: [run], priority: 1 }
root: idle
states:
  idle:
    view: [{ prose: "idle" }]
    on:
      run:
        - target: done
          effects:
            - set:
                ci_verdict:
                  schema: capsule-ci-verdict/v1
                  pipeline: change
                  outcome: passed
                  checks: []
                  promotion_eligible: true
                  source_digest: "{{ world.ci_source.digest }}"
                  story_digest: sha256:story
                  environment_digest: "{{ world.ci_environment.digest }}"
                  envelope_digest: "{{ world.ci_trigger.envelope_digest }}"
  done:
    terminal: true
    view: [{ prose: "done" }]
`
	if err := os.WriteFile(story, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	lock, err := environment.SealLock(environment.Lock{Schema: environment.LockSchema, ID: "ci", DefinitionDigest: "sha256:env-def", Network: "none", Sandbox: "supervised"})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := executor.Seal(executor.Envelope{JobID: "job", ProjectID: "p", DefinitionDigest: "sha256:def", Instance: control.Handle{ID: "w", Generation: 1}, SourceDigest: "sha256:source", StoryPath: "app.yaml", StoryDigest: "sha256:story", Environment: lock, Trigger: map[string]any{"requested_pipeline": "change"}, Policy: executor.Policy{Network: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Launcher{StoryPath: story}.Launch(context.Background(), executor.Prepared{Envelope: envelope})
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != "passed" || got.EnvelopeDigest != envelope.Digest {
		t.Fatalf("verdict %#v", got)
	}
}

// TestLauncherResolvesCrossRepoKitsokiImport proves the other half of the
// VENDORED bugfix closure fix (see internal/capsule/storydigest's package
// doc): a story that imports `@kitsoki/<name>` — content resolved by
// basestories.DefaultResolver rather than requiring an in-project vendored
// copy — not only SEALS (storydigest_test.go) but also LAUNCHES and drives
// through the imported child to a terminal ci_verdict, the same way
// storydigest.Compute and storylauncher.Launch are wired to agree on what
// `@kitsoki/<name>` resolves to. This uses the resolver's $KITSOKI_REPO
// override tier with a small synthetic child story (a deterministic stand-in
// for `@kitsoki/bugfix` — launching the real bugfix story requires a live
// coding agent, out of scope for this unit test) rather than the real
// embedded library, so the test is fast and needs no agent/model.
func TestLauncherResolvesCrossRepoKitsokiImport(t *testing.T) {
	writeFile := func(path, contents string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	repo := t.TempDir()
	writeFile(filepath.Join(repo, "stories", "childkit", "app.yaml"), `app:
  id: childkit
  version: 0.1.0
  title: Child Kit
  author: Test
  license: CC0
world:
  greeting: { type: string, default: "" }
intents:
  run: { description: run, examples: [run], priority: 1 }
root: idle
exits:
  finished:
    description: "child done"
states:
  idle:
    view: [{ prose: "{{ world.greeting }}" }]
    on_enter:
      - emit_intent: run
    on:
      run:
        - target: "@exit:finished"
`)
	t.Setenv("KITSOKI_REPO", repo)

	dir := t.TempDir()
	story := filepath.Join(dir, "app.yaml")
	writeFile(story, `app:
  id: cross-repo-wrapper
  version: 0.1.0
  title: Cross Repo Wrapper
  author: Test
  license: CC0
world:
  ci_job_id: { type: string, default: "" }
  ci_pipeline: { type: string, default: "" }
  ci_trigger: { type: object, default: {} }
  ci_source: { type: object, default: {} }
  ci_workspace: { type: object, default: {} }
  ci_environment: { type: object, default: {} }
  ci_policy: { type: object, default: {} }
  ci_verdict: { type: object, default: {} }
intents:
  run: { description: run, examples: [run], priority: 1 }
root: idle
states:
  idle:
    view: [{ prose: idle }]
    on:
      run:
        - target: child
  done:
    terminal: true
    view: [{ prose: done }]
imports:
  child:
    source: "@kitsoki/childkit"
    entry: idle
    world_in:
      greeting: "hello"
    exits:
      finished:
        to: done
        set:
          ci_verdict:
            schema: capsule-ci-verdict/v1
            pipeline: change
            outcome: passed
            checks: []
            promotion_eligible: true
            source_digest: "{{ world.ci_source.digest }}"
            story_digest: sha256:story
            environment_digest: "{{ world.ci_environment.digest }}"
            envelope_digest: "{{ world.ci_trigger.envelope_digest }}"
`)
	lock, err := environment.SealLock(environment.Lock{Schema: environment.LockSchema, ID: "ci", DefinitionDigest: "sha256:env-def", Network: "none", Sandbox: "supervised"})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := executor.Seal(executor.Envelope{JobID: "job", ProjectID: "p", DefinitionDigest: "sha256:def", Instance: control.Handle{ID: "w", Generation: 1}, SourceDigest: "sha256:source", StoryPath: "app.yaml", StoryDigest: "sha256:story", Environment: lock, Trigger: map[string]any{"requested_pipeline": "change"}, Policy: executor.Policy{Network: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Launcher{StoryPath: story}.Launch(context.Background(), executor.Prepared{Envelope: envelope})
	if err != nil {
		t.Fatalf("launch cross-repo @kitsoki/childkit import: %v", err)
	}
	if got.Outcome != "passed" || got.EnvelopeDigest != envelope.Digest {
		t.Fatalf("verdict %#v", got)
	}
}

func TestApplyAgentPolicyFailsClosedAndSealsProfiles(t *testing.T) {
	tests := []struct {
		name       string
		policy     executor.AgentPolicy
		args       map[string]any
		world      map[string]any
		wantCalled bool
		wantError  string
	}{
		{
			name:      "deny is the default",
			args:      map[string]any{"agent": "reviewer"},
			wantError: "denied",
		},
		{
			name:       "allowlisted profile reaches handler",
			policy:     executor.AgentPolicy{Policy: "allow", Profiles: []string{"reviewer"}, MaxCostUSD: 1},
			args:       map[string]any{"agent": "reviewer"},
			wantCalled: true,
		},
		{
			name:      "implicit profile is rejected",
			policy:    executor.AgentPolicy{Policy: "allow", Profiles: []string{"reviewer"}, MaxCostUSD: 1},
			args:      map[string]any{"provider": "reviewer"},
			wantError: "explicit agent profile",
		},
		{
			name:      "unlisted profile is rejected",
			policy:    executor.AgentPolicy{Policy: "allow", Profiles: []string{"reviewer"}, MaxCostUSD: 1},
			args:      map[string]any{"agent": "writer"},
			wantError: "outside the sealed allowlist",
		},
		{
			name:      "nested extract profile is sealed",
			policy:    executor.AgentPolicy{Policy: "allow", Profiles: []string{"reviewer"}, MaxCostUSD: 1},
			args:      map[string]any{"agent": "reviewer", "resolvers": []any{map[string]any{"llm": map[string]any{"agent": "writer"}}}},
			wantError: "writer",
		},
		{
			name:      "exhausted budget blocks dispatch",
			policy:    executor.AgentPolicy{Policy: "allow", Profiles: []string{"reviewer"}, MaxCostUSD: 0.25},
			args:      map[string]any{"agent": "reviewer"},
			world:     map[string]any{"session_cost_usd": 0.25},
			wantError: "exhausted",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := host.NewRegistry()
			called := false
			reg.Register("host.agent.decide", func(context.Context, map[string]any) (host.Result, error) {
				called = true
				return host.Result{Data: map[string]any{"ok": true}}, nil
			})
			if err := applyAgentPolicy(reg, tc.policy); err != nil {
				t.Fatal(err)
			}
			handler, ok := reg.Get("host.agent.decide")
			if !ok {
				t.Fatal("guarded handler missing")
			}
			ctx := host.WithWorldSnapshot(context.Background(), tc.world)
			result, err := handler(ctx, tc.args)
			if err != nil {
				t.Fatal(err)
			}
			if called != tc.wantCalled {
				t.Fatalf("handler called = %v, want %v", called, tc.wantCalled)
			}
			if tc.wantError != "" && !strings.Contains(result.Error, tc.wantError) {
				t.Fatalf("error = %q, want substring %q", result.Error, tc.wantError)
			}
			if tc.wantError == "" && result.Error != "" {
				t.Fatalf("unexpected error %q", result.Error)
			}
		})
	}
}

func TestApplyHostEffectPolicyDeniesExternalHandlersAfterEmbedding(t *testing.T) {
	registry := host.NewRegistry()
	called := false
	registry.Register("host.transport.post", func(context.Context, map[string]any) (host.Result, error) {
		called = true
		return host.Result{Data: map[string]any{"ok": true}}, nil
	})
	applyHostEffectPolicy(registry, executor.Policy{ExternalWrite: "deny"})
	result, err := registry.Invoke(context.Background(), "host.transport.post", map[string]any{})
	if err != nil || result.Error == "" || called {
		t.Fatalf("denied external call result=%#v err=%v called=%t", result, err, called)
	}

	registry = host.NewRegistry()
	registry.Register("host.transport.post", func(context.Context, map[string]any) (host.Result, error) {
		called = true
		return host.Result{Data: map[string]any{"ok": true}}, nil
	})
	called = false
	applyHostEffectPolicy(registry, executor.Policy{ExternalWrite: "allow"})
	result, err = registry.Invoke(context.Background(), "host.transport.post", map[string]any{})
	if err != nil || result.Error != "" || !called {
		t.Fatalf("allowed external call result=%#v err=%v called=%t", result, err, called)
	}
}

func TestReferenceStoryRunsEquivalentlyOnHostAndFakeRemote(t *testing.T) {
	repo := repoRoot(t)
	storyRaw, err := os.ReadFile(filepath.Join(repo, "stories", "capsule-ci", "app.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	makeProject := func(executorName string) string {
		t.Helper()
		root := filepath.Join(t.TempDir(), "project")
		files := map[string]string{
			".kitsoki/environments/ci.yaml":        "schema: capsule-environment/v1\nid: ci\nsource:\n  host_probe: true\nbootstrap:\n  command: bootstrap-workspace\nnetwork: none\ncaches:\n  - id: go-build\n    scope: project\n    mode: read_write\n  - id: runstatus-node-modules\n    scope: project\n    mode: read_write\n",
			".kitsoki/ci.yaml":                     "schema: capsule-ci/v1\ndefault_environment: ci\npipelines:\n  change:\n    story: .kitsoki/stories/capsule-ci/app.yaml\n    triggers: [local]\n    executor: " + executorName + "\n    permissions:\n      network: none\n      external_write: deny\n    result:\n      schema: capsule-ci-verdict/v1\n",
			".kitsoki/project-profile.yaml":        "schema: project-profile/v1\nid: reference\ncommands:\n  test: fixture test\n  build: fixture build\n",
			".kitsoki/stories/capsule-ci/app.yaml": string(storyRaw),
		}
		for path, raw := range files {
			full := filepath.Join(root, path)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(raw), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}
	run := func(root string) ci.RunResult {
		t.Helper()
		story := filepath.Join(root, ".kitsoki", "stories", "capsule-ci", "app.yaml")
		executors := ci.NewBuiltinExecutors()
		executors.Host.(*executor.HostProvider).Cap.Networks = []string{"none"} // fixture: parity, not host confinement.
		service := ci.Service{
			ProjectRoot: root,
			Jobs:        fixedStore("job-story-parity"),
			Env:         environment.Resolver{Probe: environment.ToolProbeFunc(func(context.Context, string) (string, error) { return "", nil })},
			Executors:   executors,
			Launcher:    projectCheckLauncher(story),
		}
		result, err := service.Run(context.Background(), ci.RunRequest{Pipeline: "change", Workspace: control.Handle{ID: "w", Generation: 1}, DefinitionDigest: "sha256:def", SourceDigest: "sha256:source", StoryDigest: "sha256:story", Trigger: ci.Trigger{Kind: "local", RequestedPipeline: "change"}})
		if err != nil {
			t.Fatal(err)
		}
		return normalizeStoryParityResult(result)
	}
	host := run(makeProject("host"))
	remote := run(makeProject("remote-fake"))
	container := run(makeProject("container-fake"))
	if host.Verdict.Outcome != "passed" || host.Job.Status != artifactjob.StatusDone {
		t.Fatalf("host reference story should pass declared fixture checks: %#v", host)
	}
	if remote.Verdict.Outcome != "passed" || remote.Job.Status != artifactjob.StatusDone {
		t.Fatalf("remote reference story should pass declared fixture checks: %#v", remote)
	}
	if container.Verdict.Outcome != "passed" || container.Job.Status != artifactjob.StatusDone {
		t.Fatalf("container reference story should pass declared fixture checks: %#v", container)
	}
	if !reflect.DeepEqual(host.Verdict, remote.Verdict) || !reflect.DeepEqual(host.Execution, remote.Execution) || host.Envelope.Digest != remote.Envelope.Digest {
		t.Fatalf("host=%#v\nremote=%#v", host, remote)
	}
	if !reflect.DeepEqual(host.Verdict, container.Verdict) || host.Envelope.Digest != container.Envelope.Digest {
		t.Fatalf("host=%#v\ncontainer=%#v", host, container)
	}
}

func TestGeneratedProjectWrapperRunsEquivalentlyAcrossExecutors(t *testing.T) {
	run := func(executorName string) ci.RunResult {
		t.Helper()
		root := writeCIProject(t, executorName, generatedProjectCIStory(t, false))
		story := filepath.Join(root, ".kitsoki", "stories", "capsule-ci", "app.yaml")
		executors := ci.NewBuiltinExecutors()
		executors.Host.(*executor.HostProvider).Cap.Networks = []string{"none"} // fixture: parity, not host confinement.
		service := ci.Service{
			ProjectRoot: root,
			Jobs:        fixedStore("job-generated-wrapper"),
			Env:         environment.Resolver{Probe: environment.ToolProbeFunc(func(context.Context, string) (string, error) { return "", nil })},
			Executors:   executors,
			Launcher:    projectCheckLauncher(story),
		}
		result, err := service.Run(context.Background(), ci.RunRequest{Pipeline: "change", Workspace: control.Handle{ID: "w", Generation: 1}, DefinitionDigest: "sha256:def", SourceDigest: "sha256:source", StoryDigest: "sha256:story", Trigger: ci.Trigger{Kind: "local", RequestedPipeline: "change"}})
		if err != nil {
			t.Fatal(err)
		}
		return normalizeStoryParityResult(result)
	}
	host := run("host")
	remote := run("remote-fake")
	container := run("container-fake")
	if host.Verdict.Outcome != "passed" || remote.Verdict.Outcome != "passed" || container.Verdict.Outcome != "passed" {
		t.Fatalf("generated wrapper should run declared project checks host=%#v remote=%#v container=%#v", host.Verdict, remote.Verdict, container.Verdict)
	}
	if !reflect.DeepEqual(host.Verdict, remote.Verdict) || !reflect.DeepEqual(host.Verdict, container.Verdict) || host.Envelope.Digest != remote.Envelope.Digest || host.Envelope.Digest != container.Envelope.Digest {
		t.Fatalf("host=%#v\nremote=%#v\ncontainer=%#v", host, remote, container)
	}
}

func TestGeneratedProjectWrapperDigestMismatchIsRejected(t *testing.T) {
	root := writeCIProject(t, "host", generatedProjectCIStory(t, true))
	story := filepath.Join(root, ".kitsoki", "stories", "capsule-ci", "app.yaml")
	executors := ci.NewBuiltinExecutors()
	executors.Host.(*executor.HostProvider).Cap.Networks = []string{"none"} // fixture: reach the verdict mismatch assertion.
	service := ci.Service{
		ProjectRoot: root,
		Jobs:        fixedStore("job-generated-mismatch"),
		Env:         environment.Resolver{Probe: environment.ToolProbeFunc(func(context.Context, string) (string, error) { return "", nil })},
		Executors:   executors,
		Launcher:    projectCheckLauncher(story),
	}
	_, err := service.Run(context.Background(), ci.RunRequest{Pipeline: "change", Workspace: control.Handle{ID: "w", Generation: 1}, DefinitionDigest: "sha256:def", SourceDigest: "sha256:source", StoryDigest: "sha256:story", Trigger: ci.Trigger{Kind: "local", RequestedPipeline: "change"}})
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected digest mismatch, got %v", err)
	}
}

func normalizeStoryParityResult(result ci.RunResult) ci.RunResult {
	result.Execution.ExecutionID = ""
	result.Job.ID = ""
	result.Job.CreatedAt = time.Time{}
	result.Job.UpdatedAt = time.Time{}
	return result
}

func writeCIProject(t *testing.T, executorName string, storyRaw string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "project")
	files := map[string]string{
		".kitsoki/environments/ci.yaml":        "schema: capsule-environment/v1\nid: ci\nsource:\n  host_probe: true\nnetwork: none\n",
		".kitsoki/ci.yaml":                     "schema: capsule-ci/v1\ndefault_environment: ci\npipelines:\n  change:\n    story: .kitsoki/stories/capsule-ci/app.yaml\n    triggers: [local]\n    executor: " + executorName + "\n    permissions:\n      network: none\n      external_write: deny\n    result:\n      schema: capsule-ci-verdict/v1\n",
		".kitsoki/project-profile.yaml":        "schema: project-profile/v1\nid: generated\ncommands:\n  test: fixture test\n  build: fixture build\n",
		".kitsoki/stories/capsule-ci/app.yaml": storyRaw,
	}
	for path, raw := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func generatedProjectCIStory(t *testing.T, wrongDigest bool) string {
	t.Helper()
	sourceDigest := "{{ world.ci_source.digest }}"
	if wrongDigest {
		sourceDigest = "sha256:wrong"
	}
	return `app:
  id: capsule-ci
  version: 0.1.0
  title: "Project Capsule CI"
  author: "Kitsoki"
  license: "CC0"
world:
  ci_job_id: { type: string, default: "" }
  ci_pipeline: { type: string, default: "" }
  ci_trigger: { type: object, default: {} }
  ci_source: { type: object, default: {} }
  ci_workspace: { type: object, default: {} }
  ci_environment: { type: object, default: {} }
  ci_policy: { type: object, default: {} }
  ci_verdict: { type: object, default: {} }
  ci_outcome: { type: string, default: "" }
hosts:
  - host.capsule_ci.project_checks
intents:
  run:
    description: "Validate the supplied Capsule CI envelope."
    examples: [run, start]
    priority: 90
root: idle
states:
  idle:
    view: [{ prose: "generated project wrapper" }]
    on:
      run:
        - target: project_checks
          effects:
            - invoke: host.capsule_ci.project_checks
              with:
                workdir: "{{ world.ci_workspace.path }}"
                job_id: "{{ world.ci_job_id }}"
                pipeline: "{{ world.ci_pipeline }}"
                source_digest: "` + sourceDigest + `"
                story_digest: "{{ world.ci_trigger.story_digest }}"
                environment_digest: "{{ world.ci_environment.digest }}"
                envelope_digest: "{{ world.ci_trigger.envelope_digest }}"
              bind:
                ci_verdict: verdict
  project_checks:
    view: [{ prose: "project checks" }]
`
}

func projectCheckLauncher(story string) Launcher {
	runner := host.CapsuleCICommandRunnerFunc(func(context.Context, string, string) (string, int, error) {
		return "fixture passed\n", 0, nil
	})
	return Launcher{StoryPath: story, ConfigureHosts: func(reg *host.Registry) error {
		reg.Replace("host.capsule_ci.project_checks", host.NewCapsuleCIProjectChecksHandler(runner))
		return nil
	}}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

type fixedJobStore struct {
	id    artifactjob.JobID
	inner *artifactjob.MemoryStore
}

func fixedStore(id string) fixedJobStore {
	store := artifactjob.NewMemoryStore()
	store.SetClock(func() time.Time { return time.Unix(123, 0).UTC() })
	return fixedJobStore{id: artifactjob.JobID(id), inner: store}
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
func (s fixedJobStore) SweepInterrupted(ctx context.Context, reason string) (int64, error) {
	return s.inner.SweepInterrupted(ctx, reason)
}

func writeJobInputs(t *testing.T, root, body string) {
	t.Helper()
	kdir := filepath.Join(root, ".kitsoki")
	if err := os.MkdirAll(kdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(kdir, "job-inputs.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEnvelopeWorldSeedsJobInputs(t *testing.T) {
	root := t.TempDir()
	writeJobInputs(t, root, `{"ticket_body":"boom","gate_command":"make repro"}`)
	w, err := envelopeWorld(executor.Envelope{JobID: "job", Trigger: map[string]any{"requested_pipeline": "change"}}, root)
	if err != nil {
		t.Fatal(err)
	}
	job, ok := w["job"].(map[string]any)
	if !ok {
		t.Fatalf("world.job not seeded: %#v", w["job"])
	}
	if job["ticket_body"] != "boom" || job["gate_command"] != "make repro" {
		t.Fatalf("job inputs %#v", job)
	}
}

func TestEnvelopeWorldSeedsSealedJobInputsWithoutSourceFile(t *testing.T) {
	w, err := envelopeWorld(executor.Envelope{
		JobID:     "job",
		Trigger:   map[string]any{"requested_pipeline": "change"},
		JobInputs: map[string]any{"ticket_body": "sealed"},
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	job, ok := w["job"].(map[string]any)
	if !ok || job["ticket_body"] != "sealed" {
		t.Fatalf("sealed job inputs not seeded: %#v", w["job"])
	}
}

func TestEnvelopeWorldRejectsConflictingSealedAndSourceJobInputs(t *testing.T) {
	root := t.TempDir()
	writeJobInputs(t, root, `{"ticket_body":"source"}`)
	_, err := envelopeWorld(executor.Envelope{
		JobID:     "job",
		JobInputs: map[string]any{"ticket_body": "sealed"},
	}, root)
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("expected conflicting job inputs to fail closed, got %v", err)
	}
}

func TestEnvelopeWorldNoJobInputsFileLeavesJobAbsent(t *testing.T) {
	w, err := envelopeWorld(executor.Envelope{JobID: "job", Trigger: map[string]any{}}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := w["job"]; ok {
		t.Fatalf("world.job should be absent without a job-inputs file: %#v", w["job"])
	}
}

func TestEnvelopeWorldMalformedJobInputsFails(t *testing.T) {
	root := t.TempDir()
	writeJobInputs(t, root, `{not valid json`)
	if _, err := envelopeWorld(executor.Envelope{JobID: "job"}, root); err == nil {
		t.Fatal("expected malformed job-inputs.json to fail the run, got nil error")
	}
}

// This exercises the production boundary: source-sealed world.job data must
// be visible to story templates on the first driven turn.
func TestLauncherTemplatesJobInputs(t *testing.T) {
	root := t.TempDir()
	writeJobInputs(t, root, `{"marker":"seen-in-story"}`)
	story := filepath.Join(root, "app.yaml")
	raw := `app:
  id: engine-ci-job
  version: 0.1.0
  title: Engine CI Job
  author: Test
  license: CC0
world:
  ci_source: { type: object, default: {} }
  ci_environment: { type: object, default: {} }
  ci_trigger: { type: object, default: {} }
  ci_verdict: { type: object, default: {} }
  job: { type: object, default: {} }
intents:
  run: { description: run, examples: [run], priority: 1 }
root: idle
states:
  idle:
    view: [{ prose: "idle" }]
    on:
      run:
        - target: done
          effects:
            - set:
                ci_verdict:
                  schema: capsule-ci-verdict/v1
                  pipeline: "{{ world.job.marker }}"
                  outcome: passed
                  checks: []
                  promotion_eligible: true
                  source_digest: "{{ world.ci_source.digest }}"
                  story_digest: sha256:story
                  environment_digest: "{{ world.ci_environment.digest }}"
                  envelope_digest: "{{ world.ci_trigger.envelope_digest }}"
  done:
    terminal: true
    view: [{ prose: "done" }]
`
	if err := os.WriteFile(story, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	lock, err := environment.SealLock(environment.Lock{Schema: environment.LockSchema, ID: "ci", DefinitionDigest: "sha256:env-def", Network: "none", Sandbox: "supervised"})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := executor.Seal(executor.Envelope{JobID: "job", ProjectID: "p", DefinitionDigest: "sha256:def", Instance: control.Handle{ID: "w", Generation: 1}, SourceDigest: "sha256:source", StoryPath: "app.yaml", StoryDigest: "sha256:story", Environment: lock, Trigger: map[string]any{"requested_pipeline": "change"}, Policy: executor.Policy{Network: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Launcher{StoryPath: story, ProjectRoot: root}.Launch(context.Background(), executor.Prepared{Envelope: envelope})
	if err != nil {
		t.Fatal(err)
	}
	if got.Pipeline != "seen-in-story" {
		t.Fatalf("story did not template world.job.marker; verdict %#v", got)
	}
}
