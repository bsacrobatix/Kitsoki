package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"kitsoki/internal/compliance"
	"kitsoki/internal/host"
	"kitsoki/internal/webconfig"
)

func (r *SessionRegistry) wireApplicationGraph(
	rt *sessionRuntime,
	appID string,
	appPath string,
) error {
	configured, ok := r.cfg.ApplicationGraphs[appID]
	if !ok {
		return nil
	}
	if rt == nil || rt.HostRegistry == nil {
		return fmt.Errorf("configure application graph %q: session host registry is unavailable", appID)
	}
	binding, err := resolveApplicationGraphBinding(appID, appPath, configured)
	if err != nil {
		return fmt.Errorf("configure application graph %q: %w", appID, err)
	}
	handlers, err := host.NewApplicationGraphHandlers(binding)
	if err != nil {
		return fmt.Errorf("configure application graph %q: %w", appID, err)
	}
	rt.HostRegistry.Replace("host.graph", handlers.Prefix)
	for op, handler := range handlers.Operations {
		rt.HostRegistry.Replace("host.graph."+op, handler)
	}
	return nil
}

func resolveApplicationGraphBinding(
	appID string,
	appPath string,
	configured webconfig.ApplicationGraphConfig,
) (host.ApplicationGraphBinding, error) {
	discoveredRoot := compliance.DiscoverRoot(appPath)
	projectRoot, err := resolveApplicationGraphDirectory(discoveredRoot, configured.ProjectRoot)
	if err != nil {
		return host.ApplicationGraphBinding{}, fmt.Errorf("resolve project_root: %w", err)
	}
	catalog, err := resolveApplicationGraphFile(projectRoot, configured.Catalog)
	if err != nil {
		return host.ApplicationGraphBinding{}, fmt.Errorf("resolve catalog: %w", err)
	}
	if err := checkApplicationGraphFileBound(catalog, configured.MaxBytes); err != nil {
		return host.ApplicationGraphBinding{}, fmt.Errorf("resolve catalog: %w", err)
	}
	overlay := ""
	if configured.Overlay != "" {
		overlay, err = resolveApplicationGraphFile(projectRoot, configured.Overlay)
		if err != nil {
			return host.ApplicationGraphBinding{}, fmt.Errorf("resolve overlay: %w", err)
		}
		if err := checkApplicationGraphFileBound(overlay, configured.MaxBytes); err != nil {
			return host.ApplicationGraphBinding{}, fmt.Errorf("resolve overlay: %w", err)
		}
	}
	return host.ApplicationGraphBinding{
		ApplicationID: appID,
		ProjectRoot:   projectRoot,
		CatalogPath:   catalog,
		OverlayPath:   overlay,
		MaxNodes:      configured.MaxNodes,
		MaxBytes:      configured.MaxBytes,
		WritePolicy:   configured.WritePolicy,
	}, nil
}

func resolveApplicationGraphDirectory(root, relative string) (string, error) {
	resolved, err := resolveApplicationGraphPath(root, relative)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("resolved path is not a directory")
	}
	return resolved, nil
}

func resolveApplicationGraphFile(root, relative string) (string, error) {
	resolved, err := resolveApplicationGraphPath(root, relative)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("resolved path is not a regular file")
	}
	return resolved, nil
}

func checkApplicationGraphFileBound(path string, maxBytes int) error {
	if maxBytes < 1 || maxBytes > host.MaxApplicationGraphBytes {
		return fmt.Errorf("max_bytes is outside its platform bound")
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size() > int64(maxBytes) {
		return fmt.Errorf("configured file exceeds max_bytes")
	}
	return nil
}

func resolveApplicationGraphPath(root, relative string) (string, error) {
	if relative == "" || relative != strings.TrimSpace(relative) ||
		filepath.IsAbs(relative) || strings.Contains(relative, "://") ||
		strings.ContainsAny(relative, "\r\n\x00") {
		return "", fmt.Errorf("path must be repository-relative")
	}
	clean := filepath.Clean(relative)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path must not traverse outside its root")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", err
	}
	candidateReal, err := filepath.EvalSymlinks(filepath.Join(rootReal, clean))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(rootReal, candidateReal)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("resolved path escapes its root through a symlink")
	}
	return candidateReal, nil
}
