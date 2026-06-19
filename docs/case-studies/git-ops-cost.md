# Case study: what does the git-ops demo *cost*?

This is the companion to [bug-fix.md](bug-fix.md). Where bug-fix shows
*how* a prompt-driven loop becomes a deterministic pipeline, this one
puts a price tag on the difference. It takes four operations a developer
actually drove in recorded Claude Code sessions — commit, rebase-with-
conflict, merge, worktree setup — and asks one question:

> The git-ops story runs these for about **$0.10**. What would the same
> four operations have cost if I'd just done them in Claude Code?

The answer is a **range**, not a point — and the range is the whole
point. In a fresh session it's 3–5×. Once you've got a real conversation
built up behind the request, it's 20–55×. And the Kitsoki number doesn't
move at all.

The method is reproducible: [`tools/session-mining/cost_estimate.py`](../../tools/session-mining/cost_estimate.py),
tested by [`tests/test_cost_estimate.py`](../../tools/session-mining/tests/test_cost_estimate.py),
no LLM and no network.

---

## 1. The honest problem: the number isn't in the data

The four operations come from real sessions mined with the
[story-coverage-mining loop](../stories/story-coverage-mining.md). The
mined transcripts live in
[`tools/session-mining/examples/git-ops/raw/`](../../tools/session-mining/examples/git-ops/raw/).
They are redacted — and crucially, they carry **no usage telemetry**.
There is no `input_tokens`, no `cache_read`, no `cost_usd`. So we cannot
*read* the answer; we have to *model* it.

That sounds like a licence to make a number up. It isn't, because the
transcripts pin the one variable that dominates agentic cost: the
**shape** of the session.

```
USER: rebase onto main and resolve the conflicts
AI:  > Bash: git rebase main           # call 1 — model sees: system+tools+user
AI:  text + > Edit: session.go         # call 2 — model sees: +conflict, re-sent
AI:  > Edit: token.go                  # call 3 — model sees: +edit result, re-sent
AI:  > Bash: go build ./...            # call 4 — model sees: +edit result, re-sent
```

That's **four assistant API calls** for one user request. Each call is a
fresh round-trip in which Claude Code re-sends the *entire conversation
so far* — plus a large system prompt and the full tool schemas — and gets
back a reply. Cost is not "one call per user turn." It's one call per
tool round-trip, and every call re-pays for everything before it.

What's verifiable from the transcript:

| operation (mined session) | assistant API calls |
|---|---|
| commit the staged fix (`sess-commit-happy`) | 2 |
| rebase + resolve conflicts (`sess-rebase-conflict`) | 4 |
| merge into main (`sess-merge-direct`) | 1 |
| set up a worktree (`sess-worktree`) | 1 |

Eight API calls across the four operations. That call count is the
load-bearing fact; everything else in the model is a knob with a
defensible default, and the script exposes all of them.

---

## 2. The cost model

Claude Code (like any agentic loop) bills the conversation the way a
ratchet turns: it only grows, and you re-send all of it on every call.

### 2.1 Per-call input is the whole prefix

For each assistant call *i* the model's input is the cumulative context:

```
input_i = base + prior + (every user/assistant/tool message before call i)
```

* **base** — the system prompt and tool schemas. These are re-sent on
  *every* call and never shrink. For Claude Code this is roughly
  15–25k tokens; the script defaults to **18,000** (`--base-tokens`).
  For a short operation this single term dominates the bill.
* **prior** — any conversation that already existed before you asked.
  This is the "how early/big is the session" axis (§3).
* **the running transcript** — each tool result and assistant reply
  joins the prefix and is re-sent on every subsequent call.

Because every call re-reads the whole prefix, an *N*-call operation pays
for the base term *N* times and pays for early turns once per later call.
That super-linear growth is exactly why a 4-call rebase costs far more
than a 1-call merge, and why the same operation gets more expensive the
deeper into a session it happens.

### 2.2 Two billing regimes: cold and warm

* **Cold (no cache).** Every call pays full input price on its entire
  prefix. This is the ceiling.
* **Warm (5-minute prompt cache).** The stable prefix is *cache-read* at
  ~0.1× input price; only the delta since the previous call is
  *cache-written* (at 1.25×). The first call writes its whole prefix.
  Over many calls this is the floor.

The two regimes bracket reality: your actual bill depends on how many
calls land inside the 5-minute cache window, and lands inside the band.

> A subtlety the model keeps honest: for a **single-call** operation
> (merge, worktree) warm is *slightly more* than cold, because the lone
> call writes its whole prefix at 1.25× and never gets to amortise a
> read. Caching only pays off across multiple calls. The script reports
> both numbers per session, so this shows up rather than being smoothed
> away.

### 2.3 Pricing and tokenisation

Prices are Claude **Sonnet 4.x** list (the Claude Code default and the
demo's oracle model), USD per 1M tokens, as of 2026-06:

| | input | output | cache write (5m) | cache read |
|---|---|---|---|---|
| $/Mtok | 3.00 | 15.00 | 3.75 | 0.30 |

All four are `--price-*` flags. Token counts use a chars-per-token
heuristic (default 3.8, `--chars-per-token`) — no tokenizer dependency,
no network. None of these change the *qualitative* result; §6 shows the
multiples survive aggressive discounting of every knob.

### 2.4 The redaction floor

The redacted tool results in the mined transcripts are tiny (`"git status
--short"`). Real ones are not — a conflict resolution reads two full
source files and a `git diff`. Taking the redacted sizes literally would
*understate* the raw cost. So every tool result is inflated to a realistic
floor (default **450 tokens**, `--tool-result-floor`). This biases the
estimate *conservatively* against the point the case study is making —
the real raw numbers are higher, not lower.

---

## 3. The two axes

The user's question was really "give me a *range*, based on the things
that actually move the number." Two things do.

* **How big the conversation already was** (`--prior-context`, swept by
  `--sweep`). A standalone "commit this" in a fresh chat is the cheap
  case. The same words on turn 40 of a long pairing session re-send all
  40 turns *on every one of the eight calls*. This is the dominant axis.
* **Whether you're cached or not** (warm↔cold). The spread within a
  column.

The output is a matrix: columns are prior-context sizes, each cell is the
4-operation total as a warm→cold band.

---

## 4. The Kitsoki side: committed ground truth

The deterministic side is not modelled at all — it's *read* from the
committed host cassette
[`stories/git-ops/flows/cassettes/demo_oracle.cassette.yaml`](../../stories/git-ops/flows/cassettes/demo_oracle.cassette.yaml),
the same fixture that drives the demo video's spend meter:

| paid surface | cost |
|---|---|
| `host.oracle.decide` — draft the commit message | $0.0121 |
| `host.oracle.task` — resolve the two-file conflict | $0.0834 |
| everything else — routing, branch detection, every git command, the whole worktree lifecycle | **$0.0000** |
| **total (4 operations)** | **$0.0955** |

The story confines the LLM to the two moments only a model can do —
authoring a commit message and resolving a merge conflict. Routing each
typed utterance to an intent, detecting the branch, staging, every merge
guard, the worktree setup: all deterministic transitions and real `git`,
re-sending nothing to a model. This is
[progressive determinism](../architecture/concept.md#4-progressive-determinism)
with a price tag. See the
[git-ops story](../stories/git-ops.md) and the demo's routing/cost
walkthrough for how those two calls surface in the trace.

---

## 5. Result

Default knobs (`python3 tools/session-mining/cost_estimate.py`), each cell
the **4-operation total**, warm→cold band:

| | fresh session | +25k prior | +50k prior | +100k prior | +200k prior |
|---|---|---|---|---|---|
| Claude Code (warm→cold) | $0.30 – $0.45 | $0.71 – $1.05 | $1.11 – $1.65 | $1.92 – $2.85 | $3.54 – $5.25 |
| **Kitsoki story (actual)** | **$0.0955** | **$0.0955** | **$0.0955** | **$0.0955** | **$0.0955** |
| **multiple** | **3× – 5×** | **7× – 11×** | **12× – 17×** | **20× – 30×** | **37× – 55×** |

Per-operation, fresh session (warm / cold):

| operation | calls | warm | cold |
|---|---|---|---|
| commit | 2 | $0.076 | $0.111 |
| rebase + resolve | 4 | $0.091 | $0.227 |
| merge | 1 | $0.068 | $0.055 |
| worktree | 1 | $0.068 | $0.055 |

Two things to read out of this:

1. **The Kitsoki row is flat.** Prior context doesn't inflate it, because
   the deterministic engine never re-sends a conversation to a model.
   This is the structural difference, not a tuning win: the raw loop's
   cost is a function of conversation size; the story's isn't.
2. **The multiple widens with context.** Even the most favourable cell —
   a fresh session, fully warm — is 3×. The realistic case (you ask for a
   rebase partway through a working session) is 10–30×. The raw loop pays
   to re-read everything, every call, every operation; the story pays
   only for the two units of judgement.

The deterministic engine scales for free; you pay for judgement, not
plumbing.

---

## 6. Reproduce it / threats to validity

```bash
# default report (Sonnet list price), written to .artifacts (gitignored)
python3 tools/session-mining/cost_estimate.py \
  --markdown .artifacts/git-ops/cost-comparison.md

# stress the knobs hard in the conservative direction
python3 tools/session-mining/cost_estimate.py \
  --base-tokens 12000 --tool-result-floor 200 --sweep 0,50000
#  => still 2×–3× fresh, 11×–16× at +50k

# invariants test — no LLM, no network
python3 tools/session-mining/tests/test_cost_estimate.py
```

What could make this wrong, and why it doesn't change the conclusion:

* **The dollar figures are a model, not a measurement.** The robust
  signal is the *multiple*, which holds across every knob setting,
  because both numerator and denominator move together when you change
  price, and the denominator (Kitsoki) is fixed ground truth.
* **The chars-per-token heuristic is approximate.** It affects every term
  proportionally; the ratio is near-invariant to it.
* **Real sessions are messier than four clean operations.** Retries,
  re-reads, larger diffs, and longer system prompts all push the *raw*
  number **up**, not down. The redaction floor (§2.4) already biases the
  estimate conservatively.
* **The cache model is idealised** (perfect prefix reuse within 5
  minutes). Real cache hit rates are lower, which again pushes the raw
  number toward the cold ceiling, not below the warm floor.

The honest claim is therefore the conservative one: doing this work as a
raw agentic loop costs *at least* a few times more in the best case, and
an order of magnitude or two more in the normal case — and unlike the
deterministic story, that cost grows with every turn you've already
spent.

---

## See also

- [Token usage comparison](../competitive-analysis/token-usage/) — the
  broader framing this case study instantiates for one workflow.
- [bug-fix case study](bug-fix.md) — the *how* behind the *how much*.
- [git-ops story](../stories/git-ops.md) and
  [story-coverage-mining](../stories/story-coverage-mining.md) — where the
  four operations and their mined sessions come from.
- [Progressive determinism](../architecture/concept.md#4-progressive-determinism)
  — the principle the price tag measures.
