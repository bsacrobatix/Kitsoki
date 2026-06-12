package host_test

import (
	"context"
	"strings"
	"testing"

	"kitsoki/internal/host"
)

// TestGitHubFileBug_WithEvidence proves the slice-#2 orchestration: ensure the
// evidence release, upload the assets, then create the issue with an Artifacts
// section (inlined screenshot + links) and the ```kitsoki metadata block.
func TestGitHubFileBug_WithEvidence(t *testing.T) {
	var createdRelease, uploaded, createdIssue bool
	var issueArgv string
	runner := func(ctx context.Context, dir, name string, args ...string) (string, string, int, error) {
		j := strings.Join(args, " ")
		switch {
		case len(args) > 0 && args[0] == "--version":
			return "gh version 2.x\n", "", 0, nil
		case strings.HasPrefix(j, "release view") && strings.Contains(j, "--json assets"):
			return `{"assets":[` +
				`{"name":"b1-screenshot.png","url":"https://github.com/o/r/releases/download/bug-evidence/b1-screenshot.png"},` +
				`{"name":"b1-har.json","url":"https://github.com/o/r/releases/download/bug-evidence/b1-har.json"}]}`, "", 0, nil
		case strings.HasPrefix(j, "release view"):
			if createdRelease {
				return "exists", "", 0, nil
			}
			return "", "release not found", 1, nil
		case strings.HasPrefix(j, "release create"):
			createdRelease = true
			return "https://github.com/o/r/releases/tag/bug-evidence\n", "", 0, nil
		case strings.HasPrefix(j, "release upload"):
			uploaded = true
			return "", "", 0, nil
		case strings.HasPrefix(j, "issue create"):
			createdIssue = true
			issueArgv = j
			return "https://github.com/o/r/issues/321\n", "", 0, nil
		}
		return "", "unexpected: " + j, 1, nil
	}
	restore := host.SetExecRunnerForTest(runner)
	defer restore()

	res, err := host.GitHubFileBug(context.Background(), host.GitHubBugFiling{
		Repo:      "o/r",
		Title:     "web: surprising judge gate",
		Body:      "The gate fired where I didn't expect.",
		Severity:  "P2",
		Component: "web",
		Target:    "kitsoki",
		TraceRef:  "trace://x",
		FiledBy:   "brad",
		Evidence: []host.EvidenceFile{
			{Name: "b1-screenshot.png", Path: "/tmp/b1-screenshot.png", Image: true, Label: "Screenshot"},
			{Name: "b1-har.json", Path: "/tmp/b1-har.json", Label: "HAR (scrubbed)"},
		},
	})
	if err != nil {
		t.Fatalf("GitHubFileBug: %v", err)
	}
	if !createdRelease || !uploaded || !createdIssue {
		t.Fatalf("expected release+upload+issue (rel=%v up=%v iss=%v)", createdRelease, uploaded, createdIssue)
	}
	if res.Number != "321" || !strings.Contains(res.URL, "/issues/321") {
		t.Fatalf("issue result: %+v", res)
	}
	if res.Assets["b1-screenshot.png"] == "" || res.Assets["b1-har.json"] == "" {
		t.Fatalf("asset URLs missing: %+v", res.Assets)
	}
	// The issue body must carry the Artifacts section (inlined screenshot + link)
	// and the ```kitsoki metadata block.
	for _, want := range []string{
		"## Artifacts",
		"![b1-screenshot.png](https://github.com/o/r/releases/download/bug-evidence/b1-screenshot.png)",
		"[HAR (scrubbed)](https://github.com/o/r/releases/download/bug-evidence/b1-har.json)",
		"```kitsoki",
		"trace_ref: trace://x",
		"--label P2",
		"--label comp:web",
		"--label target:kitsoki",
	} {
		if !strings.Contains(issueArgv, want) {
			t.Errorf("issue create argv missing %q", want)
		}
	}
}

// TestGitHubFileBug_NoEvidence skips the release path entirely (text-only file).
func TestGitHubFileBug_NoEvidence(t *testing.T) {
	var touchedRelease bool
	runner := func(ctx context.Context, dir, name string, args ...string) (string, string, int, error) {
		j := strings.Join(args, " ")
		switch {
		case len(args) > 0 && args[0] == "--version":
			return "gh version 2.x\n", "", 0, nil
		case strings.HasPrefix(j, "release"):
			touchedRelease = true
			return "", "", 0, nil
		case strings.HasPrefix(j, "issue create"):
			return "https://github.com/o/r/issues/9\n", "", 0, nil
		}
		return "", "unexpected: " + j, 1, nil
	}
	restore := host.SetExecRunnerForTest(runner)
	defer restore()

	res, err := host.GitHubFileBug(context.Background(), host.GitHubBugFiling{
		Repo: "o/r", Title: "text only", Body: "no evidence", Severity: "P3",
	})
	if err != nil {
		t.Fatalf("GitHubFileBug: %v", err)
	}
	if touchedRelease {
		t.Fatal("no evidence → must not touch the release path")
	}
	if res.Number != "9" {
		t.Fatalf("number: %q", res.Number)
	}
}
