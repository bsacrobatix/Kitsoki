# Reduced System Prompt Five-Bug Study

**Status:** pilot evidence plus pre-registered run design.
**Prepared:** 2026-07-06.
**No new live LLM calls were made for this note.** All measured rows below come
from existing traces and cell reports already on disk.

## Question

Does replacing the harness default prompt with Kitsoki's focused project prompt
reduce cost without degrading bug-fix performance?

The change under test is `f97e340a sysprompt: load project base prompt`:

- Claude receives the composed prompt through `--system-prompt`, replacing the
  Claude Code default instead of appending to it.
- Codex receives the same composed bytes through `model_instructions_file`
  instead of carrying them in stdin.
- The editable base prompt lives at `.kitsoki/system-prompt.md`.

This is a prompt-transport and prompt-focus test, not a model benchmark. The
right causal comparison is the same bug corpus, same model/profile, same
guidance policy, same scoring oracle, with only the system-prompt path changed.

## Current Evidence

Existing bug-fix traces cover five unique bugs with enough event data to measure
agent calls, token usage, prompt sidecar bytes, duration, and outcome. They do
not yet form an A/B study because they were not run on both the old and new
prompt branches.

| Bug | Project | Candidate | Outcome | Calls | Visible tokens | Cache-read tokens | Prompt sidecar bytes | Wall time | USD |
|---|---|---:|---|---:|---:|---:|---:|---:|---:|
| qs1 | query-string | gpt-5.5 | solved, oracle pass | 10 | 1,878,621 | 1,344,512 | 38,458 | 507s | n/a |
| qs2 | query-string | gpt-5.5 | solved, oracle pass | 11 | 1,355,081 | 1,063,296 | 38,722 | 416s | n/a |
| qs3 | query-string | gpt-5.5 | solved, oracle pass | 11 | 1,148,123 | 841,472 | 39,015 | 350s | n/a |
| bug9 | kitsoki | opus-4.8 | solved by adjudication | 7 | 61,288 | 3,504,121 | 92,209 | 841s | $7.73 |
| bug14 | kitsoki | GLM-5.2 | failed/incomplete | 2 completed, 3 errored | 718,734 | 0 | 21,342 | 420s | $3.90 |

Definitions:

- **Visible tokens** = input + output + reasoning tokens reported by the trace.
- **Cache-read tokens** normalizes `cached_input_tokens` and
  `cache_read_input_tokens` across Codex/OpenAI-style and Claude-style traces.
- **Prompt sidecar bytes** are the rendered task prompt files recorded by
  `agent.call.start`. They do not include hidden harness defaults.
- `gpt-5.5` rows were subscription-auth traces, so token metrics exist but USD
  was not reported by the trace.

Additional same-bug evidence shows why the report must stay honest:

| Bug | Treatment | Candidate | Outcome | Total/processed tokens | USD |
|---|---|---|---|---:|---:|
| bug9 | kitsoki | GLM-5.2 | partial | 2,890,980 | $15.58 |
| bug9 | kitsoki | opus-4.8 | solved by adjudication | 4,029,185 | $7.73 |
| bug9 | single prompt | opus-4.8 | solved by adjudication | 3,546,998 | $4.00 |
| bug9 | single prompt | sonnet-4.6 | solved by adjudication | 3,197,124 | $3.02 |

This pilot does **not** prove Kitsoki is cheaper today. It shows the current
pipeline can solve real bugs, but also that prompt/caching shape and continuation
loops dominate cost. The prompt-replacement work is worth testing precisely
because inherited harness prompts and dynamic sections are repeated across every
agent call and can also confuse the operator contract.

## Prompt-Reduction Lever

The editable Kitsoki base prompt is 2,220 bytes. That byte count did not shrink
relative to the old embedded fallback. The intended saving is that the new path
replaces the harness default instead of stacking Kitsoki underneath it.

The hidden Claude/Codex default size is not recorded in the visible prompt
sidecars, so the report should not invent a token count for it. The defensible
lower-bound model is:

```text
saved_input_token_opportunities = completed_agent_calls * removed_system_tokens
```

For the five-bug pilot corpus above, there were 41 completed agent calls. Every
1,000 tokens removed from the stable system prefix therefore avoids 41,000
input-token opportunities across the five bugs before accounting for failed
attempts, retries, or resumed conversations.

At $3-$5 per million normal input tokens, that is roughly $0.12-$0.21 per five
bugs per 1,000 removed tokens. If replacement removes 10,000 inherited default
tokens, the same five-bug run saves roughly $1.23-$2.05 in normal-input charges.
Cache-read savings bill lower than normal input but still matter for context
pressure, latency, and instruction interference. The actual number must be
reported from A/B traces, not from this model.

## Decisive Five-Bug A/B Design

Freeze this corpus:

| Bug | Project | Why included |
|---|---|---|
| qs1 | query-string | External JS parser bug; existing solved GPT trace and hidden oracle. |
| qs2 | query-string | External JS type-coercion bug; existing solved GPT trace and hidden oracle. |
| qs3 | query-string | External JS bracket-separator bug; existing solved GPT trace and hidden oracle. |
| bug9 | kitsoki | Host/worktree safety bug; existing multiple-treatment evidence. |
| bug14 | kitsoki | Web/runstatus bug; existing GLM failure trace and hidden oracle. |

Run this 10-cell matrix:

| Axis | Values |
|---|---|
| Prompt variant | `old-append-default` at `822f3584` (`f97e340a^`), `focused-replace` at current main containing `f97e340a` |
| Bugs | qs1, qs2, qs3, bug9, bug14 |
| Candidate | one fixed metered profile first; add Codex as a second pass for parity once the first pass is clean |
| Treatment | Kitsoki pipeline only; do not mix in `single` until the prompt-variant result is known |

Primary metrics:

- Solve rate: `quality == solved` after hidden-oracle scoring plus disclosed
  behaviour adjudication.
- Cost per solve: sum all attempted-cell costs divided by solved cells.
- Token per solve: same calculation with processed tokens and visible tokens.
- Call count and failed-call count, because a focused prompt should reduce
  wandering/retry loops if it is doing useful work.

Success criterion:

- `focused-replace` solve rate is at least `old-append-default`.
- `focused-replace` cost per solve is at most 80% of `old-append-default`.
- No new systematic failure class appears in the focused branch.

This is intentionally conservative. If the new prompt is merely cheaper but
causes regressions, it should fail the study.

## No-Cost Preflight

Before spending on cells:

```bash
python3 tools/bugfix-bakeoff/external/bench.py verify --project query-string
git clone --local --no-checkout . /tmp/kitsoki-bakeoff-mirror
python3 tools/bugfix-bakeoff/external/bench.py verify \
  --project kitsoki \
  --repo-dir /tmp/kitsoki-bakeoff-mirror
```

Then print prompts without driving:

```bash
tools/bugfix-bakeoff/external/drive_cell.sh \
  --project query-string --bug qs1 --candidate <candidate> --no-drive
tools/bugfix-bakeoff/external/drive_cell.sh \
  --project kitsoki --bug bug9 --candidate <candidate> --no-drive
```

The live run must remain operator-approved because `drive_cell.sh` is
cost-bearing.

## Reporting Standard

The final report should include:

- The exact commit SHA for each prompt variant.
- The exact candidate profile/model and price-table version.
- One cell JSON per bug/variant under `tools/bugfix-bakeoff/results/cells/`.
- The raw trace path for every cell.
- Hidden-oracle status and any adjudication note.
- A failure table, not just solved rows.
- A statement that subscription-auth Codex rows have token metrics but no native
  USD unless priced from a declared table.

Until those 10 cells exist, the honest conclusion is:

> The existing traces justify running the A/B: prompt overhead repeats 41 times
> across the five pilot bugs, current costs are dominated by repeated context,
> and the bug9/bug14 failures show real room for prompt-focus gains. They do not
> yet prove the new system prompt saved money causally.
