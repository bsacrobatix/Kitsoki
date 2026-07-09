# Epic: Universal Product QA Campaign

**Status:** Draft v1. First catalog/docs slice implemented in `tools/product-journey/personas.json`, `tools/product-journey/scenarios.json`, and `tools/product-journey/README.md`; story/runtime/arena/product docs slices remain.
**Kind:**   epic
**Slices:** 5 (1/5 shipped)

## Why

Kitsoki has a strong persona/scenario QA substrate, but the current use still reads as a Kitsoki-internal dogfood harness. The product needs a smooth standing campaign loop that any Kitsoki user can apply to their own stories: define personas and free-form scenarios, launch Codex/Kitsoki agents through Studio MCP, capture web/TUI/docs/product-site evidence, file credible findings through the existing issue pipeline, optionally send bounded work to a VM/arena worker, and keep a Slidey rollup current from artifacts.

## What Changes

The end state is a reusable campaign product surface:

- Personas and scenarios are project-owned catalog data, with first-class coverage for product site, docs, MCP, Codex agent launch, web, TUI, remote worker readiness, and stakeholder rollups.
- `stories/product-journey-qa` owns standing campaign cadence, watchdogs, issue/fix routing, and deck/report refreshes rather than leaving operators to paste runner commands together.
- Arena/remote worker placement becomes a supported driver backend for bounded campaign batches, with readiness, receipts, artifact collection, and recovery instructions.
- Findings reuse the existing evidence-backed issue/fix pipeline; local developer stabilization findings default to `.artifacts/issues/bugs` unless the scenario boundary is a GitHub handoff.
- The human-facing state is a Slidey deck plus a concise markdown/JSON summary regenerated from retained artifacts.

## Impact

- **Spans:** story, runtime, tracing, docs, arena.
- **Net surface:** `tools/product-journey`, `stories/product-journey-qa`, `stories/scenario-qa`, `tools/arena`, issue filing/fixing, and product docs/decks.
- **Docs on ship:** `docs/persona-qa.md`, `docs/stories/product-journey-qa.md`, `docs/guide/development/agentic-qa-campaigns.md`, and `docs/media/README.md` for deck/evidence placement.

## Slices

| # | Slice | Kind | Scope (one line) | Depends on | Status | File |
|---|---|---|---|---|---|---|
| 1 | Campaign catalog | story | Add general-purpose personas/scenarios and README guidance for product-site/docs/MCP/agent-launch/worker/rollup QA. | - | Shipped in this slice | This epic |
| 2 | Campaign story surface | story | Add explicit standing-campaign intents for plan, tick, attach, issue/fix, stats, and deck refresh. | 1 | Draft | TBD |
| 3 | Worker backend | runtime | Route bounded campaign batches to local or VM arena workers with readiness, receipts, and artifact import. | 1 | Draft | TBD |
| 4 | Finding sinks | runtime/tracing | Make local issue artifacts and GitHub evidence-backed filing a declared campaign policy with traceable sink decisions. | 1, 2 | Draft | TBD |
| 5 | Product docs and decks | docs | Publish the general user guide, campaign summary shape, and canonical Slidey rollup contract. | 1-4 | Draft | TBD |

## Sequencing

```text
#1 catalog -> #2 story cadence -> #4 finding sinks -> #5 docs/decks
      \-> #3 worker backend --------------------------/
```

The catalog can ship first because it exercises existing runner/story behavior without adding a new execution substrate. Worker placement can develop in parallel once the campaign batch and artifact receipt shape is stable.

## Shared Decisions

1. The scenario/persona catalog is the source of truth. Recorders, workers, Playwright specs, and rrweb/TUI capture adapters consume generated run bundles and `capture_routes`; they do not own private case lists.
2. Automated gates stay no-LLM. Live LLM/persona capture is an explicit campaign run mode, not a unit test behavior.
3. The issue sink is policy-driven. Local stabilization findings stay local by default; GitHub is used when the handoff boundary is remote collaboration, hosted gh-agent repair, or a user-facing issue report.
4. Slidey is the preferred human rollup when it adds clarity. Markdown/JSON remain canonical machine and audit artifacts.
5. Any red evidence, validation, issue, gh-agent, worker, or playback gate keeps the campaign summary conservative.

## Cross-Cutting Open Questions

1. Worker identity: should VM workers be registered in arena config, a project-local persona-QA config, or both? *Lean: arena owns placement; persona-QA config names campaign policy.*
2. Campaign state: should the standing summary live as one long-lived `.artifacts/product-journey/campaigns/<id>` directory or as a project graph object? *Lean: artifact directory first; graph object after the project-object graph substrate is ready.*
3. Public product proof: which campaign decks should be promoted from `.artifacts` to `docs/decks/`? *Lean: only curated, non-private, retained-proof decks with media provenance.*

## Non-Goals

- Replacing `stories/scenario-qa`; single-scenario transport checks remain the narrow path.
- Filing every developer-found bug to GitHub.
- Running live LLMs from automated tests.
- Building a Kitsoki-only harness that cannot be configured for another user's stories.
