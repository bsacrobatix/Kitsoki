# Model Harness Eval Story

This story turns a broad evaluation question into review artifacts:

- a Markdown report;
- a slide-style HTML deck;
- a machine-readable summary JSON.
- a concrete fastest/cheapest/best/selected model-harness proposal;
- an applied override result.

The operator starts with `start question=...`. The `reporter` agent runs the
offline evidence process documented in `docs/testing/model-harness-eval-pilot.md`
and returns a schema-validated `report_artifact` with exact paths, limits,
configured options, recommendations, and override status.

By default the story applies a local override to
`/Users/brad/code/Kitsoki/.kitsoki.local.yaml`. That main-checkout file is the
machine-local source of truth even when this story is being authored or run from
a worktree. `apply_mode=project` can target another override path, while
`apply_mode=author` is reserved for updating a base story call-site
`selection:` after the operator supplies `target_story` and `target_call`.

Automated flows stub `host.agent.task`; they do not call a live LLM or run paid
provider-backed benchmarks.
