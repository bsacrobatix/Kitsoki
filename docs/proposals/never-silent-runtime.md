# Runtime: never-silent runtime — surfaced errors, workbench-bound near-misses, one routing decision point

**Status:** Draft v1. Nothing implemented yet.
**Kind:**   runtime
**Epic:**   usable-kitsoki.md

## Why

Three release-blocking gaps (R2, R3 in the epic) share one root cause: the
runtime lets a turn end without the user learning what happened to their
request.

1. **Silent bounce.** `on_error:` redirects are common across stories — 464
   occurrences under `stories/` + `internal/basestories` in this checkout
   (`grep -rn 'on_error:' --exclude-dir=.git --exclude-dir=node_modules
   stories internal/basestories`; this corrects the epic doc's 1,132
   estimate, which predates this sweep).
   The runtime *partially* covers this today: `appendErrorBanner`
   (`internal/orchestrator/orchestrator.go:3331`) fires from the two live
   turn paths — `Turn`'s host-dispatch path
   (`internal/orchestrator/orchestrator.go:1507`) and the harness-continue
   path (`internal/orchestrator/helpers.go:414`) — but only when
   `world.Vars["last_error"]` happens to be set and the view doesn't already
   contain it. The **stateless one-shot path** (`Orchestrator.OneShot`,
   `internal/orchestrator/orchestrator.go:2258-2271`, the engine under
   `kitsoki turn` and MCP's `turn`/`drive` tools) calls the equivalent
   `dispatchHostCallsDetailed` and applies `hostRedirect` to `NewState` but
   **never calls `appendErrorBanner`** — a probe against that path sees a
   silent state change with no rendered explanation. The
   `kitsoki-debugging` skill (`.agents/skills/kitsoki-debugging/SKILL.md`)
   exists purely as a human workaround for this gap ("assume the TUI is
   lying about what really failed").
2. **Misroute to an adjacent command.** Routing is chronically wrong: five
   fix branches in one week culminating in `8a1f42b1` (semantic routing
   shipped **default-off**); `docs/architecture/semantic-routing.md`
   documents a live dogfood misroute. Root cause is structural, not tuning:
   the same "does this input match an authored intent" decision is made at
   four independent sites with independent thresholds —
     - the TUI pre-pass, `internal/tui/tui.go:1899`
       (`orch.MatchDeterministic(ctx, sid, input)`, run *before* `Turn`);
     - `Orchestrator.Turn`'s own routing stack,
       `internal/orchestrator/orchestrator.go:1019-1060` (exact match →
       semroute semantic → turn-cache → `default_intent` sink → LLM
       interpreter), which every non-TUI caller (MCP studio's `drive`/
       `submit`, `kitsoki turn`) reaches only through `Turn`/`OneShot` — so
       it is really "the TUI's extra pre-pass" plus "everyone's shared
       stack," not four independently-tuned pre-passes, but the TUI's copy
       *can and does* diverge from the shared stack's outcome for the same
       input, which is exactly the routing-corruption bug class the fix
       branches chased.
   A near-miss below confidence must fall into the workbench (S1) as a
   governed free-form request, never resolve to the nearest authored intent
   by subset-match — that guess is what "prose misroutes to adjacent
   commands" *is*.
3. **No engine-level gate.** Nothing today fails a story load or a flow run
   because a reachable `on_error:` target renders a view with no trace of
   what failed. The banner is a convention two call sites happen to follow,
   not an invariant the engine enforces on all of them — and the third
   (`OneShot`) proves conventions drift.

## What changes

Two engine-level guarantees, plus a decision-point consolidation:

1. **Never-silent invariant.** Every code path that applies an `on_error:`
   (or off-ramp, or workbench-escalation) redirect renders a message
   distinguishable from a normal state view before the turn ends — enforced
   once, in the shared redirect-application seam, not re-implemented per
   call site. `OneShot` gains the same `appendErrorBanner` call `Turn` and
   `ContinueTurn` already make; a new **G-FLOW gate** (modeled on the
   existing flow-fixture assertion machinery) fails any flow fixture that
   drives an `on_error:` arc and asserts a view *not* containing the banner
   marker.
2. **Near-miss binds to the workbench, never to an adjacent intent.** The
   routing stack's confidence threshold gains a explicit third outcome
   alongside "matched" / "no match, LLM interprets": *near-miss* — input
   scored above the reject floor but below the accept floor. Today that
   band silently resolves through the LLM interpreter, which is where
   adjacent-command misroutes come from (the interpreter picks the closest
   authored intent because that's the alphabet it's given). Near-miss
   routes to S1's workbench request instead — the interpreter is never
   asked "which of these commands did they mean," only "is this
   confidently one of these, or is it something else."
3. **One routing decision point.** Delete the TUI's independent
   `MatchDeterministic` pre-pass (`tui.go:1899`); the TUI calls `Turn`
   directly like every other surface and renders whatever routing tier
   resolved it. `Orchestrator.Turn`'s existing tiered stack
   (`orchestrator.go:1019-1060`) becomes the **only** place routing
   decisions are made; `OneShot` is refactored to share it instead of
   duplicating dispatch. This is a deletion, not an addition — it removes
   the one caller (TUI) that can diverge from what every other surface
   sees for the same input.

## Impact

- **Code seams:** `internal/orchestrator/orchestrator.go` (`Turn`,
  `OneShot`, `appendErrorBanner`), `internal/orchestrator/host_dispatch.go`
  (`dispatchHostCalls`, `dispatchHostCallsDetailed` — converge to one),
  `internal/tui/tui.go:1865-1899` (delete the pre-pass), `internal/testrunner`
  (new G-FLOW assertion helper).
- **Vocabulary:** one new gate class (`G-FLOW` near-silent-bounce check);
  one new routing verdict (`near_miss`) in the existing routing-tier
  decision record; no new host calls or world keys.
- **Stories affected:** none need YAML changes — this is enforcement of an
  existing convention (`on_error:`) plus a routing-stack refactor behind
  the existing `Turn`/`OneShot` API. Stories whose `on_error:` target
  renders `last_error` themselves are unaffected (banner is suppressed when
  already shown, per today's `strings.Contains` guard).
- **Backward compat:** default-on. A story whose flow fixtures exercise an
  `on_error:` path and don't assert the banner will need one assertion line
  added (mechanical; see Tasks). No cassette shape changes — the banner is
  view text, not a new event.
- **Docs on ship:** `docs/stories/state-machine.md` (`on_error:` section),
  `docs/architecture/semantic-routing.md` (single decision point +
  near-miss tier), `docs/architecture/overview.md` §3 (turn pipeline).

## Vocabulary changes

| Kind | Name | Shape | Notes |
|---|---|---|---|
| gate | `G-FLOW` (near-silent-bounce) | flow-fixture assertion | fails a fixture that drives an `on_error:` transition whose resulting view lacks the error-banner marker |
| routing verdict | `near_miss` | float confidence band | routing-stack outcome between the reject floor and the accept floor; escalates to the S1 workbench, never to the nearest authored intent |

## The model

```
on_error: redirect (host_dispatch.go) ──▶ appendErrorBanner (ALWAYS, one seam)
                                          └─▶ view carries the error before the turn rests

Turn's routing stack (orchestrator.go:1019) ──confidence──▶
    match           → authored intent, machine.Turn
    near_miss       → S1 workbench (governed free-form), NOT nearest intent
    no match        → LLM interpreter / off-ramp / workbench (existing tiers)

TUI, MCP studio, `kitsoki turn` all call Turn/OneShot directly — no
surface-local routing pre-pass.
```

## Decision recording

The routing stack already emits a decision record per turn (the
`gate_decided`-shaped event feeding `semantic-routing.md`'s route badges);
this proposal adds `near_miss` as a labeled verdict value alongside the
existing tiers, with the same confidence-vs-threshold fields already
recorded, so a near-miss is visible in the trace as "routed to workbench
because confidence was in the near-miss band," not silently absorbed into
"no match." The error-banner application is not a new decision (it is
deterministic once redirect + `last_error` are set) and needs no new event;
`host.on_error.redirect` (`internal/trace/trace.go:207`) already records the
redirect itself.

## Engine seams & invariants

- **Load-time:** none new — `on_error:` targets are already validated as
  reachable states at load. This proposal only changes what happens at the
  *render* step of an already-validated redirect.
- **Runtime:** `appendErrorBanner` moves from "called by two of three
  dispatch paths" to "called by the one shared redirect-application
  function both `Turn` and `OneShot` route through" — the fix is
  structural (share the function), not a third copy-pasted call.
- **G-FLOW gate:** runs as part of `kitsoki test flows` (alongside the
  existing flow assertion machinery in `internal/testrunner`), not as a
  separate CLI — a story fails its normal flow suite if any exercised
  `on_error:` path renders silently.

## Backward compatibility / migration

Existing stories and cassettes are unaffected at the fixture-recording
layer (no new host calls, no changed event shapes). Flow fixtures that
drive an `on_error:` arc and assert the resulting view verbatim will need
the banner text folded into that assertion — mechanical, one line per
fixture, done as part of Task 3.1's adoption pass across `stories/dev-story`
and two other stories per the epic's rollout decision (opt-in-until-green,
then default; see epic §Shared decisions).

## Tasks

```
## 1. Engine
- [ ] 1.1 Refactor OneShot's dispatch to share the same redirect-application
      seam as Turn/ContinueTurn (delete the second appendErrorBanner-less
      copy in dispatchHostCallsDetailed's caller)
- [ ] 1.2 Add the near_miss confidence band + verdict to the routing stack
      (orchestrator.go:1019-1060); wire near_miss to the S1 workbench entry
      point (stub until S1 lands; see Open questions)
- [x] 1.3 Delete the TUI's MatchDeterministic pre-pass (tui.go:1899); TUI
      calls Turn directly
- [ ] 1.4 Decision recording: near_miss verdict lands in the existing
      routing decision event with confidence + threshold fields

## 2. Verification
- [ ] 2.1 Stateless unit: kitsoki turn against a fixture whose on_error:
      target has no last_error reference — assert banner text present
- [x] 2.2 G-FLOW gate: a flow fixture that drives on_error: without the
      banner fails; the same fixture with the banner assertion passes
- [ ] 2.3 Routing near-miss fixture: an input scored in the near-miss band
      resolves to the workbench outcome, not the nearest authored intent
- [x] 2.4 TUI regression: MatchDeterministic removal doesn't change any
      existing TUI routing flow fixture's outcome

## 3. Adopt + document
- [ ] 3.1 Run G-FLOW across dev-story + two other stories per the epic's S6
      rollout gate; fix any fixture whose on_error: view was silent
- [ ] 3.2 Update docs/stories/state-machine.md (on_error: section) and
      docs/architecture/semantic-routing.md (single decision point,
      near-miss tier); add one-line pointers from
      semantic-routing-proposal.md and contextual-room-routing.md to this
      child; migrate shipped content to docs/ and trim/delete this proposal
```

## Verification

`kitsoki turn --state … --intent … --world @w.json` against a fixture whose
`on_error:` target renders a view with no `last_error` reference confirms
the banner appears without needing a live LLM (the redirect and the
missing-banner condition are both deterministic). The near-miss routing
band is exercised the same way the existing deterministic/semantic tiers
are today — no LLM call needed, since the near-miss classification comes
from the same confidence score the semroute tier already computes
offline. No new test needs a live LLM.

## Open questions

1. Near-miss threshold placement: reuse the existing semroute confidence
   threshold's lower band, or a new tunable? *Lean: reuse — a second knob
   invites the same "five fix branches" drift this proposal is trying to
   end.*
2. Does the near-miss verdict resolve to S1's workbench even before S1
   ships (falling back to today's off-ramp / no-match handling in the
   interim), or does 1.2 stay stubbed until S1 lands? *Lean: stub behind a
   feature check — land the confidence-band plumbing and decision
   recording now, wire the destination when S1's workbench entry point
   exists, so S1 doesn't block on this proposal's routing refactor.*

## Non-goals

- The workbench itself (S1) — this proposal only routes near-misses to it
  and does not build it.
- Prompt/context-cost reduction for the routing stack's LLM tier (S3).
- Any change to the `off-ramp`/`agent_off_ramp` mechanism's own converse
  behavior — untouched, still the opt-out path for rooms that decline
  work.
