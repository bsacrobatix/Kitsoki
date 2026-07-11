# Story Upgrade Testing

Story upgrades have two gates:

- CI-safe checks that never call a real LLM.
- Operator-approved live smoke runs that prove upgraded project stories still
  continue through realistic work.

## Deterministic Gate

Use capsules for reusable old-project shapes. The first fixture is
`capsules/old-dev-story-project/capsule.yaml`: it represents an older onboarded
project with a project profile and stale materialized dev-story instance.

Run:

```sh
go test ./cmd/kitsoki -run TestProjectToolsUpgrade -count=1
go run ./cmd/kitsoki capsule verify old-dev-story-project
```

The upgrade check is read-only unless `--apply` is passed:

```sh
kitsoki project-tools upgrade --target /path/to/project
kitsoki project-tools upgrade --target /path/to/project --apply
```

`--apply` refreshes only the embedded project toolkit (`.agents`, `.claude`,
`.mcp.json`). It does not overwrite `.kitsoki/stories/*/app.yaml`; story
regeneration needs an explicit reviewed flow so local customizations are not
dropped.

Add a capsule whenever an upgrade bug is fixed. Prefer the smallest project
state that reproduces the old shape: config/profile, stale generated story,
ticket fixture, PR fixture, or previously solved bug.

## Kit Lifecycle Upgrades (S7)

For a kit a project imports but does not own, the two-gate doctrine now runs
through the staged lifecycle (`docs/proposals/kit-lifecycle.md`):

```sh
kitsoki kit update <name>   # stage a semver-gated candidate; kits.lock untouched
kitsoki kit trial <name>    # judge it; failures become the migration worklist
kitsoki kit accept <name>   # promote fail-closed on the trial receipt
```

- **Deterministic gate:** `kit trial`'s contract, frozen-replay, and
  onboarding gates ARE the CI-safe check — kit conformance plus every
  consumer flow fixture replayed against both the locked and staged trees
  under `KITSOKI_CASSETTE_STRICT=1` (a cassette miss fails closed; the
  receipt reports measured per-gate spend, so "zero LLM" is observed, not
  assumed). The same tools ride the studio MCP as `kit.status` /
  `kit.update` / `kit.trial` / `kit.accept` / `kit.reject`, and
  `story.test` / `story.validate` take `staged: true` for a per-call
  staged resolution.
- **Live smoke:** operator-approved live runs map to the trial's
  `baseline_live` lane. No-cost oracles validate by replay immediately;
  live oracles (red_green, agent_eval) queue as `pending_approval` and are
  only ever run at an operator's explicit request, with the outcome
  recorded in the validation ledger (`.kitsoki/qa/validation-ledger.yaml`)
  so an already-validated case is SKIPPED on every re-trial instead of
  re-spending.

The reusable fixture is `capsules/kit-update-pair/capsule.yaml`: a consumer
project shaped to lock widget v1 while upstream ships v2 with a renamed
intent (compat hint) and a new required host. Run:

```sh
go test ./cmd/kitsoki -run TestKitLifecycle -count=1
go run ./cmd/kitsoki capsule verify kit-update-pair
```

then follow the capsule's README to exercise update → trial → accept by
hand.

## Live LLM Smoke

Live story upgrade smoke runs are manual and must be explicitly requested by an
operator. Do not wire them into normal tests.

Recommended scenario set:

- Bug fixing: open an old-project capsule with a known failing test and drive
  the bugfix path through the current dev-story.
- PR refinement: create a capsule with an existing branch and review feedback,
  then drive the PR refinement path to a patch plus validation evidence.
- PRD-from-ideas: use a captured/mined idea input and verify the current story
  can continue from discovery into a durable PRD/design artifact.

Capture outputs under `.artifacts/story-upgrade/<date-or-run-id>/`:

- the capsule name and commit/digest,
- the exact Kitsoki command and harness profile,
- trace/session IDs,
- model/provider used,
- validation commands and their output,
- whether the story continued without manual repair.

Keep any synthesis notes in `.context/` and promote only durable narrative
updates into docs.
