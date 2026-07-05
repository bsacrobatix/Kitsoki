# deliver — epic front-door of the delivery loop

Hand `deliver` a path to an epic/proposal markdown and it decomposes it into
independently-shippable briefs, lints the manifest deterministically, runs it
past an adversarial reviewer, and fans [`stories/fleet/`](../fleet/) (which
runs [`stories/ship-it/`](../ship-it/) per brief behind a merge lock) over the
result.

Layering is acyclic: **deliver → fleet → ship-it**. deliver is the only entry
above fleet; fleet never imports deliver.

> **Target shape:** deliver is the decided canonical decomposition story. It
> has absorbed the work-decomposition skill's richer manifest schema (B2a) and
> the budgeted refine loop + adversarial review gate (B2b, below); managed
> re-decompose and dev-story reachability are still to come. See
> [`docs/proposals/deliver-canonical-decomposition.md`](../../docs/proposals/deliver-canonical-decomposition.md).
> This README documents what ships **today**.

## Pipeline

```
configure {epic_path}
  └─ start ─▶ decompose (host.agent.task: read epic, write decomposition YAML)
               ├─ ok ──▶ lint (host.starlark.run — deterministic, no LLM)
               │          ├─ pass ─▶ review (host.agent.decide — adversarial)
               │          │           ├─ accept ─▶ fleet import (entry: load)
               │          │           │             — fan ship-it over every brief
               │          │           │             └─ @exit:done {delivery_summary ← fleet_summary}
               │          │           └─ revise ─▶ decompose (budgeted refine)
               │          └─ fail ─▶ decompose (budgeted refine)
               └─ host error ─▶ decompose_error ─▶ @exit:needs-human {last_error}

any refine-budget exhaustion (lint-fail or review-revise) ─▶ @exit:needs-human {last_error}
```

Every failure arc exits `@exit:needs-human` with a non-blank `last_error`. The
lint-fail and review-revise refine edges share ONE `refine_cycle` /
`refine_budget` counter — one budget for the whole decompose↔lint↔review ring
(default budget: 3 cycles).

## Rooms

| Room | Does |
|---|---|
| `configure` (root) | Captures `epic_path` (`start epic_path=<path>`, or seeded via `initial_world`); defaults `decomposition_path` to `.artifacts/deliver/decomposition.yaml`. |
| `decompose` | `host.agent.task` invokes the `decomposer` agent (`once: true` — clear `decomposition_briefs` to force a redo). The agent reads the epic, **writes the decomposition YAML to `decomposition_path`** (the format fleet's `load` parses), and submits a manifest validated against [`schemas/decomposition.json`](schemas/decomposition.json); `bind: decomposition_briefs, coverage_note`. On a re-entry (lint fail or review revise), `refine_feedback` is passed into the decomposer's prompt args so it addresses the specific gap instead of resubmitting blind. |
| `decompose_error` | Host-call failure surface; auto-routes to `@exit:needs-human` (the engine sets `last_error`). |
| `lint` | `host.starlark.run` [`scripts/lint_decomposition.star`](scripts/lint_decomposition.star) over `decomposition_path` → `{route: ok\|fail, error}`; resets `lint_route` before the invoke so stale values never route (the board.yaml pattern). Fail routes to `decompose` with `refine_feedback` set to the lint error and `refine_cycle` incremented — unless the budget is already spent, which routes straight to `@exit:needs-human`. |
| `review` | `host.agent.decide` invokes the `reviewer` agent (prompt [`prompts/review_adversary.md`](prompts/review_adversary.md), schema [`schemas/review-decision.json`](schemas/review-decision.json)) — attacks per-brief buildability and whether `coverage_note` actually covers the epic. No `once:` guard: re-runs fresh on every entry, including after a refine cycle. `accept` → `fleet`; `revise` → `decompose` with the reviewer's `questions` folded into `refine_feedback` (budgeted, same counter as lint). |
| `review_error` | Reviewer host-call failure surface; auto-routes to `@exit:needs-human`, mirroring `decompose_error`. |

## The manifest contract

Top-level, optional: `coverage_note` — the completeness claim (how the briefs
together fully cover the epic/proposal) that `review` attacks.

`briefs:` — an ordered list; each brief:

- `id` — unique, `^[a-z][a-z0-9-]*$` (**required**)
- `brief` — self-contained task for the maker agent, min 10 chars (**required**)
- `gate_command` — deterministic shell command, exit 0 = success, and
  **RED at baseline** (see [`prompts/decompose.md`](prompts/decompose.md))
  (**required**)
- `deps` — ids that must ship first, acyclic (optional, default `[]`)
- `title`, `kind` (`story\|runtime\|tui\|tracing\|test\|docs`), `scope[]`
  (write-boundary globs), `acceptance[]`, `risk` (`low\|medium\|high`) —
  optional richer fields absorbed from the work-decomposition skill's schema
  (proposal: deliver-canonical-decomposition B2a).

The lint (no LLM, [`scripts/lint_decomposition.star`](scripts/lint_decomposition.star))
enforces: at least one brief; ids unique and non-empty; non-empty `brief` and
`gate_command` per brief; every dep references a known id; no dependency
cycle; when present, `acceptance` is non-empty and every `scope` glob is
bounded inside the repo (repo-relative, no `..` escape, parent dir exists). It
also accepts the work-decomposition skill's field aliases (`agent_brief` →
`brief`, `test_plan` → `gate_command`, `depends_on` → `deps`). What the lint
CANNOT check — whether a brief is actually buildable as scoped, and whether
`coverage_note` overclaims — is the `review` room's job.

## World / agents / exits

Key world: `epic_path`, `decomposition_path`, `decomposition_briefs`,
`coverage_note`, `lint_route`, `review_verdict`, `review_reason`,
`review_questions`, `refine_feedback`, `refine_cycle`, `refine_budget`,
`last_error`, `delivery_summary`, plus `base_branch` / `main_worktree_path`
projected into fleet.

Two agents:

- `decomposer` (`claude-opus-4-8`, tools `Read/Edit/Write`, no
  Bash/subagents; gates are designed from static evidence and executed
  downstream).
- `reviewer` (`claude-sonnet-4-6`, tools `Read`; skeptic — attacks
  buildability and completeness, no shell/subagents).

Exits: `done` (requires `delivery_summary`), `needs-human` (requires
`last_error`).

## Flows (no-LLM)

```
go run ./cmd/kitsoki test flows stories/deliver/app.yaml
```

| Flow | Proves |
|---|---|
| [`decompose_happy`](flows/decompose_happy.yaml) | 3-brief manifest → real Starlark lint passes → adversarial review accepts → lands in `fleet.load` (fleet's own flows cover fan-out). |
| [`decompose_error`](flows/decompose_error.yaml) | Decomposer host error → `decompose_error` → `@exit:needs-human` with `last_error`. |
| [`lint_rejects_cycle`](flows/lint_rejects_cycle.yaml) | Dependency cycle → budgeted refine loop back to `decompose` (NOT a hard exit) → the refined manifest breaks the cycle → lint passes → review accepts → `fleet.load`. |
| [`lint_rejects_missing_dep`](flows/lint_rejects_missing_dep.yaml) | Dangling dep id with the refine budget ALREADY spent (`refine_budget: 0` seeded) → immediate `@exit:needs-human` with the wrapped budget-exhausted error. |
| [`lint_fail_refine_loop`](flows/lint_fail_refine_loop.yaml) | The generic refine-loop mechanism: lint fail → `decompose` re-arms with `refine_feedback` (the lint error) and `refine_cycle` incremented → second manifest passes → review accepts → `fleet.load`. |
| [`review_revise_loop`](flows/review_revise_loop.yaml) | Lint passing is necessary but not sufficient: adversarial review returns `revise` → `decompose` re-arms with the reviewer's `questions` as `refine_feedback` → second pass → review `accept`s → `fleet.load`. |
| [`refine_budget_exhausted`](flows/refine_budget_exhausted.yaml) | The reviewer keeps revising past the shared `refine_cycle`/`refine_budget` counter → honest `@exit:needs-human` with a specific `last_error` naming the last review reason, instead of looping forever. |
| [`rich_schema_happy`](flows/rich_schema_happy.yaml) | A manifest carrying `coverage_note` + per-brief `title/kind/scope/acceptance/risk` lints clean and reaches `fleet.load` — proves the absorbed skill schema/lint fields, not just the bare `id/brief/gate_command/deps` contract. |
| [`slidey_decomposition`](flows/slidey_decomposition.yaml) | Tour-shaped happy path (`epic_path` seeded in `initial_world` so `start` needs no slot) for the web/no-LLM demo. |

Both agents are mocked in every flow (`host_handlers.host.agent.task` /
`.host.agent.decide` `by_call`, or a `host_cassette:` when a flow needs
DIFFERENT canned responses across repeated entries into the same room — e.g.
`review_revise_loop`'s revise-then-accept, or `refine_budget_exhausted`'s
always-revise). Lint and fleet-load run **real Starlark** against the inspect
cassettes in [`cassettes/`](cassettes/); a multi-read cassette's interactions
are consumed IN ORDER, one per `ctx.fs.read` call, which is how the refine-loop
fixtures serve a failing manifest on the first lint and a passing one on the
second. Fixture epics live in [`testdata/`](testdata/); [`agent-bench/`](agent-bench/)
holds a GLM decomposer benchmark script.

## See also

- [`stories/fleet/`](../fleet/) — brief fan-out behind the merge lock.
- [`stories/ship-it/`](../ship-it/) — single-brief maker → integrate →
  re-verify loop.
- [`stories/decompose-update/`](../decompose-update/) — the managed-delta
  transaction for *changing* an existing decomposition; its `review.yaml` is
  the decide-schema-bind-gate shape this story's `review` room lifts.
- [`stories/bugfix/rooms/proposing.yaml`](../bugfix/rooms/proposing.yaml) —
  the budgeted refine-loop pattern (`refine_cycle`/`refine_budget`) this
  story's refine edges follow.
- [`.agents/skills/work-decomposition/`](../../.agents/skills/work-decomposition/SKILL.md)
  — the manual twin of this pipeline for by-hand runs; its schema is a copy of
  this story's `schemas/decomposition.json`, kept identical rather than left
  to drift.
