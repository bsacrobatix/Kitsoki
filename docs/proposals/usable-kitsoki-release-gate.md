# Tracing: usable-kitsoki release gate — the realism bar

**Status:** Draft v1. Nothing implemented yet.
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

## Event / format model

The gate does not invent new trace event *types* — it reads the workbench's
existing turn-completion trace (S1's contract) and the mined scenario's
recorded outcome (S4's IR), and emits one **parity verdict record** per
scenario per surface as the job type's `CellResult.metrics` + a sibling
JSON evidence file:

```jsonc
{
  "scenario_id": "sess-3f2a1c-turn-12",
  "persona": "impatient-debugger",
  "surface": "web",
  "source_completed": true,        // did the ORIGINAL session complete this ask
  "candidate_completed": false,    // did the workbench complete it, this run
  "silent_bounce": false,          // on_error arc fired with no rendered explanation
  "misroute_adjacent": true,       // routed to a command adjacent to the ask, not the ask
  "evidence_refs": [
    ".artifacts/usable-kitsoki-gate/<run_id>/<scenario_id>/trace.jsonl",
    ".artifacts/usable-kitsoki-gate/<run_id>/<scenario_id>/rrweb.json"
  ],
  "notes": "workbench asked a clarifying question the source session never needed"
}
```

| Field | When emitted | Key fields |
|---|---|---|
| parity verdict record | once per scenario × persona × surface, at the end of a `usable-kitsoki-gate` cell | `source_completed`, `candidate_completed`, `silent_bounce`, `misroute_adjacent`, `evidence_refs` |
| rollup gate verdict | once per cell after all scenarios in the cell finish | aggregate `PASS`/`FAIL`, the three gate conditions below, parity percentage |

**The three gate conditions** (all three must hold for the rollup to pass;
each maps directly to an epic release-blocker):

1. **Zero `silent_bounce: true` records** (kills R2 regressions).
2. **Zero `misroute_adjacent: true` records** (kills R3 regressions).
3. **Parity ≥ X%**: `count(candidate_completed AND source_completed) /
   count(source_completed)` across the cell's scenarios — binary
   completion, not a scored rubric, per the epic's shared decision 6. `X`
   is the open threshold below.

## Determinism

- **No-LLM path (standing CI):** every mined scenario replays through the
  existing zero-spend harnesses — flow fixtures / cassettes (S4's
  fixture-and-recording compiler output) and swarm tier 1/2's scripted or
  cassette-agent users (`tools/swarm/README.md`'s tier table) — so the gate
  runs on every PR without LLM cost, the same way `swarm-replay-users.spec.ts`
  and `swarm-cassette-users.spec.ts` are zero-spend today.
- **Live path (release-candidate cadence):** a real workbench (S1) drives
  the same mined scenarios; this is the only place the gate spends, and it
  runs on a cadence, not per-PR, mirroring how the swarm job type reserves
  its tier-3 live explorers for manual/gated runs
  (`tools/swarm/README.md`, tier 3 row: "manual only, gated behind
  `--live-explorers`").
- **Scenario identity is stable:** each scenario's `scenario_id` is derived
  deterministically from its source session + turn offset (S4's IR
  contract), so a rerun's parity record is diffable turn-for-turn against a
  prior run — a parity regression on scenario `sess-3f2a1c-turn-12` means
  the same real ask regressed, not a different draw from a random sample.
- **The oracle is the source session, not a judge model.** `source_completed`
  is read off the mined session's own `outcomes.py` satisfaction signal
  (epic §4), never re-adjudicated by an LLM at gate time — the same
  "hidden oracle, not judge drift" discipline the bugfix arena plugin uses
  (`tools/arena/arena/plugins/base.py`'s `score()` contract: grade from
  what actually ran, not from asking a model whether it liked the result).

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

- A small, hand-checked **calibration set** — the "20 mined Kitsoki
  scenarios in the IR, hand-checked" the epic names as S4's first
  deliverable (epic §5, "First concrete steps") — is this gate's first
  fixture: enough to prove the plugin's `image()`/`drive_command()`/
  `score()` wiring and the parity-record shape end to end, before the
  corpus is large.
- One **golden regression scenario per gate condition** (a scripted silent
  bounce, a scripted misroute, a scripted parity miss) proves the rollup
  correctly flips `PASS` → `FAIL` on each condition independently — this is
  the offline, no-LLM proof that the gate has teeth, the same role the RED
  fixture plays for the WS conformance lint (epic §1.1,
  `internal/agenteval/conformance/`).
- A reviewer regenerates the calibration set by rerunning S4's compiler
  against the same 20 source sessions; the parity verdict records for those
  20 are checked into `tools/arena/tests/fixtures/usable-kitsoki-gate/` as
  the no-LLM contract test.

## Tasks

```
## 1. Parity metric spec (day-one deliverable — S1 develops against this)
- [ ] 1.1 Write the parity verdict record schema (this doc's Event/format
      model) as a versioned JSON Schema under
      `tools/arena/arena/plugins/usable_kitsoki_gate_schema.json`
- [ ] 1.2 Define and document the three gate conditions + the parity
      threshold X (see Open questions #1) as constants, not prose, so S1
      and S6 read the same number
- [ ] 1.3 Specify the completion signal S1 must emit per scenario turn
      (the producer contract S1's proposal links back to)

## 2. Plugin skeleton (no scenarios required yet)
- [ ] 2.1 Register `usable-kitsoki-gate` in `tools/arena/arena/plugins/`
      implementing `image()`/`drive_command()`/`score()`
- [ ] 2.2 `arena plan`/`arena plugins` list it; empty-corpus case returns
      zero cells, not an error
- [ ] 2.3 No-LLM tests (`tools/arena/tests/test_usable_kitsoki_gate_plugin.py`)
      proving registration + argv/env composition, mirroring
      `test_swarm_plugin.py`

## 3. Wire the real inputs (after S1 and S4 land)
- [ ] 3.1 Consume S4's scenario IR output as the cell corpus
- [ ] 3.2 Consume S1's workbench completion trace as `candidate_completed`
- [ ] 3.3 Wire swarm tier 1/2 concurrency for the no-LLM path; a gated live
      path for the release-candidate cadence (mirrors swarm tier 3's
      manual-only gating)

## 4. Prove the gate has teeth
- [ ] 4.1 Golden regression scenarios for each of the three gate conditions
      (scripted bounce / scripted misroute / scripted parity miss) flip
      the rollup to `FAIL`
- [ ] 4.2 Calibration-set run (S4's 20 hand-checked scenarios) produces a
      checked-in, diffable parity report

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
