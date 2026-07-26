package reviewedfeedback

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ManagedLocatorResolver struct {
	WorkspaceRoot string
	ArtifactRoot  string
}

func (r ManagedLocatorResolver) Resolve(
	resumeMode, workspaceRef, retryBriefRef string,
) (ResolvedLocators, error) {
	var out ResolvedLocators
	var err error
	if resumeMode != "fresh" {
		out.WorkspacePath, err = resolveManagedLocator(r.WorkspaceRoot, workspaceRef, false)
		if err != nil {
			return ResolvedLocators{}, fmt.Errorf("resume workspace locator is not a server-owned managed workspace")
		}
	}
	if retryBriefRef != "" {
		out.RetryBriefPath, err = resolveManagedLocator(r.ArtifactRoot, retryBriefRef, true)
		if err != nil {
			return ResolvedLocators{}, fmt.Errorf("retry brief locator is not a server-owned artifact")
		}
	}
	return out, nil
}

func resolveManagedLocator(root, ref string, allowSegments bool) (string, error) {
	if strings.TrimSpace(root) == "" || !safeManagedRef(ref, allowSegments) {
		return "", fmt.Errorf("invalid managed locator")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	path, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(ref)))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || relative == ".." {
		return "", fmt.Errorf("outside managed root")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if allowSegments && !info.Mode().IsRegular() {
		return "", fmt.Errorf("artifact is not a regular file")
	}
	if !allowSegments && !info.IsDir() {
		return "", fmt.Errorf("workspace is not a directory")
	}
	return path, nil
}

func safeManagedRef(value string, allowSegments bool) bool {
	if value == "" || len(value) > 1024 || strings.Contains(value, "\\") ||
		strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") {
		return false
	}
	parts := strings.Split(value, "/")
	if !allowSegments && len(parts) != 1 {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || !safeToken(part, 180) {
			return false
		}
	}
	return true
}
