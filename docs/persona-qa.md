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
kitsoki persona-qa transports --config persona-qa.yaml --scenario project-onboarding --transport all
kitsoki persona-qa emit-run --config persona-qa.yaml --scenario project-onboarding --transport all --preview
kitsoki persona-qa emit-run --config persona-qa.yaml --project local-app --persona core-maintainer --scenario project-onboarding --transport all
kitsoki persona-qa drive --config persona-qa.yaml --run-dir .artifacts/persona-qa/<run-id> --mode replay
kitsoki persona-qa review --config persona-qa.yaml --run-dir .artifacts/persona-qa/<run-id>
kitsoki persona-qa deck --config persona-qa.yaml --run-dir .artifacts/persona-qa/<run-id> --out docs/decks/persona-qa-latest.slidey.json
kitsoki persona-qa complete --config persona-qa.yaml --run-dir .artifacts/persona-qa/<run-id>
```

`transports` and `emit-run --preview` are side-effect-free. They list the
scenario x transport legs that would be planned, the stable open/observe/act
entrypoints for each leg, the evidence contract, proof level, preflight, and
recording rule. Use this before live capture to confirm one scenario can be
driven on the intended surfaces without custom harness setup.

`drive --mode replay` is deterministic and cost-free. It proves artifact wiring
and review mechanics; proof-grade claims still need local, cassette, retained,
or external evidence that resolves through the review gate. Demo, placeholder,
synthetic, or unproven local media must be recorded as missing/blocking
evidence, not as proof.

## Deterministic Slidey Decks

`kitsoki persona-qa deck` turns an existing run bundle into a Slidey JSON deck
without recording media, opening browsers, or calling an LLM. It reads the
already-produced bundle artifacts:

- `run.json`
- `media-manifest.json`
- `scenario-outcomes.json`
- `review.json`
- `findings.json`
- `metrics.json`
- `driver-plan.json`
- `driver-journal.json`

The command writes stable JSON: the same input artifacts and flags produce the
same deck bytes. Playback scenes are derived from `media-manifest.json`, but
only proof-grade playback items become standalone video scenes. Local rrweb
clips must have capture provenance, resolve on disk, and pass a structural
placeholder check; demo or placeholder items remain visible as blocked evidence
rows instead of becoming videos. If a manifest claims captured or validated
local playback that is not proof-grade, the deck command exits `2` and does not
write a replacement deck.

Example regeneration commands:

```sh
python3 tools/persona_qa/kit.py deck \
  --run-dir tools/persona_qa/examples/runs/kitsoki-product-review \
  --out docs/decks/persona-qa-kitsoki-example.slidey.json \
  --title "Kitsoki Persona QA Example"

python3 tools/persona_qa/kit.py deck \
  --run-dir tools/persona_qa/examples/runs/slidey-architect-review \
  --out docs/decks/persona-qa-slidey-architect-review.slidey.json \
  --title "Slidey Architect Review"
```

Committed examples:

- [persona-qa-kitsoki-example.slidey.json](decks/persona-qa-kitsoki-example.slidey.json)
  shows a Kitsoki run with missing playback surfaced as blocked evidence rather
  than fake videos.
- [persona-qa-slidey-architect-review.slidey.json](decks/persona-qa-slidey-architect-review.slidey.json)
  reviews Slidey from the lens of a software architect who makes technical
  presentations frequently. The review calls out Slidey's strengths for
  reproducible deck-as-code, technical visual vocabulary, and playback evidence,
  plus the asset/bundling discipline needed for repeated presentation work. Two
  playback scenes are proof-grade reused rrweb captures; the incomplete
  deck-maintenance clip is shown as missing evidence.

When a generated deck is committed under `docs/decks/`, keep any referenced
rrweb clips under `docs/decks/assets/<deck-id>/` and bundle the viewer for the
product-site deck gallery:

```sh
slidey bundle docs/decks/persona-qa-slidey-architect-review.slidey.json \
  docs/decks/bundled/persona-qa-slidey-architect-review.html
```

## Public Contracts

Versioned schemas live under `schemas/persona-qa/v1/` and are copied into new
kits by `init`:

- `config.schema.json`
- `persona.schema.json`
- `scenario.schema.json`
- `driver-manifest.schema.json`
- `run-bundle.schema.json`
- `leg-result.schema.json`
- `transport-suite.schema.json`
- `review.schema.json`

The shared completion-state contract remains `schemas/completion-state.schema.json`.
`kitsoki persona-qa complete` emits that job-agnostic result shape so CI,
arena, and UIs can score a run without scraping stdout.

## Transport Rules

Scenarios may declare `transports.allowed`, `transports.required`, and
per-transport evidence overrides. `--transport all` expands one scenario into
the applicable TUI, web, VS Code bridge, and CLI legs.

The canonical transport catalog lives in `tools/persona_qa/transports.py`.
The product CLI, compatibility runner, scenario-qa story, schemas, and
validation gates all consume that same catalog, so adding or changing a
transport should fail validation if any surface drifts.

VS Code legs are always `bridge-level`: they prove the bridge/stub surface, not
a native editor integration. CLI legs are `terminal-level`: they prove command
transcripts, exit codes, cwd, and trace references rather than visual state. TUI
and web legs are `frame-level` unless their driver manifest defines stronger
evidence.

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
kitsoki persona-qa transports --config persona-qa.yaml --scenario project-onboarding --transport all
python3 tools/persona_qa/tests/test_kit_cli.py
python3 tools/persona_qa/tests/test_deck_cli.py
python3 tools/product-journey/transport_axis_test.py
python3 tools/product-journey/scenario_qa_report_test.py
```

Do not call a live LLM from tests. Put replay inputs under `persona-qa/fixtures`
and deterministic local checks under `persona-qa/oracles`.
