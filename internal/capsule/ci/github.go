package ci

import (
	"fmt"
	"strings"
)

// GitHubPullRequestEvent is the small webhook subset Capsule CI needs to build
// a normalized trigger. Raw webhook bodies remain ingress artifacts; this
// adapter deliberately keeps only the stable routing fields.
type GitHubPullRequestEvent struct {
	Action     string `json:"action"`
	Number     int    `json:"number"`
	After      string `json:"after,omitempty"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest struct {
		Head struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"base"`
		User struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"pull_request"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
}

func NormalizeGitHubPullRequestTrigger(event GitHubPullRequestEvent, pipeline string) (Trigger, error) {
	if event.Number <= 0 {
		return Trigger{}, fmt.Errorf("capsule ci github: pull request number is required")
	}
	headSHA := strings.TrimSpace(event.PullRequest.Head.SHA)
	if headSHA == "" {
		headSHA = strings.TrimSpace(event.After)
	}
	if headSHA == "" {
		return Trigger{}, fmt.Errorf("capsule ci github: head sha is required")
	}
	actor := strings.TrimSpace(event.Sender.Login)
	if actor == "" {
		actor = strings.TrimSpace(event.PullRequest.User.Login)
	}
	return Trigger{
		Kind:              "pull_request",
		Provider:          "github",
		EventID:           fmt.Sprintf("%s#%d:%s", event.Repository.FullName, event.Number, strings.TrimSpace(event.Action)),
		Actor:             actor,
		Ref:               event.PullRequest.Head.Ref,
		BaseRef:           event.PullRequest.Base.Ref,
		HeadSHA:           headSHA,
		RequestedPipeline: pipeline,
	}, nil
}

type GitHubCheckRun struct {
	Name        string             `json:"name"`
	HeadSHA     string             `json:"head_sha"`
	Status      string             `json:"status"`
	Conclusion  string             `json:"conclusion,omitempty"`
	DetailsURL  string             `json:"details_url,omitempty"`
	ExternalID  string             `json:"external_id,omitempty"`
	Output      GitHubCheckOutput  `json:"output"`
	Annotations []GitHubAnnotation `json:"annotations,omitempty"`
}

type GitHubCheckOutput struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

type GitHubAnnotation struct {
	Path            string `json:"path"`
	StartLine       int    `json:"start_line"`
	EndLine         int    `json:"end_line"`
	AnnotationLevel string `json:"annotation_level"`
	Message         string `json:"message"`
}

func BuildGitHubCheckRun(result RunResult, detailsURL string) (GitHubCheckRun, error) {
	headSHA, _ := result.Envelope.Trigger["head_sha"].(string)
	if strings.TrimSpace(headSHA) == "" {
		return GitHubCheckRun{}, fmt.Errorf("capsule ci github: envelope trigger head_sha is required")
	}
	conclusion := "neutral"
	switch result.Verdict.Outcome {
	case "passed":
		conclusion = "success"
	case "failed":
		conclusion = "failure"
	case "infra_failed":
		conclusion = "action_required"
	case "cancelled":
		conclusion = "cancelled"
	case "needs_input":
		conclusion = "neutral"
	}
	name := "Kitsoki Capsule CI"
	if result.Verdict.Pipeline != "" {
		name += " / " + result.Verdict.Pipeline
	}
	return GitHubCheckRun{
		Name:       name,
		HeadSHA:    headSHA,
		Status:     "completed",
		Conclusion: conclusion,
		DetailsURL: detailsURL,
		ExternalID: string(result.Job.ID),
		Output: GitHubCheckOutput{
			Title:   result.Verdict.Outcome,
			Summary: result.Verdict.Summary,
		},
	}, nil
}
