# dev-story-mining

Turn real Claude Code transcripts into named gates for **dev-story** — the
repeatable process from
[`.context/dev-story-from-transcripts.md`](../../.context/dev-story-from-transcripts.md)
(and [`docs/proposals/session-pattern-mining/`](../../docs/proposals/session-pattern-mining/)),
made first-class and runnable as a kitsoki story.

A meta / dogfood story: kitsoki improving the very state machine it runs its
own development on. The mechanical skeleton (mine → grep the inventory → author
→ test) is automated; the few real judgements are named checkpoint gates with a
recorded decision at each — the same shape mining is built to find.

## The pipeline

```
idle ──start──▶ mine ──▶ map ──▶ decide ──▶ author ──▶ record ──▶ @exit:done
                 │         │        │          │           │
                 └ refine ─┴ refine ┴ refine ──┴ refine ───┴ refine (budgeted)
                                                            │
   any room: quit / budget-exhausted ──────────────▶ @exit:abandoned
```

| Phase | Producer (persona) | Decision the gate records |
|---|---|---|
| **mine** | `miner` (`host.oracle.task`) | Is the brief fresh & large enough (≥ `min_intents`, recency sample)? |
| **map** | `mapper` (`host.oracle.task`) | Each theme classified `ALREADY-MODELED` / `ENRICH` / `GAP` against the *regenerated* gate inventory — never from memory. |
| **decide** | `ranker` (`host.oracle.ask`) | Which ENRICH/GAP item to ticket next (rank by #intents × mechanicalness). |
| **author** | `author` (`host.oracle.task`) | Gate + flow fixture authored; **accept is refused while `flows_green` is false**. |
| **record** | `recorder` (`host.oracle.ask`) | Can an existing gate drop a determinism rung (L2→L3→L4)? Empty result is valid. |

Each phase produces a schema-validated artifact in its `on_enter` (idempotent
via `once:` — reload-safe; the refine/restart arms clear the bind to force a
fresh run). The view renders the artifact; the operator (or the LLM judge)
accepts / refines / restarts / quits.

## Judge polymorphism

One `world.judge_mode` flag selects who answers every checkpoint (mirrors
`stories/bugfix`):

- `human` — operator answers (no judge LLM call).
- `llm` — run the judge; a confident verdict is captured, uncertain holds.
- `llm_then_human` — confident verdict auto-advances; uncertain falls through
  to the human view.

`judge_confidence_threshold` (default 0.8) is the auto-advance floor.

## Entry / exits (importable contract)

- **Entry state:** `idle`.
- **Exits:**
  - `done` — `requires: [record_artifact]` — a gate was authored (or a ladder
    move recorded). An importer maps it via `imports.<alias>.exits.done.to`.
  - `abandoned` — operator quit or a phase budget was exhausted.
- **`world_in` contract (optional overrides):** `job`, `project_dir`
  (transcripts dir; empty → current repo), `stories_dir` (tree to enrich,
  default `stories`), `min_intents`, `judge_mode`, `judge_confidence_threshold`.
- **Intent surface:** exports `start, accept, refine, restart_from, quit, look`.
- **Hosts required:** `host.run`, `host.oracle.task`, `host.oracle.ask`,
  `host.oracle.decide`. No `host_interfaces` — the story runs standalone with no
  transport registry; the phase artifacts are the durable record.

## Run it

```sh
# standalone, human-driven
kitsoki run stories/dev-story-mining/app.yaml

# deterministic, LLM-free flow tests (seeded artifacts short-circuit on_enter)
kitsoki test flows stories/dev-story-mining/app.yaml
```

Flows: `flows/happy_human.yaml` (accept through to `@exit:done`),
`flows/map_refine_budget.yaml` (refine past the map budget bails to
`@exit:abandoned`).

## Not yet wired

The `miner` / `author` personas describe the real kit and authoring loop in
their prompts, but this story does not yet ship cassettes for a recorded
end-to-end run against a live oracle — the flow fixtures cover the state machine
only. Recording those (and a `dev-story` / `kitsoki-dev` hub room that offers
"improve myself" as an entry into this story) is the natural next step.
