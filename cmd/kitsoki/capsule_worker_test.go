package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"kitsoki/internal/capsule/control"
	"kitsoki/internal/capsule/environment"
	"kitsoki/internal/capsule/executor"
)

func TestCapsuleWorkerRunWritesExecutorResult(t *testing.T) {
	root := t.TempDir()
	story := filepath.Join(root, "app.yaml")
	raw := `app:
  id: worker-ci
  version: 0.1.0
  title: Worker CI
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
                  checks:
                    - id: worker
                      kind: deterministic
                      outcome: passed
                      evidence: ["artifact:worker"]
                  promotion_eligible: true
                  source_digest: "{{ world.ci_source.digest }}"
                  story_digest: "{{ world.ci_trigger.story_digest }}"
                  environment_digest: "{{ world.ci_environment.digest }}"
                  envelope_digest: "{{ world.ci_trigger.envelope_digest }}"
  done:
    view: [{ prose: "done" }]
`
	if err := os.WriteFile(story, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	envelope, err := executor.Seal(executor.Envelope{JobID: "job", ProjectID: "project", DefinitionDigest: "sha256:def", Instance: control.Handle{ID: "w", Generation: 1}, SourceDigest: "sha256:source", StoryPath: story, StoryDigest: "sha256:story", Environment: environment.Lock{Schema: environment.LockSchema, ID: "ci", Digest: "sha256:env"}, Trigger: map[string]any{"requested_pipeline": "change"}, Policy: executor.Policy{Network: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	envelopePath := filepath.Join(root, "envelope.json")
	resultPath := filepath.Join(root, "result.json")
	encoded, _ := json.Marshal(envelope)
	if err := os.WriteFile(envelopePath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := execRoot(t, "capsule", "worker", "run", "--envelope", envelopePath, "--result", resultPath)
	if err != nil {
		t.Fatalf("worker run: %v\n%s", err, out)
	}
	var result struct {
		Result          executor.Result          `json:"result"`
		CompletionState executor.CompletionState `json:"completion_state"`
	}
	rawResult, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawResult, &result); err != nil {
		t.Fatal(err)
	}
	if result.CompletionState.Outcome != "passed" || len(result.Result.VerdictJSON) == 0 {
		t.Fatalf("result %#v", result)
	}
}
