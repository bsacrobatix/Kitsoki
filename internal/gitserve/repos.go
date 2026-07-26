package gitserve

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// InitBare creates a new bare repository under root and returns its absolute
// path. name is a slash-separated relative path of safe segments; a ".git"
// suffix is appended when missing, so InitBare(root, "team/project") and
// InitBare(root, "team/project.git") are equivalent. It fails if the target
// already exists.
func InitBare(root, name string) (string, error) {
	rel, err := normalizeRepoName(name)
	if err != nil {
		return "", err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("gitserve: resolve root: %w", err)
	}
	dir := filepath.Join(absRoot, filepath.FromSlash(rel))
	if _, statErr := os.Stat(dir); statErr == nil {
		return "", fmt.Errorf("gitserve: repository %q already exists", rel)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", fmt.Errorf("gitserve: create parent directories: %w", err)
	}
	out, err := exec.Command("git", "init", "--bare", dir).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gitserve: git init --bare: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return dir, nil
}

// ListRepos returns the slash-separated paths, relative to root, of every
// bare repository under root, sorted. It does not descend into repositories
// themselves.
func ListRepos(root string) ([]string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("gitserve: resolve root: %w", err)
	}
	var repos []string
	walkErr := filepath.WalkDir(absRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() || p == absRoot {
			return nil
		}
		if isBareRepo(p) {
			rel, relErr := filepath.Rel(absRoot, p)
			if relErr != nil {
				return relErr
			}
			repos = append(repos, filepath.ToSlash(rel))
			return filepath.SkipDir
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("gitserve: list repositories: %w", walkErr)
	}
	sort.Strings(repos)
	return repos, nil
}

func normalizeRepoName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("gitserve: empty repository name")
	}
	segments := strings.Split(name, "/")
	// An absolute name or empty segment ("//") fails the per-segment check
	// below because empty strings are not safe segments.
	for _, segment := range segments {
		if !safeSegment.MatchString(segment) {
			return "", fmt.Errorf("gitserve: invalid repository name %q", name)
		}
	}
	last := segments[len(segments)-1]
	if !strings.HasSuffix(last, ".git") {
		segments[len(segments)-1] = last + ".git"
	}
	return strings.Join(segments, "/"), nil
}

// isBareRepo reports whether dir looks like a plain bare git repository. The
// structural check (HEAD file plus objects and refs directories) mirrors
// git's own repository discovery without spawning a process per request.
func isBareRepo(dir string) bool {
	if info, err := os.Stat(filepath.Join(dir, "HEAD")); err != nil || info.IsDir() {
		return false
	}
	for _, sub := range []string{"objects", "refs"} {
		if info, err := os.Stat(filepath.Join(dir, sub)); err != nil || !info.IsDir() {
			return false
		}
	}
	return true
}

func gitOutput(ctx context.Context, gitPath, dir string, args ...string) (string, error) {
	argv := append([]string{"-C", dir}, args...)
	out, err := exec.CommandContext(ctx, gitPath, argv...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
