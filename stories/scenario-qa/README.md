# Scenario QA Story

`scenario-qa` is the operator-facing Persona QA surface. Use it when one
scenario should drive one or more Kitsoki transports with the same deterministic
setup contract.

```sh
kitsoki run @kitsoki/scenario-qa
```

Useful prompts:

- `preview <catalog-scenario-id> across all transports` shows the scenario x
  transport suite without creating a run bundle, launching capture, or calling
  an LLM.
- `check scenario=<catalog-scenario-id> transport=tui,web persona=<persona-id>
  target=<project-id>` creates the run bundle and drives the first transport
  leg for a catalog scenario.
- `check whether the settings form keeps validation errors transport=web`
  drafts an ad-hoc scenario from prose and checks it on the requested
  transport.
- `next leg` advances through the remaining transport legs one at a time.
- `report` rebuilds `report.md` and `deck.slidey.json` for the current run.

The idle and report rooms also expose a `check_request` free-text input. Treat
the prose as the scenario description, and add `scenario=<id>` only when you
want a catalog scenario instead of an ad-hoc behavior check.

The story writes run artifacts under `.artifacts/product-journey/<run-id>/`.
The two review entrypoints are:

- `report.md` for the per-transport verdict table.
- `deck.slidey.json` for the deterministic Slidey deck folded from the recorded
  leg results and any captured playback evidence.

The story owns the product workflow. Python modules under `tools/` remain
implementation adapters for deterministic planning, schema validation,
completion-state conversion, deck generation, and no-LLM tests. Operators should
not need to find `tools/persona_qa` to run Persona QA.
