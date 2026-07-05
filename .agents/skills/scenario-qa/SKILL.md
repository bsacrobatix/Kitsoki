---
name: scenario-qa
description: Check a single product-journey scenario (a catalog scenario id, or a free-text description of a flow to check) across one or more transports — TUI, web, and/or VS Code — with an independent per-transport judge verdict and a report.md verdict table. Use when the user says "check this scenario in the TUI", "check this on web", "check this on all transports", "does the onboarding tour render correctly in the TUI and on web", or wants a per-transport pass/fail/degraded-evidence table for one scenario. Distinct from product-journey-qa (the full persona x scenario x GitHub-target matrix pipeline) — scenario-qa is the narrow, fast, single-scenario x transport-axis check; it reuses product-journey-qa's runner, driver, and evidence contracts rather than reimplementing them.
---

# Scenario QA

Use this skill when the task is "check ONE scenario across ONE OR MORE
transports" — not a full persona/matrix sweep (that's
[[product-journey-qa]]). The durable surfaces are:

- Runner: `tools/product-journey/run.py` (owns scenario/persona/transport
  resolution — the `transports` contract on `scenarios.json`, `--transport`)
- Story wrapper: `stories/scenario-qa/app.yaml`
- Driver agent: `.agents/agents/product-journey-qa-driver.md` (transport-pinned
  per leg — the handoff's `transport:` field OVERRIDES the driver's usual
  cheapest-surface heuristic)
- Judge: a read-only agent following the kitsoki-ui-qa posture — grounded
  verdicts only, cites concrete frames, never a bare pass
- Report: `<run-dir>/report.md` (folded in Starlark, `scripts/build_report.star`)
  and `<run-dir>/deck.slidey.json` (folded by `run.py --scenario-qa-report`) —
  the ONE per-transport verdict table, in both forms, for every transport the
  check ran (single transport or `transport=all`); VS Code legs are labeled
  `bridge-level` in both, never mistaken for editor-level coverage

## What "transport" means here

Each transport leg has its own evidence contract (documented in
`tools/product-journey/schema.json`'s `scenario.transports` section and
mirrored in `stories/scenario-qa/scripts/plan_legs.star`):

| Transport | Primary tool | Evidence kind | Level |
|---|---|---|---|
| `tui` | `render.tui_png` | `rendered_tui_frame` | frame-level |
| `web` | `visual.snapshot` | `browser_screenshot` | frame-level |
| `vscode` | `visual.open (kind=vscode)` | `screenshot_or_tui_png` | **bridge-level** — the IDE bridge stub/recording path, never a genuine editor; never mistake this for editor-level coverage |

`transport=all` expands to every transport the scenario allows (`tui|web|
vscode`); a scenario without a declared `transports` contract falls back to
whatever `driver_visual_surface()` derives from its `required_mcp` (S1's
backward-compatible default).

## Operating rules

- One leg = one driver dispatch (transport-pinned) + one independent judge
  verdict. The judge never trusts the driver's own report — see
  `stories/scenario-qa/rooms/judge.yaml`'s header comment.
- A JSON-degraded visual surface is a `degraded-evidence` verdict, never a
  fabricated pass. The driver preflights the transport's visual tools before
  capturing (see `.agents/agents/product-journey-qa-driver.md`'s preflight
  step) and the judge independently reflects a blocked/degraded driver report
  as `degraded-evidence` too.
- Automated tests must not call a real LLM. `stories/scenario-qa/flows/*.yaml`
  stub every host call by id; live/model work only happens through a real
  operator session or an explicit live drive.
- A scenario can be a **catalog id** (`tools/product-journey/scenarios.json`)
  or a **free-text description** (an ad-hoc scenario). Ad-hoc mode still
  reuses `run.py`'s real project/persona resolution (against a generic
  carrier scenario) — it only drafts the scenario body (id/task/success
  criteria), never reinvents transport/evidence resolution.

## No-LLM gate

```sh
go run ./cmd/kitsoki validate stories/scenario-qa/app.yaml
go run ./cmd/kitsoki test flows stories/scenario-qa/app.yaml
```

## Story surface

Open `stories/scenario-qa/app.yaml`. Intents:

- `check scenario=<catalog-id> transport=tui|web|vscode|all persona=... target=... seed=...`
- `check description="<free text>" transport=...` — ad-hoc scenario
- `next_leg` — drive the next transport leg (one leg per turn; the loop
  pauses at `recording` between legs so the internal emit chain never
  approaches the engine's `EmitIntentMaxDepth` regardless of how many
  transports were requested)
- `report` — (re)build `report.md` from the current run's per-leg outcomes
- `status` / `look` — re-render current progress

Resolve a natural-language request into this intent before driving: "check
this scenario in the TUI" → `transport=tui`; "on web" → `transport=web`; "on
all transports" → `transport=all`. If the user names a specific scenario id
from `tools/product-journey/scenarios.json`, pass it as `scenario=`;
otherwise pass their description as `description=`.

## Driving it live

1. `session.open` (or `session.new`) on `stories/scenario-qa/app.yaml`.
2. Submit `check` with the resolved slots.
3. The room pipeline (`plan → execute → judge → recording → report`) drives
   the run bundle, dispatches the driver per leg (transport-pinned), judges
   each leg independently, and pauses at `recording` between legs.
4. Submit `next_leg` once per remaining transport leg.
5. Read `report.md` (`world.report_path`) and/or `deck.slidey.json`
   (`world.deck_path`) in the run dir for the final per-transport verdict
   table.

Point the caller at the run dir (`world.run_dir`), `report.md`
(`world.report_path`), and `deck.slidey.json` (`world.deck_path`) as the
durable evidence — not prose recollection of the session.

## Relationship to product-journey-qa

`scenario-qa` is a thin, narrow sibling of `stories/product-journey-qa`:

- It reuses `tools/product-journey/run.py`'s `--emit-run --transport` axis and
  `.agents/agents/product-journey-qa-driver.md` rather than re-deriving
  project/persona/evidence resolution.
- It does NOT do persona lens synthesis, GitHub-matrix fan-out, finding
  filing, or gh-agent autonomous fixes — those stay in `product-journey-qa`.
  If the task grows into "check this across many personas/repos", switch to
  `product-journey-qa` instead.
- Reach for `product-journey-qa`'s driver agent directly (not this skill) when
  driving a full run bundle rather than one scenario/transport check.

## Improvement loop

When refining this pipeline:

1. Identify the missing proof from a failed flow, a bounced room, or a
   review of `report.md`.
2. Patch the smallest durable surface: `tools/product-journey/run.py` (the
   transport/scenario contract — owned by the product-journey-qa slice, not
   this story), `stories/scenario-qa/rooms/*.yaml`, `scripts/*.star`, the
   driver agent, or this skill.
3. Add or update a flow fixture under `stories/scenario-qa/flows/` — every
   host call stubbed by id, no real LLM.
4. Re-run `kitsoki validate` and `kitsoki test flows` for
   `stories/scenario-qa/app.yaml`.
5. Commit only the scenario-qa slice.
