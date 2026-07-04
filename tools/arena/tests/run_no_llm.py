#!/usr/bin/env python3
"""WB.2 no-LLM arena gate.

Exercises the paired-task plugin through the real arena pipeline:
spec load -> enumerate -> fake container arm/score -> aggregate -> report.
No docker, no LLM, no network.
"""

from __future__ import annotations

import json
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
ARENA_ROOT = HERE.parent
sys.path.insert(0, str(ARENA_ROOT))

from arena.executor import CellExecutor, ContainerRun, FakeBackend  # noqa: E402
from arena.model import JobSpec  # noqa: E402
from arena.placement import run_sweep  # noqa: E402
from arena.plugins import base as plugins  # noqa: E402
from arena.rollup import write_rollup  # noqa: E402

failures: list[str] = []


def check(label: str, got, want) -> None:
    if got != want:
        failures.append(f"{label}: got {got!r}, want {want!r}")


def fixture_result(task: str, treatment: str) -> dict:
    verdict = "solved"
    if task == "flaky-test" and treatment == "single-naive":
        verdict = "failed"
    costs = {
        "kitsoki": 0.42,
        "single-briefed": 0.31,
        "single-naive": 0.12,
    }
    return {
        "verdict": verdict,
        "cost_usd": costs[treatment],
        "tokens": int(costs[treatment] * 100000),
        "wall_s": 7.5,
        "evidence_refs": [f"fixtures/{task}/{treatment}.json"],
        "trace_ref": f"traces/{task}/{treatment}.jsonl",
    }


spec_path = ARENA_ROOT / "specs" / "paired-task-fixture.yaml"
spec = JobSpec.load(spec_path)
cells = spec.cells()

check("registered plugins", "paired-task" in plugins.known(), True)
check("cell count", len(cells), 6)
check("first cell id", cells[0].id, "cost-bench-fixture--kitsoki--task:api-routing")

plugin = plugins.get("paired-task")
argv = plugin.drive_command(cells[0], live=False)
check("no-LLM arm mode", argv[-1], "--arm-only")
check("runner path", argv[1], "/workspace/kitsoki/tools/arena/lib/paired_task_runner.py")
live_argv = plugin.drive_command(cells[0], live=True)
check("live is explicit", "--live" in live_argv, True)
check("live threads model", "--model" in live_argv and "gpt-5.5" in live_argv, True)


def responder(cell, host, argv):
    check(f"{cell.id} no live spend", "--live" in argv, False)
    task = cell.axis["task"]
    payload = fixture_result(task, cell.variant.id)
    return ContainerRun(exit_code=0, stdout=json.dumps(payload), stderr="", host=host)


backend = FakeBackend(responder)
executor = CellExecutor(backend, mounts_for=lambda cell, host: {"/repo": "/workspace/kitsoki"})
results = run_sweep(spec, executor, live=False)

check("result count", len(results), 6)
check("container calls == cells", len(backend.calls), 6)
check("all fake calls arm only", all("--arm-only" in call["argv"] for call in backend.calls), True)
solved = [r for r in results if r.verdict == "solved"]
failed = [r for r in results if r.verdict == "failed"]
check("solved cells", len(solved), 5)
check("failed cells", len(failed), 1)
check("cost metric parsed", results[0].metrics["cost_usd"], 0.42)
check("evidence refs parsed", results[0].evidence_refs, ["fixtures/api-routing/kitsoki.json"])

with tempfile.TemporaryDirectory(prefix="arena-no-llm-") as td:
    paths = write_rollup(results, td)
    rollup = json.loads(Path(paths["rollup"]).read_text(encoding="utf-8"))
    check("rollup cells", rollup["summary"]["n"], 6)
    check("rollup variants", sorted(rollup["by_variant"]), ["kitsoki", "single-briefed", "single-naive"])
    check("single-naive win-rate", rollup["by_variant"]["single-naive"]["win_rate"], 0.5)
    check("kitsoki avg cost", rollup["by_variant"]["kitsoki"]["avg_cost_usd"], 0.42)
    check("markdown report exists", Path(paths["summary"]).exists(), True)

if failures:
    print("FAIL: arena no-LLM gate")
    for failure in failures:
        print(f"  - {failure}")
    sys.exit(1)

print("PASS: paired-task arena gate (enumerate -> arm -> score -> rollup, no LLM)")
