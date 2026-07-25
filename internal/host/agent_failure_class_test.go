package host

import (
	"context"
	"errors"
	"testing"
)

func TestClassifyAgentFailureText(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
	}{
		{name: "empty text is unclassified", text: "", want: ""},
		{name: "invalid api key is agent_auth", text: "Error: Invalid API Key provided", want: "agent_auth"},
		{name: "authentication_error is agent_auth", text: `{"type":"error","error":{"type":"authentication_error"}}`, want: "agent_auth"},
		{name: "please run claude login is agent_auth", text: "Please run \x60claude login\x60 to authenticate.", want: "agent_auth"},
		{name: "oauth token expired is agent_auth", text: "the OAuth token expired, please re-authenticate", want: "agent_auth"},
		{name: "bare 401 is agent_auth", text: "request failed with status 401", want: "agent_auth"},
		{name: "HTTP 1.1 401 is agent_auth", text: "HTTP/1.1 401 Unauthorized", want: "agent_auth"},
		{name: "JSON status code 401 is agent_auth", text: `{"status_code":401,"error":"invalid token"}`, want: "agent_auth"},
		{name: "rate limit text is agent_quota", text: "Error: rate_limit_error: you have hit the rate limit", want: "agent_quota"},
		{name: "429 is agent_quota", text: "HTTP 429 Too Many Requests", want: "agent_quota"},
		{name: "status code 429 is agent_quota", text: "request failed with status code: 429", want: "agent_quota"},
		{name: "JSON status 429 is agent_quota", text: `{"status":429,"error":"request rejected"}`, want: "agent_quota"},
		{name: "credit balance is agent_quota", text: "Your credit balance is too low to access the Anthropic API", want: "agent_quota"},
		{name: "typed quota survives process boundary", text: "agent_quota: coding-agent provider unavailable", want: "agent_quota"},
		{name: "typed auth survives process boundary", text: "agent_auth: coding-agent provider unavailable", want: "agent_auth"},
		{name: "timeout is infra", text: "context deadline exceeded", want: "infra"},
		{name: "connection refused is infra", text: "dial tcp: connection refused", want: "infra"},
		{name: "binary not found is infra", text: "exec: \"claude\": binary not found", want: "infra"},
		{name: "unrecognized text is unclassified", text: "the story's gate check did not pass", want: ""},
		{name: "auth checked before quota when both could match", text: "authentication failed: rate_limit exceeded during login", want: "agent_auth"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyAgentFailureText(tc.text); got != tc.want {
				t.Fatalf("ClassifyAgentFailureText(%q) = %q, want %q", tc.text, got, tc.want)
			}
		})
	}
}

func TestClassifyAgentFailureTextRejectsNumericIdentifierCollisions(t *testing.T) {
	for _, text := range []string{
		"a3a0429046",
		"dispatch-orphaned-report-dispatch-orphaned-report-a3a0429046",
		"workspace creation failed for /reports/a3a0429046/result.json",
		"ticket ABR-429271 could not create a workspace",
		"artifact /status/429/receipt.json is missing",
		"digest f401429deadbeef",
		"status 429271 from an internal sequence counter",
		"HTTP 4019 is not a three-digit response",
	} {
		if got := ClassifyAgentFailureText(text); got != "" {
			t.Errorf("ClassifyAgentFailureText(%q) = %q, want unclassified", text, got)
		}
	}
}

func TestLooksRateLimitedRejectsNumericIdentifierCollisions(t *testing.T) {
	for _, text := range []string{
		"a3a0429046",
		"ticket ABR-429271",
		"artifact /status/429/receipt.json",
	} {
		if looksRateLimited(text) {
			t.Errorf("looksRateLimited(%q) = true, want false", text)
		}
	}
}

func TestClassifyAgentFailureTextNeverMatchesPlainFilesystemPermissionErrors(t *testing.T) {
	// A bare "permission denied" (e.g. a filesystem error, not an agent auth
	// failure) must not be misclassified as agent_auth.
	if got := ClassifyAgentFailureText("open /workspace/.git/HEAD: permission denied"); got != "" {
		t.Fatalf("plain filesystem permission denied classified as %q, want unclassified", got)
	}
}

func TestClassifyAgentFailureTextTreatsCLIContractErrorsAsInfra(t *testing.T) {
	for _, message := range []string{
		"flags provided but not defined: -strict-mcp-config",
		"flag provided but not defined: -output-format",
		"unknown flag: --app_data_dir",
	} {
		if got := ClassifyAgentFailureText(message); got != "infra" {
			t.Errorf("ClassifyAgentFailureText(%q) = %q, want infra", message, got)
		}
	}
}

func TestNormalizeAgentProviderFailurePreservesCancellation(t *testing.T) {
	got := normalizeAgentProviderFailure(ClaudeRun{
		Stdout: "partial output mentioned HTTP 429 before cancellation",
		Infra:  context.Canceled,
	})
	if !errors.Is(got.Infra, context.Canceled) {
		t.Fatalf("Infra = %v, want context.Canceled", got.Infra)
	}
	if got.FailureClass != "" {
		t.Fatalf("FailureClass = %q, want empty for cancellation", got.FailureClass)
	}
}
