package main

import (
	"context"
	"testing"

	"kitsoki/internal/app"
	"kitsoki/internal/host"
	"kitsoki/internal/webconfig"
)

// TestFirstRunProviderHint verifies change 0.4: a fresh run with no provider
// gets an actionable message (not a silent replay fallback), and a run with a
// provider gets nothing.
func TestFirstRunProviderHint(t *testing.T) {
	if got := firstRunProviderHint(false, false); got == "" {
		t.Fatal("expected an actionable hint when no provider is configured, got empty")
	} else {
		for _, want := range []string{"no agent provider found", "ANTHROPIC_API_KEY", "claude", "--agent codex", "harness profile", "replay"} {
			if !contains(got, want) {
				t.Errorf("hint missing %q; hint was:\n%s", want, got)
			}
		}
	}
	if got := firstRunProviderHint(true, false); got != "" {
		t.Errorf("expected no hint when claude is on PATH, got:\n%s", got)
	}
	if got := firstRunProviderHint(false, true); got != "" {
		t.Errorf("expected no hint when a credential is present, got:\n%s", got)
	}
}

func TestAutoSelectHarnessUsesConfiguredAgentBackend(t *testing.T) {
	clearDefaultHarnessProviders(t)

	got, err := autoSelectHarness("codex")
	if err != nil {
		t.Fatalf("autoSelectHarness(codex): %v", err)
	}
	if got != "claude" {
		t.Fatalf("autoSelectHarness(codex) = %q, want claude CLI harness", got)
	}
}

func TestAutoSelectHarnessNoProviderErrorsInsteadOfReplay(t *testing.T) {
	clearDefaultHarnessProviders(t)

	got, err := autoSelectHarness("")
	if err == nil {
		t.Fatalf("autoSelectHarness with no provider = %q, want error", got)
	}
	msg := err.Error()
	for _, want := range []string{"no agent provider found", "--agent codex", "--harness replay --recording"} {
		if !contains(msg, want) {
			t.Fatalf("error missing %q; error was:\n%s", want, msg)
		}
	}
	if contains(msg, "--recording is required") {
		t.Fatalf("auto selection leaked replay-harness validation instead of provider setup guidance:\n%s", msg)
	}
}

func TestBuildHarnessDefaultCodexBackendDoesNotSelectReplay(t *testing.T) {
	clearDefaultHarnessProviders(t)
	t.Setenv(host.CodexBinEnv, "/tmp/kitsoki-test-codex")

	h, err := buildHarness("", "", "codex", "", "", &app.AppDef{})
	if err != nil {
		t.Fatalf("buildHarness default codex backend: %v", err)
	}
	if h == nil {
		t.Fatal("buildHarness default codex backend returned nil harness")
	}
	_ = h.Close()
}

func TestBugFilingAuthStartupNotice(t *testing.T) {
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("HOME", t.TempDir())
	restoreGHCLI := host.SetGHCLITokenForTest(func(context.Context) string { return "" })
	defer restoreGHCLI()

	got := bugFilingAuthStartupNotice(context.Background(), "o/r")
	for _, want := range []string{"GitHub bug filing is unavailable", "o/r", "Filing bugs is critical", "kitsoki gh-agent login"} {
		if !contains(got, want) {
			t.Fatalf("notice missing %q; notice was:\n%s", want, got)
		}
	}

	if got := bugFilingAuthStartupNotice(context.Background(), ""); got != "" {
		t.Fatalf("local bug filing should not warn, got:\n%s", got)
	}
	t.Setenv("GH_TOKEN", "test-token")
	if got := bugFilingAuthStartupNotice(context.Background(), "o/r"); got != "" {
		t.Fatalf("configured GitHub auth should not warn, got:\n%s", got)
	}
}

func TestRunAsUserStartupNotice(t *testing.T) {
	got := runAsUserStartupNotice(webconfig.WebConfig{}, "darwin")
	for _, want := range []string{"macOS agent delegation is not configured", "@kitsoki/run-as-user-setup", "agent_user_delegation", "filesystem sandbox"} {
		if !contains(got, want) {
			t.Fatalf("notice missing %q; notice was:\n%s", want, got)
		}
	}

	incomplete := webconfig.WebConfig{
		AgentUserDelegation: &webconfig.AgentUserDelegationConfig{
			Enabled:   true,
			RunAsUser: "kitsoki-agent",
		},
	}
	if got := runAsUserStartupNotice(incomplete, "darwin"); !contains(got, "missing wrapper_bin") {
		t.Fatalf("incomplete run_as_user should warn about wrapper_bin, got:\n%s", got)
	}

	configured := webconfig.WebConfig{
		AgentUserDelegation: &webconfig.AgentUserDelegationConfig{
			Enabled:    true,
			RunAsUser:  "kitsoki-agent",
			WrapperBin: "/Users/Shared/kitsoki/agent-bin",
		},
	}
	if got := runAsUserStartupNotice(configured, "darwin"); got != "" {
		t.Fatalf("configured run_as_user delegation should not warn, got:\n%s", got)
	}
	if got := runAsUserStartupNotice(webconfig.WebConfig{}, "linux"); got != "" {
		t.Fatalf("non-darwin should not warn, got:\n%s", got)
	}
}

func TestSetupWarningsFromConfigIncludesRunAsUserStory(t *testing.T) {
	warnings := setupWarningsFromConfig(webconfig.WebConfig{}, "darwin")
	if len(warnings) != 1 {
		t.Fatalf("expected one setup warning, got %#v", warnings)
	}
	got := warnings[0]
	if got.ID != "run-as-user" || got.StoryID != "run-as-user-setup" || got.StoryRef != "@kitsoki/run-as-user-setup" {
		t.Fatalf("unexpected warning identity: %#v", got)
	}
	for _, want := range []string{"macOS agent delegation is not configured", "agent_user_delegation", "filesystem sandbox", "kitsoki run @kitsoki/run-as-user-setup"} {
		if !contains(got.Title+" "+got.Body+" "+got.ActionCommand, want) {
			t.Fatalf("setup warning missing %q: %#v", want, got)
		}
	}

	incomplete := webconfig.WebConfig{
		AgentUserDelegation: &webconfig.AgentUserDelegationConfig{
			Enabled:   true,
			RunAsUser: "kitsoki-agent",
		},
	}
	if got := setupWarningsFromConfig(incomplete, "darwin"); len(got) != 1 || !contains(got[0].Body, "missing wrapper_bin") {
		t.Fatalf("incomplete run_as_user should warn about wrapper_bin, got %#v", got)
	}

	configured := webconfig.WebConfig{
		AgentUserDelegation: &webconfig.AgentUserDelegationConfig{
			Enabled:    true,
			RunAsUser:  "kitsoki-agent",
			WrapperBin: "/Users/Shared/kitsoki/agent-bin",
		},
	}
	if got := setupWarningsFromConfig(configured, "darwin"); len(got) != 0 {
		t.Fatalf("configured run_as_user delegation should suppress setup warning, got %#v", got)
	}
	if got := setupWarningsFromConfig(webconfig.WebConfig{}, "linux"); len(got) != 0 {
		t.Fatalf("non-darwin should suppress setup warning, got %#v", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func clearDefaultHarnessProviders(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
}
