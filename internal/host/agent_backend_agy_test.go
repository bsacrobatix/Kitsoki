package host

import (
	"os"
	"path/filepath"
	"testing"
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
