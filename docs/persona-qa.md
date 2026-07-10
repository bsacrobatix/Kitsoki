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

## Authorization & Harness (fail-closed)

`profile=<name>` is the ONLY live authorization. Omitting it keeps every leg
of a `check` on `harness: "replay"` for its whole nested session — never a
per-step judgment call by the driver agent, and never something the operator
finds out about only after the fact from a mysterious `degraded-evidence`
verdict. The contract runs in both directions:

- **No silent replay-only fallback.** When a leg looks interpretive (a
  catalog scenario with `natural_utterances` — free-text phrasing a cassette
  can't cover — or any ad-hoc description) and no `profile=` was supplied,
  `scripts/plan_legs.star` computes that fact deterministically at plan time
  and names it per leg, up front, before any leg is driven — visible in the
  `plan` and `execute` rooms' "Live authorization notes" and attached to the
  leg itself (`live_authorization_note`) so the driver agent sees it too.
- **No silent replay-miss-goes-live.** A `session.new` call opened with
  `harness: "replay"` hard-fails the instant it dispatches a `host.agent.*`
  call (converse/decide/task/ask/extract/search) with no matching cassette
  episode — the MCP session runtime itself refuses to fall through to a live
  agent (`internal/mcp/studio/session_runtime.go`'s replay-agent-miss
  handler). This is enforced in code the driver cannot opt out of; the
  driver's job is only to report that hard error as the leg's blocker, never
  to retry the same step by opening a new session with `harness: "live"`
  without a supplied `profile=`.
- **No unexplained degraded verdict.** Every leg whose verdict is not `pass`
  carries a `cause` (`stories/scenario-qa/scripts/record_leg_result.star`) —
  missing evidence, a reported blocker (including a replay-miss's hard error
  text), no live authorization, an unauthorized live harness, or the judge's
  own grounding — shown in `report.md`'s Cause column and the report room's
  transport-verdict lines. Nothing is left as a bare `degraded-evidence` with
  no stated why.
- **No unauthorized live self-authorization.** If a driver ever reports it
  used `harness_used: "live"` for a leg while this check never supplied
  `profile=`, `record_leg_result.star` deterministically forces that leg's
  verdict to `degraded-evidence` with an explicit policy-violation cause,
  regardless of what the driver or judge otherwise reported. Prompt
  instructions (`.agents/agents/product-journey-qa-driver.md`,
  `stories/scenario-qa/prompts/drive_leg.md`) state the same rule, but this
  deterministic check is what actually enforces it.

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
