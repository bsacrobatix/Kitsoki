package control

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FileEntry is a path-only directory listing. Paths are always relative to the
// workspace and never reveal verifier overlays or a machine-local root.
type FileEntry struct {
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
	Size      int64  `json:"size,omitempty"`
}
type SearchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}
type CommandResult struct {
	Handle   Handle `json:"workspace"`
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
	TimedOut bool   `json:"timed_out,omitempty"`
}
type VCSStatus struct {
	Handle    Handle `json:"workspace"`
	Branch    string `json:"branch"`
	Head      string `json:"head"`
	Dirty     bool   `json:"dirty"`
	Porcelain string `json:"porcelain,omitempty"`
}

func (m *Manager) ReadFile(ctx context.Context, h Handle, relative string) ([]byte, error) {
	path, err := m.resolve(ctx, h, relative, true)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

func (m *Manager) ListFiles(ctx context.Context, h Handle, relative string) ([]FileEntry, error) {
	path, err := m.resolve(ctx, h, relative, true)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	out := make([]FileEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == ".kitsoki-verifier" {
			continue
		}
		info, e := entry.Info()
		if e != nil {
			return nil, e
		}
		out = append(out, FileEntry{Path: entry.Name(), Directory: entry.IsDir(), Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (m *Manager) SearchFiles(ctx context.Context, h Handle, query string, limit int) ([]SearchMatch, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("capsule fs: query is required")
	}
	root, err := m.WorkspacePath(ctx, h)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	var out []SearchMatch
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == ".kitsoki-verifier") {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		for n, line := range strings.Split(string(raw), "\n") {
			if strings.Contains(line, query) {
				rel, _ := filepath.Rel(root, path)
				out = append(out, SearchMatch{Path: filepath.ToSlash(rel), Line: n + 1, Text: line})
				if len(out) >= limit {
					return fs.SkipAll
				}
			}
		}
		return nil
	})
	return out, err
}

func (m *Manager) WriteFile(ctx context.Context, h Handle, relative string, contents []byte) (Handle, error) {
	path, err := m.resolve(ctx, h, relative, false)
	if err != nil {
		return Handle{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Handle{}, err
	}
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		return Handle{}, err
	}
	return m.mark(ctx, h, StateDirty, "capsule.workspace.changed")
}

// RunCommand runs only a definition-declared argv unless both the definition
// and immutable grant opt into raw argv. There is no shell mode.
func (m *Manager) RunCommand(ctx context.Context, h Handle, commandID string, rawArgv []string, timeout time.Duration) (CommandResult, error) {
	in, err := m.Status(ctx, h)
	if err != nil {
		return CommandResult{}, err
	}
	if !m.Grant.Allows("effect", "exec") {
		return CommandResult{}, fmt.Errorf("%w: exec", ErrDenied)
	}
	def, err := m.Definitions.Get(ctx, in.DefinitionID)
	if err != nil {
		return CommandResult{}, err
	}
	argv := append([]string(nil), rawArgv...)
	if commandID != "" {
		cmd, ok := def.Policy.Commands[commandID]
		if !ok {
			return CommandResult{}, fmt.Errorf("%w: command %q", ErrDenied, commandID)
		}
		argv = append([]string(nil), cmd.Argv...)
		if timeout == 0 && cmd.Timeout != "" {
			timeout, err = time.ParseDuration(cmd.Timeout)
			if err != nil {
				return CommandResult{}, fmt.Errorf("capsule exec: command timeout: %w", err)
			}
		}
	} else if !def.Policy.RawArgv || !m.Grant.Allows("effect", "raw_exec") {
		return CommandResult{}, fmt.Errorf("%w: raw argv", ErrDenied)
	}
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return CommandResult{}, fmt.Errorf("capsule exec: argv is required")
	}
	path, err := m.WorkspacePath(ctx, h)
	if err != nil {
		return CommandResult{}, err
	}
	runCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, argv[0], argv[1:]...)
	cmd.Dir = path
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err = cmd.Run()
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else if runCtx.Err() == context.DeadlineExceeded {
			exit = -1
		} else {
			return CommandResult{}, fmt.Errorf("capsule exec: %w", err)
		}
	}
	next, markErr := m.mark(ctx, h, StateDirty, "capsule.workspace.changed")
	if markErr != nil {
		return CommandResult{}, markErr
	}
	return CommandResult{Handle: next, ExitCode: exit, Output: output.String(), TimedOut: runCtx.Err() == context.DeadlineExceeded}, nil
}

func (m *Manager) StatusVCS(ctx context.Context, h Handle) (VCSStatus, error) {
	path, err := m.WorkspacePath(ctx, h)
	if err != nil {
		return VCSStatus{}, err
	}
	out, err := git(ctx, path, "status", "--porcelain", "--branch")
	if err != nil {
		return VCSStatus{}, err
	}
	head, err := git(ctx, path, "rev-parse", "HEAD")
	if err != nil {
		return VCSStatus{}, err
	}
	branch, err := git(ctx, path, "branch", "--show-current")
	if err != nil {
		return VCSStatus{}, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	dirty := false
	for _, line := range lines {
		if line != "" && !strings.HasPrefix(line, "## ") {
			dirty = true
			break
		}
	}
	return VCSStatus{Handle: h, Branch: strings.TrimSpace(branch), Head: strings.TrimSpace(head), Dirty: dirty, Porcelain: out}, nil
}
func (m *Manager) DiffVCS(ctx context.Context, h Handle) (string, error) {
	path, err := m.WorkspacePath(ctx, h)
	if err != nil {
		return "", err
	}
	return git(ctx, path, "diff", "--no-ext-diff", "HEAD")
}
func (m *Manager) CommitVCS(ctx context.Context, h Handle, message string) (Handle, error) {
	if strings.TrimSpace(message) == "" {
		return Handle{}, fmt.Errorf("capsule vcs: message is required")
	}
	if !m.Grant.Allows("effect", "vcs_commit") {
		return Handle{}, fmt.Errorf("%w: vcs_commit", ErrDenied)
	}
	path, err := m.WorkspacePath(ctx, h)
	if err != nil {
		return Handle{}, err
	}
	if _, err = git(ctx, path, "add", "-A"); err != nil {
		return Handle{}, err
	}
	if _, err = git(ctx, path, "diff", "--cached", "--quiet"); err == nil {
		return Handle{}, fmt.Errorf("capsule vcs: no staged changes")
	}
	if _, err = git(ctx, path, "commit", "-m", message); err != nil {
		return Handle{}, err
	}
	return m.mark(ctx, h, StateCommitted, "capsule.workspace.committed")
}

// MarkIntegrated advances lifecycle only after a reconciler completed its
// compare-and-swap update. It deliberately cannot perform Git operations.
func (m *Manager) MarkIntegrated(ctx context.Context, h Handle) (Handle, error) {
	return m.mark(ctx, h, StateIntegrated, "capsule.workspace.integrated")
}

// Integrate delegates a complete provider-owned integration lifecycle (such as
// the protected development compatibility adapter), then advances the handle
// only after that provider has succeeded. It cannot be used by an ungranted
// agent-facing server.
func (m *Manager) Integrate(ctx context.Context, h Handle, gate string) (Handle, error) {
	if !m.Grant.Allows("effect", "local_reconcile") {
		return Handle{}, fmt.Errorf("%w: local_reconcile", ErrDenied)
	}
	in, err := m.Status(ctx, h)
	if err != nil {
		return Handle{}, err
	}
	provider := m.Providers[in.Provider]
	integrator, ok := provider.(WorkspaceIntegrator)
	if !ok {
		return Handle{}, fmt.Errorf("capsule workspace: provider %q does not own integration", in.Provider)
	}
	definition, err := m.Definitions.Get(ctx, in.DefinitionID)
	if err != nil {
		return Handle{}, err
	}
	if err := integrator.Integrate(ctx, definition, in, gate); err != nil {
		return Handle{}, err
	}
	return m.MarkIntegrated(ctx, h)
}

func (m *Manager) resolve(ctx context.Context, h Handle, relative string, mustExist bool) (string, error) {
	root, err := m.WorkspacePath(ctx, h)
	if err != nil {
		return "", err
	}
	return ResolveWorkspacePath(root, relative, mustExist)
}
func (m *Manager) mark(ctx context.Context, h Handle, state State, event string) (Handle, error) {
	in, err := m.Instances.CompareAndSwap(ctx, h.ID, h.Generation, func(cur *Instance) error {
		if cur.State == StateClosed {
			return ErrInvalidState
		}
		cur.State = state
		return nil
	})
	if err != nil {
		return Handle{}, err
	}
	if err := m.emit(ctx, event, in); err != nil {
		return Handle{}, err
	}
	return Handle{ID: in.ID, Generation: in.Generation}, nil
}
func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("capsule vcs: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
