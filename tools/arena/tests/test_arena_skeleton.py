#!/usr/bin/env python3
"""No-LLM, no-docker end-to-end test of the arena walking skeleton.

Drives the full pipeline — JobSpec → cell enumeration → container execution
(FakeBackend) → bugfix plugin scoring → rollup — with a fake container backend,
so it proves the plumbing deterministically with zero docker and zero LLM. The
real DockerBackend satisfies the same `ContainerBackend` interface.
"""

from __future__ import annotations

import sys
from pathlib import Path

# Import the arena package from the tools/arena dir (sibling of tests/).
HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))

from arena.executor import CellExecutor, ContainerRun, FakeBackend  # noqa: E402
from arena.model import JobSpec  # noqa: E402
from arena.placement import run_sweep  # noqa: E402
from arena.rollup import build_rollup  # noqa: E402

failures: list[str] = []


def check(label: str, got, want) -> None:
    if got != want:
        failures.append(f"{label}: got {got!r}, want {want!r}")


SPEC = {
    "job_type": "bugfix",
    "targets": [{"id": "query-string", "label": "qs", "stack": "javascript"}],
    "variants": [
        {"id": "kitsoki-gpt-5.5", "backend": "codex", "model": "gpt-5.5"},
        {"id": "single-gpt-5.5", "backend": "codex", "model": "gpt-5.5"},
    ],
    "axes": {"bug": ["qs1", "qs2", "qs3"]},
    "placement": {"hosts": ["local"], "concurrency": 2, "retry": 1},
}

spec = JobSpec.from_dict(SPEC)

# 1. Enumeration: 1 target × 2 variants × 3 bugs = 6 cells, deterministic ids.
cells = spec.cells()
check("cell count", len(cells), 6)
check("cell id shape", cells[0].id, "query-string--kitsoki-gpt-5.5--bug:qs1")

# 2. Plugin selects the verify (no-LLM arming) command + the repo-runtime image.
from arena.plugins import base as plugins  # noqa: E402

bugfix = plugins.get("bugfix")
argv = bugfix.drive_command(cells[0], live=False)
check("non-live uses bench verify", argv[1:4], ["/workspace/kitsoki/tools/bugfix-bakeoff/external/bench.py", "verify", "--project"])
check("image per project", bugfix.image(cells[0]), "kitsoki-arena-repo/query-string:latest")
live_argv = bugfix.drive_command(cells[0], live=True)
check("live uses drive_cell", live_argv[0:2], ["bash", "/workspace/kitsoki/tools/bugfix-bakeoff/external/drive_cell.sh"])

# 3. Fake container backend: qs2 fails the oracle once then nothing (no retry
#    config here is exercised separately); everything else arms GREEN.
seen_hosts: set[str] = set()


def responder(cell, host, argv):
    seen_hosts.add(host)
    bug = cell.axis["bug"]
    if bug == "qs2":
        return ContainerRun(exit_code=1, stdout="RED stayed RED: oracle did not go GREEN", stderr="", host=host)
    return ContainerRun(exit_code=0, stdout="verify OK: baseline RED, fix GREEN (armed)", stderr="", host=host)


backend = FakeBackend(responder)
executor = CellExecutor(backend, mounts_for=lambda c: {"/repo": "/workspace/kitsoki"})
results = run_sweep(spec, executor, live=False)

check("result count", len(results), 6)
check("all placed local", seen_hosts, {"local"})
armed = [r for r in results if r.verdict == "armed"]
failed = [r for r in results if r.verdict == "failed"]
check("armed cells (qs1+qs3 × 2 variants)", len(armed), 4)
check("failed cells (qs2 × 2 variants)", len(failed), 2)
check("armed is model health", armed[0].health, "model:result")
# FakeBackend recorded one container call per cell.
check("container calls == cells", len(backend.calls), 6)
check("mounts threaded through", backend.calls[0]["mounts"], {"/repo": "/workspace/kitsoki"})

# 4. Retry: an infra failure is retried up to placement.retry, a model verdict is final.
infra_calls = {"n": 0}


def flaky(cell, host, argv):
    if cell.axis["bug"] == "qs1" and infra_calls["n"] == 0:
        infra_calls["n"] += 1
        return ContainerRun(exit_code=1, stdout="connection refused", stderr="", host=host)
    return ContainerRun(exit_code=0, stdout="armed", stderr="", host=host)


spec_one = JobSpec.from_dict({**SPEC, "variants": [SPEC["variants"][0]], "axes": {"bug": ["qs1"]}, "placement": {"hosts": ["local"], "concurrency": 1, "retry": 1}})
backend2 = FakeBackend(flaky)
executor2 = CellExecutor(backend2, mounts_for=lambda c: {})
retry_results = run_sweep(spec_one, executor2, live=False)
check("infra retried then armed", retry_results[0].verdict, "armed")
check("retry note recorded", "infra" in retry_results[0].notes, True)

# 5. Rollup buckets by variant + target with a win-rate.
rollup = build_rollup(results)
check("summary n", rollup["summary"]["n"], 6)
check("two variant buckets", sorted(rollup["by_variant"]), ["kitsoki-gpt-5.5", "single-gpt-5.5"])
check("variant win-rate", rollup["by_variant"]["kitsoki-gpt-5.5"]["win_rate"], 0.6667)

if failures:
    print("FAIL: arena skeleton")
    for f in failures:
        print("  -", f)
    sys.exit(1)
print("PASS: arena skeleton (enumerate → container(fake) → score → rollup, no LLM)")
