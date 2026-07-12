# task-bakeoff — deprecated orchestration stub

> **Deprecated.** This story predates the native Capsule evaluation adapter and
> refers to retired `bakeoff.yaml` / `prepare.sh` / `run_cell.sh` machinery.
> Do not use it for CI, workspace lifecycle, or new comparisons. Use Capsule CI
> for project validation and the `matrix-task-comparison` skill with Arena as
> the canonical planner/scheduler, plus `tools/bugfix-bakeoff/external` for
> project/oracle adaptation.
> It remains temporarily for replay/history compatibility only.

> Sibling: for an EXTERNAL repo (onboard a third-party project + fix real bugs), see [`stories/repo-bakeoff`](../repo-bakeoff/README.md) (wraps `tools/bugfix-bakeoff/external`).

A kitsoki story that wraps the **`matrix-task-comparison`** method
(`.agents/skills/matrix-task-comparison/SKILL.md`) and its reference harness
(`tools/arena/`) into a replay-compatible workflow. New studies should review
the Arena-produced `.slidey.json` source deck; do not render static HTML, bundle,
or MP4 unless explicitly requested.

```
kitsoki run stories/task-bakeoff/app.yaml
```

## What it wraps

- **The method** — `matrix-task-comparison`: compare approaches on the *same*
  tasks under *identical* conditions, score each cell on outcome / compliance /
  cost / time, roll up, and deck it. A **cell = (task × candidate × contender)**:
  - **tasks** — the cases under test (e.g. `bug9`, `bug12`, `bug14`).
  - **candidates** — the harness/model axis (e.g. `opus-4.8`, `sonnet-4.6`).
  - **contenders** — the structure axis (e.g. `kitsoki` pipeline vs `single` prompt).
- **The harness** — Arena owns study planning, immutable attempt receipts,
  resumption, and report/deck inputs. `tools/bugfix-bakeoff/external` remains
  the project/oracle adapter. This compatibility story does not schedule cells
  or interpret traces.
- **The report** — the slidey deck schema (`schemas/deck.json`, reused verbatim
  from `slidey-edit`) + the `host.slidey.render` → `host.artifacts_dir` sequence
  from `stories/slidey-edit/rooms/rendering.yaml`.

## Rooms

```
idle ──start──▶ configure ──accept──▶ running ──accept──▶ scoring ──(auto)──▶
   reporting ──accept──▶ slideshow ──(auto render)──▶ done ──accept──▶ @exit:done
```

| Room | Split | What it does |
|---|---|---|
| `idle` | deterministic | Park. `start` boots the bake-off; `quit` → `@exit:abandoned`. |
| `configure` | deterministic | Declare the matrix (the three axes, echoed from the harness manifest) and compute the authoritative `cells_total`. |
| `running` | **orchestration stub** | Track the cell roster as a checklist. The cost-bearing per-cell run (`prepare.sh` / `run_cell.sh`) is run **manually**, never in CI/auto — so this room does **not** execute cells; it surfaces progress and advances. |
| `scoring` | deterministic | `host.starlark.run` → `host.bakeoff.run` rolls the committed `cells/*.json` into a `summary` (by treatment / candidate / cell-key). Free, no LLM. |
| `reporting` | deterministic | `host.starlark.run` → `host.bakeoff.run` builds the comparison report + the slidey **deck spec** from the rollup. Offline, zero re-spend. |
| `slideshow` | deprecated compatibility | Existing replay-only HTML rendering. New studies emit a `.slidey.json` source deck from Arena and do not use this room. |
| `done` | gallery | `media(deck_handle)` + the headline rollup. `accept` → `@exit:done` (requires `deck_handle` — a real rendered report exists). |

## Honesty: what is real vs. stubbed

Per `stories/AGENTS.md` (never paper-over):

- **`running` is a thin orchestration stub by design.** The matrix-comparison
  method is explicit that `run_cell.sh` is the only cost-bearing piece and is run
  **by hand**, never automatically (the AGENTS.md no-LLM rule). This story drives
  the *free, deterministic* half end-to-end (configure → aggregate → report →
  render) and tracks the cell roster; it does **not** fabricate cell results. In
  no-LLM mode the cell results are the harness's already-committed `cells/*.json`.
- **The render path is real.** `slideshow` is a faithful copy of `slidey-edit`'s
  render room; under `kitsoki web --flow` `host.artifacts_dir` runs for real so
  the media handle resolves through the journal. `baked/deck.json` is a real
  3-scene slidey deck so the report renders without the harness running live.

## Deterministic, no-LLM testing

```
kitsoki test flows stories/task-bakeoff/app.yaml
```

| Fixture | Covers |
|---|---|
| `flows/happy_path.yaml` | idle → configure → running → scoring → reporting → slideshow → done → `@exit:done`. All host calls stubbed by invoke id; render points at `baked/`. |
| `flows/quit_at_configure.yaml` | `quit` at configure → `@exit:abandoned`. |

## Exits

| Exit | `requires:` | When |
|---|---|---|
| `done` | `deck_handle` | A real rendered slidey report deck exists. |
| `abandoned` | — | `quit` at idle / configure. |

## Remaining work

Clearly thin spots, in priority order:

1. **Live cell execution is not wired.** `running` tracks the roster but does not
   call `prepare.sh` / `run_cell.sh`. A follow-up slice would add a guarded
   `host.run` per cell (cwd `harness_dir`) gated behind an explicit operator
   opt-in (cost-bearing — must never fire in flow/CI). The commented shape is in
   `rooms/running.yaml`.
2. **The cell roster is supplied, not computed.** A full 3-axis cartesian product
   isn't expressible in kitsoki set-effects (no nested-loop primitive), so
   `world.cells.items` is seeded by the operator/flow (in flow mode via
   `initial_world`). A configure-time `host.run` → a tiny roster-builder reading
   `bakeoff.yaml` would derive it; `cells_total` already cross-checks the count.
3. **Adjudication + guidance turns** (the coupled-oracle override and the
   oracle-gated single-prompt feedback loop from the skill) are part of the
   *manual* cell run, not yet surfaced as rooms.
4. **The `reporting` deck-builder binding** assumes the offline report boundary can
   emit the deck *spec path* (`stdout_json.deck`); the live script today writes a
   self-contained `deck.html`. Wiring the live JSON contract (or a thin wrapper
   that emits the spec path) is the only change needed to drop the baked stand-in.
