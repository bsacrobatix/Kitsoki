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

Later WS-F work (the arena release gate) will write the per-cell
completion-state verdict files this manifest points at; this skeleton
deliberately carries no CI wiring yet.
