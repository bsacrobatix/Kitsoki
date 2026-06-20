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
multi-turn sub-flow rather than complete in one shot, the gate stops one-shotting
it and instead **drives a real, persisted session to a settled resting place
synchronously** — the hook stays blocked for the duration, under a generous budget
that replaces the 5s fast-path cap — while **surfacing live progress** so the user
watches the work happen, and with a **safe-abort** that never strands the tree.

The execution is **synchronous, not backgrounded** — deliberately. A conflicting
rebase leaves the tree mid-rebase: a transient, inconsistent state on which no
other work can proceed. "Hand off and return control" would be a *false* freedom —
the agent has nothing useful to do until the tree is whole again — so the honest
posture is to keep the turn blocked (the user genuinely is waiting) and instead
spend the design effort on **feedback**, so the wait is not a degraded experience
versus a normal LLM turn (where the user would see the resolver's tool calls
stream by). Kitsoki must show the same: which conflict, which round, what ran.

One sentence: *the intercept gate escalates from a stateless one-shot to a
budgeted, abortable, traced, progress-surfacing synchronous drive the moment the
resolved command's execution doesn't terminate in one round.*

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
| trace event | `intercept.escalated` (working name) | `input`, `intent`, `session_id`, `reason: multi_turn` | the gate escalated a recognized command from one-shot to a synchronous multi-turn drive against a persisted session; the durable pointer the agent's ephemeral report references and the live-watch surface keys on. |
| trace event | `intercept.resolved` / `intercept.aborted` | `session_id`, `outcome`, `rounds`, `tokens` | the driven flow settled / was safe-aborted; closes the loop opened by `intercept.escalated`. |
| progress event | `intercept.progress` (working name) | `session_id`, `round`, `room`, `note` | one per round / host-call while the synchronous drive runs; the feed the live-watch surface and (if Claude streams hook stderr) the in-Claude progress lines render from. |

## The model

```
prompt ──▶ Classify ──▶ gate (§2 of prompt-intercept.md)
                          │ clean, single-command match
                          ├─▶ OneShot (stateless, ≤5s, no-LLM)         [unchanged]
                          │
                          │ clean match into a multi-turn / oracle flow
                          └─▶ create persisted session, drive SYNCHRONOUSLY to rest
                                 │   (SubmitDirect-style settle loop, real harness;
                                 │    hook stays blocked; progress streams to the
                                 │    live-watch surface + the durable trace)
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
`intercept.escalated` (opened) paired with `intercept.resolved` / `intercept.aborted`
(closed) plus the per-round `intercept.progress` stream, so a `kitsoki trace` / web
surface can show "kitsoki recognized this command, drove session X, which resolved
1 conflict in 3 rounds / 42k tokens, then merged" — live while it runs and as a
durable record after. These extend the existing `intercept.matched` / `.passed`
pair ([`trace.go:103`](../../internal/trace/trace.go#L103)) — likely a small
`tracing` follow-up if the web consumer needs to render them.

## Engine seams & invariants

- **Escalation trigger.** The gate must distinguish "completes in one round" from
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
  **Claude-timeout strand risk:** if Claude hits its own hook `timeout` it may
  kill the hook process outright, leaving no chance to run abort in-process. Two
  defenses: (a) kitsoki's internal budget must be set strictly *below* the
  installed Claude `timeout` so kitsoki always reaches safe-abort first; (b) a
  next-start reconciler (the [stale-worktree dogfood
  test](../../internal/orchestrator/dogfood_smoke_test.go) already detects a
  mid-rebase tree) can offer to abort a tree stranded by an out-of-band kill.
- **Budget — two caps, both must be lifted.** The 5s `hookRunTimeout`
  ([hook.go:43](../../cmd/kitsoki/hook.go#L43)) is kitsoki's own fast-path cap.
  *But Claude Code also imposes its own* — a `UserPromptSubmit` hook defaults to a
  **30s timeout**, after which Claude cancels the process (spike, §"Stderr
  spike"). So the multi-turn drive needs (a) kitsoki's internal wall-clock + token
  budget lifted past 5s for this case, **and** (b) `kitsoki hook install` to write
  a raised `timeout` (e.g. `600`) on the hook entry so Claude doesn't kill it
  mid-rebase. kitsoki's budget must sit *under* the installed Claude timeout. A
  blown budget triggers safe-abort; fail-open still governs *setup* (if the
  session/harness can't be stood up, the prompt passes through untouched). Claude
  cancelling the hook on its own timeout is itself a strand risk — see safe-abort.
- **Feedback — the live channel is the kitsoki surface, not Claude's UI.** The
  spike (§"Stderr spike") settled the mechanism: a blocking `UserPromptSubmit`
  hook **cannot** stream progress into Claude's transcript — stderr is buffered
  until exit, and `statusMessage` is a static spinner, not a progress feed. So the
  only *real-time* play-by-play (the equivalent of watching the LLM's tool calls
  stream) is surface (2). Two surfaces, with honest scope:
  - **(1) Final block report — in Claude, on exit.** Extend
    [`composeInterceptReport`](../../cmd/kitsoki/hook.go) to enumerate *every
    round's* host-call bullets, so the in-the-moment Claude report is a complete,
    auditable account ("resolved 2 conflicts over 3 rounds; here's each `git` /
    `Edit` that ran"). This is what the user sees *inside Claude* — comprehensive,
    but only at the end.
  - **(2) Live progress — the persisted session, off to the side.** The drive runs
    a real, persisted session emitting `intercept.progress` + `oracle.call.*`
    events in real time, watchable in `kitsoki web` / TUI / `kitsoki trace
    --follow` **while the hook blocks**. This is the *only* real-time surface, and
    it lives outside Claude.

  The honest consequence: **inside Claude the user sees a spinner, then a full
  report**; the live, tool-call-by-tool-call view requires glancing at the kitsoki
  surface. The synchronous wait is therefore as *auditable* as the LLM turn (more
  so — every step is recorded), but its in-Claude *liveness* is a spinner, not a
  stream. Mitigation: the installed hook's `statusMessage` (if it renders during
  the block — unverified) can carry a one-line "kitsoki resolving conflicts —
  `kitsoki trace --follow <id>`" pointer so the user knows where the live view is.

## Stderr spike (resolved)

A spike against the official Claude Code hooks reference settled the feedback
mechanism — the result shaped the synchronous design above:

- **No live stderr.** A `UserPromptSubmit` hook's stderr is **buffered and shown
  only after the process exits** (the exit-2 path), never streamed line-by-line
  while it runs. There is no incremental-output channel into Claude's transcript
  during a blocking hook.
- **`statusMessage` is a static spinner**, not a progress feed — set once, no
  mid-execution updates. At best it carries a one-line "watch here" pointer.
- **Claude imposes a 30s default timeout** on `UserPromptSubmit` hooks,
  configurable via a `timeout` field on the hook entry — so the installer **must**
  raise it (e.g. `600`) for the multi-turn case, independent of kitsoki's own cap.
- **stdout quirks:** `decision:"block"` + `reason` is the path that both bypasses
  the model and shows text to the user (what kitsoki already uses); plain stdout on
  exit 0 is currently unreliable (open upstream bugs). Stick with the block path.

Consequence already folded in: live progress = the kitsoki surface (web / TUI /
`trace --follow`); in-Claude = a spinner during, a complete report on exit; the
install path raises the Claude `timeout`.

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

## 1. Escalation trigger
- [ ] 1.1 Room flag `intercept_drive: rest` (or chosen mechanism) + load-time invariant
- [ ] 1.2 Gate branches OneShot → synchronous multi-turn drive on the flag
- [ ] 1.3 `intercept.escalated` trace event

## 2. Synchronous driver + feedback
- [ ] 2.1 Create a persisted session from the gate; drive it SYNCHRONOUSLY via the
        SubmitDirect settle loop with a REAL harness wired for host.oracle.task
- [ ] 2.2 Lift BOTH caps for the multi-turn case: (a) kitsoki's internal
        wall-clock + token budget past 5s; (b) `kitsoki hook install` writes a
        raised Claude `timeout` (e.g. 600) on the UserPromptSubmit entry, with
        kitsoki's budget sitting under it; fail-open still governs setup
- [ ] 2.3 Multi-round block report — extend composeInterceptReport to enumerate
        every round's host-call bullets; `intercept.resolved` on completion
- [ ] 2.4 Live-watch surface (the only real-time channel): per-round
        `intercept.progress` events on the persisted session (watchable via web /
        TUI / `kitsoki trace --follow`); optional `statusMessage` "watch here" pointer

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

1. **Escalation trigger** — structural room flag vs. empirical "OneShot didn't
   terminate". *Lean: structural flag* — explicit, load-time-checkable, and it
   avoids running OneShot's side effects only to discover it should have escalated.
   The flag also documents which rooms are multi-turn.
2. **Feedback channel during the synchronous block** — *Resolved (spike, below).*
   Stay synchronous, surface progress, do not background: a conflicting rebase
   leaves the tree transient, so returning control would be false freedom. The
   spike settled the in-Claude question — **no live stream is possible** — so the
   live surface is the persisted kitsoki session (web / TUI / `trace --follow`) and
   the in-Claude account is the final block report. No further verification needed
   except whether `statusMessage` renders a one-line live-view pointer during the
   block (nice-to-have, not load-bearing).
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
