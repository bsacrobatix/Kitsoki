# Tracing: usable-kitsoki release gate — the realism bar

**Status:** Tasks 1 (parity-metric spec, `4205c5af`), 2 (plugin skeleton,
`606a5181`), and 4.1 (golden regression fixtures, `e4f55ffb`) are shipped —
the parity verdict schema, gate constants, the registered
`usable-kitsoki-gate` arena plugin, and the offline golden-regression proof
that the gate flips PASS→FAIL on each condition all exist and are tested,
zero LLM spend. See `docs/tracing/usable-kitsoki-gate.md` for the narrative
doc this content has moved to. Tasks 3 (wire real S1/S4 inputs) and 4.2
(calibration-set run) are gated on S1 (workbench) and S4 (scenario foundry)
landing; Task 5 (stand it up as the CI release gate) is gated on Task 3.
This proposal stays open, trimmed to the remaining gated work, until S1/S4
land.
**Kind:**   tracing (tooling spillover — the deliverable is an arena job type; see Impact)
**Epic:**   usable-kitsoki.md

## Why

Nothing today answers "is kitsoki productive enough to release" with
evidence instead of a feeling. The loop needs babysitting — there is no
headless run-to-done gate (`.context/goal-seeker-elegance.md:38,62`, elegance
item 4, "no design" — R9 in the epic's gap table) — and every harness that
*could* prove productivity tests clean, hand-authored happy paths, not the
correction-heavy, meandering usage that actually breaks the UX (epic §4).
An evaluator's honest question — "does the free-form workbench (S1) actually
get real work done, without the silent bounces (S2) fixed, at a real
person's pace?" — has no standing, repeatable answer. This proposal is that
answer: a scored, evidence-backed pre-release gate, expressed as one more
arena job type so it reuses the placement/rollup machinery every other
comparison already runs on (`tools/arena/README.md:1-10`).

This is *also* the epic's own definition of done: S6 is where S1–S5 get
proven together, not just unit-by-unit.

## What changes

One sentence: **a new arena job-type plugin, `usable-kitsoki-gate`, drives
N mined scenarios (from `scenario-foundry.md`, S4) × persona × surface
(web / TUI / MCP) at swarm tier 1/2 concurrency, and reports PASS only if
zero scenarios hit a silent bounce, zero misroute to an adjacent command,
and the workbench's binary-completion rate against the scenario's own
source-session outcome (the *parity metric*) clears a threshold — with a
full evidence bundle attached to every scenario that doesn't pass.**

Concretely, this consumes two kinds of trace data that don't exist as a
single readable signal today: (a) the *source* trace — what the original
Claude Code / codex session actually completed, already captured by session
mining's `outcomes.py` `satisfaction` flag (epic §4, "did a follow-up turn
correct it") — and (b) the *candidate* trace — what a live kitsoki
workbench run (S1) does when driven through the identical mined scenario.
The gate's whole job is comparing those two honestly, and writing down why
whenever they disagree.

## Impact

- **Producers:** the workbench loop's turn-level trace events (S1, not yet
  built — this proposal specifies the *consumer* shape the parity scorer
  needs from them: a per-scenario-turn completion signal keyed to the
  scenario IR's expected-effects list; `scenario-foundry.md` (S4) is the
  IR's design home, absorbing `conversation-driven-development.md` slice 1);
  the scenario compiler's IR → fixture/recording
  output (S4, `scenario-foundry.md`); the swarm harness's existing
  per-run results JSON (`tools/swarm/results.ts`,
  `.artifacts/swarm/results-<run_id>.json`).
- **Consumers:** a new `tools/arena/arena/plugins/usable_kitsoki_gate.py`
  plugin (registered alongside `bugfix` / `persona-qa` / `swarm`,
  `tools/arena/README.md:250-252`); the arena rollup/CLI (`arena.py`'s
  `plan` · `run` · `plugins` — `tools/arena/README.md:62`); a standing CI
  workflow that runs the gate no-LLM (mined scenarios replayed, zero spend)
  on every PR and live (real workbench turns) on a release-candidate
  cadence.
- **Format:** one new artifact per cell — the **parity report** (below) —
  plus reuse of the existing `CellResult` schema
  (`tools/arena/arena/model.py:83-96`: `verdict`, `health`, `metrics`,
  `evidence_refs`, `trace_ref`).
- **Backward compat:** additive only. No existing trace event, cassette, or
  arena job type changes shape; this is a new plugin plus a new consumer of
  data S1/S4 already emit once they land.
- **Docs on ship:** `docs/tracing/usable-kitsoki-gate.md` (or fold into
  `docs/tracing/testing.md` if it stays small); `tools/arena/README.md`
  gains a "Status — usable-kitsoki-gate job type landed" section in the
  style of the swarm entry it's modeled on.

## Event / format model, gate conditions, determinism

Shipped and moved to the narrative doc — see
`docs/tracing/usable-kitsoki-gate.md` for the parity verdict record schema,
the three gate conditions (`GATE_CONDITIONS`), the parity threshold constant,
worst-surface gating, and the no-LLM/live determinism split. Kept here only
where it bears on the still-gated tasks below: the parity record's oracle is
the mined session's own `outcomes.py` satisfaction signal, never re-judged by
an LLM at gate time (Task 3.2 must preserve this — `candidate_completed`
comes from S1's trace facts, not a judge call), and `scenario_id := IR.id`
verbatim (Task 3.1's join key once S4 lands).

## Producers & consumers

- **S1 (workbench)** must emit, per scenario turn, a machine-readable
  completion signal keyed to the scenario IR's expected-effects list — this
  proposal is the reason that contract exists; S1's own tracing proposal
  (or its Impact section) should point back here for the consumer shape.
- **S2 (never-silent runtime)** is what makes `silent_bounce: false`
  achievable at all — this gate is the regression test for S2's invariant
  at scale, across mined real-world phrasing instead of a handful of
  hand-picked fixtures.
- **S4 (scenario foundry)** is the sole source of scenarios, personas, and
  the `source_completed` oracle. This gate has no scenario-authoring logic
  of its own — hand-authored scenarios are explicitly gap-filling only
  (epic shared decision 4), and mixing them into the gate's mined corpus
  would undermine the parity metric's honesty.
- **`tools/swarm/`** supplies the concurrency substrate (tier 1 scripted,
  tier 2 cassette-agent free text) the gate schedules scenarios onto; this
  proposal adds no new concurrency mechanism.
- **`tools/arena/`** supplies enumeration (cell = scenario × persona ×
  surface), placement (local or VM Docker host,
  `tools/arena/README.md`'s five-layer architecture), and rollup. The
  `usable-kitsoki-gate` plugin implements exactly the three-method
  `JobTypePlugin` protocol (`tools/arena/arena/plugins/base.py:19-31`):
  `image()` (the existing browser-capable arena image, same convention as
  the swarm plugin, `tools/arena/README.md:262-266`), `drive_command()`
  (dispatches into the swarm harness or a workbench-driving Playwright spec
  depending on surface), and `score()` (reads the parity verdict record off
  disk, never regexes stdout for a verdict — the swarm plugin's own
  discipline, `tools/arena/README.md:288-289`).

## Backward compatibility

Purely additive. No existing job type, `CellResult` field, trace event, or
cassette format changes. A repo with no mined scenarios yet (S4 not landed)
simply has zero cells to enumerate — `arena plan` for this job type returns
an empty cell list, not an error, so the plugin can be registered before S4
ships without breaking `arena plugins`.

## Fixtures / golden traces

The golden-regression-per-gate-condition fixture (Task 4.1) is shipped —
`tools/arena/tests/fixtures/usable-kitsoki-gate/` + `tools/arena/README.md`'s
status section. Still open: the **calibration set** — the "20 mined Kitsoki
scenarios in the IR, hand-checked" the epic names as S4's first deliverable
(epic §5) — is Task 4.2 and needs S4's compiler output to exist first. A
reviewer will regenerate it by rerunning S4's compiler against the same 20
source sessions and checking the resulting parity verdict records into
`tools/arena/tests/fixtures/usable-kitsoki-gate/` as the no-LLM contract
test.

## Tasks

```
## 1. Parity metric spec (day-one deliverable — S1 develops against this)
- [x] 1.1 Write the parity verdict record schema (this doc's Event/format
      model) as a versioned JSON Schema under
      `tools/arena/arena/plugins/usable_kitsoki_gate_schema.json`
      (`tools/arena/tests/test_usable_kitsoki_gate_schema.py` validates the
      example above + rejects malformed records)
- [x] 1.2 Define and document the three gate conditions + the parity
      threshold X (see Open questions #1) as constants, not prose, so S1
      and S6 read the same number
      (`tools/arena/arena/plugins/usable_kitsoki_gate_constants.py`;
      `PARITY_THRESHOLD_PERCENT = 90.0` is a placeholder pending the
      calibration run in Task 4.2)
- [x] 1.3 Specify the completion signal S1 must emit per scenario turn
      (the producer contract S1's proposal links back to)
      (`docs/tracing/usable-kitsoki-gate.md#producer-contract---what-s1-must-emit`,
      marked partial until S1's concrete trace-event shape lands)

## 2. Plugin skeleton (no scenarios required yet)
- [x] 2.1 Register `usable-kitsoki-gate` in `tools/arena/arena/plugins/`
      implementing `image()`/`drive_command()`/`score()`
- [x] 2.2 `arena plan`/`arena plugins` list it; empty-corpus case returns
      zero cells, not an error
- [x] 2.3 No-LLM tests (`tools/arena/tests/test_usable_kitsoki_gate_plugin.py`)
      proving registration + argv/env composition, mirroring
      `test_swarm_plugin.py`

## 3. Wire the real inputs (after S1 and S4 land)
- [ ] 3.1 Consume S4's scenario IR output as the cell corpus
- [ ] 3.2 Consume S1's workbench completion trace as `candidate_completed`
- [ ] 3.3 Wire swarm tier 1/2 concurrency for the no-LLM path; a gated live
      path for the release-candidate cadence (mirrors swarm tier 3's
      manual-only gating)

## 4. Prove the gate has teeth
- [x] 4.1 Golden regression scenarios for each of the three gate conditions
      (scripted bounce / scripted misroute / scripted parity miss) flip
      the rollup to `FAIL` — `tools/arena/tests/fixtures/usable-kitsoki-gate/`
      + `tools/arena/tests/test_usable_kitsoki_gate_golden_fixtures.py`
- [ ] 4.2 Calibration-set run (S4's 20 hand-checked scenarios) produces a
      checked-in, diffable parity report (needs S4's calibration set — skip
      until S4 lands)

## 5. Stand it up as the release gate
- [ ] 5.1 CI workflow: no-LLM path on every PR touching S1/S2/S4/S5 code
- [ ] 5.2 Release-candidate workflow: live path on a cadence, gating the
      actual release decision
- [ ] 5.3 Document in `docs/tracing/usable-kitsoki-gate.md` and
      `tools/arena/README.md`; trim/delete this proposal
```

## Open questions

1. **Parity threshold X** — a fixed percentage (e.g. 90%) vs. a per-persona
   floor (some personas' asks are inherently harder)? *Lean: start with one
   global floor calibrated against the 20-scenario calibration set; split
   per-persona only if the calibration run shows a persona systematically
   dragging the number down for reasons unrelated to workbench quality.*
2. **Surface parity or worst-surface gating** — does the release gate need
   web/TUI/MCP to each independently clear the threshold, or is an
   aggregate across surfaces enough? *Lean: gate on the worst surface — a
   product that's productive on web but silently bounces on MCP is not
   released-ready, and averaging would hide exactly that.*
3. **Cadence for the live path** — every merge to main, nightly, or only on
   an explicit release-candidate tag? *Lean: release-candidate tag only —
   the no-LLM path already gives fast per-PR signal on the two zero-tolerance
   conditions; the live parity number is expensive and only needs to be
   fresh at release time.*

## Non-goals

- **Scenario authoring or persona modeling** — entirely S4's scope; this
  gate only consumes the IR.
- **Fixing silent bounces or misrouting** — S2's and S1's scope; this gate
  only detects and reports regressions.
- **WB.4/WB.5 cost-benchmark execution** — a parallel, separate GU
  workstream; this gate's live path reuses arena's cost accounting but does
  not re-run the affordability study.
- **A scored rubric or LLM-judge verdict** — explicitly rejected per the
  epic's shared decision 6; the parity metric is binary completion plus an
  evidence bundle, full stop.
