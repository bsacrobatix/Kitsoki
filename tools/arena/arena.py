#!/usr/bin/env python3
"""arena — CLI front door for the generalized comparison-job runner.

Subcommands:
  plan   --spec S            enumerate cells for a job spec (no execution)
  run    --spec S --out D    run the sweep in containers, write per-cell results + rollup
                             (no-LLM arming by default; --live to spend)
  plugins                    list registered job types

Cost discipline: `run` defaults to the deterministic no-LLM path (oracle arming
for bugfix). `--live` is the only way to spend, and it is explicit.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

from arena.executor import CellExecutor, DockerBackend
from arena.model import Cell, JobSpec
from arena.placement import run_sweep
from arena.plugins import base as plugins
from arena.rollup import write_rollup

# Default mounts: the kitsoki checkout (carrying the bakeoff harness + bench.py)
# read-write into the container at the path the plugins expect.
REPO_ROOT = Path(__file__).resolve().parents[2]


def _mounts_for(cell: Cell) -> dict[str, str]:
    return {str(REPO_ROOT): "/workspace/kitsoki"}


def cmd_plan(args: argparse.Namespace) -> int:
    spec = JobSpec.load(args.spec)
    cells = spec.cells()
    print(f"job_type={spec.job_type}  cells={len(cells)}  hosts={spec.placement.hosts}")
    for c in cells:
        print(f"  {c.id}")
    return 0


def cmd_run(args: argparse.Namespace) -> int:
    spec = JobSpec.load(args.spec)
    backend = DockerBackend()
    executor = CellExecutor(backend, mounts_for=_mounts_for)
    if args.live:
        print("LIVE run — this WILL spend on LLM calls.", file=sys.stderr)
    results = run_sweep(
        spec, executor, live=args.live,
        on_result=lambda r: print(f"  {r.cell_id}: {r.verdict} [{r.health}]"),
    )
    paths = write_rollup(results, args.out)
    print(f"\nrollup → {paths['summary']}")
    return 0


def cmd_plugins(_args: argparse.Namespace) -> int:
    for name in plugins.known():
        print(name)
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="arena", description=__doc__)
    sub = parser.add_subparsers(dest="cmd", required=True)

    p_plan = sub.add_parser("plan", help="enumerate cells without executing")
    p_plan.add_argument("--spec", required=True)
    p_plan.set_defaults(func=cmd_plan)

    p_run = sub.add_parser("run", help="run the sweep in containers")
    p_run.add_argument("--spec", required=True)
    p_run.add_argument("--out", required=True)
    p_run.add_argument("--live", action="store_true", help="spend on real LLM drives (default: no-LLM arming)")
    p_run.set_defaults(func=cmd_run)

    p_plugins = sub.add_parser("plugins", help="list registered job types")
    p_plugins.set_defaults(func=cmd_plugins)

    args = parser.parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
