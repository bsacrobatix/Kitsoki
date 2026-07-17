package testrunner_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"kitsoki/internal/agentroot"
	"kitsoki/internal/agents"
	"kitsoki/internal/app"
	"kitsoki/internal/baseskills"
	"kitsoki/internal/effect"
	"kitsoki/internal/store"
	"kitsoki/internal/testrunner"
)

// testdata/apps/agent_mode_smoke is the agent-mode flow fixture
// (.context/agent-mode-design.md §5.2): the synthesized `agent:<name>` root
// exercised end-to-end with NO LLM, mirroring testdata/apps/workbench_smoke.
//
// The smoke flows drive checked-in app.yaml / app_readonly.yaml pins of what
// agentroot.Synthesize emits so the fixture stays reviewable on disk;
// RunFlows ALSO accepts the agent:<name> scheme directly (loadAppForRun
// head-checks agentroot.IsAgentPath and resolves through the default
// sources — cwd project dirs, embedded library, builtins), which
// TestRunFlows_AgentScheme below exercises hermetically.
// TestRunFlows_AgentModeSmoke_FixtureMatchesSynthesizedRoot is what keeps
// the checked-in pins honest: it re-resolves the fixture's agent TOMLs
// through the real agentroot.Resolve + Synthesize and asserts both roots
// desugar to the same structural shape.
const (
	agentModeSmokeDir     = "../../testdata/apps/agent_mode_smoke"
	agentModeWriteApp     = agentModeSmokeDir + "/app.yaml"
	agentModeReadonlyApp  = agentModeSmokeDir + "/app_readonly.yaml"
	agentModeSmokeGlob    = agentModeSmokeDir + "/flows/agent_smoke.yaml"
	agentModeDiscussGlob  = agentModeSmokeDir + "/flows/agent_discuss.yaml"
	agentModeFixtureAgent = "demo-agent"
	agentModeFixtureQA    = "demo-qa"
)

// agentModeFixtureSources scopes agentroot resolution to the fixture's own
// agents/ dir: the library seam reports not-staged and the builtin registry
// is empty, so resolution is hermetic regardless of the developer's HOME or
// the embedded library's staging state.
func agentModeFixtureSources() agentroot.Sources {
	return agentroot.Sources{
		ProjectDirs: []string{filepath.Join(agentModeSmokeDir, "agents")},
		Materialize: func(context.Context) (string, error) { return "", baseskills.ErrNotStaged },
		Builtins:    emptyAgentRegistry{},
	}
}

// normalisedAgentTools mirrors internal/app's (unexported) normaliseAgentTool
// canonicalization — the loader rewrites a bare YAML tool name into the
// fully-qualified host.<Name> form — so a resolved Def's bare tool list can be
// compared against the loaded decl/toolbox surfaces.
func normalisedAgentTools(tools []string) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		if strings.HasPrefix(tool, "host.") || strings.HasPrefix(tool, "mcp__") {
			out = append(out, tool)
			continue
		}
		out = append(out, "host."+tool)
	}
	return out
}

// emptyAgentRegistry keeps the fixture's builtin tier deterministic (empty).
type emptyAgentRegistry struct{}

func (emptyAgentRegistry) Get(string) (agents.Agent, bool) { return agents.Agent{}, false }
func (emptyAgentRegistry) List() []string                  { return nil }
func (emptyAgentRegistry) Register(agents.Agent)           {}

// TestRunFlows_AgentModeSmoke drives both synthesized-root shapes:
//   - workbench (write agent): capture → cassette-stubbed host.agent.task
//     dispatch → read-only floor across a repeated dispatch → off-ramp
//     residual rejection (see agent_smoke.yaml's header for why the voiced
//     host.agent.converse answer is out of a flow fixture's reach), and
//   - conversational (read agent): the synthesized agent_discuss intent's
//     no-op self-arc rests the room (agent_discuss.yaml).
func TestRunFlows_AgentModeSmoke(t *testing.T) {
	cases := []struct {
		name    string
		appPath string
		glob    string
	}{
		{"workbench_write_agent", agentModeWriteApp, agentModeSmokeGlob},
		{"conversational_read_agent", agentModeReadonlyApp, agentModeDiscussGlob},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report, err := testrunner.RunFlows(t.Context(), tc.appPath, tc.glob, testrunner.FlowOptions{})
			require.NoError(t, err, "RunFlows should not return a fatal error")
			require.NotEmpty(t, report.Results, "should have at least one result")

			for _, r := range report.Results {
				if r.Skipped {
					t.Logf("SKIP %s", filepath.Base(r.File))
					continue
				}
				for _, tr := range r.Turns {
					if !tr.Passed {
						t.Errorf("flow %s turn %d failed: %v", filepath.Base(r.File), tr.TurnIndex+1, tr.Failures)
					}
				}
			}
			require.Equal(t, 0, report.Failed, "all flows should pass")
			require.Greater(t, report.Passed, 0, "at least one flow should pass")
		})
	}
}

// TestRunFlows_AgentModeSmoke_AgentCallRecordsDispatchingStatePath mirrors
// workbench_smoke's trace-provenance assertion for the SYNTHESIZED agent-mode
// root: the workbench dispatch fires from the synthesized `agent` room's
// on_enter, and its agent.call.start event must carry state_path == "agent"
// (agentroot.RoomName) so a trace reader can attribute the call to the agent
// session's floor from the existing agent.call.* provenance alone. Reads the
// run's authoritative JSONL sink via OnRigClose, not TurnResult.Events, for
// the same reason flows_workbench_smoke_trace_test.go documents (the cassette
// dispatcher writes AgentCalled/AgentReturned straight to the sink).
func TestRunFlows_AgentModeSmoke_AgentCallRecordsDispatchingStatePath(t *testing.T) {
	var sinkHistory store.History
	opts := testrunner.FlowOptions{
		OnRigClose: func(filePath string, st store.Store, sid app.SessionID, sink *store.JSONLSink) error {
			sinkHistory = append(sinkHistory, sink.History()...)
			return nil
		},
	}

	report, err := testrunner.RunFlows(t.Context(), agentModeWriteApp, agentModeSmokeGlob, opts)
	require.NoError(t, err, "RunFlows should not return a fatal error")
	require.Equal(t, 0, report.Failed, "all flows should pass")
	require.NotEmpty(t, sinkHistory, "OnRigClose should have been invoked with a non-empty sink")

	var agentCalledEvents []store.Event
	for _, ev := range sinkHistory {
		if ev.Kind == store.AgentCalled {
			agentCalledEvents = append(agentCalledEvents, ev)
		}
	}
	require.NotEmpty(t, agentCalledEvents,
		"agent_smoke.yaml dispatches the synthesized on_enter host.agent.task through a real host_cassette and should produce agent.call.start events in the JSONL sink")

	for _, ev := range agentCalledEvents {
		require.Equal(t, app.StatePath(agentroot.RoomName), ev.StatePath,
			"the synthesized workbench dispatch fires from the agent room's on_enter; its agent.call.start event must carry the dispatching state path")
	}
}

// TestRunFlows_AgentModeSmoke_UsableKitsokiGateSignal mirrors workbench_smoke's
// S6 usable-kitsoki-gate assertion for the synthesized root: the agent room is
// a real workbench room after desugaring, so its transitioned turn.end events
// carry the gate signal, and the cassette-stubbed dispatch (a successful
// episode, no AgentError) yields candidate_completed true / silent_bounce
// false. The rejected off-ramp turn carries no signal and is skipped.
func TestRunFlows_AgentModeSmoke_UsableKitsokiGateSignal(t *testing.T) {
	var sinkHistory store.History
	opts := testrunner.FlowOptions{
		OnRigClose: func(filePath string, st store.Store, sid app.SessionID, sink *store.JSONLSink) error {
			sinkHistory = append(sinkHistory, sink.History()...)
			return nil
		},
	}

	report, err := testrunner.RunFlows(t.Context(), agentModeWriteApp, agentModeSmokeGlob, opts)
	require.NoError(t, err, "RunFlows should not return a fatal error")
	require.Equal(t, 0, report.Failed, "all flows should pass")
	require.NotEmpty(t, sinkHistory, "OnRigClose should have been invoked with a non-empty sink")

	var foundGateSignal bool
	for _, ev := range sinkHistory {
		if ev.Kind != store.TurnEnded {
			continue
		}
		var payload map[string]any
		require.NoError(t, json.Unmarshal(ev.Payload, &payload), "turn.end payload must decode as JSON")

		raw, ok := payload["usable_kitsoki_gate"]
		if !ok {
			continue // not every turn is workbench-origin (e.g. the rejected off-ramp turn)
		}
		foundGateSignal = true

		sig, ok := raw.(map[string]any)
		require.True(t, ok, "usable_kitsoki_gate must decode as an object, got %T", raw)
		require.Equal(t, true, sig["candidate_completed"],
			"the cassette episode returns a successful submitted note — candidate_completed should reflect that")
		require.Equal(t, false, sig["silent_bounce"],
			"the dispatch succeeded (no AgentError this turn) so silent_bounce must be false")
	}
	require.True(t, foundGateSignal,
		"expected at least one turn.end event to carry a usable_kitsoki_gate signal for the agent room's workbench-origin dispatch")
}

// TestRunFlows_AgentScheme proves the flow harness accepts the agent:<name>
// scheme itself (design §1: `kitsoki test flows agent:<name> --flows
// <fixture.yaml>`): loadAppForRun head-checks the scheme and synthesizes the
// root through the DEFAULT sources, so the test isolates resolution the same
// way cmd/kitsoki's agent-mode tests do — a temp cwd carrying the project
// TOML and HOME redirected away from the developer's real ~/.codex/agents.
func TestRunFlows_AgentScheme(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	require.NoError(t, os.MkdirAll(home, 0o755))
	t.Setenv("HOME", home)

	agentsDir := filepath.Join(dir, ".kitsoki", "agents")
	require.NoError(t, os.MkdirAll(agentsDir, 0o755))
	const flowdemoTOML = `name = "flowdemo"
description = "Read-only flow demo agent"
developer_instructions = "You answer questions about the project."
sandbox_mode = "read-only"
`
	require.NoError(t, os.WriteFile(filepath.Join(agentsDir, "flowdemo.toml"), []byte(flowdemoTOML), 0o600))

	flowsDir := filepath.Join(dir, "flows")
	require.NoError(t, os.MkdirAll(flowsDir, 0o755))
	const discussFlow = `test_kind: flow
app: agent:flowdemo
initial_state: agent
initial_world: {}

turns:
  - intent:
      name: agent_discuss
      slots:
        message: "what does this project do?"
    expect_state: agent
    expect_world_unchanged: true

expect_no_errors: true
`
	require.NoError(t, os.WriteFile(filepath.Join(flowsDir, "agent_discuss.yaml"), []byte(discussFlow), 0o600))
	// host_bindings has no meaning over a synthesized root (no hosts: block)
	// and must be rejected loudly, not ignored.
	const bindingsFlow = `test_kind: flow
app: agent:flowdemo
initial_state: agent
host_bindings:
  some.iface: some.impl
turns: []
`
	require.NoError(t, os.WriteFile(filepath.Join(flowsDir, "agent_bindings.yaml"), []byte(bindingsFlow), 0o600))
	t.Chdir(dir)

	report, err := testrunner.RunFlows(t.Context(), "agent:flowdemo",
		filepath.Join(flowsDir, "agent_discuss.yaml"), testrunner.FlowOptions{})
	require.NoError(t, err, "the agent scheme must load through the flow harness")
	require.Equal(t, 0, report.Failed, "the synthesized conversational root must pass its flow")
	require.Greater(t, report.Passed, 0)

	_, err = testrunner.RunFlows(t.Context(), "agent:definitely-not-a-known-agent",
		filepath.Join(flowsDir, "agent_discuss.yaml"), testrunner.FlowOptions{})
	require.Error(t, err, "an unknown agent name must fail the load, mirroring a bad story path")
	require.Contains(t, err.Error(), "not found")

	_, err = testrunner.RunFlows(t.Context(), "agent:flowdemo",
		filepath.Join(flowsDir, "agent_bindings.yaml"), testrunner.FlowOptions{})
	require.Error(t, err, "host_bindings over a synthesized root must be rejected")
	require.Contains(t, err.Error(), "host_bindings is not supported with the agent:<name> scheme")
}

// TestRunFlows_AgentModeSmoke_FixtureMatchesSynthesizedRoot is the honesty
// pin for the checked-in fixture apps: it resolves the fixture's agent TOMLs
// through the REAL agentroot.Resolve + Synthesize (the path every agent:<name>
// loader head takes) and asserts the checked-in app.yaml / app_readonly.yaml
// desugar to the same structural shape. If Synthesize's emitted root drifts
// (room name, capture/discuss intent wiring, dispatch bind, agent decl), this
// test fails and the fixture must be re-pinned — the flows can never silently
// exercise a stale shape.
func TestRunFlows_AgentModeSmoke_FixtureMatchesSynthesizedRoot(t *testing.T) {
	src := agentModeFixtureSources()

	t.Run("write_agent_workbench_shape", func(t *testing.T) {
		def, err := agentroot.Resolve(agentModeFixtureAgent, src)
		require.NoError(t, err)
		require.Equal(t, effect.Write, def.Effect, "a project TOML with no sandbox_mode resolves to the full write surface")

		synth, err := agentroot.Synthesize(def, agentroot.Options{WorkingDir: t.TempDir(), SchemaDir: t.TempDir()})
		require.NoError(t, err)

		fixture, err := app.LoadWithResolver(agentModeWriteApp, nil, nil)
		require.NoError(t, err, "checked-in fixture app must load")

		require.Equal(t, synth.App.ID, fixture.App.ID, "fixture app id must be the scheme string Synthesize stamps")

		for name, d := range map[string]*app.AppDef{"synthesized": synth, "fixture": fixture} {
			room := d.States[agentroot.RoomName]
			require.NotNil(t, room, "%s root must carry the one %q room", name, agentroot.RoomName)
			require.Equal(t, app.WriteModeReadOnly, room.WriteMode, "%s: workbench macro sets the read-only floor", name)
			require.NotNil(t, room.AgentOffRamp, "%s: workbench synthesizes an off-ramp", name)
			require.Equal(t, agentModeFixtureAgent, room.AgentOffRamp.Agent, "%s: off-ramp voice is the workbench agent", name)

			captureIntent := agentroot.RoomName + "_capture"
			require.Equal(t, captureIntent, room.DefaultIntent, "%s: free text sinks into the synthesized capture intent", name)
			require.Contains(t, d.Intents, captureIntent, "%s: capture intent registered top-level", name)

			var dispatch *app.Effect
			for i := range room.OnEnter {
				if room.OnEnter[i].Invoke == "host.agent.task" {
					dispatch = &room.OnEnter[i]
				}
			}
			require.NotNil(t, dispatch, "%s: workbench synthesizes the on_enter host.agent.task dispatch", name)
			require.Equal(t, agentModeFixtureAgent, dispatch.With["agent"], "%s: dispatch names the workbench agent", name)
			require.Equal(t, "submitted", dispatch.Bind[agentroot.RoomName+"_note"], "%s: dispatch binds the close-out note", name)
			ctxBlock, ok := dispatch.With["context"].(map[string]any)
			require.True(t, ok, "%s: dispatch carries a context block", name)
			prompt, _ := ctxBlock["prompt"].(string)
			require.True(t, strings.Contains(prompt, "{{ args.request }}"),
				"%s: the inline dispatch prompt must thread the captured request", name)

			for _, key := range []string{agentroot.RoomName + "_request", agentroot.RoomName + "_note", "workdir"} {
				require.Contains(t, d.World, key, "%s: world key %q", name, key)
			}

			decl := d.Agents[agentModeFixtureAgent]
			require.NotNil(t, decl, "%s: agents block carries the resolved agent", name)
			require.Equal(t, effect.Write, decl.Effect, "%s: agent resolves to effect write", name)
			require.NotEmpty(t, decl.Toolbox, "%s: workbench agent carries the WS toolbox vocabulary", name)
			// The tool surface is the piece the sandbox contract documents
			// (projectWriteToolbox), so pin it exactly: the loader resolves
			// toolbox: into decl.Tools, and both roots must carry the resolved
			// definition's list verbatim — same-effect-class drift (a tool
			// added or removed on either side) fails here.
			require.Equal(t, normalisedAgentTools(def.Tools), decl.Tools,
				"%s: resolved agent tool surface must match the definition's write toolbox", name)
			box := d.Toolboxes[decl.Toolbox]
			require.NotNil(t, box, "%s: agent's toolbox must be declared in toolboxes", name)
			require.Equal(t, normalisedAgentTools(def.Tools), box.Tools,
				"%s: toolbox tool list must match the resolved definition", name)
			require.Equal(t, effect.Write, box.Effect,
				"%s: toolbox declares the write effect the workbench invariant requires", name)
			require.Equal(t, def.SystemPrompt, decl.SystemPrompt,
				"%s: agent system prompt is the TOML's developer_instructions verbatim", name)
		}
	})

	t.Run("read_agent_conversational_shape", func(t *testing.T) {
		def, err := agentroot.Resolve(agentModeFixtureQA, src)
		require.NoError(t, err)
		require.Equal(t, effect.Read, def.Effect, "sandbox_mode: read-only resolves to a read agent")

		synth, err := agentroot.Synthesize(def, agentroot.Options{WorkingDir: t.TempDir(), SchemaDir: t.TempDir()})
		require.NoError(t, err)

		fixture, err := app.LoadWithResolver(agentModeReadonlyApp, nil, nil)
		require.NoError(t, err, "checked-in read-only fixture app must load")

		require.Equal(t, synth.App.ID, fixture.App.ID, "fixture app id must be the scheme string Synthesize stamps")

		for name, d := range map[string]*app.AppDef{"synthesized": synth, "fixture": fixture} {
			room := d.States[agentroot.RoomName]
			require.NotNil(t, room, "%s root must carry the one %q room", name, agentroot.RoomName)
			require.Empty(t, room.WriteMode, "%s: read agents get no workbench floor", name)
			require.NotNil(t, room.AgentOffRamp, "%s: conversational shape declares the off-ramp", name)
			require.Equal(t, agentModeFixtureQA, room.AgentOffRamp.Agent, "%s: off-ramp voice is the resolved agent", name)
			require.True(t, room.AgentOffRamp.CaptureFreeText, "%s: capture_free_text makes the off-ramp the free-text sink", name)

			discussIntent := agentroot.RoomName + app.OffRampCaptureIntentSuffix
			require.Equal(t, discussIntent, room.DefaultIntent, "%s: default_intent is the synthesized discuss sink", name)
			require.Contains(t, d.Intents, discussIntent, "%s: discuss intent registered top-level", name)

			for _, eff := range room.OnEnter {
				require.NotEqual(t, "host.agent.task", eff.Invoke,
					"%s: a read agent's room must not dispatch host.agent.task on enter", name)
			}

			decl := d.Agents[agentModeFixtureQA]
			require.NotNil(t, decl, "%s: agents block carries the resolved agent", name)
			require.Equal(t, effect.Read, decl.Effect, "%s: agent resolves to effect read", name)
			// Pin the read tool surface (projectReadToolbox): conversational
			// agents carry a bare tools: list, and both roots must match the
			// resolved definition verbatim so same-effect-class drift (e.g.
			// the fixture shrinking to [Read]) fails loudly.
			require.Empty(t, decl.Toolbox, "%s: conversational agents carry a bare tools list, not a toolbox", name)
			require.Equal(t, normalisedAgentTools(def.Tools), decl.Tools,
				"%s: resolved agent tool surface must match the definition's read surface", name)
			require.Equal(t, def.SystemPrompt, decl.SystemPrompt,
				"%s: agent system prompt is the TOML's developer_instructions verbatim", name)
		}
	})
}
