"""Job-type-agnostic rollup: aggregate CellResults into a leaderboard.

Buckets results by variant, by target, and by job_type (mirrors bakeoff's
by_candidate / by_treatment and product-journey's per-target/persona rollup).
Deterministic, no LLM. A later phase swaps the markdown for a Slidey deck via the
existing product-journey rollup deck builder.
"""

from __future__ import annotations

import json
from collections import defaultdict
from pathlib import Path
from typing import Any

from .model import CellResult

_SOLVED = {"solved", "armed"}   # "armed" is the no-LLM skeleton's success state


def _bucket(results: list[CellResult]) -> dict[str, Any]:
    n = len(results)
    counts: dict[str, int] = defaultdict(int)
    for r in results:
        counts[r.verdict] += 1
    costs = [r.metrics.get("cost_usd") for r in results if isinstance(r.metrics.get("cost_usd"), (int, float))]
    won = sum(1 for r in results if r.verdict in _SOLVED)
    infra = sum(1 for r in results if r.health.startswith("infra:"))
    return {
        "n": n,
        "verdicts": dict(counts),
        "win_rate": round(won / n, 4) if n else None,
        "infra_failures": infra,
        "avg_cost_usd": round(sum(costs) / len(costs), 6) if costs else None,
    }


def _group(results: list[CellResult], key) -> dict[str, Any]:
    groups: dict[str, list[CellResult]] = defaultdict(list)
    for r in results:
        groups[key(r)].append(r)
    return {name: _bucket(rs) for name, rs in sorted(groups.items())}


def build_rollup(results: list[CellResult]) -> dict[str, Any]:
    return {
        "summary": _bucket(results),
        "by_variant": _group(results, lambda r: r.variant_id),
        "by_target": _group(results, lambda r: r.target_id),
        "by_job_type": _group(results, lambda r: r.job_type),
        "cells": [r.to_dict() for r in results],
    }


def write_rollup(results: list[CellResult], out_dir: str | Path) -> dict[str, str]:
    out = Path(out_dir)
    out.mkdir(parents=True, exist_ok=True)
    rollup = build_rollup(results)
    (out / "rollup.json").write_text(json.dumps(rollup, indent=2, sort_keys=True), encoding="utf-8")
    (out / "rollup.md").write_text(_markdown(rollup), encoding="utf-8")
    return {"rollup": str(out / "rollup.json"), "summary": str(out / "rollup.md")}


def _markdown(rollup: dict[str, Any]) -> str:
    s = rollup["summary"]
    lines = [
        "# Arena rollup",
        "",
        f"- cells: **{s['n']}**  · win-rate: **{s['win_rate']}**  · infra failures: {s['infra_failures']}",
        "",
        "## By variant",
        "",
        "| variant | n | win-rate | avg cost | verdicts |",
        "|---|---|---|---|---|",
    ]
    for name, b in rollup["by_variant"].items():
        lines.append(f"| {name} | {b['n']} | {b['win_rate']} | {b['avg_cost_usd']} | {b['verdicts']} |")
    lines += ["", "## By target", "", "| target | n | win-rate | verdicts |", "|---|---|---|---|"]
    for name, b in rollup["by_target"].items():
        lines.append(f"| {name} | {b['n']} | {b['win_rate']} | {b['verdicts']} |")
    lines.append("")
    return "\n".join(lines)
