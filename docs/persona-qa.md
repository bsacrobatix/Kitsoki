# Persona QA Kit

Persona QA Kit is the portable product surface for Kitsoki's persona/scenario
journey evaluator. A project owns a small `persona-qa/` directory plus
`persona-qa.yaml`; Kitsoki provides the runner, story UI, replay gates, review
logic, and completion-state adapter.

The default path is no-LLM. Live capture is an explicit operator/story action,
not something tests or CI should start implicitly.

## Kit Layout

```text
persona-qa.yaml
persona-qa/
  catalog.json
  github-targets.json
  personas/*.json
  scenarios/*.json
  drivers/*.json
  oracles/
  fixtures/
  schemas/v1/*.schema.json
```

`catalog.json` names the project targets. `personas/*.json` describe reviewer
lenses. `scenarios/*.json` describe natural-use journeys. `drivers/*.json` map
abstract capabilities like `visual.open` and `session.submit` to concrete
driver tools, launch/readiness hooks, affordances, and local oracles.

## Commands

```sh
kitsoki persona-qa init --root .
kitsoki persona-qa validate --config persona-qa.yaml
kitsoki persona-qa emit-run --config persona-qa.yaml --project local-app --persona core-maintainer --scenario project-onboarding --transport all
kitsoki persona-qa drive --config persona-qa.yaml --run-dir .artifacts/persona-qa/<run-id> --mode replay
kitsoki persona-qa review --config persona-qa.yaml --run-dir .artifacts/persona-qa/<run-id>
kitsoki persona-qa complete --config persona-qa.yaml --run-dir .artifacts/persona-qa/<run-id>
```

`drive --mode replay` is deterministic and cost-free. It proves artifact wiring
and review mechanics; proof-grade claims still need local, cassette, retained,
or external evidence that resolves through the review gate.

## Public Contracts

Versioned schemas live under `schemas/persona-qa/v1/` and are copied into new
kits by `init`:

- `config.schema.json`
- `persona.schema.json`
- `scenario.schema.json`
- `driver-manifest.schema.json`
- `run-bundle.schema.json`
- `leg-result.schema.json`
- `review.schema.json`

The shared completion-state contract remains `schemas/completion-state.schema.json`.
`kitsoki persona-qa complete` emits that job-agnostic result shape so CI,
arena, and UIs can score a run without scraping stdout.

## Transport Rules

Scenarios may declare `transports.allowed`, `transports.required`, and
per-transport evidence overrides. `--transport all` expands one scenario into
the applicable TUI, web, and VS Code bridge legs.

VS Code legs are always `bridge-level`: they prove the bridge/stub surface, not
a native editor integration. TUI and web legs are `frame-level` unless their
driver manifest defines stronger evidence.

## Story And Arena

`stories/scenario-qa` is a UI over the kit command. It calls
`tools/persona_qa/kit.py emit-run` to build a scoped run bundle, then drives and
judges one transport leg at a time. The story still uses the product-journey
runner's `--scenario-qa-report` deck fold because that is a specialized
derived-artifact operation.

The arena `persona-qa` plugin calls `tools/persona_qa/kit.py replay-smoke` for
no-LLM cells and reads completion state from the resulting run bundle. Live
arena cells are gated and use the same CLI to emit and review runs.

## CI Posture

Automated checks should use:

```sh
kitsoki persona-qa validate --config persona-qa.yaml
python3 tools/persona_qa/tests/test_kit_cli.py
python3 tools/product-journey/transport_axis_test.py
python3 tools/product-journey/scenario_qa_report_test.py
```

Do not call a live LLM from tests. Put replay inputs under `persona-qa/fixtures`
and deterministic local checks under `persona-qa/oracles`.
