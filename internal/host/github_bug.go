// github_bug.go — slice #2 orchestration: file a kitsoki bug as a GitHub issue
// WITH its captured evidence.
//
// `gh` (and a PAT) cannot attach binaries to an issue the way the web UI does —
// that path needs an authenticated github.com web session. The verifiable,
// PAT-compatible substitute is **release assets**: the evidence
// (screenshot.png / har.json / rrweb.json / console.json) is uploaded to a
// dedicated `bug-evidence` release on the same repo, and the issue body inlines
// the screenshot + links the rest at their stable release-download URLs. Anyone
// can open the issue and the asset URLs to independently verify the filing.
//
// This reuses the slice-#1 create op (labels + the ```kitsoki metadata block)
// and the one cliExec seam, so it's testable with a stubbed runner (no real gh).
package host

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// EvidenceFile is one artifact uploaded alongside a GitHub-filed bug. The local
// file's basename becomes the release-asset name, so callers pre-name files
// uniquely (e.g. with a per-bug prefix) to avoid clobbering across bugs.
type EvidenceFile struct {
	Name  string // asset name (== basename of Path); also the body link text default
	Path  string // local file to upload
	Image bool   // inline as an image in the body (vs. a plain link)
	Label string // human label in the body (defaults to Name)
}

// GitHubBugFiling is the input to GitHubFileBug.
type GitHubBugFiling struct {
	Repo                          string
	Title, Body                   string
	Severity, Component, Target   string
	TraceRef, KitsokiRev, FiledBy string
	Evidence                      []EvidenceFile
	ReleaseTag                    string // default "bug-evidence"
}

// GitHubBugResult is what GitHubFileBug returns.
type GitHubBugResult struct {
	URL    string            // the new issue's URL
	Number string            // the new issue's number
	Assets map[string]string // evidence name → release-download URL
}

// GitHubFileBug uploads any evidence as release assets, builds the issue body
// (prose + an Artifacts section + the create op's ```kitsoki metadata block),
// and creates the issue. It is the single orchestration the web Report-bug RPC
// (and, later, the CLI) call to file a kitsoki bug on GitHub.
func GitHubFileBug(ctx context.Context, in GitHubBugFiling) (GitHubBugResult, error) {
	if !ghAvailable(ctx) {
		return GitHubBugResult{}, fmt.Errorf("gh CLI not available — install github.com/cli/cli and run `gh auth login`")
	}
	tag := strings.TrimSpace(in.ReleaseTag)
	if tag == "" {
		tag = "bug-evidence"
	}

	body := in.Body
	assets := map[string]string{}
	if len(in.Evidence) > 0 {
		urls, err := ghUploadReleaseAssets(ctx, in.Repo, tag, in.Evidence)
		if err != nil {
			return GitHubBugResult{}, err
		}
		assets = urls
		body += ghArtifactsSection(in.Evidence, urls)
	}

	res, err := ghTicketCreate(ctx, map[string]any{
		"repo":        in.Repo,
		"title":       in.Title,
		"body":        body,
		"severity":    in.Severity,
		"component":   in.Component,
		"target":      in.Target,
		"trace_ref":   in.TraceRef,
		"kitsoki_rev": in.KitsokiRev,
		"filed_by":    in.FiledBy,
	})
	if err != nil {
		return GitHubBugResult{}, err
	}
	if res.Error != "" {
		return GitHubBugResult{}, fmt.Errorf("%s", res.Error)
	}
	url, _ := res.Data["url"].(string)
	num, _ := res.Data["id"].(string)
	return GitHubBugResult{URL: url, Number: num, Assets: assets}, nil
}

// ghUploadReleaseAssets ensures the evidence release exists, uploads every file
// to it (--clobber so a re-file overwrites a same-named asset), and returns each
// asset's browser-download URL keyed by name.
func ghUploadReleaseAssets(ctx context.Context, repo, tag string, files []EvidenceFile) (map[string]string, error) {
	// Ensure the release exists (idempotent).
	if _, _, code, _ := cliExec(ctx, "", "gh", "release", "view", tag, "--repo", repo); code != 0 {
		_, stderr, code, err := cliExec(ctx, "", "gh", "release", "create", tag, "--repo", repo,
			"--title", "Kitsoki bug evidence", "--notes", "Evidence (screenshots, HAR, rrweb, console) for kitsoki-filed bugs, linked from their issues.")
		if err != nil {
			return nil, fmt.Errorf("release create: %v", err)
		}
		if code != 0 {
			return nil, fmt.Errorf("release create: %s", strings.TrimSpace(stderr))
		}
	}

	args := []string{"release", "upload", tag, "--repo", repo, "--clobber"}
	for _, f := range files {
		args = append(args, f.Path)
	}
	if _, stderr, code, err := cliExec(ctx, "", "gh", args...); err != nil {
		return nil, fmt.Errorf("release upload: %v", err)
	} else if code != 0 {
		return nil, fmt.Errorf("release upload: %s", strings.TrimSpace(stderr))
	}

	// Read back the asset download URLs.
	stdout, stderr, code, err := cliExec(ctx, "", "gh", "release", "view", tag, "--repo", repo, "--json", "assets")
	if err != nil {
		return nil, fmt.Errorf("release view: %v", err)
	}
	if code != 0 {
		return nil, fmt.Errorf("release view: %s", strings.TrimSpace(stderr))
	}
	var rv struct {
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal([]byte(stdout), &rv); err != nil {
		return nil, fmt.Errorf("release view: parse JSON: %v", err)
	}
	urls := make(map[string]string, len(files))
	for _, a := range rv.Assets {
		urls[a.Name] = a.URL
	}
	return urls, nil
}

// ghArtifactsSection renders the "## Artifacts" body block: the screenshot
// inlined as an image, the rest linked, all at their release-download URLs.
func ghArtifactsSection(files []EvidenceFile, urls map[string]string) string {
	var sb strings.Builder
	sb.WriteString("\n\n## Artifacts\n\n")
	sb.WriteString("_Captured in the browser, scrubbed server-side, uploaded to this repo's `bug-evidence` release._\n\n")
	for _, f := range files {
		url := urls[f.Name]
		if url == "" {
			continue
		}
		label := f.Label
		if label == "" {
			label = f.Name
		}
		if f.Image {
			fmt.Fprintf(&sb, "**%s**\n\n![%s](%s)\n\n", label, f.Name, url)
		} else {
			fmt.Fprintf(&sb, "- [%s](%s)\n", label, url)
		}
	}
	return sb.String()
}
