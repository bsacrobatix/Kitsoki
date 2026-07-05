"""Check-suite execution (WS-G G1): per-cell verdicts per check_type.

A cell's job spec declares a check SUITE (`JobSpec.checks`), not one verdict
shape. Every check emits one `CellResult` tagged with its `check_type`
(schemas/completion-state.schema.json's discriminator; absent == "replay"), so
the matrix rollup aggregates one verdict per cell per check_type.

Only `replay` is executable today — it IS the existing plugin/container path
(`CellExecutor.execute`). The other declared types (`docs-fidelity`,
`ux-heuristic`, `journey-verdict`) are accepted at spec-validation time but
report an honest `pending` verdict at execution time — never a fake green —
until their runners land (per the spark-quota / honest-PENDING discipline).
"""

from __future__ import annotations

from .executor import CellExecutor
from .model import (
    Cell,
    CellResult,
    CheckSpec,
    DEFAULT_CHECK_TYPE,
    IMPLEMENTED_CHECK_TYPES,
)


def unimplemented_check_result(cell: Cell, check_type: str) -> CellResult:
    """The honest grade for a declared-but-not-yet-executable check type.

    `pending` = "the grade was never run" (excluded from solve-rate
    denominators per the schema); `incomplete` health = not a model result and
    not an infra breakage — so the placement scheduler never burns retries on
    it and the rollup never scores it against a variant.
    """
    return CellResult(
        cell_id=cell.id,
        job_type=cell.job_type,
        target_id=cell.target.id,
        variant_id=cell.variant.id,
        axis=dict(cell.axis),
        verdict="pending",
        health="incomplete",
        check_type=check_type,
        notes=(
            f"check_type '{check_type}' declared but not implemented yet; "
            "honest PENDING (never fake green)"
        ),
    )


def run_cell_checks(
    cell: Cell,
    executor: CellExecutor,
    checks: list[CheckSpec],
    *,
    host: str = "local",
    live: bool = False,
) -> list[CellResult]:
    """Run one cell's declared check suite → one CellResult per check_type.

    The replay check delegates to the existing container path (no behavior
    change when the suite is the default `[replay]`); every other type yields
    `unimplemented_check_result`. Note: callers that need INFRA retry around
    the replay check (the placement scheduler) drive `executor.execute`
    themselves and use `unimplemented_check_result` for the rest — this helper
    is the single-cell, no-retry composition.
    """
    results: list[CellResult] = []
    for check in checks:
        if check.check_type in IMPLEMENTED_CHECK_TYPES:
            result = executor.execute(cell, host=host, live=live)
            result.check_type = DEFAULT_CHECK_TYPE
            results.append(result)
        else:
            results.append(unimplemented_check_result(cell, check.check_type))
    return results
