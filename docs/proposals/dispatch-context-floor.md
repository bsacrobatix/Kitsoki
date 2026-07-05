# Runtime: Dispatch context floor

**Status:** Draft v1. Nothing implemented yet.
**Kind:**   runtime
**Epic:**   usable-kitsoki.md

## Why

Every `host.agent.{task,decide,ask,converse}` dispatch shells out a fresh
`claude` CLI subprocess (`runClaudeOneShotReal`, `internal/host/agent_runner.go:234`;
persona forwarded via `--append-system-prompt`, `internal/host/agents.go:8-9,52-53`).
One live bugfix-pipeline drive (single-file parser fix, 10 `agent.call.complete`s)
measured **1,862,654 input tokens / 14,883 output**, with even the trivial
judge/decide calls floored at **~68k input tokens** and a `session.story`
snapshot serializing to **~774 KB** re-sent on calls that touch it
(`.context/token-bloat-finding.md`, local finding — not yet filed as a GitHub
issue). On subscription auth this is invisible ($0 metered); on metered
providers (the exact path the cost-efficiency benchmark is trying to prove
affordable) it is real money on every pipeline turn, and the harness
escalation ladder multiplies it across cells
(GU worktree `docs/goals/generalized-usage/decomposition.yaml:164-176`, WM.5).

This is the opposite of the "Claude Code inside a room" pitch: a direct
`claude` loop pays once for its context; kitsoki's route → journey-replay →
machine → nested-dispatch shape re-pays a large chunk of it on every hop
(source doc §1.2 item 4). The routing decision itself already does better —
`LiveHarness` puts one `cache_control: ephemeral` breakpoint on its stable
system-prompt prefix and reports `cache_read_tokens`/`cache_create_tokens`
per call (`internal/harness/live.go:36-52,215-217`) — but that harness only
answers "which intent," not the `host.agent.*` dispatches that do the actual
work, which have no equivalent seam. The GU goal named this gap without
cutting it: "W4 — Afford: MCP token reduction, context floor, budget gate,
non-Anthropic cost" (GU worktree `docs/goals/generalized-usage/plan.md:165`)
has no decomposed `decomposition.yaml` item — this proposal is that cut.

Separately, every foreground turn pays for a structural double-read: `loadJourney`
reconstructs state/world from the event log (`internal/orchestrator/orchestrator.go:2912`
→ `store.BuildJourney`), then the same turn calls `o.store.LoadHistory(sid)` to
build `RecentTurns` — "a second pass over the same rows loadJourney already
read" by the code's own comment (`internal/orchestrator/orchestrator.go:1131-1139`).
Every workbench turn (S1) inherits both taxes: the constructed room context plus
this per-turn double read, on top of the per-dispatch floor above.

## What changes

Four independent, additive levers, each measured with the
[`llm-usage-optimization`](../../.agents/skills/llm-usage-optimization/SKILL.md)
methodology (trace `agent.call.complete.meta.usage` before/after, no vibes):

1. **Stable-prefix caching for `host.agent.*` dispatch.** Give the `claude`
   CLI a cache-eligible, byte-stable prefix the same way `LiveHarness` already
   does for routing: order the composed system prompt (`internal/sysprompt/sysprompt.go`,
   already stable→volatile by layer — kitsoki → project → task) so nothing
   volatile (turn number, timestamps, per-call artifact IDs) lands ahead of
   the ~1024-token cache threshold, and thread `cache_read_input_tokens` /
   `cache_creation_input_tokens` out of the `claude` CLI's own usage reporting
   into `AgentCalledPayload`/`agent.call.complete` the same way `live.go` does,
   so cache hit-rate on dispatch calls becomes as visible as it is on routing.
2. **Stop re-serializing the whole story per dispatch.** Constructed context
   (the worker-brief pattern, S1) should carry only what the persona's
   toolbox/effect class needs for *this* call — not the full `session.story`
   snapshot on every task/decide/ask. Judge/decide calls in particular need
   the artifact under review, not the story graph (`.context/token-bloat-finding.md`
   candidate lever list).
3. **Kill the double journey read.** Fold `RecentTurns` extraction into the
   same event-log pass `loadJourney`/`store.BuildJourney` already does, or
   carry the slice on `JourneyState` so a single read serves both — removing
   the acknowledged duplicate pass at `internal/orchestrator/orchestrator.go:1131-1139`.
4. **Budget / early-escalation gate.** Before a dispatch is sent, check a
   per-call/per-turn token budget against the harness ladder
   (`internal/host/ladder.go`, GU worktree `docs/goals/generalized-usage/decomposition.yaml:164-176`)
   and escalate (or refuse with a recorded reason) *before* paying for an
   oversized call, rather than discovering the cost after the fact in the
   trace. This is the "budget gate" W4 named and never cut.

**Target:** <15k marginal input tokens per workbench turn (S1), measured on
the same `qs1` single-file-fix scenario used in the token-bloat finding.

## Impact

- **Code seams:** `internal/host/agents.go`, `internal/host/agent_runner.go`,
  `internal/host/sysprompt.go`, `internal/sysprompt/sysprompt.go`,
  `internal/orchestrator/orchestrator.go:1131-1139,2912`, `internal/host/ladder.go`.
- **Vocabulary:** no new host calls or world keys; extends the existing
  `agent.call.complete` payload with cache-usage fields (mirroring `live.go`)
  and adds a budget-gate decision to the trace (below).
- **Stories affected:** none change authored behavior; every story that
  dispatches `host.agent.*` (i.e. all of them) gets cheaper calls for free.
  The workbench (S1) is the first consumer built *against* the <15k target
  rather than retrofitted onto it.
- **Backward compat:** prompt content is restructured, not removed —
  existing cassettes are byte-sensitive to prompt shape (per the
  llm-usage-optimization skill's cache rule) so cassette episodes affected by
  prefix reordering must be re-recorded deliberately; behavior (what the
  agent is told) is unchanged, only ordering and trimming.
- **Docs on ship:** `docs/architecture/hosts.md` (agent dispatch), a new
  `docs/architecture/cost-and-context.md` or a section in
  `docs/stories/state-machine.md` documenting the budget gate.

## Vocabulary changes

| Kind | Name | Shape | Notes |
|---|---|---|---|
| trace field | `agent.call.complete.meta.usage.cache_read_input_tokens` / `cache_creation_input_tokens` | `int64` | already exists for `LiveHarness` routing calls (`live.go:215-217`); extended to `host.agent.*` dispatch calls |
| trace event | `agent.dispatch.budget_checked` (or a field on `agent.call.start`) | `{estimated_tokens, budget, decision: proceed\|escalate\|refuse, rung}` | records the budget-gate decision before dispatch, deterministically reconstructable |

## The model

```
host.agent.{task,decide,ask,converse}
   │
   ├─ 1. compose prompt: stable prefix (persona/contract/toolbox) FIRST,
   │      volatile per-call context (artifact under review, turn state) LAST
   ├─ 2. budget gate: estimate tokens against harness-ladder rung ──▶ proceed | escalate rung | refuse (recorded)
   └─ 3. dispatch via claude CLI subprocess ──▶ usage (incl. cache_read/cache_creation) recorded on agent.call.complete
```

The budget gate is a DETERMINISTIC check (token estimate vs. a configured
threshold) that decides only *which rung/whether to proceed* — it never
interprets content. The prompt-composition and trimming are also
deterministic (host-side assembly, no LLM judgment about what to include).
Nothing here becomes interpretive; it removes bytes an LLM never needed to
see.

## Decision recording

Every budget-gate decision is recorded as a labeled trace datapoint —
`estimated_tokens`, `budget`, the chosen `decision`, and (when escalation
fires) which `rung` was selected — so a reviewer can reconstruct *why* a call
was allowed, escalated, or refused without re-running it. Cache-usage fields
piggyback on the existing `agent.call.complete` event, matching the shape
`live.go` already emits for routing calls.

## Engine seams & invariants

- Prompt composition remains centralized in `composeAgentSystemPrompt`
  (`internal/host/sysprompt.go:69-76`) — trimming and ordering changes land
  there and in the per-verb context builders, not scattered across call
  sites.
- The budget gate is a new pre-dispatch check; a load-time invariant is not
  applicable here (it's a runtime decision, not a story-shape check), but the
  gate's threshold config must fail closed (missing/invalid budget config
  refuses rather than silently disables the gate).
- The double-read fix must not change `RecentTurnsLimit` truncation
  semantics — same bounded window, one read instead of two.

## Backward compatibility / migration

Existing stories and cassettes keep working: no story YAML changes, no new
required fields. Cassette episodes recorded against the old prompt byte
layout will mismatch after prefix reordering/trimming (expected, per the
cache-construction rule) and must be re-recorded — this is a mechanical
`kitsoki test flows --record` pass per affected story, not a design change.
The budget gate defaults to a generous threshold (effectively off) until
tuned per-story, so no story silently starts refusing calls on rollout.

## Tasks

```
## 1. Engine
- [x] 1.1 Reorder + trim `host.agent.*` system-prompt composition (stable-prefix-first, trim story serialization per verb) in internal/host/sysprompt.go + agents.go — **audited, already satisfied; no reorder needed.** `sysprompt.Compose` (internal/sysprompt/sysprompt.go) already joins kitsoki → project → task in that fixed, stable→volatile order (pre-existing tests: `TestCompose_LayerOrderAndPresence`, `TestCompose_Deterministic`, `TestCompose_VerbContractLeadsTask`), and every layer fed into it by `composeAgentSystemPrompt` (agent-static kitsoki fragment, app-declared project context, author-declared `agent.SystemPrompt`) is static per (verb, agent) — no turn number, timestamp, artifact ID, or `session.story` snapshot is threaded through this seam anywhere in `internal/host`. There was nothing to trim at this seam. What *was* stale: `internal/host/agents.go`'s package doc and `Agent.SystemPrompt` doc comment described the legacy `--append-system-prompt`-onto-Claude's-default posture as the default, when the actual default path is `--system-prompt` (replaces Claude's default, layered) — fixed both comments and documented the stable-prefix invariant directly on `composeAgentSystemPrompt`. Added `TestComposeAgentSystemPrompt_StablePrefixAcrossCalls` (internal/host/sysprompt_test.go) locking byte-identical output across repeat calls at the ctx-resolving wrapper (not just the pure `Compose` below it). **The proposal's headline evidence (68k-token floor, 774 KB `session.story` re-send) does not originate from this seam** — `host.agent.decide`/`.ask` take no built-in story argument; the prompt bulk is the story-author-supplied `args:`-rendered template (e.g. `stories/bugfix`'s own `prompt`/`prompt_path` resolution in `agent_decide.go`'s `resolveDecidePrompt`), which is out of `internal/host`/`internal/sysprompt` and needs a follow-up scoped to story-level prompt construction (or per-verb context builders in `agent_decide.go`/`agent_task.go`, not named in this proposal's Impact list). All 71 `stories/bugfix` flows pass unchanged; no cassette re-recording was needed (confirms the separate finding that cassette matching keys on `(handler, phase, schema, declared args)`, not prompt bytes).
- [x] 1.2 Surface cache_read/cache_creation usage from the claude CLI subprocess onto agent.call.complete (mirror live.go's usage fields) — **spike found the raw numbers were already threaded** (`agent_runner.go` parses `cache_read_input_tokens`/`cache_creation_input_tokens` off the CLI's stream-json `result` event into the per-call usage box; `agentUsageMeta` already copied the whole map onto `AgentReturned.Meta.usage`; consumers like `internal/agentbench/bench.go` already dig them out by key). The actual gap was visibility, not plumbing: added a named, typed `host.CacheUsage{ReadTokens, CreationTokens, Hit}` (internal/host/agent_event_sink.go, `cacheUsageFromMap`) mirroring `internal/harness/live.go`'s `UsageInfo` field names, surfaced as `Meta.cache` on `agent.call.complete` alongside the untouched raw `Meta.usage` map (fully additive — no existing key removed or renamed). `Hit` is true only when `ReadTokens > 0` (a cache-write-only call is a miss, not a hit). `Meta.cache` stays omitted (not a false all-zero struct) when the transport's usage object carries neither cache key at all, covering non-cache-reporting transports (e.g. copilot). Tests: `TestAgentAsk_UsageMeta_CacheFields` (canned CLI JSON with both cache fields → typed surface populated + Hit=true) and `TestAgentAsk_UsageMeta_CacheFields_Absent` (canned CLI JSON with no cache keys → Meta.cache nil, Meta.usage untouched) in `internal/host/agent_usage_test.go`, no live LLM. Open question 1 below is answered: PRESENT, confirmed without a live probe.
- [ ] 1.3 Kill the double journey read (orchestrator.go:1131-1139): single event-log pass feeds both loadJourney and RecentTurns
- [ ] 1.4 Budget/early-escalation gate wired to internal/host/ladder.go, with fail-closed config and a recorded decision event

## 2. Verification
- [ ] 2.1 Stateless: `kitsoki turn` exercises the budget gate at proceed/escalate/refuse thresholds
- [ ] 2.2 Flow fixtures cover the reordered prompt shape (existing flows re-recorded where cassette-sensitive)
- [ ] 2.3 Before/after cost table on the qs1 single-file-fix scenario (per llm-usage-optimization skill Step 1/3), confirming <15k marginal tokens/turn and cache_read dominating input on repeat calls

## 3. Adopt + document
- [ ] 3.1 Re-run the bugfix pipeline live once (gated, not CI) to confirm the token-bloat finding is resolved; close it out
- [ ] 3.2 Update docs/architecture/hosts.md + state-machine.md with the budget gate and context-trimming contract; migrate shipped content out of this proposal and delete it
```

## Verification

Prefer stateless probes: `kitsoki turn --state … --intent … --world @w.json`
against a fixture that forces a large synthetic prior artifact, asserting the
budget gate's `proceed`/`escalate`/`refuse` decision and that the composed
prompt trims the expected sections. The cost-improvement claim (§Why) does
need one live re-run of the qs1 scenario per the token-bloat finding's own
repro path (`tools/bugfix-bakeoff/external/drive_cell.sh --project
query-string --bug qs1 --candidate gpt-5.5 --score`) — gated, run once for
the before/after table, not part of the default test suite.

## Open questions

1. Cache-usage visibility depends on whether the `claude` CLI subprocess
   surfaces Anthropic's `cache_read_input_tokens`/`cache_creation_input_tokens`
   in its own JSON output today — needs a spike to confirm before task 1.2 is
   scoped. *Lean: spike first; if the CLI doesn't expose it, the fallback is
   estimating cache eligibility from prefix byte-stability alone (still lets
   1/2/3 ship independently).*
2. Budget-gate threshold: a single global default, or per-verb (task vs.
   decide have very different natural sizes)? *Lean: per-verb defaults,
   overridable per-agent — mirrors the existing effect/toolbox declaration
   pattern rather than inventing a new config surface.*

## Non-goals

- Changing which model/provider a dispatch uses (the harness ladder already
  owns that; this only gates *whether* to proceed at the current rung).
- A new prompt-templating system — trimming works within the existing
  pongo2/sysprompt layering.
- WB.4/WB.5 cost-benchmark execution (GU-owned; this proposal only removes a
  structural tax those benchmarks would otherwise measure honestly).
