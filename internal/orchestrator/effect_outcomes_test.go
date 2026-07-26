package orchestrator_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/app"
	"kitsoki/internal/host"
	"kitsoki/internal/machine"
	"kitsoki/internal/orchestrator"
	"kitsoki/internal/store"
)

func TestEffectOutcomeBindsAndTransitionsDeterministically(t *testing.T) {
	const story = `
app: {id: effect-outcome, version: 1.0.0}
hosts: [host.probe]
world:
  message: {type: string, default: ""}
intents:
  enter: {title: Enter}
root: start
states:
  start:
    on:
      enter: [{target: probe}]
  probe:
    on_enter:
      - invoke: host.probe
        result:
          status: {type: string, required: true}
          message: {type: string, required: true}
        bind:
          message: message
        outcomes:
          accepted:
            when: result.status == "ok"
            target: success
          rejected:
            default: true
            target: failed
  success:
    view: "Accepted {{ world.message }}"
    terminal: true
  failed:
    view: "Rejected {{ world.message }}"
    terminal: true
`
	def, err := app.LoadBytes([]byte(story))
	require.NoError(t, err)
	m, err := machine.New(def)
	require.NoError(t, err)
	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	reg := host.NewRegistry()
	reg.Register("host.probe", func(context.Context, map[string]any) (host.Result, error) {
		return host.Result{Data: map[string]any{"status": "ok", "message": "bound"}}, nil
	})
	orch := orchestrator.New(def, m, s, noopHarness{}, orchestrator.WithHostRegistry(reg))
	sid, err := orch.NewSession(context.Background())
	require.NoError(t, err)

	out, err := orch.SubmitDirect(context.Background(), sid, "enter", nil)
	require.NoError(t, err)
	require.Equal(t, app.StatePath("success"), out.NewState)
	require.Contains(t, out.View, "Accepted bound")

	var recordedOutcome string
	for _, event := range out.Events {
		if event.Kind != store.HostReturned {
			continue
		}
		var payload map[string]any
		require.NoError(t, json.Unmarshal(event.Payload, &payload))
		recordedOutcome, _ = payload["outcome"].(string)
	}
	require.Equal(t, "accepted", recordedOutcome)
}

func TestEffectWithoutOutcomesPreservesLegacyHostBehavior(t *testing.T) {
	const story = `
app: {id: legacy-effect, version: 1.0.0}
hosts: [host.probe]
world:
  message: {type: string, default: ""}
intents:
  enter: {title: Enter}
root: start
states:
  start:
    on:
      enter: [{target: probe}]
  probe:
    on_enter:
      - invoke: host.probe
        bind: {message: message}
    view: "Legacy {{ world.message }}"
`
	def, err := app.LoadBytes([]byte(story))
	require.NoError(t, err)
	m, err := machine.New(def)
	require.NoError(t, err)
	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	reg := host.NewRegistry()
	reg.Register("host.probe", func(context.Context, map[string]any) (host.Result, error) {
		return host.Result{Data: map[string]any{"message": "bound"}}, nil
	})
	orch := orchestrator.New(def, m, s, noopHarness{}, orchestrator.WithHostRegistry(reg))
	sid, err := orch.NewSession(context.Background())
	require.NoError(t, err)

	out, err := orch.SubmitDirect(context.Background(), sid, "enter", nil)
	require.NoError(t, err)
	require.Equal(t, app.StatePath("probe"), out.NewState)
	require.Contains(t, out.View, "Legacy bound")
	for _, event := range out.Events {
		if event.Kind != store.HostReturned {
			continue
		}
		var payload map[string]any
		require.NoError(t, json.Unmarshal(event.Payload, &payload))
		_, hasOutcome := payload["outcome"]
		require.False(t, hasOutcome)
	}
}
