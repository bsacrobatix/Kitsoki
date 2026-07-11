package ci

import (
	"testing"

	"kitsoki/internal/artifactjob"
	"kitsoki/internal/capsule/executor"
)

func TestNormalizeGitHubPullRequestTrigger(t *testing.T) {
	var event GitHubPullRequestEvent
	event.Action = "synchronize"
	event.Number = 42
	event.Repository.FullName = "owner/repo"
	event.PullRequest.Head.Ref = "feature/capsule-ci"
	event.PullRequest.Head.SHA = "abc123"
	event.PullRequest.Base.Ref = "main"
	event.PullRequest.User.Login = "author"
	event.Sender.Login = "sender"
	trigger, err := NormalizeGitHubPullRequestTrigger(event, "change")
	if err != nil {
		t.Fatal(err)
	}
	if trigger.Kind != "pull_request" || trigger.Provider != "github" || trigger.EventID != "owner/repo#42:synchronize" || trigger.HeadSHA != "abc123" || trigger.RequestedPipeline != "change" {
		t.Fatalf("trigger %#v", trigger)
	}
}

func TestBuildGitHubCheckRunUsesCapsuleVerdict(t *testing.T) {
	check, err := BuildGitHubCheckRun(RunResult{
		Job: artifactjob.Job{ID: "job-1"},
		Envelope: executor.Envelope{Trigger: map[string]any{
			"head_sha": "abc123",
		}},
		Verdict: Verdict{Pipeline: "change", Outcome: "passed", Summary: "all gates passed"},
	}, "https://runs.example/job-1")
	if err != nil {
		t.Fatal(err)
	}
	if check.Name != "Kitsoki Capsule CI / change" || check.HeadSHA != "abc123" || check.Conclusion != "success" || check.ExternalID != "job-1" {
		t.Fatalf("check %#v", check)
	}
}

func TestBuildGitHubCheckRunRequiresHeadSHA(t *testing.T) {
	if _, err := BuildGitHubCheckRun(RunResult{Verdict: Verdict{Outcome: "passed"}}, ""); err == nil {
		t.Fatal("expected missing head sha error")
	}
}
