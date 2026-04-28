package host_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"hally/internal/host"
)

// fakeOneShotMCPBin returns the path to testdata/fake-oneshot-mcp.sh.
func fakeOneShotMCPBin(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(thisFile), "testdata", "fake-oneshot-mcp.sh")
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("fake-oneshot-mcp.sh not found at %s: %v", path, err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Fatalf("fake-oneshot-mcp.sh is not executable")
	}
	return path
}

// TestOracleAskWithMCP_RegisteredAsBuiltin verifies the handler is wired in.
func TestOracleAskWithMCP_RegisteredAsBuiltin(t *testing.T) {
	r := host.NewRegistry()
	host.RegisterBuiltins(r)
	if _, ok := r.Get("host.oracle.ask_with_mcp"); !ok {
		t.Fatal("host.oracle.ask_with_mcp was not registered by RegisterBuiltins")
	}
}

// TestOracleAskWithMCP_NoServers behaves identically to host.oracle.ask when
// mcp_servers is missing — no --mcp-config is passed, prompt is echoed back.
func TestOracleAskWithMCP_NoServers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-oneshot-mcp.sh requires bash")
	}
	t.Setenv(host.OracleBinEnv, fakeOneShotMCPBin(t))

	dir := t.TempDir()
	promptPath := filepath.Join(dir, "p.md")
	if err := os.WriteFile(promptPath, []byte("hello {{ args.who }}"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := host.OracleAskWithMCPHandler(context.Background(), map[string]any{
		"prompt_path": promptPath,
		"who":         "world",
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected Result.Error: %s", res.Error)
	}
	out, _ := res.Data["stdout"].(string)
	if !strings.Contains(out, "hello world") {
		t.Fatalf("stdout missing rendered prompt: %q", out)
	}
	// No --mcp-config should be passed → fake binary echoes mcp_config= empty.
	if !strings.Contains(out, "mcp_config=\n") && !strings.HasSuffix(strings.TrimSpace(out), "mcp_config=") {
		// Allow either "mcp_config=\n" or trailing "mcp_config=" without newline.
		if !strings.Contains(out, "mcp_config=\n") {
			t.Fatalf("expected empty mcp_config in stdout, got %q", out)
		}
	}
}

// TestOracleAskWithMCP_ServersMaterialized verifies that mcp_servers is written
// to a temp --mcp-config JSON and passed to the binary.
func TestOracleAskWithMCP_ServersMaterialized(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-oneshot-mcp.sh requires bash")
	}
	t.Setenv(host.OracleBinEnv, fakeOneShotMCPBin(t))

	dir := t.TempDir()
	promptPath := filepath.Join(dir, "p.md")
	if err := os.WriteFile(promptPath, []byte("propose a fix"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	mcpServers := map[string]any{
		"wiggum": map[string]any{
			"command": "python3",
			"args":    []any{"tools/loopy/wiggum-mcp.py", "--schema", "schemas/03.json"},
		},
	}

	res, err := host.OracleAskWithMCPHandler(context.Background(), map[string]any{
		"prompt_path":   promptPath,
		"mcp_servers":   mcpServers,
		"output_format": "json",
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected Result.Error: %s", res.Error)
	}

	// JSON parse exposed via stdout_json.
	parsed, ok := res.Data["stdout_json"].(map[string]any)
	if !ok {
		t.Fatalf("stdout_json missing or wrong shape: %T %v", res.Data["stdout_json"], res.Data["stdout_json"])
	}
	mcpCfgPath, _ := parsed["mcp_config_path"].(string)
	if mcpCfgPath == "" {
		t.Fatal("expected mcp_config_path to be set; --mcp-config was not passed")
	}

	// The fake binary captured the body — assert the wrapping under "mcpServers".
	body, ok := parsed["mcp_body"].(map[string]any)
	if !ok {
		t.Fatalf("mcp_body missing: %v", parsed["mcp_body"])
	}
	servers, ok := body["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("expected top-level mcpServers wrapping, got: %v", body)
	}
	wiggum, ok := servers["wiggum"].(map[string]any)
	if !ok {
		t.Fatalf("wiggum entry missing: %v", servers)
	}
	if wiggum["command"] != "python3" {
		t.Fatalf("wiggum.command = %v, want python3", wiggum["command"])
	}
}

// TestOracleAskWithMCP_TempFileCleanedUp verifies the temp config file is
// removed after the handler returns.
func TestOracleAskWithMCP_TempFileCleanedUp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-oneshot-mcp.sh requires bash")
	}
	t.Setenv(host.OracleBinEnv, fakeOneShotMCPBin(t))

	dir := t.TempDir()
	promptPath := filepath.Join(dir, "p.md")
	if err := os.WriteFile(promptPath, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := host.OracleAskWithMCPHandler(context.Background(), map[string]any{
		"prompt_path":   promptPath,
		"mcp_servers":   map[string]any{"wiggum": map[string]any{"command": "true"}},
		"output_format": "json",
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	parsed, _ := res.Data["stdout_json"].(map[string]any)
	mcpCfgPath, _ := parsed["mcp_config_path"].(string)
	if mcpCfgPath == "" {
		t.Fatal("expected mcp_config_path")
	}
	if _, err := os.Stat(mcpCfgPath); !os.IsNotExist(err) {
		t.Fatalf("temp mcp config %q not cleaned up: %v", mcpCfgPath, err)
	}
}

// TestOracleAskWithMCP_PromptAlias accepts `prompt:` as alias for `prompt_path:`.
func TestOracleAskWithMCP_PromptAlias(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-oneshot-mcp.sh requires bash")
	}
	t.Setenv(host.OracleBinEnv, fakeOneShotMCPBin(t))

	dir := t.TempDir()
	promptPath := filepath.Join(dir, "p.md")
	if err := os.WriteFile(promptPath, []byte("via prompt alias"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := host.OracleAskWithMCPHandler(context.Background(), map[string]any{
		"prompt": promptPath,
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected Result.Error: %s", res.Error)
	}
	out, _ := res.Data["stdout"].(string)
	if !strings.Contains(out, "via prompt alias") {
		t.Fatalf("stdout missing rendered prompt: %q", out)
	}
}

// TestOracleAskWithMCP_BinaryMissing returns Result.Error.
func TestOracleAskWithMCP_BinaryMissing(t *testing.T) {
	t.Setenv(host.OracleBinEnv, "/definitely/does/not/exist/claude")

	dir := t.TempDir()
	promptPath := filepath.Join(dir, "p.md")
	if err := os.WriteFile(promptPath, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := host.OracleAskWithMCPHandler(context.Background(), map[string]any{
		"prompt_path": promptPath,
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.Error == "" {
		t.Fatal("expected Result.Error when binary is missing")
	}
}

// TestOracleAskWithMCP_NonZeroExit propagates exit_code and Result.Error.
func TestOracleAskWithMCP_NonZeroExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-oneshot-mcp.sh requires bash")
	}
	t.Setenv(host.OracleBinEnv, fakeOneShotMCPBin(t))

	dir := t.TempDir()
	promptPath := filepath.Join(dir, "p.md")
	if err := os.WriteFile(promptPath, []byte("FAIL on purpose"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := host.OracleAskWithMCPHandler(context.Background(), map[string]any{
		"prompt_path": promptPath,
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if res.Error == "" {
		t.Fatal("Result.Error should be set on non-zero exit")
	}
	if code, _ := res.Data["exit_code"].(int); code == 0 {
		t.Fatal("exit_code should be non-zero")
	}
}

// TestOracleAskWithMCP_StdoutJSONParseError surfaces a parse-error sentinel
// when output_format=json and the binary returns non-JSON.
func TestOracleAskWithMCP_StdoutJSONParseError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake-oneshot-mcp.sh requires bash")
	}
	// Use the plain fake-oneshot.sh which always echoes plain text — no JSON.
	t.Setenv(host.OracleBinEnv, fakeOneShotBin(t))

	dir := t.TempDir()
	promptPath := filepath.Join(dir, "p.md")
	if err := os.WriteFile(promptPath, []byte("not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := host.OracleAskWithMCPHandler(context.Background(), map[string]any{
		"prompt_path":   promptPath,
		"output_format": "json",
	})
	if err != nil {
		t.Fatalf("unexpected Go error: %v", err)
	}
	if _, ok := res.Data["stdout_json"]; ok {
		t.Fatal("stdout_json should be absent when parse fails")
	}
	if _, ok := res.Data["stdout_json_parse_error"].(string); !ok {
		t.Fatal("expected stdout_json_parse_error sentinel")
	}
	// Sanity: the raw stdout is still available.
	out, _ := res.Data["stdout"].(string)
	if !strings.Contains(out, "not json") {
		t.Fatalf("stdout missing prompt echo: %q", out)
	}

	// Sanity: ensure the stdout is not accidentally valid JSON via some quirk.
	var any any
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &any) == nil {
		t.Fatalf("test premise broken: stdout %q is valid JSON", out)
	}
}
