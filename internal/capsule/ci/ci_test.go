package ci

import (
	"context"
	"os"
	"path/filepath"
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
