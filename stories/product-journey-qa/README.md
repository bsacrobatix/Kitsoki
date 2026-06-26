# Product Journey QA Story

This story wraps the deterministic product-journey runner so operators can drive
the standard persona/scenario evidence workflow inside Kitsoki.

It is intentionally no-LLM:

- `matrix` calls `tools/product-journey/run.py --emit-matrix --json-output` to
  create the 10-repo GitHub assignment plan.
- `rollup` calls `tools/product-journey/run.py --rollup-matrix --json-output`
  to create or refresh the matrix-level Slidey deck from reviewed run bundles.
- `validate_matrix` calls `tools/product-journey/run.py --validate-matrix
  --json-output` to check the matrix and rollup artifact contract without
  rewriting files.
- `start` calls `tools/product-journey/run.py --emit-run --json-output`.
- `attach` calls `tools/product-journey/run.py --attach-evidence --json-output`.
- `record` calls `tools/product-journey/run.py --record-finding --json-output`
  for strengths, weaknesses, issues, and fixes.
- `seed_demo` calls `tools/product-journey/run.py --seed-demo-evidence
  --json-output` to populate a no-LLM review bundle.
- `review` calls `tools/product-journey/run.py --review-run --json-output` to
  write `review.json` and score whether the bundle is ready for human review.
- `validate` calls `tools/product-journey/run.py --validate-run --json-output`
  to check required files, metrics freshness, media coverage, scenario
  outcomes, review statuses, and Slidey scenes without rewriting files.
- Flow fixtures stub `host.run`, so automated tests never call a live model or
  external service.

The generated run bundle under `.artifacts/product-journey/<run-id>/` is the
handoff point for visual MCP, Studio MCP, oracle results, and Slidey review.
Use `agent-brief.md` inside that bundle to drive the live persona session, then
use `driver-plan.md` for the scenario harness/visual-surface/action contract and
`execution-plan.md` to copy the generated `--attach-evidence` commands.
Captured media is indexed in `media-manifest.json` so the generated Slidey deck
can expose playback-ready videos and screenshots without scraping prose.
Scenario-level evidence and finding summaries are written to
`scenario-outcomes.md` for review and matrix rollups.
Run `review` before `validate`; review refreshes derived artifacts, while
validation deliberately catches stale or inconsistent bundles.
