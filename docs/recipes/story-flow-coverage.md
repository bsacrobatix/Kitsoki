# Story Flow Coverage

`kitsoki test flow-coverage` is the cheap completeness gate for story flow
fixtures. It answers a different question than `kitsoki test flows`:

- `kitsoki test flows` executes fixtures and proves behavior.
- `kitsoki test flow-coverage` reads the story graph plus fixtures and reports
  which authored transition branches, effects, host calls, and bounded enum
  parameters the fixtures claim to exercise.

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

To fail when any authored effect site is uncovered:

```bash
go run ./cmd/kitsoki test flow-coverage stories/git-ops/app.yaml \
  --require-all-effects
```

To require covered host calls to have explicit fixture evidence:

```bash
go run ./cmd/kitsoki test flow-coverage stories/git-ops/app.yaml \
  --require-host-assertions
```

To set a ratcheting threshold first:

```bash
go run ./cmd/kitsoki test flow-coverage stories/git-ops/app.yaml \
  --min-branch 85 \
  --min-effect 80
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

## Effect and Host Coverage

Each authored effect in `on_enter`, transition `effects`, nested `effects`,
and `on_complete` chains is a coverable effect site.

An `on_enter` effect is covered when a fixture starts in or deterministically
reaches that state. A transition effect is covered when the transition branch
is covered.

For `invoke:` effects, the JSON reports the handler name and whether the host
call was asserted. A covered host call is considered asserted when the fixture
uses either:

- `expect_host_calls` / host-dispatch event assertions for that handler, or
- `host_cassette`, which backs the fixture with recorded host episodes.

`host.run` is called out with its authored `cmd`, `args`, `cwd`, and `bind`
shape in JSON because it is a high-risk side-effect surface. Use
`--require-host-assertions` to keep a reached `host.run` from counting as
fully tested unless the fixture also proves the call happened or replays it
from a cassette.

## Parameter Coverage

For enum slots, the report lists observed and missing values. When a
state+intent has a small bounded enum product, the JSON also lists missing
slot combinations. Use `--max-combinations` to control that expansion.

Parameter gaps are reported but do not fail the command by default. Branch,
effect, and host-assertion gates are first-class CLI flags; stricter parameter
gates can layer onto the JSON report after the branch/effect ledger is stable.

## Success Story Loop

The intended autonomous-improvement loop is:

1. Run `kitsoki test flow-coverage --json ...` for the target story.
2. Sort uncovered branches and effects by the story's critical path.
3. Add or strengthen fixtures, preferring `trace to-flow` plus cassettes for
   recorded-reality paths and hand-authored stubs for edge envelopes. Add
   `expect_host_calls` for important `host.*` calls when a cassette is not the
   right fixture shape.
4. Re-run `kitsoki test flows` and `kitsoki test flow-coverage`.
5. Ratchet the branch threshold.

This gives agents a precise backlog: every uncovered branch or effect is a
concrete state-machine site, not a vague request for "more tests."
