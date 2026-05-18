# Kitsoki documentation

Welcome. This is the navigation hub for the documentation tree.

For the elevator pitch and quickstart, see the top-level
[`../README.md`](../README.md). For comparative grounding against
prior art (interactive fiction, statecharts, dialogue managers, LLM
orchestration), see [`prior-art.md`](prior-art.md).

---

## Read in this order

1. **[`architecture.md`](architecture.md)** — layers, packages, data
   flow, persistence model, conversation surfaces.
2. **[`state-machine.md`](state-machine.md)** — the directed cyclic
   graph: rooms, phases, states, intents, slots, world, guards, and
   the orchestrator's turn loop.
3. **[`authoring.md`](authoring.md)** — how to write an `app.yaml`.
   Patterns, common mistakes, scaling-up features.
4. **[`developer-guide.md`](developer-guide.md)** — for contributors:
   build, test, debug, add an intent / host / transport / subcommand.
5. **[`testing.md`](testing.md)** — Mode 1 (intent pass-rate) and
   Mode 2 (deterministic flow) tests; recordings; demo capture.
6. **[`hosts.md`](hosts.md)** — every built-in `host.*` handler with
   its input/output contract.
7. **[`transports.md`](transports.md)** — TUI, Jira, Bitbucket;
   sessions keyed by external thread; phase checkpoints.
8. **[`background-jobs/`](background-jobs/README.md)** — long-running
   handlers, inbox notifications, mid-flight clarifications.
9. **[`imports.md`](imports.md)** — composing apps across files and
   repos via the `imports:` block; the `/warp` slash command and
   `kitsoki run --warp` for operator smoke testing.
10. **[`prior-art.md`](prior-art.md)** — comparative grounding: what
    kitsoki borrows from (and rejects from) Inform/TADS/Ink/Yarn,
    XState/SCXML/Temporal/LangGraph, Rasa/Dialogflow/Bot Framework,
    and the MCP tool-shape conventions.
11. **[`semantic-routing.md`](semantic-routing.md)** — the four-tier
    routing stack between the deterministic match and the LLM:
    synonyms, templates, typed slot parsers, and the turncache. Plus
    `kitsoki replay-routing` and `kitsoki inspect --synonym-suggestions`
    for growing the synonym library.
12. **[`bugs.md`](bugs.md)** — filing story and kitsoki bug reports
    (`/meta story bug`, `/meta kitsoki bug`, `kitsoki bug create`),
    the on-disk markdown format, and the future `bug sync` design.
13. **[`story-style.md`](story-style.md)** — how a story should look:
    blocks, elements, colors, action menus, placeholders. The short
    guide; copy Oregon Trail when in doubt.

## Reference (embedded in the binary)

The files under [`embedded/`](embedded/) are compiled into the `kitsoki`
binary via `//go:embed` and served by `kitsoki docs <topic>`. They are
field-reference / LLM-prompt material — narrative + design rationale
lives in the top-level `docs/*.md` above.

| Topic | Where |
|---|---|
| Authoritative `app.yaml` schema | `kitsoki docs app-schema` (or [`embedded/app-schema.md`](embedded/app-schema.md)) |
| LLM-facing operator manual | `kitsoki docs llm-guide` (or [`embedded/llm-guide.md`](embedded/llm-guide.md)) |
| Implement a prose proposal against `app.yaml` | `kitsoki docs apply-proposal` (or [`embedded/apply-proposal.md`](embedded/apply-proposal.md)) |
| Markdown shape produced by `kitsoki render` | `kitsoki docs render-format` (or [`embedded/render-format.md`](embedded/render-format.md)) |

## Historical material

- [`proposals/`](proposals/) — proposal documents in design or
  partially shipped; kept for design context. The semantic-routing
  proposal has fully shipped; its design discussion is preserved at
  [`proposals/semantic-routing-proposal.md`](proposals/semantic-routing-proposal.md)
  for the open-questions appendix and the calibration history, but
  the user-facing reference now lives at
  [`semantic-routing.md`](semantic-routing.md).
