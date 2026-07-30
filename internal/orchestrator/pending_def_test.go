package orchestrator_test

// pending_def_test.go proves the one-shot pending-def cell (SetPendingDef /
// the unexported takePendingDef it arms, consulted at the top of Reload) does
// what appdef_binding.go's doc comments claim: an armed def always wins over
// the injected reloader closure, the cell is consumed (win or lose) by
// exactly the next Reload call, and a FAILED Reload still clears it so a bad
// revision swap can never leak into a later, unrelated reload.
//
// This is exactly the trap the slice brief calls out: without the cell,
// SessionBinding.SwapDef calling o.Reload("", state) would silently reload
// from the injected WithReloader closure instead of installing the intended
// def — the most dangerous failure mode in this design, because it would
// look like it worked.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/host"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/store"
)

// writePendingDefFixture writes a minimal single-terminal-state app.yaml
// identifying itself by id (so a failed load/compile is easy to attribute in
// a test failure message) and, when withBadHost is true, declares a host no
// registry in this test file registers — the deliberate way to make Reload
// fail (ValidateAllowList) for case (3) below.
func writePendingDefFixture(t *testing.T, id string, withBadHost bool) string {
	t.Helper()
	dir := t.TempDir()
	hosts := ""
	if withBadHost {
		hosts = "hosts:\n  - host.bad_" + id + "\n"
	}
	yaml := `app:
  id: ` + id + `
  version: 0.1.0
  title: "pending-def cell fixture ` + id + `"

` + hosts + `root: idle

states:
  idle:
    view: "` + id + `"
    terminal: true
`
	path := filepath.Join(dir, "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o644))
	return path
}

func loadPendingDefFixture(t *testing.T, id string, withBadHost bool) *app.AppDef {
	t.Helper()
	def, err := app.Load(writePendingDefFixture(t, id, withBadHost))
	require.NoError(t, err)
	return def
}

func TestOrchestrator_PendingDefCell(t *testing.T) {
	def0 := loadPendingDefFixture(t, "pending-def-base", false)
	defA := loadPendingDefFixture(t, "pending-def-a", false)
	defB := loadPendingDefFixture(t, "pending-def-b", false)
	defBad := loadPendingDefFixture(t, "pending-def-bad", true)

	m0, err := machine.New(def0)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// injectBuiltinStoryAuthoringRoom appends host.agent.task to every loaded
	// def's hosts: allow-list unconditionally, so it must be registered even
	// though none of these fixtures' rooms invoke it. defBad additionally
	// declares host.bad_pending-def-bad, which this registry deliberately
	// does NOT register — that's what makes swapping to it fail.
	reg := host.NewRegistry()
	reg.Register("host.agent.task", func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{}, nil
	})

	reloaderCalls := 0
	// noopHarness is the black-box zero-behavior Harness already declared in
	// hostdispatch_test.go (same package, orchestrator_test) — reused here
	// rather than redeclared.
	orch := orchestrator.New(def0, m0, s, noopHarness{},
		orchestrator.WithHostRegistry(reg),
		orchestrator.WithReloader(func() (*app.AppDef, error) {
			reloaderCalls++
			return defB, nil
		}),
	)

	// (1) An armed pending def wins over the injected reloader, which would
	// return a DIFFERENT def (defB) if consulted.
	orch.SetPendingDef(defA)
	res1, err := orch.Reload("", app.StatePath("idle"))
	require.NoError(t, err)
	require.Same(t, defA, res1.Def, "an armed pending def must win over the injected reloader")
	require.Equal(t, 0, reloaderCalls, "the reloader must not even be consulted while a pending def is armed")

	// (2) The NEXT Reload, with nothing armed, falls back to the injected
	// reloader — proving the cell was consumed by (1), not left armed.
	res2, err := orch.Reload("", app.StatePath("idle"))
	require.NoError(t, err)
	require.Same(t, defB, res2.Def, "with nothing armed, Reload must fall back to the injected reloader")
	require.Equal(t, 1, reloaderCalls)

	// (3) A Reload that FAILS (defBad declares an unregistered host) still
	// clears the cell — proven by making the FOLLOWING Reload (nothing
	// re-armed) land on the reloader's def, not silently retry defBad.
	orch.SetPendingDef(defBad)
	_, err = orch.Reload("", app.StatePath("idle"))
	require.Error(t, err, "Reload must fail when the pending def declares an unregistered host")

	res3, err := orch.Reload("", app.StatePath("idle"))
	require.NoError(t, err)
	require.Same(t, defB, res3.Def, "a failed Reload must have cleared the cell, so this call falls back to the reloader")
	require.Equal(t, 2, reloaderCalls)
}
