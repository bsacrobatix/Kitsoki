package orchestrator

import (
	"strings"

	"kitsoki/internal/app"
)

// workbenchGateSignal is room-workbench Task 1.4(b): the S6 usable-kitsoki-gate
// producer contract (docs/tracing/usable-kitsoki-gate.md, "Producer contract —
// what S1 must emit"). Per that doc's Decision-recording philosophy (no new
// event kind), this is carried as an additional key on the EXISTING
// store.TurnEnded ("turn.end") payload — usable_kitsoki_gate — rather than a
// new trace event.
//
// Field names deliberately mirror
// tools/arena/arena/plugins/usable_kitsoki_gate_schema.json's per-scenario
// parity-verdict-record properties (candidate_completed, silent_bounce,
// misroute_adjacent, evidence_refs) so a downstream S6 rollup can read this
// turn's signal with the same field names it already knows, per that schema's
// candidate_completed doc: "Read off S1's per-scenario-turn completion signal
// keyed to the scenario IR's expected_effects list — see
// docs/tracing/usable-kitsoki-gate.md '#producer-contract'".
//
// dispatchingState identifies which room's on_enter this turn's host calls
// belonged to (the machine's transition target BEFORE any on_error redirect —
// callers capture result.NewState right after machine.Turn, before
// dispatchHostCalls's hostRedirect return value can reassign it). This
// function looks up that state directly rather than scanning this turn's
// store.Event slice for agent.call.* events, because every agent.call.* event
// (both live dispatch, internal/host/agent_event_sink.go, and cassette replay,
// internal/testrunner/cassette.go's writeCassetteAgentEvents) is appended
// straight to the run's EventSink out-of-band from the batch orchestrator
// assembles into result.Events/successEvents — it is never present in the
// slice a turn.end builder has in hand. State identity is the one thing that
// IS reliably in scope at every transitionedTurnEndWithGateSignal call site.
//
// HONESTY NOTE (documented gap, not a fake value): the full producer contract
// asks S1 to key candidate_completed to a scenario's expected_effects list
// (S4's scenario IR, docs/proposals/scenario-foundry.md). S4 had not landed in
// this worktree's history at the time this was written (no
// tools/session-mining/schema/scenario_ir.schema.json, no
// stories/scenario-foundry-harness/ present here) — there is no expected_effects
// list to join against. What IS computed here, honestly, from only this
// turn's own dispatch outcome (no LLM, no invented judgment):
//
//   - candidate_completed: true iff dispatchFailed is false — i.e. the
//     workbench room's synthesized on_enter host.agent.task call this turn
//     did not take its on_error redirect. This is a necessary-but-not-
//     sufficient proxy for "the source session's ask got done" — it answers
//     "did the workbench's own dispatch succeed", not "did it satisfy the
//     scenario's expected_effects" (that join is S6's job once S4 lands;
//     this field is the raw material, not the final verdict).
//   - silent_bounce: true iff dispatchFailed is true AND the turn's final
//     rendered view is empty. In this codebase that should always evaluate
//     false, by construction, thanks to the RunIntent/RunIntentWithInput
//     on_error safety net (see the "Usability safety-net" comment near each
//     of this function's call sites) which forces a rendered explanation
//     onto every on_error redirect before turn.end is built — this field is
//     S2's regression test at scale, per the tracing doc's own framing, not
//     something S1 is meant to make happen.
//   - misroute_adjacent: always false. NOT computed — there is no way to know
//     "the scenario's expected command" without S4's IR. This is an explicit,
//     documented absence (an honest "not detected here"), never a fabricated
//     true/false claim about routing quality.
//   - evidence_refs: always empty. A real parity record's evidence_refs point
//     at durable per-run artifacts (trace.jsonl / rrweb.json paths) that only
//     exist once a scenario harness assembles a run directory; a single
//     mid-session turn.end payload has no such path to point at. The schema's
//     own minItems:1 constraint is NOT satisfied by this turn-level signal on
//     purpose — it is raw per-turn material for S6's rollup to combine with
//     real evidence paths, not a standalone parity record.
//
// Returns nil when dispatchingState is not a workbench: room (the
// overwhelming majority of turns in any app, workbench or not) so ordinary
// turn.end payloads are byte-identical to before this change.
func workbenchGateSignal(def *app.AppDef, dispatchingState app.StatePath, dispatchFailed bool, view string) map[string]any {
	if def == nil || dispatchingState == "" {
		return nil
	}
	st := def.States[string(dispatchingState)]
	if st == nil || st.Workbench == nil {
		return nil
	}

	candidateCompleted := !dispatchFailed
	silentBounce := dispatchFailed && strings.TrimSpace(view) == ""

	return map[string]any{
		"usable_kitsoki_gate": map[string]any{
			"candidate_completed": candidateCompleted,
			"silent_bounce":       silentBounce,
			"misroute_adjacent":   false,
			"evidence_refs":       []string{},
		},
	}
}
