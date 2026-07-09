# Scenario QA Story

`scenario-qa` is the operator-facing Persona QA surface. Use it when one
scenario should drive one or more Kitsoki transports with the same deterministic
setup contract. `check` runs automatically create or reuse a managed
clone-backed capsule workspace through `scripts/dev-workspace.sh`; preview runs
stay side-effect-free and do not create a workspace.

```sh
kitsoki run @kitsoki/scenario-qa
```

Useful prompts:

- `preview <catalog-scenario-id> across all transports` shows the scenario x
  transport suite without creating a run bundle, launching capture, or calling
  an LLM.
- `check scenario=<catalog-scenario-id> transport=tui,web persona=<persona-id>
  target=<project-id>` creates the run bundle and drives the first transport
  check for a catalog scenario.
- `check whether settings validation persists transport=web`
  drafts an ad-hoc scenario from prose and checks it on the requested
  transport.
- `next transport` advances through the remaining transport checks one at a time.
- `report` rebuilds `report.md` and `deck.slidey.json` for the current run.
- `main room` returns from the closeout report to the Scenario QA start screen
  while keeping the last run available.

The idle and report rooms also expose a `check_request` free-text input. Treat
the prose as the scenario description, and add `scenario=<id>` only when you
want a catalog scenario instead of an ad-hoc behavior check.

The story writes run artifacts under
`.capsules/workspaces/scenario-qa/.artifacts/product-journey/<run-id>/` by
default, or under the workspace named by `KITSOKI_SCENARIO_QA_WORKSPACE_ID`.
The two review entrypoints are:

- `report.md` for the per-transport verdict table.
- `deck.slidey.json` for the deterministic Slidey deck folded from the recorded
  transport-check results and any captured playback evidence.

The report room shows result counts first and keeps `report.md` in a `kv` row
so web and TUI render it as an openable markdown artifact.

The story owns the product workflow. Python modules under `tools/` remain
implementation adapters for deterministic planning, schema validation,
completion-state conversion, deck generation, and no-LLM tests. Operators should
not need to find `tools/persona_qa` to run Persona QA.
