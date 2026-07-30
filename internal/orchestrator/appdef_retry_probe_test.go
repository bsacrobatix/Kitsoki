package orchestrator_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/host"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/store"
)

// TestAppdefRetryProbe settles Slice 0's highest-risk unknown BEFORE any
// production code from the live-edit spec
// (.context/appdef-live-edit-implementation-spec.md) is built: does the
// error-room retry path need RerunOnEnterOptions{ForceOnce: true}, or does a
// plain RerunOnEnter (fired once, right after a definition swap) plus the
// ORDINARY error_retry transition suffice to re-execute the fixed on_enter?
//
// The architect's expected answer is "no ForceOnce needed", for two
// independent reasons:
//
//  1. Right after the swap the session's state is __error__ (the on_error
//     redirect already moved it there), and __error__ has no on_enter: of its
//     own — so the RerunOnEnter that runs immediately after a reload has
//     nothing to force; it only re-renders the error room.
//  2. The retry itself is a NORMAL transition (error_retry: __error__ ->
//     world.error_origin, i.e. "work"), and a normal state entry always fires
//     on_enter regardless of once:. once: cannot elide it here either way:
//     the fixture never declares once: true, and even if it did, the failed
//     call left probe_result unset, so allBindTargetsSet would be false.
//
// This test measures that instead of assuming it. If step 8 below ever needs
// ForceOnce to pass, that is a load-bearing discovery for Slice 4/5 of the
// spec and must be escalated, not silently patched around.
func TestAppdefRetryProbe(t *testing.T) {
	const fixturePath = "../../testdata/apps/liveedit/app.yaml"

	original, err := os.ReadFile(fixturePath)
	require.NoError(t, err)

	def0, err := app.Load(fixturePath)
	require.NoError(t, err)

	m, err := machine.New(def0)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// Two plain, stateless host closures. The "fix" this test applies later
	// changes WHICH host the story invokes (host.probe.fail -> host.probe.ok)
	// — it is not a fail-then-succeed counter, because the whole point is
	// that the STORY DEFINITION changed, not that some retry budget expired.
	reg := host.NewRegistry()
	reg.Register("host.probe.fail", func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{Error: "deliberate probe failure"}, nil
	})
	reg.Register("host.probe.ok", func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{Data: map[string]any{"value": "probe ok"}}, nil
	})
	// injectBuiltinStoryAuthoringRoom (internal/app/builtin_story_authoring.go)
	// appends host.agent.task to EVERY loaded def's hosts: allow-list
	// unconditionally, whether or not the fixture's own rooms ever invoke
	// it. Orchestrator.Reload re-validates that whole allow-list against the
	// registry, so it must be registered even though this probe never
	// dispatches through it.
	reg.Register("host.agent.task", func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{}, nil
	})

	// served is the mutable holder behind WithReloader — it stands in for
	// the revision pin that Slices 1-6 of the spec build for real. Reload
	// always prefers an injected reloader closure over reading appPath from
	// disk (internal/orchestrator/reload.go), so mutating served.def and
	// calling orch.Reload("", ...) is exactly the seam a real revision swap
	// uses.
	served := &struct{ def *app.AppDef }{def0}

	orch := orchestrator.New(def0, m, s, noopHarness{},
		orchestrator.WithHostRegistry(reg),
		orchestrator.WithReloader(func() (*app.AppDef, error) { return served.def, nil }),
	)

	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)
	require.NoError(t, orch.RunInitialOnEnter(ctx, sid))

	// Step 1: begin -> work's on_enter invokes host.probe.fail, which has no
	// on_error: of its own, so on_error_default: builtin redirects the whole
	// state to __error__.
	out, err := orch.SubmitDirect(ctx, sid, "begin", nil)
	require.NoError(t, err)
	require.Equal(t, app.StatePath(app.ErrorRoomState), out.NewState)

	// Step 5: the failure is visible and attributed in the replayed journey.
	history, err := s.LoadHistory(sid)
	require.NoError(t, err)
	journey, err := store.BuildJourney(def0, orch.InitialState(), orch.InitialWorld(), history)
	require.NoError(t, err)
	require.Equal(t, "work", journey.World.Vars[app.ErrorOriginWorldKey])
	errLog, ok := journey.World.Vars[app.ErrorLogWorldKey].([]any)
	require.True(t, ok, "error_log must be a slice, got %T", journey.World.Vars[app.ErrorLogWorldKey])
	require.Len(t, errLog, 1)

	// Step 6: "fix" the story by swapping which host work's on_enter invokes.
	// A single global string replace, exactly as the spec's patch RPC will
	// eventually do via a set_file op.
	fixed := strings.ReplaceAll(string(original), "host.probe.fail", "host.probe.ok")
	require.NotEqual(t, string(original), fixed, "replace must actually change the fixture")

	fixedPath := filepath.Join(t.TempDir(), "app.yaml")
	require.NoError(t, os.WriteFile(fixedPath, []byte(fixed), 0o644))

	def1, err := app.Load(fixedPath)
	require.NoError(t, err)
	served.def = def1

	// Step 7: swap the served definition in while the session sits in
	// __error__. PrevStateExists must be true — __error__ still exists in
	// def1 because on_error_default: builtin survives the patch untouched.
	res, err := orch.Reload("", app.StatePath(app.ErrorRoomState))
	require.NoError(t, err)
	require.True(t, res.PrevStateExists)

	// Step 8 — THE MEASUREMENT. This is the ForceOnce:false path: __error__
	// has no on_enter:, so this call only re-renders. If this needs
	// RerunOnEnterWithOptions(..., RerunOnEnterOptions{ForceOnce: true}) to
	// succeed, that is the settled answer this probe exists to produce — see
	// this test's doc comment and the "ForceOnce: SETTLED" section appended
	// to the spec.
	_, err = orch.RerunOnEnter(ctx, sid)
	require.NoError(t, err)

	// Step 9: the retry is an ordinary transition — error_retry routes
	// __error__ -> world.error_origin ("work") — and an ordinary state entry
	// always fires on_enter.
	out2, err := orch.SubmitDirect(ctx, sid, app.ErrorRoomRetryIntent, nil)
	require.NoError(t, err)
	require.Equal(t, app.StatePath("work"), out2.NewState)

	// Step 10 — THE LOAD-BEARING ASSERTION. This proves the retried
	// on_enter actually RE-EXECUTED the (now-fixed) invoke: rather than being
	// elided by once:/replay caching: probe_result can only be "probe ok" if
	// host.probe.ok actually ran again after the swap.
	history, err = s.LoadHistory(sid)
	require.NoError(t, err)
	journey, err = store.BuildJourney(def1, orch.InitialState(), orch.InitialWorld(), history)
	require.NoError(t, err)
	require.Equal(t, "probe ok", journey.World.Vars["probe_result"])

	// Step 11: the story continues past the previously-failed step.
	out3, err := orch.SubmitDirect(ctx, sid, "done", nil)
	require.NoError(t, err)
	require.Equal(t, app.StatePath("finish"), out3.NewState)
}
