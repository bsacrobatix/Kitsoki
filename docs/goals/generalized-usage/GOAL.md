# GOAL — Kitsoki is usable by a stranger, generally

**Status:** Active target for the `stabilize/generalized-usage` line of work.
Read by `/goal` (the goal-seeker) from this location. Provenance:
`.context/state-vs-target-generalized-usage-2026-07-03.md` §1 (target) + §3 (walls).

**One line:** A stranger — *not Brad, not this machine* — can download Kitsoki,
run the flagship pipelines live on their own repo and their own provider, trust
the result, afford it, and evaluate it, with every step replayable without LLM
spend.

## The stranger

Not the author on his own machine. A developer who has never seen this repo, on
macOS / Linux / Windows; brings *their* provider (Claude subscription, API key,
codex, GLM, local llama); points Kitsoki at *their* repo; runs interactively **or**
headless / via MCP. Every criterion is judged from that chair.

## Done-when (criteria — each falsifiable; `/goal` verdict=done requires all)

- **G1 — Can install it.** Download a release binary, `cd their-repo && kitsoki`
  → onboarding profiles the repo, installs the toolkit, registers the studio MCP.
  No `/Users/brad` leakage; a fresh clone has zero broken MCP servers; the first
  screen advertises only actions that exist. *Gate: release/CI smoke runs the
  built binary in a scratch temp repo.*
- **G2 — Can run it on their stack.** dev-story, bugfix, prd, git-ops run live on
  their provider, interactively or headless/MCP, with imported base-stories
  resolving against a foreign checkout, cost visible. *Gate: portability flows +
  gated live eval, each with a recorded replay in CI.*
- **G3 — Can trust it.** Errors always surface (no silent `on_error` bounce);
  destructive actions gated; agents can't exceed declared capabilities; "green"
  means independently verified. *Gate: RED flow fixtures per failure mode +
  capability tests + CI running the real verify suites.* The agent-sandboxing /
  capability-model design (effect taxonomy → toolboxes+enforcement → FS/runtime
  sandbox → conformance lint) is now DECOMPOSED as wall WS — it was design-only
  (deferred proposal §6.4) before this slice.
- **G4 — Can afford it.** Metered cost bounded and visible: per-call floor reduced,
  a budget/early-escalation gate, `cost_usd` for non-Anthropic. *Gate: token
  assertions on recorded traces + a budget-gate flow.*
- **G5 — Can evaluate it.** Binary-first getting-started; truthful site/docs; honest
  maturity label (not "PoC."); ≥1 scored external-repo bake-off as public evidence.
  *Gate: doc/site truthfulness lints + one archived scored bake-off.*
- **G6 — Replayable throughout (cross-cutting).** Every pipeline testable/demoable
  without LLM spend; the JSONL trace is the paper trail. *Gate: every other gate
  has a no-LLM replay in CI; live is acceptance-only, never automatic.*

## Non-goals (parked — must not gate stabilization)

GitHub-agent epic, LINE channel, feedback SDK, artifact-driven stories, trainable
stories. Parking is a decision, not neglect — "generalized usage" is defined by
the five walls a stranger actually hits.

## Aggregate

Done when, for a stranger on a foreign repo + own provider, all six `Gn` are green
and each was reached by a change whose deterministic gate went RED→GREEN with its
trace on record. Highest-ROI first step: **change 0.1** (release embed + bare-repo
smoke) — flips every existing release artifact from broken-for-strangers to working.
