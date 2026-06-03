# Runtime: idempotent on_enter host calls (`once:`)

**Status:** Draft v1. Nothing implemented yet.
**Kind:**   runtime
**Epic:**   — standalone

## Why

`on_enter` re-fires on `/reload` (`RerunOnEnter`, `internal/orchestrator/reload.go:114-182`)
and on any re-entry (self-target, `on_error:` loops). Its own doc comment
admits the cost: *"If the chain … calls an oracle … those side effects will
repeat."* For an expensive, non-idempotent call (`host.oracle.decide/task/
converse`) that is a real bug — a dogfood session proved it: at
`proposal_brief`, a `/reload` re-ran the `brief_check` LLM call **and** a
transient error then bounced the operator out to intake via `on_error`
(trace `5360cfaa…`, turn 4: `harness.called host.oracle.decide` →
`machine.transition on_error` → `turn.end reloaded`).

Today the only fix is a hand-rolled guard on every such call —
`when: "<result_key> == ''"` — plus a clear of that key on every re-run
intent. `stories/dev-story/rooms/proposal_*.yaml` now carries this in five
rooms (brief / existing-state / completeness / references / draft); `prd`
and `bugfix` have the same latent exposure. It is repeated, easy-to-forget
boilerplate guarding a property the engine already has all the information
to enforce — the bind target **is** the cache. This generalizes it.

## What changes

Add an opt-in `once: true` flag to an `invoke:` effect. **When set, the
engine skips the invoke if its `bind:` targets are already populated
(non-default) — the call runs once to produce its result, and re-entry
(reload, self-transition, `on_error`) re-renders from the cached world
instead of recomputing.** To force a re-run, clear the bind target (which
re-run intents already do). One word replaces the `when`-guard + the
authoring discipline to remember it.

## Impact

- **Code seams:** `internal/app/types.go:728` (the `Effect` struct — add the
  field next to `When`/`Bind`/`Background`); `internal/machine/machine.go:1493`
  (`case eff.Invoke != ""` in the effect runner — the skip check);
  `internal/orchestrator/reload.go:114-182` (the behavior it most obviously
  protects, though the check is entry-kind-agnostic).
- **Vocabulary:** one new effect field (table below). No new host call, no
  new world key, no new event kind required (a skip is recordable on the
  existing `EffectApplied`).
- **Stories affected:** none forced. `stories/dev-story/rooms/proposal_*.yaml`
  can drop their hand `when:`-guards once this lands (the clears stay — they
  are still how you force a re-run).
- **Backward compat:** opt-in, default off. Absent `once:`, behavior is
  unchanged (`/reload` still re-runs as documented). No cassette/flow churn.
- **Docs on ship:** `docs/stories/state-machine.md` §"on_enter must be
  idempotent" (it currently tells authors to hand-guard — point it here),
  `docs/architecture/`.

## Vocabulary changes

| Kind | Name | Shape | Notes |
|---|---|---|---|
| effect field | `once` | `bool` (on an `invoke:` effect) | Skip the invoke when all its `bind:` targets are already non-default. Only meaningful with `bind:`. |

## The model

```
enter state (fresh | reload | self | on_error)
        │
        ▼
   on_enter effect: invoke (once: true)
        │
        ├─ all bind targets non-default?  ── yes ──▶  SKIP (record EffectApplied{skipped:"cached"})
        │
        └─ no ──▶ dispatch host call ──▶ bind result   (the cache fill)
```

The call is **deterministic with respect to its cache**: given a populated
bind target, the engine provably does not re-invoke. "Non-default" is the
world key not equal to its schema zero-value (string `""`, object `{}`,
list `[]`) — the same test the hand-guards use, now computed by the engine
from the world schema rather than authored per call.

## Decision recording

No interpretive decision is added — this is deterministic flow control. The
skip is recorded on the existing `EffectApplied` event with a
`skipped: "cached"` (or `once_skipped: true`) field so a trace consumer can
see the call was elided and why. The moat is unaffected: the *original*
`oracle.decide` that filled the cache is still recorded as before; a reload
simply doesn't manufacture a second, misleading decision event.

## Engine seams & invariants

- Skip check sits in `applyEffectsTraced` at `machine.go:1493`, before the
  `HostInvocation` is appended to `hostCalls`.
- Load-time invariant: `once: true` on an effect with no `bind:` is a
  load error (`once` with nothing to cache is meaningless — fail fast with a
  clear message, mirroring the existing `background` validation at
  `types.go:768`).
- Entry-kind-agnostic by design: the same check protects self-transitions
  and `on_error` re-entry, not just `/reload`. (The proposal_* rooms' guards
  are already entry-agnostic for this reason.)

## Backward compatibility / migration

Default off; existing stories and cassettes unchanged. Migration is
*optional* and mechanical: replace a guarded invoke

```yaml
- when: "world.proposal_brief_decision.verdict ?? '' == ''"
  invoke: host.oracle.decide
  bind: { proposal_brief_decision: submitted }
```

with

```yaml
- invoke: host.oracle.decide
  once: true
  bind: { proposal_brief_decision: submitted }
```

The re-run intents keep their `set: { proposal_brief_decision: {} }` clears.

## Tasks

```
## 1. Engine
- [ ] 1.1 Add `Once bool` to app.Effect (types.go:728)
- [ ] 1.2 Skip logic in applyEffectsTraced (machine.go:1493): when Once && all Bind targets non-default → skip + EffectApplied{skipped:"cached"}
- [ ] 1.3 Load-time invariant: `once` requires `bind` (clear error)

## 2. Verification
- [ ] 2.1 `kitsoki turn` re-entering a state with a populated bind target shows no host_call fired
- [ ] 2.2 Flow fixture: the proposal_reload_safe pattern, but driven by `once:` instead of a hand guard; legacy (un-flagged) path still re-runs
- [ ] 2.3 Reload smoke: RerunOnEnter on a `once:` room fires zero oracle calls

## 3. Adopt + document
- [ ] 3.1 Migrate stories/dev-story/rooms/proposal_*.yaml off the hand-guards onto `once:`
- [ ] 3.2 Update state-machine.md §"on_enter must be idempotent"; trim/delete this proposal
```

## Verification

No LLM needed: a flow fixture seeds a state whose bind target is already
populated, re-enters via a self-transition, and asserts (a) the world value
is unchanged and (b) no `HostInvoked` event fired for that namespace —
exactly `stories/dev-story/flows/proposal_reload_safe.yaml`, but proving the
engine flag rather than the hand guard. The reload path is covered by a
`RerunOnEnter` unit asserting zero host calls on a `once:` room.

## Open questions

1. **Authoring `/reload` that *wants* the re-run.** During authoring you
   `/reload` to test a prompt edit — `once:` would skip it. Options: (a) a
   `/reload --force` that bypasses `once` for one turn; (b) accept that you
   clear the world key to re-run; (c) `once:` honored only outside a
   "dev/authoring" session mode. *Lean: (a) — a force flag on the reload
   command, since clearing world by hand mid-authoring is awkward.*
2. **Name.** `once:` vs `cache_bind:` vs `idempotent:` vs `skip_if_bound:`.
   *Lean: `once:` — shortest, reads as "run once".*
3. **"Non-default" for scalar binds.** An `int` bind target of `0` is
   ambiguous (unset vs a real 0 result). *Lean: document that `once:` suits
   object/string binds; for scalars, guard by hand.*

## Non-goals

- Changing the *default* `/reload` semantics (still re-runs un-flagged
  on_enter). This is opt-in only.
- Caching across sessions or persisting a separate cache — the bind target
  in world is the cache; nothing new is stored.
- Auto-inferring which calls are expensive — the author opts in per call.
