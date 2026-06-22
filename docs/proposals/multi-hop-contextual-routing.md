# Runtime: multi-hop contextual routing

**Status:** Draft v1. Nothing implemented yet.
**Kind:**   runtime
**Epic:**   - standalone
**Relation:** extends [`contextual-room-routing.md`](contextual-room-routing.md)
and the shipped contextual routing tier in
[`docs/architecture/semantic-routing.md`](../architecture/semantic-routing.md#7-contextual-routing-tier).

## Why

The contextual router can now classify unmatched input inside the current room
as one of four local outcomes: declared intent, help, room request, or meta edit.
That solves "what did the operator mean in this room?", but it still assumes the
answer is local to the active room.

Some real operator turns are cross-room commands. From a ticket-search room,
"file a bug for the failing trace, then bring me back here" is not help, not a
room chat message, and not a single intent on the active state's `on:` table. A
human reads it as a short route plan:

1. leave the current room,
2. enter the bug-filing room,
3. run the filing intent with slots,
4. return to the original room.

Today that either fails routing, gets misclassified as in-room free-form work,
or requires every room to duplicate escape hatches for neighboring workflows.
The runtime should let the contextual LLM propose this as a bounded multi-hop
route while keeping the existing "bad routing" correction affordance: show the
operator the interpretation, let them rewind to the pre-route point, and choose
a different route interpretation.

## What changes

Add a `route_plan` verdict to the contextual routing tier. A route plan is a
small, recorded list of deterministic steps:

- optional snapshot of the origin state/world,
- one or more state transitions through already-declared intents,
- optional intent execution in the target room with slots,
- optional return to the origin state.

The LLM is allowed to propose the plan shape, but it is not allowed to mutate
world or invent edges. The orchestrator validates every step against the loaded
state graph and existing intent/slot contracts before executing anything. If
any step is invalid or ambiguous, the plan is rejected and the turn falls back to
the existing route alternatives / clarification path.

The route receipt becomes plan-aware: it displays a compact path such as
`ticket_search -> bugfix.open -> bugfix.file -> ticket_search`, the reason, and
alternatives. The existing rewind surface gets a plan-level target that restores
the session to the state/world before step 1, records the override, and
re-dispatches the original utterance under the operator's selected
interpretation.

## Impact

- **Code seams:** extend `ContextRouteVerdict` /
  `ContextRouteReceipt` in `internal/orchestrator/context_route.go:45`; execute
  plans from `routeViaContextualRouter` in `internal/orchestrator/semantic.go:164`;
  generalize `Orchestrator.RewindRoute` from one foreground decision at
  `internal/orchestrator/rewind.go:37`; surface plan receipts through the
  existing runstatus `context_route` wire shape.
- **Vocabulary:** one new contextual verdict class, one plan step shape, and
  plan-level trace events. No new story effect or host call.
- **Stories affected:** only states that opt in to contextual routing and route
  plan support change behavior.
- **Backward compat:** existing contextual verdicts, cassettes, and lane
  dispatches keep working. `route_plan` is opt-in and rejected by default.
- **Docs on ship:** update `docs/architecture/semantic-routing.md` section 7, routing
  receipt UI docs, and authoring guidance for when a story should enable
  multi-hop routing.

## Vocabulary changes

| Kind | Name | Shape | Notes |
|---|---|---|---|
| contextual class | `route_plan` | `{class, confidence, reason, steps, alternatives}` | fifth contextual verdict class; only offered when enabled |
| config | `contextual_routing.route_plans` | `{enabled, max_steps?, allow_return?}` | default disabled; `max_steps` default 4 |
| receipt field | `Plan` | `[]ContextRouteStepReceipt` | rendered as the route path and rewind scope |
| trace event | `turn.context_route_plan_decided` | `{decision_id, origin, steps, reason, confidence, alternatives}` | recorded before execution |
| trace event | `turn.context_route_plan_step` | `{decision_id, index, from, intent, to, slots}` | recorded after each validated step applies |
| trace event | `turn.context_route_plan_overridden` | `{from_decision_id, old_plan, new_class|new_plan, reason}` | plan-level correction event |

## The model

Interpretive work happens once: the contextual LLM proposes a structured route
plan. Deterministic execution happens after validation:

```text
origin room
  -> contextual LLM proposes route_plan
       -> validate state graph + intent slots + return target
            -> execute step 1, step 2, ... step N
                 -> emit one plan receipt with one DecisionID
```

The plan step schema is intentionally narrow:

```go
type ContextRoutePlanStep struct {
    From   string         `json:"from"`
    Intent string         `json:"intent"`
    Slots  map[string]any `json:"slots,omitempty"`
    To     string         `json:"to,omitempty"`
}
```

`From` must match the current state at that point in the plan. `Intent` must be
declared on that state. `Slots` must satisfy the same slot validation as a
normal routed intent. `To`, when present, must match the transition target that
the loaded machine computes; it is a check, not an instruction.

The return hop is represented as an ordinary validated step when the story has a
declared route back. If there is no declared return route, the plan is invalid
unless `allow_return: snapshot` is explicitly enabled later; v1 leans against
snapshot-return because it would skip story-authored transitions.

## Decision recording

`route_plan` needs stronger recording than a single intent receipt because the
operator must be able to audit and correct every hop.

The trace records:

- the proposed plan before execution, including origin state/world hash,
  confidence, reason, alternatives, and step list;
- each applied step after validation, including state before/after and slots;
- the final plan receipt attached to the turn outcome;
- any override event, with `from_decision_id` pointing at the original plan.

Replay never calls the LLM for an already-recorded plan. It replays the recorded
steps through the same deterministic validation path and fails loudly if the
current story no longer admits the route.

## Engine seams & invariants

`ParseContextRouteVerdict` currently accepts a closed set of four classes
(`intent | help | room_request | meta_edit`) in
`internal/orchestrator/context_route.go:72`. Add `route_plan` only when the
state config enables it; otherwise a model-emitted plan is treated as an invalid
verdict and falls through like any other parse miss.

`routeViaContextualRouter` currently executes either one `SubmitDirectRouted`
intent or one lane append in `internal/orchestrator/semantic.go:309`. Insert the
plan path beside `class=intent`, but execute it through a small plan runner that
uses the same submit/transition machinery as normal foreground turns. The plan
runner should stop before any step with side effects until the full plan has
validated structurally.

`RewindRoute` currently restores the pre-turn state/world for a single
contextual decision and supports lane re-dispatch; `class=intent` rewind is
still explicitly incomplete in `internal/orchestrator/rewind.go:130`. Multi-hop
requires finishing intent re-dispatch first, then extending the rewind target
from one decision to one plan. A plan override should restore the snapshot taken
before step 1, not before the failed/corrected intermediate hop.

Load-time invariants:

- `route_plans.enabled` requires `contextual_routing.enabled`.
- `max_steps` must be positive and capped at a small bound, default 4.
- plan support requires a store capable of snapshots and route override events.
- UI "switch route" can only be immediate when the original plan made no world
  mutation; otherwise it must use full rewind.

## Backward compatibility / migration

No story migrates automatically. Existing contextual routing stays four-class
unless a state opts in:

```yaml
states:
  ticket_search:
    contextual_routing:
      enabled: true
      route_plans:
        enabled: true
        max_steps: 4
        allow_return: declared_only
```

Old cassettes that contain four-class contextual verdicts keep parsing. New
route-plan cassettes record the plan verdict and step events so tests replay
without a real LLM.

## Tasks

```text
## 1. Substrate
- [ ] 1.1 Finish `class=intent` support in `Orchestrator.RewindRoute` so
      existing bad-routing correction can re-dispatch to a chosen intent.
- [ ] 1.2 Add `route_plans` config, load-time validation, and a closed
      `route_plan` verdict parser behind the opt-in.
- [ ] 1.3 Add plan/step receipt structs and runstatus wire types without
      changing existing four-class receipts.

## 2. Plan execution
- [ ] 2.1 Implement a validator that proves every step is an existing
      state+intent+slot transition before applying any step.
- [ ] 2.2 Implement the deterministic plan runner using the same transition
      machinery as foreground routed intents.
- [ ] 2.3 Emit plan-decided, plan-step, and plan-overridden trace events.

## 3. Correction UX
- [ ] 3.1 Extend route receipts to render the whole path and alternatives.
- [ ] 3.2 Add plan-level rewind that restores the pre-step-1 snapshot and
      re-dispatches the original utterance under a chosen class or plan.
- [ ] 3.3 Keep immediate switch-route only for plans that made no world change;
      otherwise force full rewind.

## 4. Verification
- [ ] 4.1 Unit tests for parser rejection when `route_plans.enabled` is false.
- [ ] 4.2 No-LLM orchestrator tests with stub contextual-router verdicts:
      valid two-hop route, valid route+return, invalid missing edge, invalid
      slot, and over-`max_steps` rejection.
- [ ] 4.3 Flow fixtures/cassettes for the dogfood story exercising a cross-room
      route, bad-route rewind, and alternative interpretation.
- [ ] 4.4 Replay test proving recorded route plans do not call a live LLM.

## 5. Adopt + document
- [ ] 5.1 Enable route plans in one dogfood room where cross-room commands are
      expected.
- [ ] 5.2 Update semantic-routing and authoring docs; trim/delete this proposal
      after the shipped behavior is documented.
```

## Verification

Automated coverage must use stubbed contextual-router responses and recorded
cassettes only. No test should call a real LLM or paid model. The minimum
verification set is:

- focused Go tests for verdict parsing, plan validation, execution, and rewind;
- flow fixtures that replay recorded `route_plan` decisions without model calls;
- runstatus unit tests that prove the plan receipt renders and the rewind RPC
  passes the plan `DecisionID`.

## Open questions

1. Should v1 support returning by snapshot, or only by declared story route?
   *Lean: declared route only.* Snapshot return is surprising because it skips
   authored transitions and side effects.
2. Should the LLM be allowed to propose navigation-only plans with no final
   intent? *Lean: yes, but only as a validated sequence of declared intents.*
3. How much of the target room's intent surface should be exposed to the LLM?
   *Lean: only a bounded neighborhood from the story graph plus intent summaries,
   not the entire app prompt.*

## Non-goals

- No autonomous search over arbitrary room graphs in v1; the model proposes a
  bounded plan and the runtime validates it.
- No hidden world mutation, synthetic transition, or undeclared return jump.
- No automatic synonym promotion from accepted multi-hop plans.
- No live-model tests in CI.
