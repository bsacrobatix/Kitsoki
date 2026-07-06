# Product Journey QA Story

This story wraps the deterministic product-journey runner so operators can drive
the standard persona/scenario evidence workflow inside Kitsoki.

It is intentionally no-LLM:

- `validate_corpus` calls `tools/product-journey/run.py --validate-corpus
  --json-output` to preflight personas, scenarios, quality gates, evidence
  hints, and the 10-repo GitHub target catalog before a sweep.
- `refresh_targets` calls `tools/product-journey/run.py
  --refresh-github-targets --json-output` to write a GitHub target-proof
  artifact for the 100-open-bug and popularity matrix contract.
- `load` calls `tools/product-journey/run.py --summarize-run --json-output` to
  load an existing run bundle into the story. This is the MCP-only driver entry
  point when the reusable driver receives a `run_dir` and needs to attach
  evidence through Kitsoki instead of reading files directly. After `load`, the
  story world `last_result` contains `driver_scenarios`,
  `missing_proof_evidence`, and `driver_final_gates` for the driver to inspect
  through MCP.
- `matrix` calls `tools/product-journey/run.py --emit-matrix --json-output` to
  create the 10-repo GitHub assignment plan. Pass `target_proof_file=...` after
  `refresh_targets` when the matrix should embed current GitHub proof.
- `dogfood_smoke` calls `tools/product-journey/run.py --dogfood-smoke
  --json-output` to prove the no-LLM artifact loop from matrix creation through
  assignment run, seeded evidence, review, validation, rollup, and Slidey decks.
- `driver_replay_smoke` calls `tools/product-journey/run.py
  --driver-replay-smoke --json-output` to prove one reusable-driver scenario
  with cassette-backed proof evidence, linked driver journal refs, media
  manifest coverage, review, validation, and a compact Slidey smoke deck. Pass
  `scenario=project-onboarding`, `scenario=prd-design`, or another scenario id
  to exercise a specific journey path.
- `driver_replay_sweep` calls `tools/product-journey/run.py
  --driver-replay-sweep --json-output` to run the same replay proof for every
  product-journey scenario and summarize playback/validation coverage.
- `persona_autofix_smoke` calls `tools/product-journey/run.py
  --persona-autofix-smoke --json-output` to prove that a persona replay bundle
  with an observed issue finding enters the native `kitsoki gitops
  autonomous-fix` gate and produces filed issue, gh-agent run, fix evidence,
  and `independent-verify.md` artifacts without live GitHub or LLM work.
- `autonomous_marathon_smoke` calls `tools/product-journey/run.py
  --autonomous-marathon-smoke --json-output` to prove the standing-loop shell
  across the core `core-use-cases` scope (`project-onboarding`, `prd-design`,
  and `bugfix`) for every active curated persona: scoped persona runs, replayed
  driver proof, target/persona/lens preservation in the driver and brief
  contracts, credible issue filing, gh-agent fixes, independent verification,
  integration landing proof, retained JSON/Markdown ledger artifacts,
  human-review artifacts, and mechanically derived found/filed/fixed stats.
  Re-check a retained ledger through the story with
  `validate_marathon_smoke_ledger`, which defaults to the last smoke ledger, or
  pass `marathon_smoke_ledger=<path>`. The underlying CLI validator is
  `tools/product-journey/run.py --validate-marathon-smoke-ledger
  --marathon-smoke-ledger <path> --json-output`.
- `rollup` calls `tools/product-journey/run.py --rollup-matrix --json-output`
  to create or refresh the matrix-level Slidey deck from reviewed run bundles.
- `validate_matrix` calls `tools/product-journey/run.py --validate-matrix
  --json-output` to check the matrix and rollup artifact contract without
  rewriting files.
- `start` calls `tools/product-journey/run.py --emit-run --json-output`.
- `attach` calls `tools/product-journey/run.py --attach-evidence --json-output`;
  include `source` when the driver knows whether the artifact is `retained`,
  `external`, `local`, or `cassette`.
- `record` calls `tools/product-journey/run.py --record-finding --json-output`
  for strengths, weaknesses, issues, and fixes.
- `blocker` calls `tools/product-journey/run.py --record-blocker --json-output`
  when a scenario was attempted but cannot honestly proceed without live
  authorization, a missing cassette, unavailable repo state, or another
  external prerequisite.
- `driver_event` calls `tools/product-journey/run.py --record-driver-event
  --json-output` to append what the reusable driver actually attempted to
  `driver-journal.md/json`.
- `autonomous_fix` calls the native `kitsoki gitops autonomous-fix` facade to
  file credible issue findings, claim each queued GitHub issue with a
  `kitsoki-autofix-claim` comment, drain gh-agent fixes, refresh review artifacts,
  require each completed fix run to publish `independent-verify.md`, post a
  `kitsoki-fixed-in` close-out comment, close the GitHub issue, write
  `autonomous-fix-report.md`, and validate the bundle without exposing raw
  `gh` or gh-agent plumbing as the operator contract. The native gate runs
  `autonomous_watchdog` before filing anything and returns a reviewable
  `autonomous_fix_invalid` result when the standing-loop control is missing or
  stale.
- `autonomous_marathon scenarios=core-use-cases autonomous_driver_mode=replay
  ...` creates a scoped run for onboarding, PRD/design, and bugfix, attaches
  cassette-backed local proof artifacts, records the driver journal and
  credible findings, then runs the same native gitops autonomous fix, review,
  validation, PRD weakness routing, close-out, and stats gates in one
  story-owned call. Missing `ticket_repo` or `gh_agent_public_base_url` fails
  closed in the story view as `autonomous_marathon_invalid` so the operator can
  review the invalid gate state without reading host errors. Omit the mode, or
  use `pending`, when a live driver should capture evidence before finalization;
  use `replay` for the no-LLM autonomous proof. Explicit `record` or `live`
  driver modes create the same control/report bundle, auto-queue the
  story-owned `dispatch_driver` intent, launch the reusable product-journey QA
  driver through `host.agent.task`, and then queue the native autonomous
  finalizer. They require `autonomous_driver_live_profile=<profile>` up front,
  so replay misses cannot silently fall through to an ambient live backend.
  GitHub filing/fixing stays inside the story-owned gitops/gh-agent gates; the
  driver must not call `gh` directly.
  Live pending mode also requires the hosted gh-agent `/healthz` and
  `/api/ready` checks to pass for the same ticket repo before returning a driver
  handoff.
  The direct `autonomous_fix` gate repeats those hosted gh-agent checks before
  filing anything, so loaded or separately finalized runs fail closed with
  `filing_status=not_run` when the agent is unhealthy or points at another repo.
  Each marathon run also writes `autonomous-marathon-control.json/md` with the
  cadence, per-scenario live budget, human role, heartbeat/watchdog timing, and
  final gates, and the story view surfaces that control state for review.
  Open weakness findings are also materialized as `prd-design-intake.json/md`:
  each item includes the `stories/prd` `start` handoff, upstream evidence paths,
  and the persona lens so usability gaps can enter the PRD/design path instead
  of being mistaken for bugfix queue work.
- `autonomous_watchdog` calls `tools/product-journey/run.py
  --autonomous-marathon-watchdog --json-output` to compare the latest
  `driver-journal.json` heartbeat with the marathon control policy. Stale runs
  write `autonomous-marathon-watchdog.json/md` and bind
  `autonomous_watchdog_blocked` into the story so the loop stops before spend
  with a human-reviewable blocker artifact.
  `autonomous_marathon` finalization enforces that same check before filing
  issues, draining gh-agent fixes, closing tickets, or deriving stats.
- `seed_demo` calls `tools/product-journey/run.py --seed-demo-evidence
  --json-output` to populate a no-LLM review bundle.
- `handoff` calls `tools/product-journey/run.py --driver-handoff --json-output`
  to refresh `driver-handoff.md/json` for the reusable QA driver without
  launching live LLM work.
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
`execution-plan.md` to copy the generated `--attach-evidence` and
`--record-blocker` commands.
Use `driver-handoff.md` when handing the bundle to
`.agents/agents/product-journey-qa-driver.md`; it names the run directory,
driver inputs, dispatch modes, missing evidence, and final watchdog,
autonomous-fix, review, and validation commands without spending live model
calls.
Loaded runs expose `last_result.next_driver_capture`,
`last_result.next_driver_attach_command`, and
`last_result.next_driver_blocker_command` so the reusable driver can begin with
the first missing proof slot directly from story state and can record an honest
blocker instead of faking evidence.
Use `driver-journal.md` to review the driver's attempted actions, MCP tools,
evidence references, blockers, and per-scenario status before judging whether a
run reflects natural product usage.
Captured media is indexed in `media-manifest.json` so the generated Slidey deck
can expose playback-ready videos and screenshots without scraping prose.
Scenario-level evidence and finding summaries are written to
`scenario-outcomes.md` for review and matrix rollups.
Run `review` before `validate`; review refreshes derived artifacts and only
marks a run ready when every scenario has evidence or an explicit blocker, while
validation deliberately catches stale or inconsistent bundles. The story view
surfaces review pass/total/fail/warn counts so operators can tell whether a run
is progressing against the full review gate set without opening `review.json`.
Validation views also show a compact issue summary, so the operator can see the
first failing or warning check IDs before opening the raw validator output.
`accept` is validation-gated in run, matrix, and dogfood rooms; if validation has
not reported `valid`, the story stays put and sets `status=accept_needs_validation`.
