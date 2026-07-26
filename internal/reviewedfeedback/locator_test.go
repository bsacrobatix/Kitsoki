package reviewedfeedback

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManagedLocatorResolverAcceptsOnlyServerOwnedOpaqueReferences(t *testing.T) {
	root := t.TempDir()
	workspaces := filepath.Join(root, ".capsules", "workspaces")
	artifacts := filepath.Join(root, ".artifacts")
	if err := os.MkdirAll(filepath.Join(workspaces, "workspace-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(artifacts, "feedback"), 0o700); err != nil {
		t.Fatal(err)
	}
	brief := filepath.Join(artifacts, "feedback", "retry.md")
	if err := os.WriteFile(brief, []byte("reviewed"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolver := ManagedLocatorResolver{
		WorkspaceRoot: workspaces,
		ArtifactRoot:  artifacts,
	}
	got, err := resolver.Resolve(
		"reused-workspace", "workspace-1", "feedback/retry.md",
	)
	if err != nil {
		t.Fatalf("resolve managed locators: %v", err)
	}
	wantWorkspace, err := filepath.EvalSymlinks(filepath.Join(workspaces, "workspace-1"))
	if err != nil {
		t.Fatal(err)
	}
	wantBrief, err := filepath.EvalSymlinks(brief)
	if err != nil {
		t.Fatal(err)
	}
	if got.WorkspacePath != wantWorkspace || got.RetryBriefPath != wantBrief {
		t.Fatalf("resolved = %#v", got)
	}

	for _, ref := range []string{
		"/private/path",
		"../private",
		"workspace-1;cat-secret",
		"workspace-1/child",
		"${HOME}",
	} {
		_, err := resolver.Resolve("resumed-session", ref, "")
		if err == nil || strings.Contains(err.Error(), ref) {
			t.Fatalf("workspace ref %q error = %v", ref, err)
		}
	}
	for _, ref := range []string{
		"/private/retry.md",
		"feedback/../../private",
		"feedback/retry.md;cat-secret",
		"${HOME}/secret",
	} {
		_, err := resolver.Resolve("fresh", "", ref)
		if err == nil || strings.Contains(err.Error(), ref) {
			t.Fatalf("artifact ref %q error = %v", ref, err)
		}
	}
}

func TestManagedLocatorResolverRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	workspaces := filepath.Join(root, "workspaces")
	artifacts := filepath.Join(root, "artifacts")
	outside := t.TempDir()
	if err := os.MkdirAll(workspaces, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(artifacts, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspaces, "escape")); err != nil {
		t.Fatal(err)
	}
	resolver := ManagedLocatorResolver{
		WorkspaceRoot: workspaces,
		ArtifactRoot:  artifacts,
	}
	if _, err := resolver.Resolve("reused-workspace", "escape", ""); err == nil {
		t.Fatal("symlink escape resolved")
	}
}
