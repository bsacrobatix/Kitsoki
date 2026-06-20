# Runtime: Conflict-capable stateful intercept

**Status:** Draft v1. Prerequisite shipped (the git-ops exit-code/JSON-binding
fixes below); the stateful execution path is nothing-implemented-yet.
**Kind:**   runtime
**Epic:**   — standalone

## Why

The pre-LLM [prompt-intercept gate](../architecture/prompt-intercept.md) executes
a recognized command **statelessly, in one shot** —
[`Orchestrator.OneShot`](../../internal/orchestrator/orchestrator.go#L2013) runs a
single `machine.Turn` plus one round of host dispatch and returns, capped at a
`5s` budget ([`hook.go:43`](../../cmd/kitsoki/hook.go#L43)). That is exactly right
for a single self-contained command ("rebase onto main" when it applies cleanly,
"run the tests", "open the PR"). It **structurally cannot** drive a command whose
real execution is a *multi-turn, oracle-in-the-loop loop*.

The canonical case is **"rebase, and have the LLM resolve any conflicts."** When
the rebase conflicts, git-ops routes to the [`conflict`
room](../../stories/git-ops/rooms/conflict.yaml), whose `on_enter` runs
`host.oracle.task` against the `conflict_resolver` agent
([git-ops/app.yaml:27](../../stories/git-ops/app.yaml#L27)), then — only if the
verdict is `resolved:true` — emits `rebase_continue`, runs `git rebase --continue`
+ a build check, and finally routes to `branch_ops` (or escalates to the human).
This is inherently:

- **multi-round** — `conflict` → resolve → `rebase_continue` → build-check →
  `conflict_resolved`, each a separate post-bind emit the gate's OneShot never
  settles (it stops at the first resting place, `conflict`);
- **LLM-bound** — the resolver *is* an oracle call, so the gate's "no-LLM bypass"
  framing does not hold here (the same caveat prompt-intercept.md §6 already makes
  for `commit`/`squash`, which reach `host.oracle.decide`);
- **slow** — an agent reading and editing conflicted files is minutes, not the
  hook's 5 seconds;
- **stateful and dangerous** — a conflicting rebase leaves the tree mid-rebase;
  abandoning it (OneShot returns, nothing continues) **strands the working tree**.

A real-conflict integration test
([`TestGitOps_RebaseConflict_RoutesToConflictRoom`](../../internal/orchestrator/gitops_rebase_conflict_test.go))
now proves routing reaches `conflict`; the loop *past* it is the work here.

## What changes

Add a **stateful intercept execution path**: when a gated command would enter a
multi-turn sub-flow rather than complete in one shot, the gate stops trying to do
it in-process and instead **hands off to a real, persisted, background-driven
session** — returning to the agent immediately with a "resolving in the
background, watch here" report — then drives that session to a settled resting
place under a generous budget, with a **safe-abort** that never strands the tree.

One sentence: *the intercept gate escalates from a stateless one-shot to a
budgeted, abortable, traced background session the moment the resolved command's
execution doesn't terminate in one round.*

## Impact

- **Code seams:** [`cmd/kitsoki/intercept.go`](../../cmd/kitsoki/intercept.go)
  (`runInterceptEngine` — the gate), [`cmd/kitsoki/hook.go`](../../cmd/kitsoki/hook.go)
  (the Claude shim + budget), [`orchestrator.go:2013`](../../internal/orchestrator/orchestrator.go#L2013)
  (`OneShot` — extended or bypassed for the multi-turn case), the session
  store + scheduler the orchestrator already owns
  ([`startSessionListener`](../../internal/orchestrator/orchestrator.go#L704)).
- **Vocabulary:** a way for a room to declare "this is a multi-turn sub-flow,
  drive to rest, don't one-shot it"; an intercept handoff trace event. See table.
- **Stories affected:** git-ops only — the `conflict` room becomes reachable *and
  drivable* through the gate. Its YAML is already correct (rooms exist; the
  resolver agent is declared); no story rewrite, only the engine that drives it.
- **Backward compat:** purely additive. Single-command intercepts keep using the
  stateless OneShot fast path unchanged; the handoff fires only for flows that
  don't terminate in one round. Default behavior of existing bindings is identical.
- **Docs on ship:** `docs/architecture/prompt-intercept.md` (a §"Multi-turn
  commands" section), `docs/stories/state-machine.md` if a room flag is added.

## Vocabulary changes

| Kind | Name | Shape | Notes |
|---|---|---|---|
| room flag | `intercept_drive: rest` (working name) | `string` enum on a state | marks a room whose entry begins a multi-turn flow the gate must drive to rest, not snapshot after one round. Lets the gate decide handoff structurally instead of guessing. |
| trace event | `intercept.handoff` | `input`, `intent`, `session_id`, `reason: multi_turn` | the gate handed a recognized command to a background session; the durable pointer the agent's ephemeral report references. |
| trace event | `intercept.resolved` / `intercept.aborted` | `session_id`, `outcome`, `rounds`, `tokens` | the background flow settled / was safe-aborted; closes the loop opened by `intercept.handoff`. |

## The model

```
prompt ──▶ Classify ──▶ gate (§2 of prompt-intercept.md)
                          │ clean, single-command match
                          ├─▶ OneShot (stateless, ≤5s, no-LLM)         [unchanged]
                          │
                          │ clean match into a multi-turn / oracle flow
                          └─▶ create persisted session, drive in background
                                 │   (SubmitDirect-style settle loop, real harness)
                                 ▼
                      conflict ─resolve(oracle.task)─▶ rebase_continue ─build─▶ conflict_resolved ─▶ branch_ops
                                 │ resolved:false / build_fail / budget / panic
                                 └─▶ SAFE-ABORT: git rebase --abort ─▶ branch_ops (escalation view)
```

- **Interpretive (LLM, recorded):** the `conflict_resolver` verdict — already an
  `host.oracle.task` call, already traced. No new interpretive surface; the gate
  just lets it run.
- **Deterministic (engine, replayable):** the routing, the `git rebase
  --continue`, the build check, the abort. All already deterministic host calls in
  git-ops; the new code is the *driver* that settles the rounds, not new logic.

The driver is the existing settle machinery, not a new one: `SubmitDirect`
already settles multi-round post-bind emits via
[`settlePostBindEmits`](../../internal/orchestrator/orchestrator.go#L667). The
handoff path reuses it against a persisted session, with a real harness wired so
`host.oracle.task` reaches the model (the gate's hook path runs no harness today).

## Decision recording

The resolution decision is already recorded — `host.oracle.task` emits its
`oracle.call.*` events with the dispatched prompt and the returned verdict, the
provenance moat intact. This proposal adds only the **gate-level** record:
`intercept.handoff` (opened) paired with `intercept.resolved` / `intercept.aborted`
(closed), so a `kitsoki trace` / web surface can show "kitsoki recognized this
command, handed it to session X, which resolved 1 conflict in 3 rounds / 42k
tokens, then merged." These extend the existing `intercept.matched` / `.passed`
pair ([`trace.go:103`](../../internal/trace/trace.go#L103)) — likely a small
`tracing` follow-up if the web consumer needs to render them.

## Engine seams & invariants

- **Handoff trigger.** The gate must distinguish "completes in one round" from
  "enters a multi-turn flow" *before* committing to OneShot. Two options (Open
  question 1): structurally, the resolved intent's target room carries
  `intercept_drive: rest`; or empirically, OneShot lands at a non-terminal room
  with pending post-bind emits. Lean structural — explicit beats inference, and a
  load-time invariant can require the flag only on rooms reachable from an
  intercept binding.
- **Safe-abort is mandatory, not best-effort.** Any non-success exit of the
  driven flow (resolver `resolved:false`, build failure that the human doesn't
  rescue within budget, budget exhaustion, harness error, panic) MUST run the
  `conflict` room's existing `abort` arc
  ([conflict.yaml:53](../../stories/git-ops/rooms/conflict.yaml#L53), `git rebase
  --abort`) so the tree returns to a clean branch tip. The invariant: **the gate
  never leaves a session it started mid-rebase.** A test asserts the worktree is
  not mid-rebase after an abort (mirrors the
  [stale-worktree dogfood test](../../internal/orchestrator/dogfood_smoke_test.go)).
- **Budget.** The 5s hook cap is for the *synchronous* fast path. The background
  flow needs its own budget (wall-clock + token), independent of the hook, with
  the hook returning immediately once the session is created. A blown budget
  triggers safe-abort.

## Backward compatibility / migration

Existing single-command intercepts are unchanged: the fast path is still the
stateless ≤5s OneShot, and the handoff only triggers for rooms that opt into the
multi-turn drive. No cassette or flow fixture changes for the legacy path;
`stories/git-ops/flows/intercept_hub.yaml` keeps passing. The Claude hook's
**fail-open** rule still governs: if anything about the handoff can't be set up,
the prompt passes through to the model untouched.

## Tasks

```
## 0. Prerequisite (SHIPPED)
- [x] 0.1 Fix git-ops real exit codes (rebase/merge/pull) + clean-tree detection
- [x] 0.2 Fix host.run multi-line stdout_json binding so routing works un-mocked
- [x] 0.3 Real-conflict routing test reaches `conflict`

## 1. Handoff trigger
- [ ] 1.1 Room flag `intercept_drive: rest` (or chosen mechanism) + load-time invariant
- [ ] 1.2 Gate branches OneShot → background-session handoff on the flag
- [ ] 1.3 `intercept.handoff` trace event

## 2. Background driver
- [ ] 2.1 Create a persisted session from the gate; drive it via the SubmitDirect
        settle loop with a REAL harness wired for host.oracle.task
- [ ] 2.2 Independent wall-clock + token budget (decoupled from the 5s hook cap)
- [ ] 2.3 Hook returns immediately with a "resolving in background, watch here"
        report pointing at the session; `intercept.resolved` on completion

## 3. Safe-abort
- [ ] 3.1 Any non-success / budget-exhausted / panic path runs the `abort` arc
- [ ] 3.2 Invariant test: worktree is never left mid-rebase after a gate-started flow
- [ ] 3.3 `intercept.aborted` trace event

## 4. Verify + document
- [ ] 4.1 Real-conflict E2E: gate handoff resolves a conflict and lands branch_ops
        (oracle stubbed to actually edit the file — mirror ImplementingActuallyEditsFiles)
- [ ] 4.2 Abort E2E: resolver returns resolved:false → safe-abort → clean tree
- [ ] 4.3 Migrate to prompt-intercept.md §"Multi-turn commands"; trim/delete this proposal
```

## Verification

Without an LLM, via the dogfood-smoke harness
([`gitops_rebase_conflict_test.go`](../../internal/orchestrator/gitops_rebase_conflict_test.go)
+ [`dogfood_smoke_test.go`](../../internal/orchestrator/dogfood_smoke_test.go)
patterns): a real `git init` conflict repo, the real host registry, and the
`conflict_resolver` oracle **stubbed to actually edit the conflicted file** (the
[`ImplementingActuallyEditsFiles`](../../internal/orchestrator/dogfood_smoke_test.go#L875)
technique) — so `git rebase --continue` really succeeds and the flow really lands
`branch_ops`, deterministically and free. A second test stubs the resolver to
return `resolved:false` and asserts the safe-abort leaves a clean (not mid-rebase)
tree. The one genuinely LLM-needing test — does the *real* `conflict_resolver`
agent resolve a real conflict — stays gated/manual (memory: no automatic real-LLM
tests).

## Open questions

1. **Handoff trigger** — structural room flag vs. empirical "OneShot didn't
   terminate". *Lean: structural flag* — explicit, load-time-checkable, and it
   avoids running OneShot's side effects only to discover it should have handed
   off. The flag also documents which rooms are multi-turn.
2. **Async surface for the Claude hook** — the `UserPromptSubmit` block is
   synchronous and ephemeral; the agent can't await minutes. Does the handoff
   report point the user at `kitsoki web` / `kitsoki trace --turns <session>` for
   live progress, or does it block briefly then background? *Lean: return
   immediately, point at the web/trace surface* (the durable record per
   prompt-intercept.md §5). Confirm the block `reason` can carry a live-watch
   pointer usefully.
3. **Budget shape** — a single wall-clock cap, or wall-clock + token (the
   [oracle-capability-model](oracle-capability-model.md) budget surface)? *Lean:
   both, conservative defaults, configurable in the `intercept:` block.*
4. **Should this be an epic?** The driver (runtime), the trace events (tracing),
   and any web "live-watch" surface (tui) are separable. *Lean: keep as one
   runtime proposal until the driver lands; split the tracing/tui surfaces out
   only if they grow real design weight.*

## Non-goals

- Resolving conflicts for `merge`/`pull`/`squash` through the gate — same
  machinery once the rebase case works, but out of scope for v1.
- Changing the conflict_resolver agent's write-fence (tools `[Read, Edit]`, no
  Bash — [git-ops/app.yaml:39](../../stories/git-ops/app.yaml#L39)). v1 keeps it.
- A Codex/Copilot equivalent — they have no pre-model hook (prompt-intercept.md
  §4); this rides the Claude path only.
- Generalizing "drive to rest" as a public execution mode beyond the intercept
  gate — that overlaps [`execution-modes-and-gate-deciders.md`](execution-modes-and-gate-deciders.md)
  and should be reconciled there, not invented here.
