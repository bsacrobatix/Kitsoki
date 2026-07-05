# Runtime: never-silent runtime — surfaced errors, workbench-bound near-misses, one routing decision point

**Status:** Shipped except S1-dependent wiring. All engine work (Tasks
1.1–1.4, 2.1–2.4, 3.1) landed on branch `s2/never-silent`
(`8ec3d36d` never-silent seam, `185088b8` near_miss verdict,
`4529fab1`/`c9c30e84` TUI pre-pass removal, `3d42c39c` G-FLOW gate).
Docs migrated to
[`state-machine.md`](../stories/state-machine.md#on_error-redirects-and-the-recursion-cap),
[`semantic-routing.md`](../architecture/semantic-routing.md#13-synonym-templates),
and [`overview.md`](../architecture/overview.md#3-the-journey-of-one-turn)
(Task 3.2). **Remaining:** wiring `near_miss` to an actual S1 workbench
destination — `nearMissWorkbenchEnabled` stays a stubbed-`false` feature
check until S1 ships (see Open questions).
**Kind:**   runtime
**Epic:**   usable-kitsoki.md

## Why (as shipped)

Three release-blocking gaps shared one root cause: the runtime let a turn
end without the user learning what happened to their request — a silent
`on_error:` bounce on the stateless `OneShot` path, adjacent-command
misroutes from a near-miss confidence band silently falling through to the
LLM interpreter, and a TUI routing pre-pass that could diverge from the
shared `Turn` stack for the same input. Full rationale and code-line
citations are preserved in git history at the pre-implementation revision
(`f3255039`); the shipped behavior is documented in place — no need to
duplicate it here (see docs/AGENTS.md: one authoritative location per
concept).

## What shipped

- **Never-silent invariant** — every dispatch path that applies an
  `on_error:` redirect renders a distinguishable failure message before the
  turn ends, enforced once in a shared seam
  (`internal/orchestrator/host_dispatch.go`'s `applyErrorBannerSeam`).
  See [state-machine.md](../stories/state-machine.md#on_error-redirects-and-the-recursion-cap).
- **G-FLOW gate** — `kitsoki test flows` automatically fails any flow
  fixture whose `on_error:` arc renders silently; no opt-in field. Same doc
  section above.
- **`near_miss` routing verdict** — a confidence-band outcome between the
  reject floor and the accept floor that never auto-resolves to the
  nearest authored intent. See
  [semantic-routing.md](../architecture/semantic-routing.md#13-synonym-templates).
- **One routing decision point** — the TUI's independent
  `MatchDeterministic` pre-pass is deleted; every surface resolves through
  `Orchestrator.Turn`'s shared tiered stack. See
  [overview.md §3](../architecture/overview.md#3-the-journey-of-one-turn)
  and [semantic-routing.md §1](../architecture/semantic-routing.md#1-the-four-tiers).

## Tasks

```
## 1. Engine
- [x] 1.1 Refactor OneShot's dispatch to share the same redirect-application
      seam as Turn/ContinueTurn (8ec3d36d)
- [x] 1.2 Add the near_miss confidence band + verdict to the routing stack;
      destination stubbed behind nearMissWorkbenchEnabled (false) until S1
      lands (185088b8)
- [x] 1.3 Delete the TUI's MatchDeterministic pre-pass; TUI calls Turn
      directly (4529fab1)
- [x] 1.4 Decision recording: near_miss verdict lands on the trace
      (turn.near_miss) with confidence + threshold fields (185088b8)

## 2. Verification
- [x] 2.1 Stateless unit: OneShot against a fixture whose on_error: target
      has no last_error reference — banner text present
      (oneshot_error_banner_test.go)
- [x] 2.2 G-FLOW gate: a flow fixture that drives on_error: without the
      banner fails; the same fixture with the banner assertion passes
      (3d42c39c)
- [x] 2.3 Routing near-miss fixture: an input scored in the near-miss band
      resolves to the next tier's outcome, not the nearest authored intent
      (semantic_near_miss_test.go)
- [x] 2.4 TUI regression: MatchDeterministic removal doesn't change any
      existing TUI routing flow fixture's outcome (c9c30e84)

## 3. Adopt + document
- [x] 3.1 Run G-FLOW across dev-story + two other stories per the epic's S6
      rollout gate. Zero failures — see commit d19a8258 for the full
      per-story breakdown.
- [x] 3.2 Update state-machine.md / semantic-routing.md / overview.md;
      trim this proposal to shipped-summary + remaining S1-dependent item
```

## Open questions

1. ~~Near-miss threshold placement~~ — resolved: reuses the existing
   `semantic_high_bar`/`semantic_mid_bar` knobs, no new tunable.
2. **Still open:** wiring `near_miss` to S1's actual workbench entry point.
   `nearMissWorkbenchEnabled` (`internal/orchestrator/semantic.go`) is a
   stub that always returns `false`, so the band falls back to today's
   off-ramp/no-match handling in the interim. Flip the stub and route
   `TrySemantic`'s `near_miss` case to the workbench once S1 ships — no
   other change to the band logic is needed.

## Non-goals

- The workbench itself (S1) — this proposal only routes near-misses to it
  and does not build it.
- Prompt/context-cost reduction for the routing stack's LLM tier (S3).
- Any change to the `off-ramp`/`agent_off_ramp` mechanism's own converse
  behavior — untouched, still the opt-out path for rooms that decline
  work.
