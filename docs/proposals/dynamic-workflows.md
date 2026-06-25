# Epic: Dynamic Workflows

**Status:** Draft v1. No slices implemented yet.
**Kind:**   epic
**Slices:** 4 (0/4 shipped)

## Why

Claude's dynamic workflows make an agent write a JavaScript plan, decompose the task, and install validation gates. Kitsoki already has the safer substrate for that pattern: YAML stories, load-time validation, deterministic flow tests, host cassettes, JSONL traces, Studio MCP sessions, and a web UI that can track a live run. The missing product loop is letting an operator ask Kitsoki to synthesize a workflow, validate and execute it in the Kitsoki engine immediately, then promote the proven run into a reusable base story with starter fixtures.

## What changes

Kitsoki gains a dynamic workflow lane across MCP, CLI, TUI, and web:

1. An operator describes a one-off workflow in natural language.
2. Kitsoki creates a temporary story package in YAML, not JavaScript, with explicit rooms, intents, world schema, host calls, and validation gates.
3. The draft is validated before it can run.
4. The draft runs through the existing session engine, trace sink, cassettes, and runstatus UI.
5. The operator can export the proven draft as a reusable base story, plus starter flow fixtures and host cassettes derived from the run.

The first implementation should be conservative: dynamic workflows are ordinary stories in a scratch namespace with extra provenance, promotion, and cleanup metadata. There is no second workflow interpreter.

## Impact

- **Spans:** runtime, story, tracing, TUI/web/CLI/MCP surface.
- **Net surface:** new dynamic-workflow authoring session, new draft-story package convention, CLI/MCP/web/TUI entrypoints, runstatus tracking URL, promotion/export commands.
- **Docs on ship:** `docs/architecture/mcp-studio.md`, `docs/stories/architecture.md`, `docs/stories/state-machine.md`, `docs/tracing/trace-format.md`, `docs/web/README.md`, and a new `docs/stories/dynamic-workflows.md`.

## Slices

| # | Slice | Kind | Scope (one line) | Depends on | Status | File |
|---|---|---|---|---|---|---|
| 1 | Draft package and validator | runtime | Define the scratch story package, load invariants, provenance, and validation gate for generated workflows. | - | Draft | [`dynamic-workflow-drafts.md`](dynamic-workflow-drafts.md) |
| 2 | Authoring and launch surfaces | story | Add the operator-facing lane across Studio MCP, CLI, TUI, and web that creates and starts a dynamic workflow. | 1 | Draft | [`dynamic-workflow-authoring-surfaces.md`](dynamic-workflow-authoring-surfaces.md) |
| 3 | Tracking URL and execution receipts | tracing | Reuse sessions, traces, runstatus, and Studio MCP handles so every dynamic run has a durable receipt and browser URL. | 1, 2 | Draft | [`dynamic-workflow-tracking.md`](dynamic-workflow-tracking.md) |
| 4 | Promotion and cassette export | tracing | Promote a successful scratch workflow into a reusable base story and starter deterministic artifacts. | 1, 3 | Draft | [`dynamic-workflow-promotion.md`](dynamic-workflow-promotion.md) |

## Sequencing

```text
#1 draft package + validator
  -> #2 authoring surfaces
  -> #3 tracked execution URL
  -> #4 promotion/export
```

Slice 1 must land first because every surface should call the same validator and scratch-package writer. Slice 3 can begin once a scratch workflow can launch. Slice 4 waits until traces and run receipts are stable enough to export fixtures honestly.

## Shared decisions

1. Dynamic workflows are Kitsoki stories, not a new language. The generated artifact is YAML plus scripts/cassettes that load through `app.Load`, `story.validate`, `story.test`, `session.new`, and `kitsoki web`.
2. The interpretive seam is authoring only. Once a draft validates, execution uses ordinary transitions, effects, host calls, and deciders. Any LLM decision remains a declared `agent.*` call with trace records.
3. Scratch drafts live under `.artifacts/dynamic-workflows/<id>/` by default. Promotion copies only reviewed files into `stories/<name>/` or `internal/basestories/stories/<name>/`.
4. The tracking URL is a runstatus session URL. CLI and MCP return it when a web server is available; otherwise they return the trace path and a command to open it.
5. Exported cassettes are starters, not claims of complete test coverage. Promotion should also write a TODO/checklist for missing gates and hand-authored fixture hardening.

## Cross-cutting Open Questions

1. Should the first authoring lane be a dev-story room, a direct MCP tool, or both? *Lean: both, with the MCP/CLI command using the same host function as the story room.*
2. Should promotion target project-local `stories/` by default or internal base stories? *Lean: project-local by default; base-story promotion requires an explicit flag and clean review.*
3. How much can cassette export infer automatically from a trace? *Lean: generate a starter fixture and mark unresolved live decisions rather than synthesizing false determinism.*

## Non-goals

- Running arbitrary JavaScript workflow definitions.
- Keeping a second workflow runtime beside the story engine.
- Auto-promoting generated workflows without operator review.
- Replacing existing flow fixtures or hand-authored story QA.
