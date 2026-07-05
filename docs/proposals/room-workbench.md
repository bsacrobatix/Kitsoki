# Runtime: The room workbench primitive

**Status:** Draft v1. Nothing implemented yet.
**Kind:**   runtime (story spillover — see Impact)
**Epic:**   usable-kitsoki.md

**Supersedes** [`ad-hoc-workbench.md`](ad-hoc-workbench.md) (deleted by this
proposal). That epic's Slice 1 open question — "is the free-form landing a
`mode: conversational` room, a full-tool `host.agent.task` room, or just
`main` + off-ramp?" — is answered here: **a full-tool task room, governed by
WS** (effect taxonomy + toolbox enforcement + sandbox), not a new permission
system. `ad-hoc-workbench.md` Slices 2–4 (the ambient miner, `/mine`, blank
project roots) are a separate future proposal — see Non-goals.

## Why

`stories/dev-story/rooms/landing.yaml` already *is* a governed, work-capable
free-form floor: a full-tool `landing_agent` (`Read, Grep, Glob, Edit, Write,
Bash`) dispatched on `on_enter`, gated `write_mode: read_only` so mutating
tool calls hold for an operator grant
(`internal/host/write_mode_gate.go:1-56`), an `agent_off_ramp: {agent:
agent_qa}` Q&A floor for genuine no-match prose, a `default_intent: work`
catch-all that captures free text into the task loop, and an "ad-hoc plan"
propose→accept→apply→verify sub-loop
([`docs/stories/ad-hoc-plan.md`](../stories/ad-hoc-plan.md)). It is, by a
wide margin, the best UX in the product — and every line of it is
hand-rolled: ~150 lines of YAML plus explanatory comments
(`stories/dev-story/rooms/landing.yaml:1-30`), a bespoke agent declaration
using the legacy `tools:` / `bash_profile:` / `external_side_effect:` triplet
rather than the shipped WS `toolboxes:` / `effect:` vocabulary
(`stories/dev-story/app.yaml:146-151`; contrast
[`state-machine.md#agent-toolboxes`](../stories/state-machine.md)), and four
files (`applying.yaml`, `verifying.yaml`, `plan_done.yaml`, the `work` arc)
that any second story wanting the same floor would have to copy by hand.

Two of the mechanisms it depends on are already engine-level and reusable:
`write_mode: read_only` (`internal/host/write_mode_gate.go`) and
`routing.free_form_fallback` — an app can declare, or the loader can
auto-detect, one canonical work-intake room that every non-conversational
room without its own `default_intent`/`agent_off_ramp` falls back to on
unmatched free text
([`semantic-routing.md` §1.6](../architecture/semantic-routing.md)). dev-story
relies on the **auto-detect** path (`stories/dev-story/app.yaml` declares no
explicit `routing:` block) — `R1` ("free text that isn't an authored intent
produces talk, not work") is *already fixed for dev-story specifically*. What
is missing is the room-level primitive: a declarative `workbench:` block any
room in any story can add to get the same governed floor — the agent
dispatch, the write-mode gate, the off-ramp, and the free-text capture — as
one small piece of config instead of a hand-authored recipe, bound to the
shipped WS vocabulary instead of the legacy per-field mechanism, and (Studio
MCP permitting) usable from a Claude-subscription session.

## What changes

**One sentence:** a room may declare a `workbench:` block instead of
hand-rolling `write_mode` + `agent_off_ramp` + an `on_enter host.agent.task` +
a free-text capture arc; the loader **desugars** it at load time into exactly
those four already-shipped primitives, wired to the declared agent's WS
`toolbox:` + `effect:` (not the legacy `tools:`/`bash_profile:` fields), with
load-time invariants that fail fast on a workbench whose agent can't actually
do work or whose declared capability disagrees with its runtime posture.

This is intentionally **not** a new execution engine or a new permission
system — it is a macro over primitives that already exist (`write_mode`,
`agent_off_ramp`, `on_enter`/`host.agent.task`, `default_intent` /
`free_form_fallback`), the same way `imports:` and `extends:` are load-time
desugaring today. dev-story's `landing.yaml` becomes the reference
consumer: replacing its hand-rolled block with `workbench:` is Task 3.1
(below), proving the primitive against its most demanding existing user
before any other story adopts it.

## Impact

- **Code seams:** `internal/app/loader.go` (new `Workbench` struct on
  `State`, desugaring pass alongside the existing `imports:`/`extends:`
  expansion and the `roomDispatchesAgent` precondition check at
  `internal/app/loader.go:2010-2036`); `internal/host/agents.go:731`
  (`enforceToolbox` — the workbench's synthesized `on_enter` dispatch must
  resolve through this exact policy, not a bespoke path);
  `internal/orchestrator/offpath.go:337-422` (`maybeOffRamp` — unchanged
  mechanism, but a workbench room's synthesized `default_intent` claims
  free text *before* a no-match code is ever produced, so `maybeOffRamp`
  only ever sees the residual genuine-Q&A case, exactly as it does for
  `landing` today).
- **Vocabulary:** one new state-level YAML block (`workbench:`), no new
  effects or host calls — table below.
- **Stories affected:** `stories/dev-story/rooms/landing.yaml` migrates onto
  the primitive (Task 3.1). No other story is required to adopt it; this is
  additive.
- **Backward compat:** default-off. A room with no `workbench:` block is
  byte-for-byte unaffected. Existing hand-rolled workbenches (dev-story's
  `landing`) keep working unmigrated until Task 3.1 lands.
- **Docs on ship:** `docs/stories/state-machine.md` (new §"Room workbenches"),
  `docs/architecture/room-workbench.md` (new), `stories/dev-story/README.md`
  (the `landing` section updated to point at the primitive).

## Vocabulary changes

| Kind | Name | Shape | Notes |
|---|---|---|---|
| state block | `workbench:` | `{ agent, prompt, acceptance_schema, capture_slot?, off_ramp_agent?, plan? }` | desugars to `write_mode`, `agent_off_ramp`, `on_enter`, `default_intent` at load time |
| load-time invariant | workbench agent capability | — | the named `agent:` must declare WS `toolbox:` + `effect:` (`internal/app/types.go` agent declaration) with `effect ∈ {write, external}` — a workbench that can only read isn't a workbench, it's `agent_off_ramp` |
| load-time invariant | workbench room shape | — | a state with `workbench:` may not *also* declare `write_mode`, `agent_off_ramp`, or a conflicting `default_intent` — pick one authoring path, don't blend them |
| world key (synthesized) | `<room>_note` | object | the close-out note bind target (mirrors `landing_note`); name derived from the room, no author choice needed |

## The model

```
free text ──▶ [routing: free_form_fallback / room default_intent]
                        │ (deterministic, no LLM — semantic-routing.md §1.6)
                        ▼
              workbench room's synthesized capture intent
                        │
                        ▼
              on_enter: host.agent.task(agent, prompt, acceptance_schema)
                bound to the agent's WS toolbox + effect class
                (enforceToolbox, agents.go:731)
                        │
              write_mode: read_only gate on every mutating call
                (write_mode_gate.go — operator grant or headless deny)
                        │
                        ▼
              close-out note bound ─┬─▶ plain summary (done)
                                    ├─▶ landing_note.route  → deterministic
                                    │   emit_intents into an authored pipeline
                                    │   (initial_world set BEFORE the target
                                    │   room's on_enter runs — never
                                    │   LLM-mediated seeding)
                                    └─▶ landing_note.plan   → propose/accept/
                                        apply/verify sub-loop (ad-hoc-plan.md)

pure Q&A that never reaches the capture intent (rare — the capture sink
claims almost everything) ──▶ agent_off_ramp (read-only converse, unchanged)
```

**Deterministic dispatch to an authored pipeline stays a hard rule.** When
the workbench agent's close-out note names a target pipeline
(`landing_note.route.intent`), the transition that fires it is an ordinary
`on:` arc with `effects: [set: {...}, emit_intents: [...]]` — the agent
*names* an intent, the engine *sets the world and fires it*, deterministically
and recorded. The agent never constructs the intent call or the target
room's `initial_world` itself. This is the GU cycle-6–8 lesson (three
independent failures at this exact seam: an LLM operator won't reliably pass
`initial_world`) and it is non-negotiable for `workbench:` consumers: the
desugared arc always routes through a real transition with real `set:`/`emit`
effects, never a raw agent-constructed call.

## Decision recording

No new event kind. The synthesized `on_enter host.agent.task` records the
ordinary `agent.call.*` trace events; the write-mode gate records
`machine.write_mode_granted` (or a headless deny) exactly as it does for
`landing` today (`internal/host/write_mode_gate.go`); a route-out records
the ordinary `TransitionApplied` + `emit_intents` provenance. The one new
thing worth a trace-legible label is which room's floor produced a given
agent dispatch, so a reviewer scanning a trace from an unfamiliar story can
tell "this was the workbench, not an authored pipeline room" without reading
YAML — carried as an existing field (`agent.call` already records the
dispatching state path) rather than a new event.

## Engine seams & invariants

- **Loader** (`internal/app/loader.go`): a `workbench:` block on a `State`
  expands, before the existing `roomDispatchesAgent` / write-mode
  precondition pass (`internal/app/loader.go:1929`), into:
  - `write_mode: read_only` (unless the agent's resolved effect class is
    already `≤ read`, which is a load error per the invariant below)
  - `agent_off_ramp: { agent: <off_ramp_agent, default: agent's persona> }`
  - one synthesized `on_enter` entry invoking `host.agent.task` with
    `agent`, `prompt`, `acceptance.schema: acceptance_schema`,
    `working_dir: "{{ world.workdir }}"`, bound to `<room>_note`
  - one synthesized catch-all intent (the `capture_slot`, default `request`)
    set as the room's `default_intent`, or wired via
    `routing.free_form_fallback` when the workbench room is the app's
    canonical floor (mirrors dev-story's current auto-detect path)
  - if `plan: true`, a load-time check that `acceptance_schema` embeds
    the shared `plan` object shape
    ([`stories/dev-story/schemas/plan.json`](../../stories/dev-story/schemas/plan.json))
    — the propose/accept/apply/verify rooms themselves stay hand-authored
    (copy dev-story's `applying`/`verifying`/`plan_done` as the reference),
    since generating a checkpointed sub-loop from a boolean flag is more
    engine magic than the win is worth; the invariant only guards the
    *contract* the planner and the hand-wired rooms must agree on.
  - Fail-fast invariants: the named `agent:` must exist in `agents:` and
    declare `toolbox:` + `effect:` with `effect ∈ {write, external}`
    (extends the existing effect-taxonomy hard-fail,
    `internal/app/types.go:640-650`); a room may not declare `workbench:`
    alongside a hand-authored `write_mode`, `agent_off_ramp`, or
    `default_intent` (pick one authoring path).
- **Dispatch**: the synthesized `on_enter` call is an ordinary
  `host.agent.task` invocation — it goes through `enforceToolbox`
  (`internal/host/agents.go:731`) exactly like any other agent dispatch.
  `workbench:` adds **zero** new permission surface; WS's toolbox +
  effect-class declaration is the only thing that governs what the
  synthesized agent may touch.
- **Precondition (must land before Task 1 starts):** Studio MCP tool
  injection under the `claude` backend. The evaluation that motivates this
  epic recorded a GU goal-seeker finding (cycle 3, "WM.10": the Studio MCP
  reaches a dispatched maker under the `codex` backend but not `claude`) —
  that specific ledger entry could not be re-verified against this worktree
  (the GU `log.jsonl` referenced is empty per the same evaluation sweep).
  Current MCP-config code on `main` (`internal/host/agent_backend_codex.go:85-274`,
  `internal/host/agent_backend_claude.go:1-58`) shows both backends translate
  `--mcp-config` into their own wire format, which suggests this may already
  be fixed or was backend-config-specific rather than structural — **re-check
  at Task 1 kickoff with a live claude-backend workbench dispatch before
  assuming this precondition is still open.** If it is still open, a
  Claude-subscription operator cannot get the "Claude Code inside a room"
  experience `workbench:` is meant to deliver, and that gap blocks the
  primitive's headline claim regardless of how clean the YAML is.

## Backward compatibility / migration

Fully additive. `workbench:` is new syntax; no existing `State` field
changes shape. dev-story's `landing.yaml` is migrated by hand in Task 3.1 —
mechanical (delete the hand-rolled `write_mode`/`agent_off_ramp`/`on_enter`/
`work` arc, add one `workbench:` block) but reviewed by hand rather than
scripted, since `landing.yaml` also carries the ad-hoc-plan sub-loop and the
on-path-bail routing that the macro does not (and should not) auto-generate.
The 61 existing dev-story flow fixtures are the regression suite for that
migration — if the desugared room doesn't reproduce their behavior, the
macro is wrong, not the fixtures.

## Tasks

```
## 1. Engine
- [ ] 1.1 `Workbench` struct on `State`; loader desugaring pass (write_mode,
      agent_off_ramp, on_enter host.agent.task, default_intent/free_form_fallback)
- [x] 1.2 Load-time invariants: agent toolbox+effect required (effect ∈
      {write, external}); mutual-exclusion with hand-authored write_mode/
      agent_off_ramp/default_intent; clear error messages
- [ ] 1.3 Re-verify the claude-backend Studio MCP precondition with a live
      dispatch; file/fix if still broken before proceeding
- [x] 1.4 Decision recording: confirm agent.call / write_mode_granted /
      TransitionApplied provenance is unchanged and legible for a desugared room.
      Confirmed via a flow-fixture test on workbench_smoke
      (`internal/testrunner/flows_workbench_smoke_trace_test.go`): the
      synthesized on_enter dispatch's AgentCalled event carries
      `state_path == "bench"`, using host_cassette: (not host_handlers:,
      which replaces the handler wholesale and never writes AgentCalled).
      Also lands the S6 usable-kitsoki-gate producer contract's minimal
      honest version (`docs/tracing/usable-kitsoki-gate.md`): a
      `usable_kitsoki_gate` object on the existing `turn.end` payload
      (`internal/orchestrator/workbench_gate_signal.go`, no new event kind)
      carrying `candidate_completed`/`silent_bounce`/`misroute_adjacent`/
      `evidence_refs`, computed from the dispatching state's on_error
      outcome — not yet joined against a scenario's `expected_effects` list,
      since S4's scenario IR was not present in this worktree at the time
      (documented gap in the file's own doc comment). Field names are
      cross-checked against
      `tools/arena/arena/plugins/usable_kitsoki_gate_schema.json` by a
      deterministic test.

## 2. Verification
- [x] 2.1 Stateless unit: loader desugars a minimal `workbench:` block into
      the expected State shape; invariant violations produce the expected
      load errors
- [ ] 2.2 Flow fixture: a synthetic story with one `workbench:` room —
      capture free text (no LLM routing), dispatch (stubbed), write-mode
      hold + grant, off-ramp Q&A path, route-out to a second authored room
      with deterministic world set before its on_enter
- [ ] 2.3 dev-story's existing 61-fixture flow suite still passes unmigrated
      (this task doesn't touch landing.yaml yet — proves additivity)

## 3. Adopt + document
- [ ] 3.1 Migrate `stories/dev-story/rooms/landing.yaml` onto `workbench:`;
      all 61 dev-story flow fixtures pass unmodified in behavior
- [ ] 3.2 `docs/stories/state-machine.md` §"Room workbenches";
      `docs/architecture/room-workbench.md`; update dev-story README; trim/
      delete this proposal
```

## Verification

Everything above the claude-backend precondition (1.3) and the agent's own
draft quality is stateless or flow-fixture-testable with no LLM:

- `kitsoki turn` probes exercise the loader's desugaring directly (feed a
  minimal app with a `workbench:` room, assert the expanded `State` shape).
- Invariant violations are load-time unit tests (a workbench agent with
  `effect: read` fails to load; a room with both `workbench:` and hand-rolled
  `write_mode:` fails to load) — table-driven, alongside
  `write_mode_gate_test.go`'s existing style.
- The flow fixture in Task 2.2 stubs the one `host.agent.task` call; the
  write-mode gate, routing, and off-ramp paths are exercised for real
  end-to-end, no LLM.
- Task 1.3 (the claude-backend precondition) is the one item that needs a
  live dispatch to confirm — a one-time manual check, not a recurring test.

## Open questions

1. **Capture-slot naming** — always synthesize the intent name from the room
   (`<room>_capture`) or let the author name it? *Lean: synthesize* — the
   author never needs to type the intent name (the `work` sink is entirely
   internal machinery today), and a fixed name avoids collisions across
   imports the way bare intent names already must not collide.
2. **Does `plan: true` belong in v1, or should the ad-hoc-plan sub-loop stay
   a documented hand-authored pattern until a second story wants it?** *Lean:
   ship the invariant (schema-contract check) in v1 since it's cheap, defer
   any code generation of the sub-loop rooms until a second real consumer
   proves the shape generalizes* — one example is not enough evidence to
   freeze the applying/verifying/plan_done room shapes into the macro.

## Non-goals

- **The ambient miner / `/mine` control surface / blank project roots**
  (`ad-hoc-workbench.md` Slices 2–4). Those are the *proposing new structure
  from mined history* half of that epic; this proposal is only the
  *governed floor* half. A future proposal can retarget the miner at
  `workbench:` rooms once this primitive exists.
- **A new permission model.** WS `toolbox:`/`effect:` declarations remain
  the only mechanism `workbench:` honors — this proposal adds zero new
  grants, scopes, or sandboxing tiers.
- **LLM-mediated dispatch of `initial_world`.** Route-outs to authored
  pipelines always go through a real transition's `set:`/`emit_intents`
  effects, never an agent-constructed call.
- **Code-generating the propose/accept/apply/verify sub-loop rooms** from
  `plan: true` (see Open question 2) — deferred until a second consumer
  exists.
