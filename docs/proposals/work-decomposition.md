# Story: Work decomposition — turn a proposal into a coordinated build

**Status:** Superseded by
[`deliver-canonical-decomposition.md`](deliver-canonical-decomposition.md)
(decided 2026-07-05): the proposed `stories/decompose/` sub-story will not be
built; `stories/deliver/` is the canonical decomposition story and absorbs
this proposal's discipline as rooms/gates. This file is trimmed to what
actually shipped from it; delete it once the absorption slices land.
**Kind:**   story
**Epic:**   — standalone

## What shipped from this proposal

- **`.agents/skills/work-decomposition/`** — the manual, Claude-driven
  version of the pipeline: the brief-manifest schema
  (`schemas/decomposition.json`: `coverage_note` + per-brief
  `id/title/kind/goal/scope/depends_on/acceptance/test_plan/agent_brief/risk`),
  the deterministic structural validator
  (`scripts/validate_decomposition.py`, with a Starlark twin for the
  pure-logic checks), and the discovery → manifest → lint → adversarial
  feasibility/completeness review loop, run by hand. This is the shipped
  home of the manifest shape and the validation discipline.
- **Adjacent, independently shipped:** `stories/deliver/` (a simpler
  configure → decompose → lint → fleet pipeline, 5 no-LLM flows — see its
  [README](../../stories/deliver/README.md)) and `stories/fleet/` /
  `stories/ship-it/` for per-brief execution. They were not built from this
  proposal, but they are why the richer `stories/decompose/` design here
  was never needed as written.

## What was retired

The full `stories/decompose/` design — discovery room, board/dispatch rooms
with per-brief ticket materialisation into the `impl` import, `impl__*`
carry-key resets — lives in git history
(`git log -- docs/proposals/work-decomposition.md`). The parts still wanted
(richer schema, budgeted refine loops, the adversarial review gate, a
decompose-vs-direct route from dev-story) are carried forward, re-grounded
against today's tree, in
[`deliver-canonical-decomposition.md`](deliver-canonical-decomposition.md).
