# Tracing: usable-kitsoki release gate

**Status:** Tasks 1 (parity-metric spec), 2 (plugin skeleton), and 4.1
(golden regression fixtures) are shipped and tested, zero LLM spend. Tasks 3
(wire real S1/S4 inputs), 4.2 (calibration-set run), and 5 (stand it up as
the CI release gate) are gated on S1 (workbench) and S4 (scenario foundry)
landing. See `docs/proposals/usable-kitsoki-release-gate.md` for the
proposal's remaining open questions and the gated tasks.

This is the day-one contract `S1` (the free-form workbench) develops
against, and the schema `S6` (`tools/arena/arena/plugins/usable_kitsoki_gate.py`)
scores against. Nothing here is prose-only: the schema and the gate
constants are checked-in, importable artifacts, not just this description of
them.

## Parity verdict record

Schema:
[`tools/arena/arena/plugins/usable_kitsoki_gate_schema.json`](../../tools/arena/arena/plugins/usable_kitsoki_gate_schema.json)
(versioned, draft-07, `$id` ends in `/v1.0.0`).

One record per scenario x persona x surface, at the end of a
`usable-kitsoki-gate` arena cell:

```jsonc
{
  "schema_version": "1.0.0",
  "scenario_id": "scn-git-ops-0007",
  "persona": "impatient-debugger",
  "surface": "web",
  "source_completed": true,
  "candidate_completed": false,
  "silent_bounce": false,
  "misroute_adjacent": true,
  "evidence_refs": [
    ".artifacts/usable-kitsoki-gate/<run_id>/<scenario_id>/trace.jsonl",
    ".artifacts/usable-kitsoki-gate/<run_id>/<scenario_id>/rrweb.json"
  ],
  "notes": "workbench asked a clarifying question the source session never needed"
}
```

**`scenario_id` naming note:** the release-gate proposal's own worked example
writes `scenario_id` where S4's scenario IR (`docs/proposals/scenario-foundry.md`)
names the same concept `id` (e.g. `"scn-git-ops-0007"`). This is resolved as
a documented **alias, not a second identity scheme**: `scenario_id :=
IR.id`, verbatim. Do not derive a different string (e.g. from
`provenance.session_id` + a turn offset) — join a parity record back to its
scenario by exact string equality on this field.

This record is job-type-specific evidence alongside the job-agnostic
`CellResult` (`schemas/completion-state.schema.json`) every other arena
plugin scores through — the cell's aggregate rollup still produces a
`verdict`/`health`/`metrics` grade the same way `swarm.py`'s
`_completion_from_swarm_results` does; this record is what an aggregate rule
reduces over (see "Gate conditions" below).

## Gate conditions and parity threshold

Constants (not prose):
[`tools/arena/arena/plugins/usable_kitsoki_gate_constants.py`](../../tools/arena/arena/plugins/usable_kitsoki_gate_constants.py).

- `GATE_CONDITIONS` — the three named conditions, all of which must hold for
  a cell to pass: `zero_silent_bounce`, `zero_misroute_adjacent`,
  `parity_at_or_above_threshold`.
- `PARITY_THRESHOLD_PERCENT = 90.0` — the initial global floor (open
  question 1's lean: one global floor, calibrated later against S4's
  20-scenario calibration set; split per-persona only if that run shows a
  persona systematically dragging the number down for reasons unrelated to
  workbench quality). **This is a placeholder pending calibration** — see the
  constant's own docstring for the sign-off Brad needs to give before it
  gates a real release decision.
- `WORST_SURFACE_GATING = True` — open question 2's lean: parity is computed
  per surface (web / TUI / MCP) and the cell's overall parity is the
  **minimum** across surfaces, never an average. A product that's
  productive on web but silently bounces on MCP is not released-ready.
- `parity_percent()` / `gate_passes()` — the two pure functions the rollup
  calls; both take counts, not records, so `usable_kitsoki_gate.py`'s
  rollup (Task 2) owns reducing the per-scenario records into those counts.

## Producer contract — what S1 must emit

S1 (the free-form workbench, not yet built) must emit, **per scenario
turn**, a machine-readable completion signal keyed to the scenario IR's
`expected_effects` list (`docs/proposals/scenario-foundry.md`'s IR shape).
This is the *consumer* shape the parity scorer needs; S1's own tracing
proposal should link back here rather than re-deriving it.

Concretely, for each turn kitsoki drives against a mined scenario, S1's
trace must let a downstream reader answer, without re-running an LLM judge:

1. **Which `expected_effects` (if any) fired for this turn** — the effect
   names/predicates from the scenario IR that this turn's actions actually
   satisfied. `candidate_completed` for the scenario as a whole is derived
   from whether the full `expected_effects` set was covered by the run, the
   same way `source_completed` is read off the mined session's own
   `outcomes.py` satisfaction signal (never re-adjudicated by an LLM at gate
   time — S1 emits a fact, not an opinion).
2. **Whether an `on_error` arc fired with no rendered explanation** — the
   raw signal `silent_bounce` is computed from. S2 (the never-silent
   runtime) is what should make this always `false`; this gate is S2's
   regression test at scale.
3. **Which command/room this turn actually routed to, vs. the scenario's
   expected command** — the raw signal `misroute_adjacent` is computed from
   (true when the workbench routed to a command *adjacent* to the ask, not
   the ask itself).

This section stays partial until S1 lands and its concrete trace-event shape
(field names, event type, where it's emitted from) can be pinned down here
by reference rather than by this description alone. When S1 ships, replace
this section with a link to S1's own doc plus the exact event/field it
promises, and keep only the *why* (join key = scenario IR's `expected_effects`)
here to avoid duplicating the contract in two places.

## Determinism

No-LLM path replays every mined scenario through existing zero-spend
harnesses (flow fixtures / cassettes, swarm tier 1/2); this is what
Task 2.3's plugin tests and Task 4.1's golden regression scenarios prove
without touching a real workbench or an LLM. The live path (real workbench
turns) is a separate, gated, release-candidate-cadence run — see the
proposal's own "Determinism" section for the full split; nothing about the
schema or constants above changes between the two paths, only what produces
`candidate_completed`.

## Plugin usage

`usable-kitsoki-gate` is a fourth `arena` job type, registered alongside
`bugfix`/`persona-qa`/`swarm` (`arena plugins` lists it;
`tools/arena/arena/plugins/usable_kitsoki_gate.py`). One cell drives the
whole mined scenario corpus for one `persona x surface` combination; `arena
plan`/`arena run` enumerate cells the same way as every other job type
(`tools/arena/README.md`'s "Status — usable-kitsoki-gate job type
registered" section has the full `image()`/`drive_command()`/`score()`
walkthrough). With no scenario corpus yet (S4 not landed), `arena plan`
returns zero cells, not an error — the plugin can be exercised today only
through its own no-LLM tests
(`tools/arena/tests/test_usable_kitsoki_gate_plugin.py`,
`tools/arena/tests/test_usable_kitsoki_gate_schema.py`,
`tools/arena/tests/test_usable_kitsoki_gate_golden_fixtures.py`), all of
which run with zero docker and zero LLM spend.
