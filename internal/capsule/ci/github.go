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
	repo := strings.TrimSpace(event.Repository.FullName)
	if repo == "" {
		return Trigger{}, fmt.Errorf("capsule ci github: repository full_name is required")
	}
	headSHA := strings.TrimSpace(event.PullRequest.Head.SHA)
	if headSHA == "" {
		headSHA = strings.TrimSpace(event.After)
	}
	if headSHA == "" {
		return Trigger{}, fmt.Errorf("capsule ci github: head sha is required")
	}
	headRef := strings.TrimSpace(event.PullRequest.Head.Ref)
	if headRef == "" {
		return Trigger{}, fmt.Errorf("capsule ci github: head ref is required")
	}
	baseRef := strings.TrimSpace(event.PullRequest.Base.Ref)
	if baseRef == "" {
		return Trigger{}, fmt.Errorf("capsule ci github: base ref is required")
	}
	actor := strings.TrimSpace(event.Sender.Login)
	if actor == "" {
		actor = strings.TrimSpace(event.PullRequest.User.Login)
	}
	action := strings.TrimSpace(event.Action)
	if action == "" {
		action = "unknown"
	}
	return Trigger{
		Kind:              "pull_request",
		Provider:          "github",
		EventID:           fmt.Sprintf("%s#%d:%s", repo, event.Number, action),
		Actor:             actor,
		Ref:               headRef,
		BaseRef:           baseRef,
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
	conclusion, err := githubConclusion(result.Verdict.Outcome)
	if err != nil {
		return GitHubCheckRun{}, err
	}
	summary := strings.TrimSpace(result.Verdict.Summary)
	if summary == "" {
		summary = githubCheckSummary(result.Verdict)
	}
	title := result.Verdict.Outcome
	if title == "" {
		title = "unknown"
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
		DetailsURL: strings.TrimSpace(detailsURL),
		ExternalID: string(result.Job.ID),
		Output: GitHubCheckOutput{
			Title:   title,
			Summary: summary,
		},
	}, nil
}

func githubConclusion(outcome string) (string, error) {
	switch outcome {
	case "passed":
		return "success", nil
	case "failed":
		return "failure", nil
	case "infra_failed":
		return "action_required", nil
	case "cancelled":
		return "cancelled", nil
	case "needs_input":
		return "neutral", nil
	default:
		return "", fmt.Errorf("capsule ci github: unsupported verdict outcome %q", outcome)
	}
}

func githubCheckSummary(v Verdict) string {
	if len(v.Checks) == 0 {
		return "Capsule CI produced no check details."
	}
	var b strings.Builder
	b.WriteString("| check | kind | outcome |\n|---|---|---|\n")
	for _, check := range v.Checks {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", check.ID, check.Kind, check.Outcome)
	}
	return b.String()
}
