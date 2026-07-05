package host

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

var githubAPI = githubAPIClient{
	baseURL: "https://api.github.com",
	client:  http.DefaultClient,
}

type githubAPIClient struct {
	baseURL string
	client  *http.Client
}

func SetGitHubAPIForTest(baseURL string, client *http.Client) func() {
	prev := githubAPI
	githubAPI = githubAPIClient{baseURL: strings.TrimRight(baseURL, "/"), client: client}
	return func() { githubAPI = prev }
}

func githubToken(ctx context.Context) string {
	if env := CLIExecEnvFromCtx(ctx); len(env) > 0 {
		if tok := strings.TrimSpace(env["GH_TOKEN"]); tok != "" {
			return tok
		}
		if tok := strings.TrimSpace(env["GITHUB_TOKEN"]); tok != "" {
			return tok
		}
	}
	if tok := strings.TrimSpace(os.Getenv("GH_TOKEN")); tok != "" {
		return tok
	}
	return strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
}

func githubAPIJSON(ctx context.Context, method, path string, body any, out any) (int, string, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, "", err
		}
		r = bytes.NewReader(b)
	}
	return githubAPIRequest(ctx, method, path, "application/json", r, out)
}

func githubAPIRequest(ctx context.Context, method, path, contentType string, body io.Reader, out any) (int, string, error) {
	return githubAPIRequestAccept(ctx, method, path, "application/vnd.github+json", contentType, body, out)
}

func githubAPIRequestAccept(ctx context.Context, method, path, accept, contentType string, body io.Reader, out any) (int, string, error) {
	token := githubToken(ctx)
	if token == "" && githubMethodRequiresToken(method) {
		return 0, "", fmt.Errorf("GH_TOKEN or GITHUB_TOKEN is required for native GitHub API calls")
	}
	base := strings.TrimRight(githubAPI.baseURL, "/")
	if base == "" {
		base = "https://api.github.com"
	}
	client := githubAPI.client
	if client == nil {
		client = http.DefaultClient
	}
	target := path
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		target = base + "/" + strings.TrimLeft(path, "/")
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return 0, "", err
	}
	if strings.TrimSpace(accept) == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, "", err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, string(data), err
		}
	}
	return resp.StatusCode, string(data), nil
}

func githubMethodRequiresToken(method string) bool {
	return strings.ToUpper(strings.TrimSpace(method)) != http.MethodGet
}

func githubIssueURL(repo string, number int) string {
	return fmt.Sprintf("https://github.com/%s/issues/%d", repo, number)
}

func githubLabelPath(repo, label string) string {
	return fmt.Sprintf("repos/%s/labels/%s", strings.Trim(repo, "/"), url.PathEscape(label))
}
