# dev-workflow-matrix

The standing 5-workflow × 4-surface × 2-repo support matrix (WS-F F1 of
`.context/dev-workflows-surface-matrix-plan.md`), as a generated artifact
instead of a hand-maintained table.

- `manifest.yaml` — the hand-edited truth: every `{workflow, surface, repo}`
  cell with a status (`works` / `proof-thin` / `gap` / `out-of-scope`), a
  one-line reason, and optional pointers to standing
  `schemas/completion-state.schema.json` verdict files — one per proof class:
  - **mechanical** (`check_type: replay`) — the no-LLM per-commit proof.
  - **experience** (`check_type: docs-fidelity | ux-heuristic |
    journey-verdict`) — the persona-judged proof (plan WS-G).
- `generate.py` — validates the manifest (full cross-product coverage, no
  duplicates, legal statuses/check_types), reads any verdict files, and
  renders `docs/testing/dev-workflow-matrix.md` with both verdicts per cell
  plus verdict freshness (file mtime date). Cells without a verdict render as
  "no standing verdict" — honest, not an error. Output is deterministic.
- `generate_test.py` — run with `python3 tools/dev-workflow-matrix/generate_test.py`
  (or `make dev-workflow-matrix-check`); also asserts the checked-in matrix
  matches a fresh render of the checked-in manifest.

Regenerate after editing the manifest:

```
make dev-workflow-matrix
```

## The standing gate (WS-F F1 exit)

`run_checks.py` is the check suite: it maps each matrix cell that has a real,
runnable, no-LLM proof today onto a plain invocation (`go run ./cmd/kitsoki
test flows <app.yaml>` for the `prd` / `bugfix` / `dev-story` / `deliver`
story flow suites, plus one `tools/product-journey/run.py
--driver-replay-smoke` pass as a `journey-verdict` experience-class pilot),
runs it, and writes one `schemas/completion-state.schema.json`-conformant
verdict JSON per cell into a verdicts directory — keyed by `(workflow,
surface, repo, check_type)` via `axis.workflow`/`axis.surface`/`target_id`/
`check_type`, independent of any manifest `verdicts.*.path` pointer. It
deliberately does not go through `tools/arena`'s container executor (none of
these checks need a container), but reuses arena's check-type vocabulary and
completion-state shape.

`generate.py --verdicts-dir DIR` ingests that directory: a verdict found
there always wins over a manifest pointer for the same cell + proof class. A
cell with no verdict in the directory (and no manifest pointer either) keeps
the manifest's static status standing — the honest default. A `failed` /
`blocked` verdict (or an `infra:*` health) downgrades the cell to at least
`gap`; a verdict older than `--stale-days` (default 14) downgrades a `works`
cell to `proof-thin`. `out-of-scope` cells are never downgraded. `--gate`
additionally exits non-zero if any cell the manifest marks `works` has
regressed — the WS-F F1 exit criterion ("a red cell blocks declaring the
workflow supported").

```
make dev-workflow-gate
```

runs the check suite into `.artifacts/dev-workflow-matrix/verdicts/` (never
committed), writes a LIVE verdict-aware report to
`.artifacts/dev-workflow-matrix/dev-workflow-matrix.live.md`, and — only if
that passes — regenerates the checked-in, manifest-only
`docs/testing/dev-workflow-matrix.md`. The checked-in doc never carries
verdict-derived timestamps or a pass/fail from a given run (it is a pure
function of the manifest, same as before this gate existed); freshness and
live pass/fail live only in the uncommitted gate output. `run_checks_test.py`
and the verdict-ingestion cases in `generate_test.py` cover the writer/reader
contract, downgrade-on-fail, stale detection, and the `--gate` exit-code
contract — all offline, zero LLM, zero docker (`run_checks.py`'s tests inject
a fake subprocess runner rather than really invoking `go run`/`python3`).

`go run ./cmd/kitsoki test routing` does not exist yet (checked 2026-07-05) so
the routing-fixture check the WS-F plan names is not wired here — add a
`CheckDef` for it once that CLI lands.
