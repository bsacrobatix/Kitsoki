package queue

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// StagingIntegration is the local production adapter for a protected project.
// It never changes main itself: it creates a disposable managed workspace,
// merges the candidate there, and delegates staging/local updates to the
// existing development-workspace lifecycle script. Final main promotion stays
// an explicit staging-capsule operation.
type StagingIntegration struct {
	ProjectRoot string
	GateCommand string
	Runner      CommandRunner
}

// CommandRunner makes lifecycle composition testable without weakening the
// production path, which uses exec.CommandContext.
type CommandRunner interface {
	Run(context.Context, string, string, ...string) ([]byte, error)
}

type CommandRunnerFunc func(context.Context, string, string, ...string) ([]byte, error)

func (f CommandRunnerFunc) Run(ctx context.Context, dir, program string, args ...string) ([]byte, error) {
	return f(ctx, dir, program, args...)
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, dir, program string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

func (s StagingIntegration) Speculate(ctx context.Context, c Candidate, _ []Candidate) (Speculation, error) {
	root, err := s.root()
	if err != nil {
		return Speculation{}, err
	}
	id := "queue-" + c.ID
	workspaceRoot := filepath.Join(root, ".capsules", "workspaces")
	workspace := filepath.Join(workspaceRoot, id)
	branch := "queue/speculative/" + c.ID
	if err := s.run(ctx, root, filepath.Join(root, "scripts", "dev-workspace.sh"), "create", "--repo", root, "--root", workspaceRoot, "--id", id, "--branch", branch, "--base", "staging/local", "--target", "staging/local"); err != nil {
		return Speculation{}, err
	}
	if err := s.run(ctx, workspace, "git", "merge", "--no-ff", "--no-edit", c.SHA); err != nil {
		return Speculation{}, fmt.Errorf("queue: speculative merge %s: %w", c.SHA, err)
	}
	sha, err := gitOutput(ctx, workspace, "rev-parse", "HEAD")
	if err != nil {
		return Speculation{}, err
	}
	return Speculation{SHA: sha, WorkspaceID: id, WorkspacePath: workspace, Evidence: []string{"queue:speculative-workspace=" + filepath.ToSlash(filepath.Join(".capsules", "workspaces", id))}}, nil
}

// Land delegates staging/local mutation to the established protected
// dev-workspace lifecycle. The deterministic gate is intentionally repeated
// after the merge helper's rebase; main promotion remains a separate
// refresh-staging-local.sh / merge-to-main.sh operation.
func (s StagingIntegration) Land(ctx context.Context, spec Speculation) error {
	root, err := s.root()
	if err != nil {
		return err
	}
	if spec.WorkspaceID == "" || spec.WorkspacePath == "" {
		return fmt.Errorf("queue: staging integration requires a managed speculative workspace")
	}
	workspaceRoot := filepath.Join(root, ".capsules", "workspaces")
	if filepath.Clean(spec.WorkspacePath) != filepath.Join(workspaceRoot, spec.WorkspaceID) {
		return fmt.Errorf("queue: speculative workspace escapes managed workspace root")
	}
	if err := s.run(ctx, root, filepath.Join(root, "scripts", "dev-workspace.sh"), "merge", "--repo", root, "--root", workspaceRoot, spec.WorkspaceID, "--gate", s.GateCommand, "--teardown"); err != nil {
		return err
	}
	return nil
}

// ShellGate runs the declared deterministic command only in the managed
// speculative workspace. It rejects a command that leaves that workspace dirty
// or moves HEAD, so a gate cannot smuggle unvalidated changes into staging.
type ShellGate struct {
	Command string
	Runner  CommandRunner
}

func (g ShellGate) Run(ctx context.Context, spec Speculation) (GateResult, error) {
	if strings.TrimSpace(g.Command) == "" {
		return GateResult{}, fmt.Errorf("queue: deterministic gate command is required")
	}
	if spec.WorkspacePath == "" {
		return GateResult{}, fmt.Errorf("queue: gate requires a speculative workspace")
	}
	before, err := gitOutput(ctx, spec.WorkspacePath, "rev-parse", "HEAD")
	if err != nil {
		return GateResult{}, err
	}
	runner := g.Runner
	if runner == nil {
		runner = execCommandRunner{}
	}
	output, runErr := runner.Run(ctx, spec.WorkspacePath, "sh", "-c", g.Command)
	evidence := commandEvidence("queue:gate", output)
	if runErr != nil {
		return GateResult{Passed: false, Evidence: evidence}, fmt.Errorf("queue: deterministic gate: %w", runErr)
	}
	after, err := gitOutput(ctx, spec.WorkspacePath, "rev-parse", "HEAD")
	if err != nil {
		return GateResult{Passed: false, Evidence: evidence}, err
	}
	if before != after {
		return GateResult{Passed: false, Evidence: evidence}, fmt.Errorf("queue: deterministic gate moved speculative HEAD (%s -> %s)", before, after)
	}
	dirty, err := gitOutput(ctx, spec.WorkspacePath, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return GateResult{Passed: false, Evidence: evidence}, err
	}
	for _, line := range strings.Split(dirty, "\n") {
		if line != "" && !strings.HasSuffix(line, ".kitsoki-capsule") && !strings.HasSuffix(line, ".kitsoki-clone") && !strings.HasSuffix(line, "capsule-manifest.json") && !strings.HasSuffix(line, ".kitsoki-dev-workspace.json") && !strings.HasSuffix(line, ".kitsoki-owner") {
			return GateResult{Passed: false, Evidence: evidence}, fmt.Errorf("queue: deterministic gate left speculative workspace dirty: %s", line)
		}
	}
	return GateResult{Passed: true, Evidence: evidence}, nil
}

func (s StagingIntegration) root() (string, error) {
	if strings.TrimSpace(s.GateCommand) == "" {
		return "", fmt.Errorf("queue: deterministic gate command is required")
	}
	root, err := filepath.Abs(s.ProjectRoot)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(root, "scripts", "dev-workspace.sh")); err != nil {
		return "", fmt.Errorf("queue: managed workspace lifecycle unavailable: %w", err)
	}
	return root, nil
}

func (s StagingIntegration) run(ctx context.Context, dir, program string, args ...string) error {
	runner := s.Runner
	if runner == nil {
		runner = execCommandRunner{}
	}
	output, err := runner.Run(ctx, dir, program, args...)
	if err != nil {
		return fmt.Errorf("queue: lifecycle %s: %w%s", filepath.Base(program), err, outputSuffix(output))
	}
	return nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("queue: git %s: %w%s", strings.Join(args, " "), err, outputSuffix(out))
	}
	return strings.TrimSpace(string(out)), nil
}

func commandEvidence(prefix string, output []byte) []string {
	text := strings.TrimSpace(string(output))
	if len(text) > 2048 {
		text = text[:2048] + "…"
	}
	if text == "" {
		return []string{prefix + ":passed"}
	}
	return []string{prefix + ":" + text}
}

func outputSuffix(output []byte) string {
	if text := strings.TrimSpace(string(output)); text != "" {
		return ": " + text
	}
	return ""
}
