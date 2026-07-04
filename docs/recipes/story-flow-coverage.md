# Story Flow Coverage

`kitsoki test flow-coverage` is the cheap completeness gate for story flow
fixtures. It answers a different question than `kitsoki test flows`:

- `kitsoki test flows` executes fixtures and proves behavior.
- `kitsoki test flow-coverage` reads the story graph plus fixtures and reports
  which authored transition branches and bounded enum parameters the fixtures
  claim to exercise.

The command is deterministic and never calls an LLM.

## Basic Use

```bash
go run ./cmd/kitsoki test flow-coverage stories/git-ops/app.yaml
```

The default fixture glob is the same as `test flows`:

```bash
go run ./cmd/kitsoki test flow-coverage stories/git-ops/app.yaml \
  --flows 'stories/git-ops/flows/*.yaml'
```

Use JSON output for CI artifacts and autonomous improvement loops:

```bash
go run ./cmd/kitsoki test flow-coverage stories/git-ops/app.yaml \
  --json .artifacts/flow-coverage/git-ops.json
```

## Gating

To fail when any authored transition branch is uncovered:

```bash
go run ./cmd/kitsoki test flow-coverage stories/git-ops/app.yaml \
  --require-all-branches
```

To set a ratcheting threshold first:

```bash
go run ./cmd/kitsoki test flow-coverage stories/git-ops/app.yaml \
  --min-branch 85
```

This is the recommended rollout path for existing stories: publish the JSON
report, fix the most important gaps, then ratchet `--min-branch` upward before
turning on `--require-all-branches`.

## Branch Semantics

Each authored `state + intent + transition index` is a coverable branch. A flow
turn covers a branch when it submits that state and intent and either:

- the intent has exactly one branch, or
- the turn pins `expect_state` / `expect_state_in` to the branch target.

Guarded multi-branch intents are intentionally not credited from intent name
alone. Add `expect_state` to prove which branch the fixture is asserting.

## Parameter Coverage

For enum slots, the report lists observed and missing values. When a
state+intent has a small bounded enum product, the JSON also lists missing
slot combinations. Use `--max-combinations` to control that expansion.

Parameter gaps are reported but do not fail the command by default. The first
ROI step is to make the gaps visible; stricter parameter gates can layer onto
the JSON report after the branch ledger is stable.

## Success Story Loop

The intended autonomous-improvement loop is:

1. Run `kitsoki test flow-coverage --json ...` for the target story.
2. Sort uncovered branches by the story's critical path.
3. Add or strengthen fixtures, preferring `trace to-flow` plus cassettes for
   recorded-reality paths and hand-authored stubs for edge envelopes.
4. Re-run `kitsoki test flows` and `kitsoki test flow-coverage`.
5. Ratchet the branch threshold.

This gives agents a precise backlog: every uncovered branch is a concrete
state+intent target, not a vague request for "more tests."
