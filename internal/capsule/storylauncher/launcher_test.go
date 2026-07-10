package storylauncher

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
