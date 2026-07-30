package orchestrator_test

// appdef_binding_test.go exercises SessionBinding directly — no appdef, no
// RPC — against the same testdata/apps/liveedit/app.yaml fixture and
// error->fix->retry shape Slice 0's TestAppdefRetryProbe measured. That test
// proved plain RerunOnEnter suffices for the retry; this test proves
// SessionBinding.Closure/SwapDef/Reenter, wired together, drive the same
// swap through the adapter's three methods instead of calling
// Orchestrator.Reload/RecordEffectiveStory/RerunOnEnter by hand.

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

func TestSessionBinding_ClosureSwapReenter(t *testing.T) {
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

	reg := host.NewRegistry()
	reg.Register("host.probe.fail", func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{Error: "deliberate probe failure"}, nil
	})
	reg.Register("host.probe.ok", func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{Data: map[string]any{"value": "probe ok"}}, nil
	})
	// injectBuiltinStoryAuthoringRoom appends host.agent.task to every loaded
	// def's hosts: allow-list unconditionally (see Slice 0's notes).
	reg.Register("host.agent.task", func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{}, nil
	})

	// served stands in for the revision pin a real appdef.Service/Binding
	// would maintain. SessionBinding never touches it directly — it only
	// arms the orchestrator's pending-def cell via SetPendingDef, which is
	// exactly what makes a real SwapDef call install a specific compiled
	// revision rather than whatever this closure would return.
	served := &struct{ def *app.AppDef }{def0}
	orch := orchestrator.New(def0, m, s, noopHarness{},
		orchestrator.WithHostRegistry(reg),
		orchestrator.WithReloader(func() (*app.AppDef, error) { return served.def, nil }),
	)

	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)
	require.NoError(t, orch.RunInitialOnEnter(ctx, sid))

	binding := orchestrator.SessionBinding{Orch: orch, SID: sid}

	// Closure, before anything has gone wrong: the effective story is
	// exactly the fixture's one file.
	entry, files, err := binding.Closure()
	require.NoError(t, err)
	require.Equal(t, "app.yaml", entry)
	require.Contains(t, files, "app.yaml")
	require.Equal(t, string(original), string(files["app.yaml"]))

	// Drive the session into the error room via the broken host call.
	out, err := orch.SubmitDirect(ctx, sid, "begin", nil)
	require.NoError(t, err)
	require.Equal(t, app.StatePath(app.ErrorRoomState), out.NewState)

	// "Fix" the story exactly as Slice 0's probe did: swap which host
	// work's on_enter invokes.
	fixed := strings.ReplaceAll(string(original), "host.probe.fail", "host.probe.ok")
	require.NotEqual(t, string(original), fixed)

	fixedPath := filepath.Join(t.TempDir(), "app.yaml")
	require.NoError(t, os.WriteFile(fixedPath, []byte(fixed), 0o644))
	def1, err := app.Load(fixedPath)
	require.NoError(t, err)
	served.def = def1 // NOT consulted by SwapDef — see the comment above.

	// SwapDef installs def1 via the pending-def cell and reports whether the
	// error room the session is currently sitting in survived the edit.
	prevExists, err := binding.SwapDef(ctx, def1)
	require.NoError(t, err)
	require.True(t, prevExists, "__error__ must survive: the fix doesn't touch on_error_default")

	// Reenter re-fires __error__'s on_enter chain (it has none — this is
	// purely a re-render) without erroring.
	require.NoError(t, binding.Reenter(ctx))

	// Closure again, post-swap: the effective story now reflects def1.
	entry, files, err = binding.Closure()
	require.NoError(t, err)
	require.Equal(t, "app.yaml", entry)
	require.Equal(t, fixed, string(files["app.yaml"]))

	// The retry itself: error_retry routes __error__ -> world.error_origin
	// ("work"), and the retried on_enter re-executes the now-fixed invoke:.
	out2, err := orch.SubmitDirect(ctx, sid, app.ErrorRoomRetryIntent, nil)
	require.NoError(t, err)
	require.Equal(t, app.StatePath("work"), out2.NewState)

	history, err := s.LoadHistory(sid)
	require.NoError(t, err)
	journey, err := store.BuildJourney(def1, orch.InitialState(), orch.InitialWorld(), history)
	require.NoError(t, err)
	require.Equal(t, "probe ok", journey.World.Vars["probe_result"])

	// The story continues past the previously-failed step.
	out3, err := orch.SubmitDirect(ctx, sid, "done", nil)
	require.NoError(t, err)
	require.Equal(t, app.StatePath("finish"), out3.NewState)
}
