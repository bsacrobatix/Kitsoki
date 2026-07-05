# Runtime: Dispatch context floor

**Status:** Shipped except the live-gated cost proof. Engine + verification +
docs tasks (1.1-1.4, 2.1, 2.2, 3.2) landed on `s3/dispatch-context-floor` at
`8563b5df` (1.1 prefix audit), `cc55d902` (1.2 cache-usage surface),
`8b23fc40`/`e911362a` (1.3 double-read fix), `7261c471` (1.4 budget gate),
and this commit (3.2 docs). **2.3** (before/after qs1 cost table) and **3.1**
(live qs1 pipeline re-run) remain open — both require one real metered LLM
run of the bugfix pipeline, which is explicitly BLOCKED pending Brad's
go-ahead per the no-live-LLM-in-automation rule; this proposal stays open
(not deleted) until that run happens and closes them out.
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

## What shipped

Three of the four levers landed as engine changes (task 1.1's audit found the
fourth, prefix-ordering, already correct — see Tasks below for the honest
per-task account and where each is documented):

- **Cache-usage visibility** — `agent.call.complete.meta.cache` (`host.CacheUsage`).
  Documented in [hosts.md](../architecture/hosts.md#cache-usage-visibility-and-the-pre-dispatch-budget-gate).
- **Pre-dispatch budget gate** — `agent.dispatch.budget_checked`, the
  per-agent `token_budget:` override. Documented in the same hosts.md section.
- **Single event-log read for `RecentTurns`** — folded into `loadJourney`'s
  existing replay. Documented in
  [state-machine.md §8](../stories/state-machine.md#8-the-turn-loop-state-machine-of-the-orchestrator).
- **Stable-prefix prompt ordering** — audited, not changed: already
  stable→volatile (kitsoki → project → task) per
  [system-prompt.md](../architecture/system-prompt.md); this proposal's
  headline 68k/774KB evidence does not originate at this seam (see task 1.1).

**Target (not yet measured):** <15k marginal input tokens per workbench turn
(S1) on the `qs1` single-file-fix scenario — blocked on the live re-run (2.3/3.1
below); the engine levers are in place but the before/after number itself
requires the gated live run.

No new host calls, world keys, or story-authoring surface: the budget gate's
only story-facing addition is the optional `agents.<name>.token_budget:`
declaration (backward compatible — omitting it uses the generous shipped
default, "effectively off"). Cassette matching keys on `(handler, phase,
schema, declared args)`, never composed-prompt bytes (verified in
`internal/testrunner/cassette.go`), so no existing flow needed re-recording
(see task 2.2).

## Tasks

```
## 1. Engine
- [x] 1.1 Reorder + trim `host.agent.*` system-prompt composition (stable-prefix-first, trim story serialization per verb) in internal/host/sysprompt.go + agents.go — **audited, already satisfied; no reorder needed.** `sysprompt.Compose` (internal/sysprompt/sysprompt.go) already joins kitsoki → project → task in that fixed, stable→volatile order (pre-existing tests: `TestCompose_LayerOrderAndPresence`, `TestCompose_Deterministic`, `TestCompose_VerbContractLeadsTask`), and every layer fed into it by `composeAgentSystemPrompt` (agent-static kitsoki fragment, app-declared project context, author-declared `agent.SystemPrompt`) is static per (verb, agent) — no turn number, timestamp, artifact ID, or `session.story` snapshot is threaded through this seam anywhere in `internal/host`. There was nothing to trim at this seam. What *was* stale: `internal/host/agents.go`'s package doc and `Agent.SystemPrompt` doc comment described the legacy `--append-system-prompt`-onto-Claude's-default posture as the default, when the actual default path is `--system-prompt` (replaces Claude's default, layered) — fixed both comments and documented the stable-prefix invariant directly on `composeAgentSystemPrompt`. Added `TestComposeAgentSystemPrompt_StablePrefixAcrossCalls` (internal/host/sysprompt_test.go) locking byte-identical output across repeat calls at the ctx-resolving wrapper (not just the pure `Compose` below it). **The proposal's headline evidence (68k-token floor, 774 KB `session.story` re-send) does not originate from this seam** — `host.agent.decide`/`.ask` take no built-in story argument; the prompt bulk is the story-author-supplied `args:`-rendered template (e.g. `stories/bugfix`'s own `prompt`/`prompt_path` resolution in `agent_decide.go`'s `resolveDecidePrompt`), which is out of `internal/host`/`internal/sysprompt` and needs a follow-up scoped to story-level prompt construction (or per-verb context builders in `agent_decide.go`/`agent_task.go`, not named in this proposal's Impact list). All 71 `stories/bugfix` flows pass unchanged; no cassette re-recording was needed (confirms the separate finding that cassette matching keys on `(handler, phase, schema, declared args)`, not prompt bytes).
- [x] 1.2 Surface cache_read/cache_creation usage from the claude CLI subprocess onto agent.call.complete (mirror live.go's usage fields) — **spike found the raw numbers were already threaded** (`agent_runner.go` parses `cache_read_input_tokens`/`cache_creation_input_tokens` off the CLI's stream-json `result` event into the per-call usage box; `agentUsageMeta` already copied the whole map onto `AgentReturned.Meta.usage`; consumers like `internal/agentbench/bench.go` already dig them out by key). The actual gap was visibility, not plumbing: added a named, typed `host.CacheUsage{ReadTokens, CreationTokens, Hit}` (internal/host/agent_event_sink.go, `cacheUsageFromMap`) mirroring `internal/harness/live.go`'s `UsageInfo` field names, surfaced as `Meta.cache` on `agent.call.complete` alongside the untouched raw `Meta.usage` map (fully additive — no existing key removed or renamed). `Hit` is true only when `ReadTokens > 0` (a cache-write-only call is a miss, not a hit). `Meta.cache` stays omitted (not a false all-zero struct) when the transport's usage object carries neither cache key at all, covering non-cache-reporting transports (e.g. copilot). Tests: `TestAgentAsk_UsageMeta_CacheFields` (canned CLI JSON with both cache fields → typed surface populated + Hit=true) and `TestAgentAsk_UsageMeta_CacheFields_Absent` (canned CLI JSON with no cache keys → Meta.cache nil, Meta.usage untouched) in `internal/host/agent_usage_test.go`, no live LLM. Open question 1 below is answered: PRESENT, confirmed without a live probe.
- [x] 1.3 Kill the double journey read (orchestrator.go:1131-1139): single event-log pass feeds both loadJourney and RecentTurns — carried the replayed slice on `store.JourneyState.History` (set by `BuildJourney` in both the SQLite/dual-write and pure-JSONL eventSink-authority branches of `loadJourney`), and `Turn()` now derives `RecentTurns` via `extractRecentTurns(journey.History)` instead of a fresh `o.store.LoadHistory` call. Truncation semantics (`RecentTurnsLimit`, ordering) untouched — only the input source changed. Added `TestOrchestrator_RecentTurnsSingleHistoryRead` (internal/orchestrator/recent_turns_test.go), which wraps the store to count `LoadHistory` calls and locks the total at 2 per `Turn()` call, not 3. **Note:** the 2 remaining calls are `TryDeterministic`'s own separate pre-LLM `loadJourney` pass (deterministic.go:180, runs unconditionally before the session lock for routing) plus `Turn()`'s own `loadJourney` — that duplication is a distinct, pre-existing routing-tier concern outside this task's scope; only the RecentTurns-specific 3rd read was eliminated here.
- [x] 1.4 Budget/early-escalation gate wired to internal/host/ladder.go, with fail-closed config and a recorded decision event — **implemented as a new pre-dispatch step inside `runAgentVerbWithLadder`** (`internal/host/ladder.go`), the shared wrap point host.agent.decide/task already went through for the harness ladder — so it covers exactly the two verbs currently ladder-wrapped; host.agent.ask/converse don't route through this wrap point today and are out of scope here (a follow-up would need to extend the ladder wrap to those verbs first). New file `internal/host/budget_gate.go`: `estimateDispatchTokens` deterministically approximates a call's size (JSON-marshaled args + the resolved agent's SystemPrompt, ÷4 chars/token — an approximation, not the exact CLI count task 1.2 already surfaces after the fact) and `checkDispatchBudget` compares it against `BudgetThresholds{WarnTokens, RefuseTokens}` resolved via `resolveBudgetThresholds` (per-agent `Agent.TokenBudget` override, else a per-verb default, else a generic fallback — all shipped defaults generous: 300k warn / 1M refuse, "effectively off"). Three outcomes: **proceed** (dispatch unchanged), **escalate** (over warn, at/under refuse — skips the ladder's cheapest effort tier via `escalateLadderStart`/`escalatedLadderConfig` so the walk's first attempt starts one step up; a no-op with no ladder installed), **refuse** (over refuse, or an invalid agent-declared `TokenBudget` — fails CLOSED: returns a terminal `FailureFatal` Result **before any claude subprocess is spawned**, so an oversized/misconfigured call is never metered). Every decision is recorded as a new trace event `agent.dispatch.budget_checked` (`internal/store/event.go`, forward-compatible no-op in `BuildJourney`'s replay switch) carrying `{verb, estimated_tokens, budget_warn_tokens, budget_refuse_tokens, decision, reason, rung}`. Per-agent override is YAML-declarable via `agents.<name>.token_budget: {warn_tokens, refuse_tokens}` (`internal/app/types.go` `TokenBudgetDecl`, mirroring the `toolbox:`/`bash_profile:` declaration pattern), validated at load time (`internal/app/loader.go`) AND again at runtime (`BudgetThresholds.valid()`) as a double-check, same posture as the Bash-profile checks. Tests: `internal/host/budget_gate_test.go` (proceed/escalate/refuse/fail-closed-invalid-config, each a stateless `AgentDecideHandler` probe with a `WithClaudeRunner` stub and a synthetic oversized `context` arg — refuse/fail-closed assert the runner is **never invoked**; escalate asserts the captured `--effort` flag lands on the ladder's second tier, not its first) + `internal/host/export_test.go` (`DefaultVerbBudgetsExport`/`BudgetThresholdsValidExport` lock that every shipped default is itself valid()) + `internal/app/loader_token_budget_test.go` (YAML parse + load-time fail-closed validation). All 71 `stories/bugfix` flows still pass unchanged (no ladder/budget config wired ⇒ pure pass-through, confirming the rollout invariant).

## 2. Verification
- [x] 2.1 Stateless: `kitsoki turn` exercises the budget gate at proceed/escalate/refuse thresholds — covered via the stateless `AgentDecideHandler` probes in `internal/host/budget_gate_test.go` (the same idiom `ladder_agent_verbs_test.go` already used for ladder verification) rather than the `cmd/kitsoki turn` CLI directly, since the gate is a `host` package internal; see task 1.4 above for the four scenarios (proceed, refuse, escalate, fail-closed-invalid-config).
- [x] 2.2 Flow fixtures cover the reordered prompt shape (existing flows re-recorded where cassette-sensitive) — **resolved as zero-cost, not re-recorded.** Verified `internal/testrunner/cassette.go`'s `MatchEpisode`/`episodeMatches` (lines ~450-500): episode matching keys on `(handler, phase, schema_name, declared arg fields)` — it never compares composed system-prompt bytes. `EpisodeAgent.SystemPrompt`/`Prompt` are informational sidecar fields for humans/re-record tooling, not consulted by the matcher, and no test under `internal/testrunner` asserts on them. Since 1.1 made no prompt-shape change anyway (the engine seam was already stable→volatile; see 1.1's note), there is nothing to re-record. All 71 `stories/bugfix` flows pass unchanged (confirmed under both 1.1 and 1.4).
- [ ] 2.3 Before/after cost table on the qs1 single-file-fix scenario (per llm-usage-optimization skill Step 1/3), confirming <15k marginal tokens/turn and cache_read dominating input on repeat calls — **blocked**, needs the same live qs1 re-run as 3.1 (see below); not attempted.

## 3. Adopt + document
- [ ] 3.1 Re-run the bugfix pipeline live once (gated, not CI) to confirm the token-bloat finding is resolved; close it out — **explicitly BLOCKED pending Brad.** Requires a real metered LLM run (`tools/bugfix-bakeoff/external/drive_cell.sh --project query-string --bug qs1 --candidate gpt-5.5 --score`), which is out of scope for automated/agent work per the hard no-live-LLM-in-automation rule. 2.3's before/after table is gated on the same run.
- [x] 3.2 Update docs/architecture/hosts.md + state-machine.md with the budget gate and context-trimming contract; migrate shipped content out of this proposal and delete it — done. Sized per docs/AGENTS.md's "one authoritative location per concept, referenced elsewhere" rule rather than adding a new file: cache-usage visibility + the budget gate (verb table, decision outcomes, `token_budget:` field, fail-closed posture) now live in `docs/architecture/hosts.md`'s new "Cache-usage visibility and the pre-dispatch budget gate" section (plus a `token_budget` row on the Agent declaration fields table); the single-event-log-read fix (`RecentTurns` folded into `loadJourney`'s replay) is noted in `docs/stories/state-machine.md` §8 (the turn loop) next to the `acquire` state it changes. Prefix-ordering itself needed no new prose — it was already fully documented and accurate in `docs/architecture/system-prompt.md` (task 1.1 found the engine seam already compliant); this proposal's docs only had to cover what genuinely changed (1.2 cache surface, 1.3 double-read, 1.4 budget gate).
```

## Verification

Stateless probes landed (task 2.1/2.2): `internal/host/budget_gate_test.go`
exercises `proceed`/`escalate`/`refuse`/fail-closed via a stateless
`AgentDecideHandler` probe, and all 71 `stories/bugfix` flows pass unchanged
with no re-recording needed. What's left is the one *live* piece: the
cost-improvement claim (§Why) needs a live re-run of the qs1 scenario per the
token-bloat finding's own repro path
(`tools/bugfix-bakeoff/external/drive_cell.sh --project query-string --bug
qs1 --candidate gpt-5.5 --score`) — gated on Brad, not part of the default
test suite, tracked as tasks 2.3/3.1.

## Open questions — resolved

1. **Does the claude CLI surface cache tokens?** Yes — confirmed without a
   live probe. `agent_runner.go` already parses `cache_read_input_tokens` /
   `cache_creation_input_tokens` off the CLI's `stream-json` terminal
   `result` event; no estimation fallback was needed.
2. **Budget-gate threshold: global or per-verb?** Per-verb defaults,
   overridable per-agent (`token_budget:`) — shipped as designed, mirroring
   the existing toolbox/bash_profile declaration pattern.

## Non-goals

- Changing which model/provider a dispatch uses (the harness ladder already
  owns that; this only gates *whether* to proceed at the current rung).
- A new prompt-templating system — trimming works within the existing
  pongo2/sysprompt layering.
- WB.4/WB.5 cost-benchmark execution (GU-owned; this proposal only removes a
  structural tax those benchmarks would otherwise measure honestly).
