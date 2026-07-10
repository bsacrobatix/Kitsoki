package control

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var fullCommit = regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`)

const instanceSentinel = ".kitsoki-capsule"

// GitSourceProvider materializes project-self and pinned Git definitions into
// manager-owned workspaces. It never receives credentials; a future remote
// adapter injects a narrow credential reference only into its clone/fetch call.
type GitSourceProvider struct{ ProjectRoot string }

func (GitSourceProvider) Name() string { return "git" }
func (p GitSourceProvider) Create(ctx context.Context, def Definition, in Instance) (MaterializedWorkspace, error) {
	root, err := projectRoot(p.ProjectRoot)
	if err != nil {
		return MaterializedWorkspace{}, err
	}
	var source string
	switch def.Source.Kind {
	case SourceSelf:
		source = root
	case SourcePinned:
		source = strings.TrimSpace(def.Source.Ref)
		if source == "" {
			return MaterializedWorkspace{}, fmt.Errorf("git source: pinned ref is required")
		}
		if !fullCommit.MatchString(def.Source.Commit) {
			return MaterializedWorkspace{}, fmt.Errorf("git source: pinned commit must be a full immutable id")
		}
		if !filepath.IsAbs(source) && !strings.Contains(source, "://") {
			source = filepath.Join(root, source)
		}
	default:
		return MaterializedWorkspace{}, fmt.Errorf("git source: unsupported source %q", def.Source.Kind)
	}
	if err := os.MkdirAll(filepath.Dir(in.Path), 0o755); err != nil {
		return MaterializedWorkspace{}, err
	}
	if _, err := runGit(ctx, "", "clone", "--no-local", source, in.Path); err != nil {
		return MaterializedWorkspace{}, err
	}
	if def.Source.Kind == SourcePinned {
		if _, err := runGit(ctx, in.Path, "checkout", "--detach", def.Source.Commit); err != nil {
			_ = os.RemoveAll(in.Path)
			return MaterializedWorkspace{}, err
		}
	}
	head, err := runGit(ctx, in.Path, "rev-parse", "HEAD")
	if err != nil {
		_ = os.RemoveAll(in.Path)
		return MaterializedWorkspace{}, err
	}
	branch, _ := runGit(ctx, in.Path, "branch", "--show-current")
	if err := os.WriteFile(filepath.Join(in.Path, instanceSentinel), []byte(in.ID+"\n"), 0o644); err != nil {
		_ = os.RemoveAll(in.Path)
		return MaterializedWorkspace{}, err
	}
	return MaterializedWorkspace{Path: in.Path, SourceRef: def.Source.Ref, Head: strings.TrimSpace(head), Branch: strings.TrimSpace(branch)}, nil
}
func (GitSourceProvider) Close(_ context.Context, in Instance) error {
	if strings.TrimSpace(in.Path) == "" {
		return fmt.Errorf("git source: instance path is required")
	}
	if _, err := os.Stat(filepath.Join(in.Path, instanceSentinel)); err != nil {
		return fmt.Errorf("git source: refusing close without sentinel: %w", err)
	}
	return os.RemoveAll(in.Path)
}
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
