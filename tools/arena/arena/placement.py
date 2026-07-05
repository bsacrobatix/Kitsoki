"""Placement scheduler: run a sweep of cells across docker hosts.

P0 covers local placement with a concurrency cap and INFRA-vs-MODEL retry. The
host list is round-robined, so adding VM hosts (later phase) needs no scheduler
change — a VM is just another host string that resolves to a docker context.
"""

from __future__ import annotations

import concurrent.futures
from typing import Callable

from .checks import unimplemented_check_result
from .executor import CellExecutor
from .model import Cell, CellResult, JobSpec, IMPLEMENTED_CHECK_TYPES


def _is_infra(result: CellResult) -> bool:
    return result.health.startswith("infra:")


def run_sweep(
    spec: JobSpec,
    executor: CellExecutor,
    *,
    live: bool = False,
    on_result: Callable[[CellResult], None] | None = None,
) -> list[CellResult]:
    """Execute every enumerated cell's check suite, honoring placement
    concurrency + retry.

    Each cell runs its `spec.checks` suite and yields one CellResult PER
    check_type (WS-G G1). The `replay` check is the container execution below;
    its INFRA failures are retried up to `placement.retry` times (the failure
    is the harness, not the model) while a MODEL result is final (the verdict
    stands). Declared-but-unimplemented check types report an honest `pending`
    without touching a container (see arena/checks.py) — no retries, no spend.
    """
    cells = spec.cells()
    hosts = spec.placement.hosts or ["local"]
    retry = spec.placement.retry
    check_types = [c.check_type for c in spec.checks] or ["replay"]
    run_replay = any(ct in IMPLEMENTED_CHECK_TYPES for ct in check_types)
    results: list[CellResult] = []

    def run_one(idx_cell: tuple[int, Cell]) -> list[CellResult]:
        idx, cell = idx_cell
        host = hosts[idx % len(hosts)]
        out: list[CellResult] = []
        if run_replay:
            attempt = 0
            while True:
                result = executor.execute(cell, host=host, live=live)
                if _is_infra(result) and attempt < retry:
                    attempt += 1
                    continue
                if attempt:
                    result.notes = (result.notes + f" (after {attempt} infra retr{'y' if attempt == 1 else 'ies'})").strip()
                break
            result.check_type = "replay"
            out.append(result)
        out.extend(
            unimplemented_check_result(cell, ct)
            for ct in check_types
            if ct not in IMPLEMENTED_CHECK_TYPES
        )
        return out

    workers = max(1, spec.placement.concurrency)
    with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as pool:
        for cell_results in pool.map(run_one, enumerate(cells)):
            for result in cell_results:
                results.append(result)
                if on_result:
                    on_result(result)
    # Deterministic order regardless of completion timing.
    results.sort(key=lambda r: (r.cell_id, r.check_type))
    return results
