package host_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"kitsoki/internal/host"
)

func TestGitVCS_OpenPR_PublishesBranchBeforeCreate(t *testing.T) {
	runner := &openPRPublishRunner{}
	restore := host.SetExecRunnerForTest(runner.run)
	defer restore()

	res, err := host.GitVCSHandler(context.Background(), map[string]any{
		"op":      "open_pr",
		"workdir": "/repo",
		"title":   "PR",
		"body":    "body",
		"base":    "main",
	})
	if err != nil {
		t.Fatalf("infra: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("open_pr did not publish the feature branch before `gh pr create`; PR creation aborted with: %s", res.Error)
	}
	if res.Data["pr_id"] != "42" {
		t.Fatalf("pr_id: %v", res.Data["pr_id"])
	}
	if got := fmt.Sprint(res.Data["url"]); !strings.Contains(got, "/pull/42") {
		t.Fatalf("url: %v", res.Data["url"])
	}
}

type openPRPublishRunner struct {
	published bool
}

func (r *openPRPublishRunner) run(_ context.Context, _ string, name string, args ...string) (string, string, int, error) {
	key := name + " " + strings.Join(args, " ")
	switch {
	case key == "gh --version":
		return "gh version 2.x\n", "", 0, nil
	case key == "git push -u origin HEAD":
		r.published = true
		return "", "", 0, nil
	case strings.HasPrefix(key, "gh pr create "):
		if !r.published {
			return "", "pull request create failed: GraphQL: No commits between main and feature/fix (createPullRequest)", 1, nil
		}
		return "https://github.com/o/r/pull/42\n", "", 0, nil
	default:
		return "", fmt.Sprintf("unexpected command: %s", key), 1, nil
	}
}
