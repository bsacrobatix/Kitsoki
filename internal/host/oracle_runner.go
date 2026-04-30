// Package host — internal helpers shared between the one-shot oracle
// handlers (host.oracle.ask, host.oracle.ask_with_mcp). Both run the
// claude CLI once with a rendered prompt piped on stdin and normalize
// exit / context errors the same way; this file is the seam.
package host

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// claudeRun is the outcome of one `claude -p` invocation.
type claudeRun struct {
	// Stdout has its trailing newline trimmed.
	Stdout string
	// Stderr is left raw (untrimmed) so callers can format messages.
	Stderr   string
	ExitCode int
	// Infra is non-nil iff the process failed for a reason other than
	// producing an exit code (e.g. binary vanished mid-run). Callers
	// surface this through Result.Error rather than a Go error so
	// on_error: routing stays deterministic.
	Infra error
}

// runClaudeOneShot executes the claude CLI with the given args, prompt
// piped on stdin, and working directory. Context cancellation is
// returned as a Go error; all other failures populate the claudeRun
// fields.
func runClaudeOneShot(ctx context.Context, bin string, cliArgs []string, stdin, workingDir string) (claudeRun, error) {
	cmd := exec.CommandContext(ctx, bin, cliArgs...)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.Dir = workingDir

	var so, se strings.Builder
	cmd.Stdout = &so
	cmd.Stderr = &se

	runErr := cmd.Run()
	out := strings.TrimRight(so.String(), "\n")
	if runErr != nil {
		if ctx.Err() != nil {
			return claudeRun{}, ctx.Err()
		}
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			return claudeRun{Stdout: out, Stderr: se.String(), ExitCode: exitErr.ExitCode()}, nil
		}
		return claudeRun{Stdout: out, Stderr: se.String(), Infra: runErr}, nil
	}
	return claudeRun{Stdout: out, Stderr: se.String()}, nil
}

// claudeExitErrorMessage builds the Result.Error string for a non-zero
// claude exit, preferring stderr text, then stdout, then a fallback.
func claudeExitErrorMessage(exitCode int, stderr, stdout string) string {
	if s := strings.TrimSpace(stderr); s != "" {
		return s
	}
	if stdout != "" {
		return stdout
	}
	return fmt.Sprintf("claude exited with code %d", exitCode)
}

// resolveOracleBin returns the path to the claude binary, honoring
// OracleBinEnv and falling back to a PATH lookup. Returns
// ErrOracleUnavailable if neither is set.
func resolveOracleBin() (string, error) {
	if bin := os.Getenv(OracleBinEnv); bin != "" {
		return bin, nil
	}
	path, err := exec.LookPath("claude")
	if err != nil {
		return "", ErrOracleUnavailable
	}
	return path, nil
}
