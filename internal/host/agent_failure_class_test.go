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
		{name: "rate limit text is agent_quota", text: "Error: rate_limit_error: you have hit the rate limit", want: "agent_quota"},
		{name: "429 is agent_quota", text: "HTTP 429 Too Many Requests", want: "agent_quota"},
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

func TestClassifyAgentFailureTextNeverMatchesPlainFilesystemPermissionErrors(t *testing.T) {
	// A bare "permission denied" (e.g. a filesystem error, not an agent auth
	// failure) must not be misclassified as agent_auth.
	if got := ClassifyAgentFailureText("open /workspace/.git/HEAD: permission denied"); got != "" {
		t.Fatalf("plain filesystem permission denied classified as %q, want unclassified", got)
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
