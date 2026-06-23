# Case study: room-by-room routing model benchmarks

This is the corrected benchmark report for cheaper room-level routing models in
Kitsoki. The earlier versions were wrong: they treated mined corpus counts,
static replay, and blocked provider attempts as if they were live model results.

This version uses real `kitsoki test intents` fixture runs against the same room:

```sh
stories/oregon-trail/intents/refine_purchase.yaml
state: general_store.reviewing
inputs: 7
runs per input: 1
threshold: 80%
```

Current answer: **no tested cheap model is promotable for this room yet.** Haiku,
Opus, Synthetic small, and Codex default all fail the authored slot-contract
threshold.

## Benchmark Results

| candidate | command path | pass rate | elapsed | avg latency | measured cost | verdict |
|---|---|---:|---:|---:|---:|---|
| Claude Haiku | `--harness claude --claude-model haiku` | 2/7, 28.6% | 141.053s | 20.150s | $0.250849 | fail |
| Claude Opus | `--harness claude --claude-model opus` | 2/7, 28.6% | 102.057s | 14.580s | $1.897333 | fail |
| Synthetic small | `--harness live --profile synthetic-claude --claude-model syn:small:text` | 1/7, 14.3% | 39.792s | 5.685s | not emitted by SDK report | fail |
| Codex default | `--harness claude --agent codex` | 1/7, 14.3% | 58.676s | 8.382s | $0 reported by Codex CLI | fail |

Artifacts:

- `.artifacts/routing-model-live-bench/claude-haiku-refine-purchase.json`
- `.artifacts/routing-model-live-bench/claude-opus-refine-purchase.json`
- `.artifacts/routing-model-live-bench/synthetic-live-small-refine-purchase.json`
- `.artifacts/routing-model-live-bench/codex-default-refine-purchase.json`

These are single-run fixtures, so they are not statistically stable. They are
enough to reject promotion for this room because every candidate is far below
the 80% threshold.

## What Failed

The dominant failure is not intent-name selection. The models usually picked
`refine_purchase`. They failed the room because the slots did not match the
fixture contract.

| input | expected behavior | common actual behavior |
|---|---|---|
| `actually give me 12 oxen` | precise correction payload | `refine_purchase` with partial slots such as `{oxen:"12", feedback:""}` |
| `actually give me 12 draaft animalz` | typo-tolerant correction | partial structured slots or incorrect feedback |
| `I want 7 oxen not 6` | preserve correction semantics | partial `{oxen:"7"}` |
| `add 50 more bullets` | preserve additive correction | partial `{bullets:"50"}` |
| `actually 250 lbs of food` | precise food correction | partial `{food:"250"}` |
| `refine` | generic feedback/refine | usually passes |
| `less food` | vague feedback-style correction | passes on Haiku/Opus; fails on Synthetic/Codex in the latest run |

This is the important routing lesson: Kitsoki routing benchmarks must score the
validated intent call, including slots. A label-only classifier would overstate
quality here.

## Fixes Made To Make This Real

The benchmark required code changes because the repo did not actually have a
working live intent fixture runner for this use case.

- `kitsoki test intents` now supports model-backed runs via `--harness claude`.
- It accepts `--agent claude|codex|copilot`.
- It accepts `--profile <name>` from `.kitsoki.yaml` / `.kitsoki.local.yaml`.
- `--harness live` can use profile env such as `ANTHROPIC_BASE_URL` and
  `ANTHROPIC_AUTH_TOKEN`, which is how `synthetic-claude` runs.
- Live fixture runs now pass the room's allowed intents and initial world into
  `TurnInput`.
- JSON reports now include harness/profile metadata, elapsed time, actual
  intent, actual slots, first error, and Claude/Codex usage/cost where the
  backend reports it.
- `machine.validateSlots` no longer panics when a model returns nil slots.

## Provider Notes

Claude native works when run outside the sandbox. The sandboxed process saw
`claude auth status` as logged out, but the escalated process sees the active
Max subscription and can run both Haiku and Opus.

Synthetic small must use the Synthetic profile through the live
Anthropic-compatible SDK path. Running `syn:small:text` through Codex/ChatGPT is
the wrong path and is rejected by that account integration. The corrected
Synthetic run used:

```sh
go run ./cmd/kitsoki test intents stories/oregon-trail/app.yaml \
  --harness live \
  --profile synthetic-claude \
  --claude-model syn:small:text \
  --runs 1 \
  --intents stories/oregon-trail/intents/refine_purchase.yaml \
  --json .artifacts/routing-model-live-bench/synthetic-live-small-refine-purchase.json
```

GPT mini was not included in the final routing matrix because the installed
Codex CLI rejected explicit `gpt-5-mini` under the current ChatGPT account path.
That is a provider/account limitation, not evidence about GPT mini quality.

## Decision

Do not promote a cheap router for `general_store.reviewing`.

The cost-saving mechanism is still the right mechanism: deterministic tiers
first, then the cheapest passing room-local model, then fallback. This room does
not yet have a passing cheap model, and Opus did not pass either. The next
engineering work is prompt/schema/fixture refinement, not model promotion.

## Next Benchmark Steps

1. Run at least 3-5 repetitions per input for the same matrix.
2. Add cost calculation for the Synthetic SDK path from response usage and
   catalog pricing.
3. Add a label-only diagnostic alongside the validated-call score to separate
   intent confusion from slot-shape failures.
4. Test an easier room where slot contracts are not the dominant failure mode.
5. Promote only rooms that pass the authored threshold with hard negatives.
