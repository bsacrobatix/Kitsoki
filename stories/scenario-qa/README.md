# Scenario QA Story

`scenario-qa` is the operator-facing Persona QA surface. Use it when one
scenario should drive one or more Kitsoki transports with the same deterministic
setup contract.

```sh
kitsoki run @kitsoki/scenario-qa
```

Useful prompts:

- `preview bugfix across all transports` shows the scenario x transport suite
  without creating a run bundle, launching capture, or calling an LLM.
- `check bugfix across all transports for core-maintainer on gears-rust`
  creates the run bundle and drives the first transport leg.
- `next leg` advances through the remaining transport legs one at a time.
- `report` rebuilds `report.md` and `deck.slidey.json` for the current run.

The story writes run artifacts under `.artifacts/product-journey/<run-id>/`.
The two review entrypoints are:

- `report.md` for the per-transport verdict table.
- `deck.slidey.json` for the deterministic Slidey deck folded from the recorded
  leg results and any captured playback evidence.

The story owns the product workflow. Python modules under `tools/` remain
implementation adapters for deterministic planning, schema validation,
completion-state conversion, deck generation, and no-LLM tests. Operators should
not need to find `tools/persona_qa` to run Persona QA.
