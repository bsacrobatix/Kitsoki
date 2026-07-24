package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"kitsoki/internal/host/agentruntime"
)

func TestAgyTranslationCopiesCurrentWorkerCredentialAndMCPLayouts(t *testing.T) {
	sourceHome := t.TempDir()
	t.Setenv("HOME", sourceHome)

	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(sourceHome, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	write(".gemini/antigravity-cli/antigravity-oauth-token", "worker-token")
	write(".gemini/antigravity-cli/installation_id", "worker-installation")
	write(".gemini/config/config.json", `{"configured":true}`)

	mcpPath := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(mcpPath, []byte(`{"mcpServers":{"validator":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	inv := agyBackend{}.TranslateInvocation(
		[]string{"-p", "--mcp-config", mcpPath, "--strict-mcp-config"},
		"prompt",
		"",
	)
	if inv.Cleanup != nil {
		defer inv.Cleanup()
	}
	isolatedHome := inv.EnvOverrides["HOME"]
	if isolatedHome == "" || !inv.InheritHome {
		t.Fatalf("isolated HOME missing: env=%v inherit=%v", inv.EnvOverrides, inv.InheritHome)
	}

	assertFile := func(rel, want string) {
		t.Helper()
		got, err := os.ReadFile(filepath.Join(isolatedHome, rel))
		if err != nil {
			t.Fatalf("read isolated %s: %v", rel, err)
		}
		if string(got) != want {
			t.Fatalf("isolated %s = %q, want %q", rel, got, want)
		}
	}
	assertFile(".gemini/antigravity-cli/antigravity-oauth-token", "worker-token")
	assertFile(".gemini/antigravity-cli/installation_id", "worker-installation")
	assertFile(".gemini/config/config.json", `{"configured":true}`)
	assertFile(".gemini/config/mcp_config.json", `{"mcpServers":{"validator":{}}}`)
	assertFile(".gemini/antigravity-cli/mcp_config.json", `{"mcpServers":{"validator":{}}}`)
}

func TestAgyBufferedTransportReliesOnAbsoluteTimeoutNotStreamInactivity(t *testing.T) {
	t.Setenv("KITSOKI_AGENT_ACTIVITY_TIMEOUT", "20ms")

	runner := func(ctx context.Context, _ []string, _ string, _ string) (ClaudeRun, error) {
		select {
		case <-time.After(75 * time.Millisecond):
			return ClaudeRun{Stdout: "done"}, nil
		case <-ctx.Done():
			return ClaudeRun{Infra: ctx.Err()}, nil
		}
	}
	ctx := WithAgentBackend(context.Background(), agyBackend{})
	ctx = WithAgyRunner(ctx, runner)

	run, _, err := (AgentStreamer{
		Bin:        "stub://agy",
		CLIArgs:    []string{"-p"},
		Stdin:      "prompt",
		WorkingDir: t.TempDir(),
		Sandbox: &AgentSandboxSpec{
			InheritHome: true,
			Resources: agentruntime.ResourcePolicy{
				Timeout:         time.Second,
				ActivityTimeout: 20 * time.Millisecond,
			},
		},
	}).Run(ctx)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if run.Infra != nil {
		t.Fatalf("buffered agy call was canceled by stream inactivity: %v", run.Infra)
	}
	if run.Stdout != "done" {
		t.Fatalf("stdout = %q, want done", run.Stdout)
	}
}
