// Package host — host.oracle.ask_with_mcp handler for one-shot Claude calls
// that need MCP servers attached (typed JSON via wiggum-style schema validators
// being the primary use-case).
//
// This is host.oracle.ask plus an mcp_servers: arg that is materialized into a
// temporary --mcp-config file and passed to `claude -p`. The bug-fix room
// uses this for every LLM-driven phase (proposal §5.2, §7.1).
package host

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"hally/internal/expr"
)

// OracleAskWithMCPHandler implements host.oracle.ask_with_mcp.
//
// Required args:
//   - prompt_path | prompt (string): path to a prompt template file. If
//     relative, resolved against HALLY_APP_DIR (set by the loader) or cwd.
//
// Optional args:
//   - working_dir   (string): cwd for the claude subprocess.
//   - mcp_servers   (map):    server-name → { command: str, args: [str],
//                              env: {k:v} }. Materialized into a temp
//                              --mcp-config JSON file for the duration of
//                              the call. Empty/missing → no --mcp-config.
//   - output_format (string): "text" (default) or "json". When "json", the
//                              handler additionally parses stdout as JSON
//                              and exposes it as `stdout_json` for binding.
//   - schema        (string): informational; passed through unchanged. The
//                              MCP server is responsible for enforcement.
//
// All other keys in args are template variables ({{ args.X }}).
//
// Returns Result.Data with:
//   - stdout      (string): claude's text reply
//   - stdout_json (any):    parsed JSON when output_format=="json" and parse succeeds
//   - exit_code   (int):    claude's exit code
//   - ok          (bool):   exit_code == 0
//
// On all expected errors (binary missing, prompt unreadable, MCP config
// marshal failure, non-zero exit) the handler returns Result{Error: ...}
// rather than a Go error so on_error: routing remains deterministic.
func OracleAskWithMCPHandler(ctx context.Context, args map[string]any) (Result, error) {
	promptPath, _ := args["prompt_path"].(string)
	if strings.TrimSpace(promptPath) == "" {
		// Accept the proposal-style "prompt:" alias too.
		if alt, _ := args["prompt"].(string); strings.TrimSpace(alt) != "" {
			promptPath = alt
		}
	}
	if strings.TrimSpace(promptPath) == "" {
		return Result{Error: "host.oracle.ask_with_mcp: prompt_path (or prompt) argument is required"}, nil
	}

	resolved := resolvePromptPath(promptPath)
	raw, err := os.ReadFile(resolved)
	if err != nil {
		return Result{Error: fmt.Sprintf("host.oracle.ask_with_mcp: read prompt %q: %v", resolved, err)}, nil
	}

	rendered, err := expr.Render(string(raw), expr.Env{Args: args})
	if err != nil {
		return Result{Error: fmt.Sprintf("host.oracle.ask_with_mcp: render prompt %q: %v", resolved, err)}, nil
	}

	bin := os.Getenv(OracleBinEnv)
	if bin == "" {
		path, lookErr := exec.LookPath("claude")
		if lookErr != nil {
			return Result{Error: ErrOracleUnavailable.Error()}, nil
		}
		bin = path
	}

	workingDir, _ := args["working_dir"].(string)
	if workingDir == "" {
		workingDir = filepath.Dir(resolved)
	}

	outputFormat := "text"
	if of, _ := args["output_format"].(string); of != "" {
		outputFormat = of
	}

	cliArgs := []string{
		"-p",
		"--output-format", outputFormat,
		"--permission-mode", "bypassPermissions",
	}

	// Materialize mcp_servers (if any) into a temp config file.
	var mcpConfigPath string
	if mcpServers, ok := args["mcp_servers"].(map[string]any); ok && len(mcpServers) > 0 {
		mcpConfig := map[string]any{"mcpServers": mcpServers}
		mcpBytes, mErr := json.Marshal(mcpConfig)
		if mErr != nil {
			return Result{Error: fmt.Sprintf("host.oracle.ask_with_mcp: marshal mcp_servers: %v", mErr)}, nil
		}
		f, fErr := os.CreateTemp("", "hally-mcp-*.json")
		if fErr != nil {
			return Result{Error: fmt.Sprintf("host.oracle.ask_with_mcp: create mcp config tempfile: %v", fErr)}, nil
		}
		if _, wErr := f.Write(mcpBytes); wErr != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
			return Result{Error: fmt.Sprintf("host.oracle.ask_with_mcp: write mcp config: %v", wErr)}, nil
		}
		_ = f.Close()
		mcpConfigPath = f.Name()
		defer os.Remove(mcpConfigPath)
		cliArgs = append(cliArgs, "--mcp-config", mcpConfigPath)
	}

	cmd := exec.CommandContext(ctx, bin, cliArgs...)
	cmd.Stdin = strings.NewReader(rendered)
	cmd.Dir = workingDir

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	exitCode := 0
	if runErr != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			stderrText := strings.TrimSpace(stderr.String())
			msg := fmt.Sprintf("host.oracle.ask_with_mcp: claude exec failed: %v", runErr)
			if stderrText != "" {
				msg = fmt.Sprintf("%s\nstderr: %s", msg, stderrText)
			}
			return Result{Error: msg}, nil
		}
	}

	out := strings.TrimRight(stdout.String(), "\n")
	res := Result{
		Data: map[string]any{
			"stdout":    out,
			"exit_code": exitCode,
			"ok":        exitCode == 0,
		},
	}

	if outputFormat == "json" && exitCode == 0 && out != "" {
		var parsed any
		if jErr := json.Unmarshal([]byte(out), &parsed); jErr == nil {
			res.Data["stdout_json"] = parsed
		} else {
			// Don't fail the handler — bind: { foo: stdout_json } will silently
			// not bind, and an explicit on_error: route can still fire if the
			// state machine treats absent-binding as a failure. The text stdout
			// remains available for diagnostics.
			res.Data["stdout_json_parse_error"] = jErr.Error()
		}
	}

	if exitCode != 0 {
		stderrText := strings.TrimSpace(stderr.String())
		if stderrText != "" {
			res.Error = stderrText
		} else if out != "" {
			res.Error = out
		} else {
			res.Error = fmt.Sprintf("claude exited with code %d", exitCode)
		}
	}
	return res, nil
}
