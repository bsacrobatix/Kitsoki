package orchestrator

// reload_promptrenderer_test.go proves the fix described in reload.go's
// Reload doc comment: o.promptRenderer used to be built exactly once, in New,
// so a reload onto a definition rooted at a DIFFERENT directory (exactly what
// a revision swap does — see appdef_binding.go's SessionBinding.SwapDef) kept
// rendering agent prompts from the ORIGINAL story dir. That was invisible
// while every reload re-read the same on-disk directory (old and "new" def
// shared a BaseDir), which is why it went unnoticed until the live-edit spec
// needed reload to swap BaseDir out from under a live orchestrator.
//
// promptRenderer has no exported accessor, so this test lives in package
// orchestrator (white-box) and reads o.promptRenderer.RootDir() directly, per
// the slice brief.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/harness"
	"kitsoki/internal/host"
	"kitsoki/internal/machine"
	"kitsoki/internal/store"
)

// promptRendererNoopHarness is a zero-behavior Harness; this test drives
// Reload directly and never asks the harness to route a turn.
type promptRendererNoopHarness struct{}

func (promptRendererNoopHarness) RunTurn(ctx context.Context, in harness.TurnInput) (mcp.CallToolParams, error) {
	return mcp.CallToolParams{}, nil
}
func (promptRendererNoopHarness) Close() error { return nil }

// writePromptRendererFixture writes a minimal, single-terminal-state app.yaml
// into dir (with a prompts/ dir alongside it, so the story has an actual
// prompt-rooted file to search even though this test only asserts the
// renderer's root, not a rendered prompt's content) and returns its path.
func writePromptRendererFixture(t *testing.T, dir, rootState string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "prompts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "prompts", "system.md"), []byte("You are a helpful assistant.\n"), 0o644))
	yaml := `app:
  id: promptrenderer-test
  version: 0.1.0
  title: "promptRenderer reload fixture"

root: ` + rootState + `

states:
  ` + rootState + `:
    view: "hello"
    terminal: true
`
	path := filepath.Join(dir, "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o644))
	return path
}

func TestReload_RebuildsPromptRendererForNewBaseDir(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()

	pathA := writePromptRendererFixture(t, dirA, "idle_a")
	pathB := writePromptRendererFixture(t, dirB, "idle_b")

	defA, err := app.Load(pathA)
	require.NoError(t, err)
	defB, err := app.Load(pathB)
	require.NoError(t, err)

	// Sanity: app.Load must have actually rooted each def where we expect,
	// or this test would pass for the wrong reason.
	require.Equal(t, dirA, defA.BaseDir)
	require.Equal(t, dirB, defB.BaseDir)

	m, err := machine.New(defA)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// injectBuiltinStoryAuthoringRoom appends host.agent.task to every loaded
	// def's hosts: allow-list unconditionally (internal/app/builtin_story_authoring.go),
	// so Reload's ValidateAllowList needs it registered even though this
	// fixture never invokes it (see Slice 0's notes on the same gap).
	reg := host.NewRegistry()
	reg.Register("host.agent.task", func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{}, nil
	})

	o := New(defA, m, s, promptRendererNoopHarness{},
		WithHostRegistry(reg),
		WithReloader(func() (*app.AppDef, error) { return defB, nil }),
	)

	require.NotNil(t, o.promptRenderer, "New must build a promptRenderer for an on-disk story")
	require.Equal(t, dirA, o.promptRenderer.RootDir())

	res, err := o.Reload("", app.StatePath("idle_a"))
	require.NoError(t, err)
	require.False(t, res.PrevStateExists, "idle_a does not exist in defB")

	require.NotNil(t, o.promptRenderer, "Reload must rebuild the promptRenderer, not drop it")
	require.Equal(t, dirB, o.promptRenderer.RootDir(),
		"promptRenderer must track the just-installed def's BaseDir after Reload, not the def New was built with")
}
