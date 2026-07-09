# Persona QA

Persona QA is a Kitsoki story workflow, not a separate tool the operator should
hunt for under `tools/`. The canonical surface is:

```sh
kitsoki run @kitsoki/scenario-qa
```

From the story, use natural prompts:

```text
preview bugfix across all transports
check bugfix across all transports for core-maintainer on gears-rust
next leg
report
```

`preview` is side-effect-free: it asks the deterministic product-journey runner
for the scenario x transport suite, binds the leg count into story state, and
does not create a run bundle, launch capture, or call an LLM.

`check` creates the run bundle under `.artifacts/product-journey/<run-id>/`,
then drives one transport-pinned leg at a time. Multi-transport checks pause
after each leg so the operator can inspect evidence before continuing with
`next leg`.
`report` folds the recorded driver and judge outcomes into:

- `report.md` for the per-transport verdict table.
- `deck.slidey.json` for the deterministic Slidey deck.

## Transport Contract

One scenario can drive any transport it declares without a custom harness path.
The story passes `transport=tui`, `web`, `vscode`, `cli`, a comma list, or
`all` into `tools/product-journey/run.py`. The runner expands each applicable
scenario into scenario x transport legs and writes the stable route into
`driver-plan.json`.

Each leg carries:

- `leg_id`, `scenario`, `transport`, and `visual_surface`.
- The primary story and natural task prompt.
- Required MCP capabilities and resolved driver tools.
- The evidence contract and proof level.
- Stable open, observe, and act entrypoints.
- A no-substitution policy: demo, placeholder, synthetic, or unrelated media
  cannot satisfy proof.

VS Code legs are bridge-level proof unless a future native editor integration
raises the contract. CLI legs are terminal-level proof: command transcript,
exit code, cwd, and trace references, not visual state. TUI and web legs are
frame-level proof by default.

## Evidence And Decks

The story never treats a driver self-report as proof. The driver captures what
it can, then a read-only judge grades the captured evidence. Missing, degraded,
or fake media becomes `degraded-evidence` or a blocker, not a pass.

`deck.slidey.json` is generated from existing run artifacts. The same run
bundle and flags produce stable JSON bytes. Playback scenes are derived from
`media-manifest.json`; only proof-grade local, retained, external, or cassette
items become video scenes. Demo or placeholder media remains visible as blocked
evidence.

Committed examples:

- [persona-qa-kitsoki-example.slidey.json](decks/persona-qa-kitsoki-example.slidey.json)
  shows missing playback surfaced as blocked evidence rather than fake video.
- [persona-qa-slidey-architect-review.slidey.json](decks/persona-qa-slidey-architect-review.slidey.json)
  reviews Slidey from the lens of a software architect who frequently makes
  technical presentations.

Maintainer-only deterministic regeneration for those fixture decks still uses
the internal deck adapter because the source is a retained run fixture, not a
live story session:

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

When a generated deck is committed under `docs/decks/`, keep referenced rrweb
clips under `docs/decks/assets/<deck-id>/` and bundle the viewer for the product
site deck gallery.

## Why `tools/persona_qa` Exists

`tools/persona_qa` is not the product surface. It is a deterministic support
package used by the story, runner, arena adapters, and tests for work that does
not belong in YAML:

- Public schema templates for portable persona/scenario/driver catalogs.
- Completion-state conversion shared with arena and CI scoring.
- Deck generation from retained run fixtures.
- UI QA/UI review verdict adapters.
- No-LLM unit tests for portable kit compatibility.

The story owns orchestration, operator pacing, evidence capture, judging, and
deck/report close-out. The Python package owns pure data transforms and
compatibility adapters.

## Public Contracts

Versioned schemas live under `schemas/persona-qa/v1/`:

- `config.schema.json`
- `persona.schema.json`
- `scenario.schema.json`
- `driver-manifest.schema.json`
- `run-bundle.schema.json`
- `leg-result.schema.json`
- `transport-suite.schema.json`
- `review.schema.json`

The shared completion-state contract is
`schemas/completion-state.schema.json`.

## No-LLM Gates

Automated checks should stay deterministic:

```sh
GOCACHE=/private/tmp/kitsoki-gocache go run ./cmd/kitsoki test flows stories/scenario-qa/app.yaml
python3 tools/product-journey/transport_axis_test.py
python3 tools/product-journey/scenario_qa_report_test.py
python3 tools/persona_qa/tests/test_kit_cli.py
python3 tools/persona_qa/tests/test_deck_cli.py
```

Do not call a live LLM from tests. Put replay inputs under fixtures and record
honest blockers when proof evidence cannot be captured.
