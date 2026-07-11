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
	envelope, err := executor.Seal(executor.Envelope{JobID: "job", ProjectID: "p", DefinitionDigest: "sha256:def", Instance: control.Handle{ID: "w", Generation: 1}, SourceDigest: "sha256:source", StoryDigest: "sha256:story", Environment: environment.Lock{Schema: environment.LockSchema, ID: "ci", Digest: "sha256:env"}, Trigger: map[string]any{"requested_pipeline": "change"}, Policy: executor.Policy{Network: "none"}})
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
		service := ci.Service{
			ProjectRoot: root,
			Jobs:        fixedStore("job-story-parity"),
			Env:         environment.Resolver{Probe: environment.ToolProbeFunc(func(context.Context, string) (string, error) { return "", nil })},
			Executors:   ci.NewBuiltinExecutors(),
			Launcher:    Launcher{StoryPath: story},
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
	if host.Verdict.Outcome != "needs_input" || host.Job.Status != artifactjob.StatusAwaitingInput {
		t.Fatalf("host reference story should park honestly: %#v", host)
	}
	if remote.Verdict.Outcome != "needs_input" || remote.Job.Status != artifactjob.StatusAwaitingInput {
		t.Fatalf("remote reference story should park honestly: %#v", remote)
	}
	if container.Verdict.Outcome != "needs_input" || container.Job.Status != artifactjob.StatusAwaitingInput {
		t.Fatalf("container reference story should park honestly: %#v", container)
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
		service := ci.Service{
			ProjectRoot: root,
			Jobs:        fixedStore("job-generated-wrapper"),
			Env:         environment.Resolver{Probe: environment.ToolProbeFunc(func(context.Context, string) (string, error) { return "", nil })},
			Executors:   ci.NewBuiltinExecutors(),
			Launcher:    Launcher{StoryPath: story},
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
	if host.Verdict.Outcome != "needs_input" || remote.Verdict.Outcome != "needs_input" || container.Verdict.Outcome != "needs_input" {
		t.Fatalf("generated wrapper should park honestly host=%#v remote=%#v container=%#v", host.Verdict, remote.Verdict, container.Verdict)
	}
	if !reflect.DeepEqual(host.Verdict, remote.Verdict) || !reflect.DeepEqual(host.Verdict, container.Verdict) || host.Envelope.Digest != remote.Envelope.Digest || host.Envelope.Digest != container.Envelope.Digest {
		t.Fatalf("host=%#v\nremote=%#v\ncontainer=%#v", host, remote, container)
	}
}

func TestGeneratedProjectWrapperDigestMismatchIsRejected(t *testing.T) {
	root := writeCIProject(t, "host", generatedProjectCIStory(t, true))
	story := filepath.Join(root, ".kitsoki", "stories", "capsule-ci", "app.yaml")
	service := ci.Service{
		ProjectRoot: root,
		Jobs:        fixedStore("job-generated-mismatch"),
		Env:         environment.Resolver{Probe: environment.ToolProbeFunc(func(context.Context, string) (string, error) { return "", nil })},
		Executors:   ci.NewBuiltinExecutors(),
		Launcher:    Launcher{StoryPath: story},
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
        - target: parked
          effects:
            - set:
                ci_outcome: "needs_input"
                ci_verdict:
                  schema: capsule-ci-verdict/v1
                  pipeline: "{{ world.ci_pipeline }}"
                  outcome: needs_input
                  summary: "Generated Capsule CI needs project-specific checks before it can pass."
                  checks: []
                  promotion_eligible: false
                  source_digest: "` + sourceDigest + `"
                  story_digest: "{{ world.ci_trigger.story_digest }}"
                  environment_digest: "{{ world.ci_environment.digest }}"
                  envelope_digest: "{{ world.ci_trigger.envelope_digest }}"
  parked:
    view: [{ prose: "parked" }]
`
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
