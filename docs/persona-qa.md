# Persona QA

Persona QA is a Kitsoki story workflow, not a separate tool the operator should
hunt for under `tools/`. The canonical surface is:

```sh
kitsoki run @kitsoki/scenario-qa
```

`@kitsoki/persona-qa` is a story alias for the same story — use whichever name
you remember; see [Naming](#naming) below.

## Naming

One product, four names — keep them straight:

| Name | What it is |
|---|---|
| **Persona QA** | The product. This doc is the canonical reference. |
| **`scenario-qa`** | The story you run: `kitsoki run @kitsoki/scenario-qa` (alias `@kitsoki/persona-qa`, [`stories/scenario-qa/README.md`](../stories/scenario-qa/README.md)). |
| **`product-journey-qa`** | The broader story for persona x scenario x 10-repo matrix sweeps, marathons, and autonomous fixing ([`stories/product-journey-qa/README.md`](../stories/product-journey-qa/README.md)). |
| **`product-journey`** | The deterministic runner backend both stories drive: `tools/product-journey/run.py` ([`tools/product-journey/README.md`](../tools/product-journey/README.md)). |
| **`persona_qa`** | A shared support kit (schemas, completion-state conversion, deck generation, no-LLM tests) — not an operator surface; see [Why `tools/persona_qa` Exists](#why-toolspersona_qa-exists). |

## Quickstart (5 minutes)

One prompt in, a verdict table and a deck out:

```sh
kitsoki run @kitsoki/scenario-qa
> check whether the onboarding tour renders on web transport=web
> report
```

`check` plans a run bundle, drives the transport(s) with the reusable
[`product-journey-qa-driver`](../.agents/agents/product-journey-qa-driver.md)
agent, and independently judges the captured evidence. Add `transport=tui,web`
or `transport=all` to check more than one transport (pausing after each for
`next transport`). `report` folds every recorded leg into
`.artifacts/product-journey/<run-id>/report.md` (the verdict table) and
`deck.slidey.json` (a Slidey deck of the run). For the fuller persona x
scenario x 10-repo matrix workflow, see
[`stories/product-journey-qa/README.md`](../stories/product-journey-qa/README.md)
and the [`product-journey-qa`](../.agents/skills/product-journey-qa/SKILL.md)
skill instead — this quickstart covers the narrow single-scenario `scenario-qa`
surface. The [`scenario-qa`](../.agents/skills/scenario-qa/SKILL.md) skill
covers the same narrow surface with the driving-agent contract.

From the story, use natural prompts or explicit `key=value` qualifiers:

```text
preview required user input affordance transport=web,tui target=<project-id>
preview scenario=<catalog-scenario-id> transport=all
check scenario=<catalog-scenario-id> transport=tui,web persona=<persona-id> target=<project-id>
check whether settings validation persists transport=web target=<project-id>
next transport
report
main room
```

The resting room also accepts unmatched prose as a `check_request`, so an
operator can type the behavior they want checked without first adding it to the
catalog. Use `scenario=<id>` when the request should bind to a catalog scenario;
otherwise the remaining prose becomes an ad-hoc scenario description. Supported
transport values are `all`, `tui`, `web`, `vscode`, `cli`, or a comma list.

`preview` is side-effect-free for both ad-hoc and catalog requests. For an
ad-hoc request it parses the same plain prose and qualifiers as `check`, uses a
generic transport carrier only to resolve the requested evidence contracts, then
renders the operator's requested behavior in the preview. It does not create a
run bundle, launch capture, or call an LLM.

`check` creates the run bundle under `.artifacts/product-journey/<run-id>/`,
then drives one transport-pinned check at a time. Multi-transport checks pause
after each transport so the operator can inspect evidence before continuing
with `next transport`.
`report` folds the recorded driver and judge outcomes into:

- `report.md` for the per-transport verdict table.
- `deck.slidey.json` for the deterministic Slidey deck.

The closeout report keeps the result counts at the top, exposes `report.md` as
the primary clickable summary artifact in the web/TUI `kv` renderer, and offers
`main room` to return to the Scenario QA start screen without discarding the
last run.

## Transport Contract

One scenario can drive any transport it declares without a custom harness path.
The story passes `transport=tui`, `web`, `vscode`, `cli`, a comma list, or
`all` into `tools/product-journey/run.py`. The runner expands each applicable
scenario into scenario x transport checks and writes the stable route into
`driver-plan.json`.

Each transport check carries:

- `leg_id`, `scenario`, `transport`, and `visual_surface`.
- The primary story and natural task prompt.
- Required MCP capabilities and resolved driver tools.
- The evidence contract and proof level.
- Stable open, observe, and act entrypoints.
- A no-substitution policy: demo, placeholder, synthetic, or unrelated media
  cannot satisfy proof.

VS Code checks are bridge-level proof unless a future native editor integration
raises the contract. CLI checks are terminal-level proof: command transcript,
exit code, cwd, and trace references, not visual state. TUI and web checks are
frame-level proof by default.

## Evidence And Decks

The story never treats a driver self-report as proof. The driver captures what
it can, then a read-only judge grades the captured evidence. Missing, degraded,
or fake media becomes `degraded-evidence` or a blocker, not a pass.

`deck.slidey.json` is generated from existing run artifacts. The same run
bundle and flags produce stable JSON bytes. Scenario QA report decks derive
session replay scenes from recorded leg results (`playback_path`, `rrweb_path`,
`video_path`, or playback-looking `evidence_refs`). Matrix and retained-run
decks still derive playback scenes from `media-manifest.json`. Demo or
placeholder media remains visible as blocked evidence rather than being treated
as proof.

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

## History

Persona QA's exploratory-walkthrough posture (drive a story as a skeptical
persona, judge the rendered screen, report findings) originated in the
`story-qa-agent` / `qa-agent-skill` proposals. Their substrate shipped as the
MCP studio (`docs/architecture/mcp-studio.md`) and the `.agents/skills/story-qa/`
skill + `docs/stories/story-qa.md` guide; their persona/scenario/transport
product surface shipped as this story. Both proposals were deleted per the
proposal lifecycle once their content was covered here and in those docs.
