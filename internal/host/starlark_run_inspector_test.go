package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestStarlarkRunDefaultInspector_PrefersAppDir locks the resolution order for
// the default production inspector's root: world.workdir, then KITSOKI_APP_DIR
// (AppDirEnv, agent_ask.go), then the process cwd. Without the AppDirEnv step,
// any cwd-detached driver of a story (the MCP studio's story.test/story.turn
// chief among them) resolves ctx.fs.* paths against the driving PROCESS's cwd
// instead of the target app's directory — see WM.8 in
// docs/goals/generalized-usage/decomposition.yaml.
//
// dirA plays the role of KITSOKI_APP_DIR (the target app's directory); dirB
// plays the role of the process cwd (the driving process's own checkout). Only
// dirA holds the marker file, so a script that resolves ctx.fs.exists against
// dirB (or fails to consult AppDirEnv at all) would report false and fail the
// test.
func TestStarlarkRunDefaultInspector_PrefersAppDir(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()

	const marker = "marker.txt"
	if err := os.WriteFile(filepath.Join(dirA, marker), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join(dirA, "check.star")
	if err := os.WriteFile(script, []byte(
		"def main(ctx):\n    return {\"found\": ctx.fs.exists(\""+marker+"\")}\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script+".yaml", []byte(
		"outputs:\n  found: { type: bool }\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv(AppDirEnv, dirA)

	// Move the process cwd to dirB (which lacks the marker) so a fallback to
	// os.Getwd() instead of AppDirEnv would resolve against the wrong root and
	// the assertion below would catch it.
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dirB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	// No world.workdir supplied and no inspector pre-injected: the adapter must
	// fall through to KITSOKI_APP_DIR before the process cwd.
	ctx := WithWorldSnapshot(context.Background(), map[string]any{})
	res, err := StarlarkRunHandler(ctx, map[string]any{"script": script})
	if err != nil {
		t.Fatalf("StarlarkRunHandler: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected domain error: %s", res.Error)
	}
	if got := res.Data["found"]; got != true {
		t.Fatalf("ctx.fs.exists(%q) = %v, want true (inspector should root at KITSOKI_APP_DIR=%s, not cwd=%s)", marker, got, dirA, dirB)
	}
}

// TestStarlarkRunDefaultInspector_AppDirWidensToRepoRoot locks that when
// KITSOKI_APP_DIR points at a subdirectory of a repo (kitsoki's own
// stories/<name>/ layout), the default inspector roots at the ENCLOSING repo
// root (.kitsoki-root / go.mod marker), not the bare app dir. ctx.fs paths are
// repo-relative by contract, and host.run effects in the same room execute at
// the repo root — rooting ctx.fs at the app dir makes the two host surfaces
// disagree about the same relative path (scenario-qa's plan room hit exactly
// this: run.py wrote .artifacts/... at the repo root, plan_legs.star read it
// back under stories/scenario-qa/.artifacts/...).
func TestStarlarkRunDefaultInspector_AppDirWidensToRepoRoot(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".kitsoki-root"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	appDir := filepath.Join(repo, "stories", "example")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// The marker lives at the REPO root, so a repo-relative read only succeeds
	// if the inspector widened the app dir to the enclosing repo root.
	const marker = ".artifacts/marker.txt"
	if err := os.MkdirAll(filepath.Join(repo, ".artifacts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, filepath.FromSlash(marker)), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join(appDir, "check.star")
	if err := os.WriteFile(script, []byte(
		"def main(ctx):\n    return {\"found\": ctx.fs.exists(\""+marker+"\")}\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script+".yaml", []byte(
		"outputs:\n  found: { type: bool }\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv(AppDirEnv, appDir)

	ctx := WithWorldSnapshot(context.Background(), map[string]any{})
	res, err := StarlarkRunHandler(ctx, map[string]any{"script": script})
	if err != nil {
		t.Fatalf("StarlarkRunHandler: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected domain error: %s", res.Error)
	}
	if got := res.Data["found"]; got != true {
		t.Fatalf("ctx.fs.exists(%q) = %v, want true (inspector should widen KITSOKI_APP_DIR=%s to repo root %s)", marker, got, appDir, repo)
	}
}
