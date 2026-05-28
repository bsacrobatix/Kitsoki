# implementation — small-task pipeline

Wave 3 / Phase 6 of the dev-story / bugfix unify design. A
lighter-weight sibling to `stories/bugfix/` for tickets of type
`task` — no reproduction, no separate security-review pass. Five
visible rooms plus a `handoff` that drills into the `pr-refinement`
import:

```
idle → review_task → write_code → test → review → handoff (→ pr-refinement)
```

Each `_awaiting_reply` checkpoint follows the canonical checkpoint
shape from the dev-story implementation contract: post + inbox +
LLM-judge + emit_intent. `world.judge_mode` selects who answers
without forking the state graph.

## Standalone

```
kitsoki run stories/implementation/app.yaml
```

## Imported

Parent stories (`stories/dev-story/`, `stories/kitsoki-dev/`) import
implementation as one import edge. The `done` exit projects through to
`pr-refinement`'s `merged` and back to the parent's main; `abandoned`
short-circuits to the parent's main with `status: abandoned`.

## Exits

| Name | Description | `requires:` keys |
|---|---|---|
| `done` | Pipeline succeeded; PR was opened + merged via `pr-refinement`. | `code_artifact` |
| `abandoned` | User or LLM bailed. | — |

## Visible rooms

| Room | Substates | Checkpoint? | On `accept` |
|---|---|---|---|
| `idle` | one atomic | n/a | `review_task_executing` (via `start`) |
| `review_task` | `_executing`, `_awaiting_reply` | yes — `task_summary_artifact` | `write_code_executing` |
| `write_code` | `_executing`, `_awaiting_reply` | yes — `code_artifact` | `test_executing` |
| `test` | `_executing`, `_awaiting_reply` | yes — `test_artifact` | `review_executing` |
| `review` | `_executing`, `_awaiting_reply` | yes — `review_artifact` | `handoff` |
| `handoff` | one atomic | n/a | `pr` (the pr-refinement import compound) |

The `test` and `review` rooms' `refine` arcs both bounce back to
`write_code_executing` with feedback — the loop closes around the
code-write room rather than the local checkpoint.

## See also

- [`stories/bugfix/`](../bugfix/) — the heavier sibling with reproduction.
- [`stories/pr-refinement/`](../pr-refinement/) — the shared tail.
