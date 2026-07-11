package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/ci"
	"kitsoki/internal/capsule/executor"
)

func TestCapsuleCIGitHubTriggerCommandNormalizesPullRequestPayload(t *testing.T) {
	root := t.TempDir()
	payloadPath := filepath.Join(root, "payload.json")
	payload := []byte(`{
  "action": "synchronize",
  "number": 42,
  "repository": {"full_name": "owner/repo"},
  "pull_request": {
    "head": {"ref": "feature/capsule-ci", "sha": "abc123"},
    "base": {"ref": "main", "sha": "base123"},
    "user": {"login": "author"}
  },
  "sender": {"login": "sender"}
}`)
	if err := os.WriteFile(payloadPath, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := execRoot(t, "capsule", "ci", "github", "trigger", "--payload", payloadPath, "--pipeline", "change")
	if err != nil {
		t.Fatalf("trigger: %v\n%s", err, out)
	}
	var trigger ci.Trigger
	if err := json.Unmarshal([]byte(out), &trigger); err != nil {
		t.Fatalf("decode trigger: %v\n%s", err, out)
	}
	if trigger.Kind != "pull_request" || trigger.Provider != "github" || trigger.EventID != "owner/repo#42:synchronize" || trigger.HeadSHA != "abc123" || trigger.RequestedPipeline != "change" {
		t.Fatalf("trigger %#v", trigger)
	}
}

func TestCapsuleCIGitHubCheckCommandProjectsRunRecord(t *testing.T) {
	root := t.TempDir()
	result := ci.RunResult{
		Job: artifactjob.Job{ID: "job-1"},
		Envelope: executor.Envelope{Trigger: map[string]any{
			"head_sha": "abc123",
		}},
		Verdict: ci.Verdict{Pipeline: "change", Outcome: "failed", Checks: []ci.Check{{ID: "tests", Kind: "deterministic", Outcome: "failed"}}},
	}
	if err := (ci.FileRunStore{ProjectRoot: root}).Write(ci.RunRecord{JobID: "job-1", Result: result}); err != nil {
		t.Fatal(err)
	}
	out, err := execRoot(t, "capsule", "ci", "github", "check", "--project", root, "--job", "job-1", "--details-url", "https://runs.example/job-1")
	if err != nil {
		t.Fatalf("check: %v\n%s", err, out)
	}
	var check ci.GitHubCheckRun
	if err := json.Unmarshal([]byte(out), &check); err != nil {
		t.Fatalf("decode check: %v\n%s", err, out)
	}
	if check.Name != "Kitsoki Capsule CI / change" || check.HeadSHA != "abc123" || check.Conclusion != "failure" || check.DetailsURL != "https://runs.example/job-1" || check.ExternalID != "job-1" {
		t.Fatalf("check %#v", check)
	}
	if check.Output.Summary == "" {
		t.Fatalf("missing fallback summary %#v", check.Output)
	}
}
