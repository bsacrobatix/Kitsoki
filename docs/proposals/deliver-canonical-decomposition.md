# Story: deliver — the canonical decomposition story

**Status:** Draft v1. Decision made (2026-07-05, dev-workflows plan WS-B B1):
`stories/deliver` is the canonical decomposition story. **B2a shipped**
(schema + lint absorption, deliver-local, no graph change — see Task 1
below): `stories/deliver/schemas/decomposition.json` and
`scripts/lint_decomposition.star` now carry the work-decomposition skill's
richer optional fields (`coverage_note`, per-brief `title/kind/scope/
acceptance/risk`) and scope-bound/acceptance checks, and the skill's schema is
retargeted to be a manual copy of deliver's. **B2b shipped** (refine loop +
adversarial review — see Task 2 below): a lint failure or a `review` verdict
of `revise` now routes back to `decompose` with `refine_feedback` and a
shared `refine_cycle`/`refine_budget` counter instead of hard-exiting, and a
new `review` room (`host.agent.decide`, skeptic prompt, `{verdict, reason,
questions[]}`) gates `lint` → `fleet` on feasibility/completeness, not just
structure. **B2c shipped** (managed re-decompose — see Task 3 below):
`decompose` is now a router; `scripts/detect_prior_decomposition.star`
detects a prior `decomposition.yaml` and, when one exists, routes to a new
`redecompose` room (decomposer authors an additive delta) →
`redecompose_apply` (`host.run tools/decomposition-update/apply_delta.py
--list-key briefs --skip-validate` — the SAME decompose-update transaction,
invoked directly rather than importing that story) → `lint`, instead of
letting the decomposer overwrite the prior manifest. See
[`stories/deliver/README.md`](../../stories/deliver/README.md) for the
current graph and
[`tools/decomposition-update/README.md`](../../tools/decomposition-update/README.md)
for the transaction's CLI. **B3/B4 are not yet implemented** — no dev-story
entry yet. Supersedes [`work-decomposition.md`](work-decomposition.md).
**Kind:**   story
**Epic:**   — standalone (slice B1 of the dev-workflows surface-matrix plan)

## Why

Decomposition → implementation is the least-consolidated core workflow on
every surface. An operator who wants to turn an accepted proposal or epic
into implemented branches today faces **five fragments with no canonical
path between them**:

- **`.agents/skills/work-decomposition/`** — a *manual*, Claude-driven
  workflow with the richest manifest schema
  (`schemas/decomposition.json`: `coverage_note` + per-brief
  `title/kind/goal/scope/acceptance/test_plan/agent_brief/risk`), a
  deterministic structural validator
  (`scripts/validate_decomposition.py` + a Starlark twin), and an
  adversarial feasibility/completeness review discipline — none of it
  reachable as a story.
- **`stories/deliver/`** — a real story (5 no-LLM flows) with a *simpler*
  schema (`id/brief/gate_command/deps`), a deterministic Starlark lint
  (`scripts/lint_decomposition.star`), and a straight-through shape: lint
  failure exits `@exit:needs-human` — there is **no refine loop and no
  review gate**.
- **`stories/decompose-update/`** (1 flow) — an adversarial-review +
  deterministic-transaction wrapper around
  `tools/decomposition-update/apply_delta.py` for *changing* an existing
  decomposition, unconnected to `deliver`.
- **`stories/fleet/`** (1 flow) — fans `ship-it` over a brief list behind a
  merge lock; only reachable via `deliver`.
- **`stories/dev-story/`** — its `design_done` room offers only
  `go_implementation` (direct → the `impl` import,
  `stories/dev-story/rooms/design_draft.yaml:348`); there is **no route to
  `deliver` at all** — the decomposition chain is unreachable from the hub
  that publishes the proposals it should consume.

The old [`work-decomposition.md`](work-decomposition.md) proposal designed a
sixth fragment (`stories/decompose/`, never built). The decision (dev-workflows
plan, WS-B B1 — resolved, no further sign-off needed): **stop designing a new
story; make `deliver` absorb the skill's discipline** and wire the chain into
dev-story.

## What changes

One sentence: **`stories/deliver` grows the work-decomposition skill's
manifest richness, refine loops, and adversarial feasibility/completeness
review as rooms/gates; `decompose-update` stays the managed-delta
transaction `deliver` wraps for re-decomposition; and
`deliver → fleet → ship-it` becomes the standard decomposition chain,
reachable from dev-story beside the direct `impl` path.**

Concretely, in dependency order:

1. **Schema absorption.** `stories/deliver/schemas/decomposition.json`
   gains the skill schema's fields — `coverage_note` at the top level;
   optional per-brief `title`, `kind`, `scope[]`, `acceptance[]`, `risk` —
   while keeping `id/brief/gate_command/deps` required (fleet's contract).
   The lint script already reads the aliases (`agent_brief`→`brief`,
   `test_plan`→`gate_command`, `depends_on`→`deps` —
   `scripts/lint_decomposition.star:84-91`), so the two schemas converge
   instead of coexisting. The skill's schema is then retargeted to stay
   identical to deliver's (the skill's own Maintenance section already
   demands this — it just points at the never-built `stories/decompose/`).
2. **Lint absorption.** `lint_decomposition.star` grows the skill
   validator's remaining checks: scope paths bounded inside the repo
   (parent dir exists), non-empty `acceptance` when present. It stays
   deterministic, Starlark, exit-shaped `{route, error}`.
3. **Refine loop.** Lint failure routes back to `decompose` with the error
   as feedback and a cycle budget (today it hard-exits
   `@exit:needs-human`), following the budgeted-refine pattern of
   `stories/bugfix/rooms/proposing.yaml`. Budget exhaustion keeps the
   honest `needs-human` exit.
4. **Adversarial review room.** A new `review` room between `lint` and
   `fleet`: `host.agent.decide` with a skeptic prompt (per brief: buildable
   as scoped, deps right; across briefs: attack the `coverage_note`, no
   file double-ownership) → `{verdict: accept|revise, reason, questions[]}`.
   `revise` re-enters `decompose` with the questions as feedback, budgeted.
   The room shape is lifted from
   `stories/decompose-update/rooms/review.yaml` (same
   decide-schema-bind-gate pattern).
5. **Re-decompose as a managed delta.** When a decomposition already exists
   (a prior `decomposition.yaml` for the same epic), `deliver` routes the
   change through the `decompose-update` transaction
   (`tools/decomposition-update/apply_delta.py`: versioned prior graph,
   validated deltas, `plan-evolution.jsonl` events) instead of overwriting
   — `decompose-update` is the transaction `deliver` wraps, not a parallel
   story.
6. **Dev-story integration (plan slice B3).** `dev-story` imports `deliver`
   and `design_done` / `landing` offer **decompose-vs-direct**: small
   ticket → `go_implementation` (the existing `impl` import), epic-sized →
   `go_deliver` (new arc into the `deliver` import), so
   `landing → design_draft → decompose → briefs → fleet → PRs` is one
   continuous walk.
7. **The skill is demoted to the manual twin.** It stays (useful for
   by-hand runs and as the schema's second consumer) but its framing flips:
   the story is canonical, the skill mirrors it.

## Impact

Story-layer composition only — every mechanism exists.

- **Changed:** `stories/deliver/` (schema, lint script, `configure` /
  `decompose` / `lint` rooms, new `review` room + prompt + decision schema,
  new flows), `stories/dev-story/app.yaml` (+ `deliver` import, routing
  intents), `stories/dev-story/rooms/design_draft.yaml` + `landing.yaml`
  (decompose-vs-direct arcs), `.agents/skills/work-decomposition/SKILL.md`
  (retarget to deliver, reconcile schema).
- **Net-new files:** `stories/deliver/rooms/review.yaml`,
  `prompts/review_adversary.md`, `schemas/review-decision.json`, ~5 new
  flow fixtures, one dev-story end-to-end flow.
- **Engine/host changes:** none.
- **Docs on ship:** `docs/stories/deliver.md` (narrative home),
  `stories/deliver/README.md` + `stories/dev-story/README.md` updates,
  delete [`work-decomposition.md`](work-decomposition.md) and then this
  file per lifecycle.

## Reuse inventory

| Pipeline step | Mechanism | Reference |
|---|---|---|
| Decompose epic → manifest | `host.agent.task` + acceptance schema (shipped) | `stories/deliver/rooms/decompose.yaml` |
| Richer brief schema | skill schema fields folded into deliver's | `.agents/skills/work-decomposition/schemas/decomposition.json` |
| Deterministic structural lint | `host.starlark.run`, `{route, error}` (shipped; extend) | `stories/deliver/scripts/lint_decomposition.star` |
| Budgeted refine loop | feedback + cycle/budget world keys | `stories/bugfix/rooms/proposing.yaml` |
| Adversarial review gate | `host.agent.decide` + decision schema + `accept` guard | `stories/decompose-update/rooms/review.yaml` |
| Managed re-decompose delta | versioned transaction tool behind a review gate | `stories/decompose-update/` + `tools/decomposition-update/apply_delta.py` |
| Per-brief execution | fleet fans `ship-it` (maker → integrate → re-verify) behind a merge lock | `stories/fleet/app.yaml`, `stories/ship-it/app.yaml` |
| Hub routing by ticket size | `drive` router / `go_*` intents on `design_done` and `landing` | `stories/dev-story/app.yaml:1190`, `rooms/design_draft.yaml:303` |
| Direct (no-decompose) path | `impl` import, self-provisioning worktree | `stories/dev-story/flows/design_to_implementation.yaml` |

## Story graph (target)

```
configure ── start ──▶ decompose ──▶ lint ──┬─ ok ────▶ review ──┬─ accept ─▶ fleet (import)
                          ▲                 │                    │              └─ @exit:done {delivery_summary}
                          │                 └─ fail ─▶ decompose │
                          │                    (budgeted refine) │
                          └──────── revise (questions[], budgeted) ┘
   any budget exhausted / host error ──▶ @exit:needs-human {last_error}
   prior decomposition exists ──▶ managed delta via decompose-update transaction
```

Today's graph is the same minus `review`, minus both refine edges (lint
fail exits immediately), and minus the managed-delta route.

## World schema (delta sketch)

```yaml
world:
  # existing keys unchanged: epic_path, decomposition_path, base_branch,
  # main_worktree_path, delivery_summary, last_error, decomposition_briefs,
  # lint_route
  coverage_note:    { type: string, default: "" }   # completeness claim the reviewer attacks
  review_verdict:   { type: string, default: "" }   # accept | revise
  review_reason:    { type: string, default: "" }
  review_questions: { type: list,   default: [] }
  refine_feedback:  { type: string, default: "" }
  refine_cycle:     { type: int,    default: 0 }
  refine_budget:    { type: int,    default: 3 }
```

`exits:` unchanged — `done: { requires: [delivery_summary] }`,
`needs-human: { requires: [last_error] }`.

## Proof per surface (plan slice B4)

The chain counts as supported per surface only with a no-LLM proof (the
dev-workflows plan's "testing infra is in the SUT" rule):

- **Engine/TUI:** the deliver flow suite below + a dev-story
  `design_to_decompose_to_impl.yaml` end-to-end flow (publish → decompose →
  review → fleet fan-out over a 2-brief fixture → back to main), sibling to
  `flows/design_to_implementation.yaml`.
- **Web:** a Playwright walk of the same fixture via `kitsoki web --flow`
  (the `slidey_decomposition.yaml` tour-shaping precedent: seed `epic_path`
  in `initial_world` so `start` needs no slot).
- **VS Code:** an extension e2e spec of the same walk using the extension's
  `flow`/`hostCassette` settings — first-class, not a "rides the web proof"
  footnote (plan decision #3).
- **gh-agent:** deferred to WS-E (epic-labeled issue → deliver) as a
  stretch; out of scope here.

## Flow fixtures

Existing five stay green (`decompose_happy`, `decompose_error`,
`lint_rejects_cycle` — reshaped to prove the refine loop instead of the
hard exit, `lint_rejects_missing_dep`, `slidey_decomposition`). New:

- `lint_fail_refine_loop` — lint fail → decompose re-arms with feedback →
  second manifest passes.
- `review_revise_loop` — adversary returns `revise` → decompose → second
  pass `accept`.
- `refine_budget_exhausted` — revise at budget → `@exit:needs-human` with a
  specific `last_error`.
- `rich_schema_happy` — a manifest carrying `coverage_note` +
  `scope/acceptance/risk` lints clean and reaches fleet.
- `redecompose_managed_delta` — a prior `decomposition.yaml` exists → the
  delta routes through the decompose-update transaction (stubbed
  `host.run`), never a blind overwrite.
- dev-story: `design_to_decompose_to_impl` (above) + a router fixture
  proving decompose-vs-direct picks by ticket size.

## Tasks

Sequenced as implementable slices (B2 → B3 → B4 of the plan's WS-B):

```
## 1. B2a — schema + lint absorption (deliver-local, no graph change)
- [x] 1.1 Fold skill schema fields into stories/deliver/schemas/decomposition.json (coverage_note; optional title/kind/scope/acceptance/risk)
- [x] 1.2 Extend lint_decomposition.star (scope-path bounds, acceptance non-empty when present); keep {route,error}
- [x] 1.3 Reconcile .agents/skills/work-decomposition schema + SKILL.md to point at deliver as canonical
- [x] 1.4 rich_schema_happy flow; existing 5 flows stay green

## 2. B2b — refine loop + adversarial review
- [x] 2.1 lint fail → decompose refine edge with refine_feedback + budget; budget exhausted → @exit:needs-human
- [x] 2.2 review room (host.agent.decide, review_adversary prompt, review-decision schema) between lint and fleet
- [x] 2.3 Flows: lint_fail_refine_loop, review_revise_loop, refine_budget_exhausted; reshape lint_rejects_cycle
- [x] 2.4 stories/deliver/README.md updated to the new graph

## 3. B2c — managed re-decompose
- [x] 3.1 Prior-decomposition detection in configure/decompose; route deltas through the decompose-update transaction
- [x] 3.2 redecompose_managed_delta flow; decompose-update README cross-link

## 4. B3 — dev-story integration
- [ ] 4.1 deliver import in stories/dev-story/app.yaml (world_in: epic_path from the published proposal; exits → main)
- [ ] 4.2 go_deliver arcs on design_done + landing; drive router offers decompose-vs-direct by ticket size
- [ ] 4.3 design_to_decompose_to_impl.yaml end-to-end flow + router fixture

## 5. B4 — proof per surface + docs
- [ ] 5.1 Web Playwright walk of the dev-story fixture (kitsoki web --flow)
- [ ] 5.2 VS Code extension e2e spec of the same walk (flow/hostCassette settings)
- [ ] 5.3 Migrate to docs/stories/deliver.md; delete work-decomposition.md and this proposal
```

## Open questions

1. **Ship-it vs. implementation for per-brief execution.** Fleet fans
   `ship-it` (brief + gate_command, cherny-loop maker); the old proposal
   wanted per-brief dispatch into ticket-driven `implementation`. *Lean:
   keep ship-it — it is shipped, lost-work-safe, and gate-verified; the
   `impl` pipeline remains the direct path for un-decomposed tickets. If a
   brief needs the heavier checkpointed pipeline, materialise it as a
   ticket and drive it from dev-story — don't build a second dispatcher.*
2. **Interactive discovery room.** The old design had a conversational
   scope-sharpening room before decompose. *Lean: defer — the decomposer
   prompt already front-loads constraints; add discovery only if live use
   shows manifests failing review for scope reasons. Keeps B2 small.*
3. **Where does decompose-vs-direct threshold live?** Operator choice on
   `design_done` vs. an automatic size heuristic. *Lean: operator choice
   (two labelled arcs) for v1; the router can grow a heuristic later.*

## Non-goals

- **Parallel maker fan-out** — fleet stays sequential under the merge lock
  (fleet OQ #1); unchanged here.
- **A new `stories/decompose/`** — explicitly rejected; that design is
  retired with `work-decomposition.md`.
- **gh-agent epic dispatch** — WS-E's slice, not this one.
- **Authoring the proposal being decomposed** — the design pipeline /
  `proposal-authoring` skill own that.
