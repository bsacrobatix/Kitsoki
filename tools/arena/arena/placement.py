"""Placement scheduler: run a sweep of cells across docker hosts.

P0 covers local placement with a concurrency cap and INFRA-vs-MODEL retry. The
host list is round-robined, so adding VM hosts (later phase) needs no scheduler
change — a VM is just another host string that resolves to a docker context.
"""

from __future__ import annotations

import concurrent.futures
from typing import Callable

from .executor import CellExecutor
from .model import Cell, CellResult, JobSpec


def _is_infra(result: CellResult) -> bool:
    return result.health.startswith("infra:")


def run_sweep(
    spec: JobSpec,
    executor: CellExecutor,
    *,
    live: bool = False,
    on_result: Callable[[CellResult], None] | None = None,
) -> list[CellResult]:
    """Execute every enumerated cell, honoring placement concurrency + retry.

    INFRA failures are retried up to `placement.retry` times (the failure is the
    harness, not the model); a MODEL result is final (the verdict stands).
    """
    cells = spec.cells()
    hosts = spec.placement.hosts or ["local"]
    retry = spec.placement.retry
    results: list[CellResult] = []

    def run_one(idx_cell: tuple[int, Cell]) -> CellResult:
        idx, cell = idx_cell
        host = hosts[idx % len(hosts)]
        attempt = 0
        while True:
            result = executor.execute(cell, host=host, live=live)
            if _is_infra(result) and attempt < retry:
                attempt += 1
                continue
            if attempt:
                result.notes = (result.notes + f" (after {attempt} infra retr{'y' if attempt == 1 else 'ies'})").strip()
            return result

    workers = max(1, spec.placement.concurrency)
    with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as pool:
        for result in pool.map(run_one, enumerate(cells)):
            results.append(result)
            if on_result:
                on_result(result)
    # Deterministic order regardless of completion timing.
    results.sort(key=lambda r: r.cell_id)
    return results
