// Tests for the deterministic free-text tier (default_intent): when an
// utterance matches no intent deterministically or semantically, a state that
// declares default_intent sinks the whole input into that intent's single
// required string slot — without calling the main-turn LLM. A command the
// operator does name still wins in the earlier semantic tier, and a state
// without default_intent falls through to the harness exactly as before.
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

func newDefaultIntentApp(t *testing.T, withDefault bool) (*orchestrator.Orchestrator, *countingHarness, store.Store, app.SessionID) {
	t.Helper()
	defaultLine := ""
	if withDefault {
		defaultLine = "    default_intent: discuss\n"
	}
	appYAML := `
app:
  id: default-intent-test
  version: 0.1.0
world:
  last_message: { type: string, default: "" }
routing:
  enabled: true
intents:
  discuss:
    title: "Discuss"
    slots:
      message: { type: string, required: true }
  quit:
    title: "Quit"
    synonyms: ["quit"]
root: chat
states:
  chat:
    mode: conversational
` + defaultLine + `    view: "chat msg={{ world.last_message }}"
    on:
      discuss:
        - target: .
          effects:
            - set:
                last_message: "{{ slots.message }}"
      quit:
        - target: ended
  ended:
    terminal: true
    view: "done"
`
	def, err := app.LoadBytes([]byte(appYAML))
	require.NoError(t, err)
	m, err := machine.New(def)
	require.NoError(t, err)
	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	// Fallback routes to quit so a harness-handled turn has a sane outcome.
	h := &countingHarness{fall: staticHarness{intentName: "quit"}}
	orch := orchestrator.New(def, m, s, h)
	sid, err := orch.NewSession(context.Background())
	require.NoError(t, err)
	return orch, h, s, sid
}

func newFreeFormFallbackApp(t *testing.T) (*orchestrator.Orchestrator, *countingHarness, store.Store, app.SessionID) {
	t.Helper()
	const appYAML = `
app:
  id: freeform-fallback-test
  version: 0.1.0
world:
  landing_request: { type: string, default: "" }
  landing_note: { type: object, default: {} }
routing:
  enabled: true
intents:
  work:
    title: "Work"
    slots:
      request: { type: string, required: true }
  go_main:
    title: "Home"
    examples: ["home", "go home"]
root: tickets
states:
  landing:
    view: "landing request={{ world.landing_request }} summary={{ world.landing_note.summary }}"
    on_enter:
      - when: "world.landing_request != ''"
        invoke: host.oracle.task
        with:
          acceptance:
            schema: schemas/note.json
          context:
            prompt: prompts/landing.md
            args:
              request: "{{ world.landing_request }}"
        bind:
          landing_note: submitted
    on:
      work:
        - target: landing
          effects:
            - set:
                landing_request: "{{ slots.request }}"
                landing_note: {}
      go_main:
        - target: landing
  tickets:
    view: "tickets"
    on:
      go_main:
        - target: landing
`
	def, err := app.LoadBytes([]byte(appYAML))
	require.NoError(t, err)
	m, err := machine.New(def)
	require.NoError(t, err)
	s, err := store.OpenMemory()
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	h := &countingHarness{fall: staticHarness{intentName: "go_main"}}
	reg := host.NewRegistry()
	reg.Register("host.oracle.task", func(ctx context.Context, args map[string]any) (host.Result, error) {
		return host.Result{Data: map[string]any{
			"ok": true,
			"submitted": map[string]any{
				"summary": "processed by workbench",
			},
		}}, nil
	})
	orch := orchestrator.New(def, m, s, h, orchestrator.WithHostRegistry(reg))
	sid, err := orch.NewSession(context.Background())
	require.NoError(t, err)
	return orch, h, s, sid
}

// TestStateDefaultIntent reports the resolved free-text sink for a state — the
// web composer reads it (via the driver) to default its text box to the sink
// intent. A state with a default_intent returns its name; one without returns
// "".
func TestStateDefaultIntent(t *testing.T) {
	t.Parallel()

	withDI, _, _, _ := newDefaultIntentApp(t, true)
	require.Equal(t, "discuss", withDI.StateDefaultIntent(app.StatePath("chat")),
		"a state declaring default_intent reports its resolved name")
	require.Equal(t, "", withDI.StateDefaultIntent(app.StatePath("ended")),
		"a state without default_intent reports empty")
	require.Equal(t, "", withDI.StateDefaultIntent(app.StatePath("nonexistent")),
		"an unknown state reports empty")

	withoutDI, _, _, _ := newDefaultIntentApp(t, false)
	require.Equal(t, "", withoutDI.StateDefaultIntent(app.StatePath("chat")),
		"no default_intent declared anywhere → empty")
}

// TestDefaultIntent_UnmatchedFreeTextRoutesToDefault is the core fix: prose that
// matches no command routes to `discuss` with the whole utterance as
// slots.message, deterministically, without the harness.
func TestDefaultIntent_UnmatchedFreeTextRoutesToDefault(t *testing.T) {
	t.Parallel()
	orch, h, s, sid := newDefaultIntentApp(t, true)
	ctx := context.Background()

	const msg = "this doc — what about the open file?"
	out, err := orch.Turn(ctx, sid, msg)
	require.NoError(t, err)
	require.EqualValues(t, 0, h.calls.Load(),
		"default tier must resolve free text without the main-turn LLM")
	require.Equal(t, app.StatePath("chat"), out.NewState, "discuss is a self-loop (target: .)")
	require.Contains(t, out.View, "msg="+msg,
		"the whole utterance must fill slots.message and reach the effect")

	// Provenance: the turn must record that the default tier routed it.
	history, err := s.LoadHistory(sid)
	require.NoError(t, err)
	assertRoutedBy(t, history, "default")
}

// TestDefaultIntent_NamedCommandStillWins — a command the operator names ("quit")
// resolves in the semantic tier before the default tier is reached.
func TestDefaultIntent_NamedCommandStillWins(t *testing.T) {
	t.Parallel()
	orch, h, _, sid := newDefaultIntentApp(t, true)
	ctx := context.Background()

	out, err := orch.Turn(ctx, sid, "quit")
	require.NoError(t, err)
	require.EqualValues(t, 0, h.calls.Load(), "synonym 'quit' resolves in the semantic tier")
	require.Equal(t, app.StatePath("ended"), out.NewState,
		"named command must win over the free-text default")
}

// TestDefaultIntent_AbsentFallsThroughToHarness — without default_intent the
// state behaves as before: unmatched prose falls through to the main-turn LLM.
func TestDefaultIntent_AbsentFallsThroughToHarness(t *testing.T) {
	t.Parallel()
	orch, h, _, sid := newDefaultIntentApp(t, false)
	ctx := context.Background()

	_, err := orch.Turn(ctx, sid, "this doc — what about the open file?")
	require.NoError(t, err)
	require.Positive(t, h.calls.Load(),
		"without default_intent, unmatched prose must fall through to the harness")
}

// TestFreeFormFallback_UnmatchedProseRoutesToWorkbench pins the general
// defense: a strict/menu room with no default_intent or off-ramp receives the
// app-level free-form fallback arc, so long actionable prose becomes workbench
// work instead of giving the main LLM a chance to guess go_main.
func TestFreeFormFallback_UnmatchedProseRoutesToWorkbench(t *testing.T) {
	t.Parallel()
	orch, h, s, sid := newFreeFormFallbackApp(t)
	ctx := context.Background()

	const msg = "we have a bunch of tickets saved in the repo itself we need to migrate to github"
	out, err := orch.Turn(ctx, sid, msg)
	require.NoError(t, err)
	require.EqualValues(t, 0, h.calls.Load(), "fallback must resolve without the main-turn LLM")
	require.Equal(t, app.StatePath("landing"), out.NewState)
	require.Contains(t, out.View, "request="+msg)
	require.Contains(t, out.View, "summary=processed by workbench")

	history, err := s.LoadHistory(sid)
	require.NoError(t, err)
	assertRoutedBy(t, history, "fallback")
}

// TestFreeFormFallback_NamedNavigationStillWins proves the fallback only catches
// genuinely unmatched prose; explicit navigation remains semantic/deterministic.
func TestFreeFormFallback_NamedNavigationStillWins(t *testing.T) {
	t.Parallel()
	orch, h, s, sid := newFreeFormFallbackApp(t)
	ctx := context.Background()

	out, err := orch.Turn(ctx, sid, "home")
	require.NoError(t, err)
	require.EqualValues(t, 0, h.calls.Load(), "semantic routing handles named navigation")
	require.Equal(t, app.StatePath("landing"), out.NewState)

	history, err := s.LoadHistory(sid)
	require.NoError(t, err)
	assertRoutedBy(t, history, "semantic")
}

func assertRoutedBy(t *testing.T, history []store.Event, want string) {
	t.Helper()
	var found bool
	for _, ev := range history {
		if ev.Kind != store.TurnStarted {
			continue
		}
		var p map[string]any
		require.NoError(t, json.Unmarshal(ev.Payload, &p))
		if p["routed_by"] == nil {
			continue
		}
		found = true
		require.Equal(t, want, p["routed_by"], "TurnStarted must record the resolving tier")
	}
	require.True(t, found, "a TurnStarted event carrying routing provenance must appear")
}
