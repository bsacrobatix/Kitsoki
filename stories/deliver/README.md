# deliver — epic front-door of the delivery loop

Hand `deliver` a path to an epic/proposal markdown and it decomposes it into
independently-shippable briefs, lints the manifest deterministically, and fans
[`stories/fleet/`](../fleet/) (which runs [`stories/ship-it/`](../ship-it/)
per brief behind a merge lock) over the result.

Layering is acyclic: **deliver → fleet → ship-it**. deliver is the only entry
above fleet; fleet never imports deliver.

> **Target shape:** deliver is the decided canonical decomposition story —
> it will absorb the work-decomposition skill's richer manifest schema, a
> budgeted refine loop, and an adversarial feasibility/completeness review
> gate, and become reachable from dev-story. See
> [`docs/proposals/deliver-canonical-decomposition.md`](../../docs/proposals/deliver-canonical-decomposition.md).
> This README documents what ships **today**.

## Pipeline

```
configure {epic_path}
  └─ start ─▶ decompose (host.agent.task: read epic, write decomposition YAML)
               ├─ ok ──▶ lint (host.starlark.run — deterministic, no LLM)
               │          ├─ pass ─▶ fleet import (entry: load) — fan ship-it over every brief
               │          │           └─ @exit:done {delivery_summary ← fleet_summary}
               │          └─ fail ─▶ @exit:needs-human {last_error}
               └─ host error ─▶ decompose_error ─▶ @exit:needs-human {last_error}
```

There is no refine loop today: a lint failure exits `needs-human` with a
specific error rather than re-arming the decomposer. Every failure arc exits
`@exit:needs-human` with a non-blank `last_error`.

## Rooms

| Room | Does |
|---|---|
| `configure` (root) | Captures `epic_path` (`start epic_path=<path>`, or seeded via `initial_world`); defaults `decomposition_path` to `.artifacts/deliver/decomposition.yaml`. |
| `decompose` | `host.agent.task` invokes the `decomposer` agent (`once: true` — clear `decomposition_briefs` to force a redo). The agent reads the epic, **writes the decomposition YAML to `decomposition_path`** (the format fleet's `load` parses), and submits a manifest validated against [`schemas/decomposition.json`](schemas/decomposition.json); `bind: decomposition_briefs`. |
| `decompose_error` | Host-call failure surface; auto-routes to `@exit:needs-human` (the engine sets `last_error`). |
| `lint` | `host.starlark.run` [`scripts/lint_decomposition.star`](scripts/lint_decomposition.star) over `decomposition_path` → `{route: ok\|fail, error}`; resets `lint_route` before the invoke so stale values never route (the board.yaml pattern). |

## The manifest contract

`briefs:` — an ordered list; each brief:

- `id` — unique, `^[a-z][a-z0-9-]*$`
- `brief` — self-contained task for the maker agent (min 10 chars)
- `gate_command` — deterministic shell command, exit 0 = success, and
  **RED at baseline** (see [`prompts/decompose.md`](prompts/decompose.md))
- `deps` — ids that must ship first (acyclic)

The lint (no LLM) enforces: at least one brief; ids unique and non-empty;
non-empty `brief` and `gate_command` per brief; every dep references a known
id; no dependency cycle. It also accepts the work-decomposition skill's field
aliases (`agent_brief`, `test_plan`, `depends_on`).

## World / agents / exits

Key world: `epic_path`, `decomposition_path`, `decomposition_briefs`,
`lint_route`, `last_error`, `delivery_summary`, plus `base_branch` /
`main_worktree_path` projected into fleet.

One agent — `decomposer` (`claude-opus-4-8`, tools `Read/Edit/Write`, no
Bash/subagents; gates are designed from static evidence and executed
downstream).

Exits: `done` (requires `delivery_summary`), `needs-human` (requires
`last_error`).

## Flows (no-LLM)

```
go run ./cmd/kitsoki test flows stories/deliver/app.yaml
```

| Flow | Proves |
|---|---|
| [`decompose_happy`](flows/decompose_happy.yaml) | 3-brief manifest → real Starlark lint passes → lands in `fleet.load` (fleet's own flows cover fan-out). |
| [`decompose_error`](flows/decompose_error.yaml) | Decomposer host error → `decompose_error` → `@exit:needs-human` with `last_error`. |
| [`lint_rejects_cycle`](flows/lint_rejects_cycle.yaml) | Dependency cycle → specific lint error → `needs-human`. |
| [`lint_rejects_missing_dep`](flows/lint_rejects_missing_dep.yaml) | Dangling dep id → specific lint error → `needs-human`. |
| [`slidey_decomposition`](flows/slidey_decomposition.yaml) | Tour-shaped happy path (`epic_path` seeded in `initial_world` so `start` needs no slot) for the web/no-LLM demo. |

The agent is mocked in every flow; lint and fleet-load run **real Starlark**
against the inspect cassettes in [`cassettes/`](cassettes/). Fixture epics
live in [`testdata/`](testdata/); [`agent-bench/`](agent-bench/) holds a GLM
decomposer benchmark script.

## See also

- [`stories/fleet/`](../fleet/) — brief fan-out behind the merge lock.
- [`stories/ship-it/`](../ship-it/) — single-brief maker → integrate →
  re-verify loop.
- [`stories/decompose-update/`](../decompose-update/) — the managed-delta
  transaction for *changing* an existing decomposition.
- [`.agents/skills/work-decomposition/`](../../.agents/skills/work-decomposition/SKILL.md)
  — the manual twin of this pipeline (richer schema; being reconciled per
  the proposal above).
