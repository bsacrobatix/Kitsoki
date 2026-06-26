# Model Harness Eval Story

This story turns a broad evaluation question into review artifacts:

- a Markdown report;
- a deterministic Slidey JSON deck source under `.artifacts/model-harness-eval/<run>/`;
- a durable case study under `docs/case-studies/` when requested;
- a machine-readable summary JSON.
- a concrete fastest/cheapest/best/selected model-harness proposal;
- an override result, or an explicit report-only result.

The operator starts with `start question=...`. The `reporter` agent runs the
evidence process documented in `docs/testing/model-harness-eval-pilot.md` and
returns a schema-validated `report_artifact` with exact paths, limits,
configured options, recommendations, and override status.
The story then writes that validated object to
`.artifacts/model-harness-eval/<run>/report_artifact.json` and runs the shared
deterministic deck generator (`tools/report-deck/deterministic_deck.py --kind
model-harness`) to produce the trusted Slidey source. The reporter agent does
not author deck structure or slide prose; it only supplies schema-checked data
and summaries that the deterministic deck consumes.

Use `apply_mode=report` when the desired output is a case study or deck and the
story must not mutate any harness defaults. In report mode the artifact still
records the selected candidate, but `override.applied` is `false`.

Use `live_policy=operator_approved` only when the operator has explicitly allowed
subscription-backed or provider-backed benchmark collection for the current run.
Default and automated flow runs stay `live_policy=no_cost` and stub
`host.agent.task`; they must not call a real LLM or incur costs.

When `apply_mode=local`, the story applies a local override to
`/Users/brad/code/Kitsoki/.kitsoki.local.yaml`. That main-checkout file is the
machine-local source of truth even when this story is being authored or run from
a worktree. `apply_mode=project` can target another override path, while
`apply_mode=author` is reserved for updating a base story call-site
`selection:` after the operator supplies `target_story` and `target_call`.

Automated flows stub `host.agent.task`; they do not call a live LLM or run paid
provider-backed benchmarks. The deck post-processing calls are deterministic and
stubbed in flow fixtures.
