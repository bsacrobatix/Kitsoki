package orchestrator_test

import (
	"context"
	"strings"
	"testing"

	mcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"hally/internal/app"
	"hally/internal/harness"
	"hally/internal/host"
	"hally/internal/machine"
	"hally/internal/orchestrator"
	"hally/internal/store"
)

// TestOrchestrator_HostDispatchBindsAndRefreshesView covers the orchestrator's
// post-machine host-call dispatch path: after a state's on_enter invokes a
// host.*, the binding lands in world and the returned view reflects it on the
// same turn (not the next one).
func TestOrchestrator_HostDispatchBindsAndRefreshesView(t *testing.T) {
	def, err := app.Load("testdata/hostbind/app.yaml")
	require.NoError(t, err)

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	reg := host.NewRegistry()
	reg.Register("host.probe", func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{Data: map[string]any{"message": "hello world"}}, nil
	})

	orch := orchestrator.New(def, m, s, noopHarness{}, orchestrator.WithHostRegistry(reg))

	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	out, err := orch.SubmitDirect(ctx, sid, "ask", map[string]any{})
	require.NoError(t, err)
	require.Equal(t, orchestrator.ModeTransitioned, out.Mode)
	require.Equal(t, app.StatePath("probe"), out.NewState)
	require.True(t, strings.Contains(out.View, "hello world"),
		"expected refreshed view to include bound value, got: %q", out.View)
}

// TestOrchestrator_HostDispatchDisabledWhenNoRegistry verifies the orchestrator
// is safe to run without a host registry: host calls are ignored, bindings do
// not land, and the view still renders (with the pre-host world).
func TestOrchestrator_HostDispatchDisabledWhenNoRegistry(t *testing.T) {
	def, err := app.Load("testdata/hostbind/app.yaml")
	require.NoError(t, err)

	m, err := machine.New(def)
	require.NoError(t, err)

	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	// Note: no WithHostRegistry — deterministic flow-test posture.
	orch := orchestrator.New(def, m, s, noopHarness{})

	ctx := context.Background()
	sid, err := orch.NewSession(ctx)
	require.NoError(t, err)

	out, err := orch.SubmitDirect(ctx, sid, "ask", map[string]any{})
	require.NoError(t, err)
	require.Equal(t, orchestrator.ModeTransitioned, out.Mode)
	require.Equal(t, app.StatePath("probe"), out.NewState)
	require.False(t, strings.Contains(out.View, "hello world"),
		"host binding should be skipped when no registry is wired")
}

// noopHarness is a zero-behavior Harness for SubmitDirect tests. RunTurn is
// never invoked by SubmitDirect, so a stub is sufficient.
type noopHarness struct{}

func (noopHarness) RunTurn(ctx context.Context, in harness.TurnInput) (mcp.CallToolParams, error) {
	return mcp.CallToolParams{}, nil
}
func (noopHarness) Close() error { return nil }
