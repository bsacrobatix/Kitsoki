"""bugfix job-type plugin — wraps the proven bugfix-bakeoff oracle.

A bugfix cell grades a (project, bug) on whether a candidate fix turns the hidden
oracle GREEN. The plugin reuses `tools/bugfix-bakeoff/external/bench.py` verbatim
as the scorer inside the container:

  * non-live (P0 skeleton, no LLM): `bench.py verify` proves the oracle is armed
    (RED@baseline → GREEN@fix). A passing arming is the deterministic proof that
    enumerate → container → score → rollup all work, with zero spend.
  * live (gated, paid): the same plumbing runs `drive_cell.sh` to generate a real
    candidate, then `bench.py score`. Wired here; spending is gated at the CLI.

The cell's `axis["bug"]` selects the bug; `target.id` is the bakeoff project.
"""

from __future__ import annotations

import re

from ..model import Cell, CellResult
from . import base

# Path *inside the container* where the kitsoki checkout (incl. the bakeoff
# harness) is mounted. The executor's mounts map the host repo here.
KITSOKI_MNT = "/workspace/kitsoki"
BENCH = f"{KITSOKI_MNT}/tools/bugfix-bakeoff/external/bench.py"


class BugfixPlugin:
    name = "bugfix"

    def image(self, cell: Cell) -> str:
        # The repo-runtime image carries go/node/python/rust + test runners. One
        # image per project keeps deps cached; falls back to a shared default.
        return cell.target.meta.get("image") or f"kitsoki-arena-repo/{cell.target.id}:latest"

    def drive_command(self, cell: Cell, *, live: bool) -> list[str]:
        project = cell.target.id
        bug = cell.axis.get("bug", "")
        if not live:
            # No-LLM arming proof: verify RED@baseline, GREEN@fix for this bug.
            return ["python3", BENCH, "verify", "--project", project, "--bug", bug]
        # Paid path: drive a candidate then score it (drive_cell.sh handles both).
        drive = f"{KITSOKI_MNT}/tools/bugfix-bakeoff/external/drive_cell.sh"
        return [
            "bash", drive,
            "--project", project,
            "--bug", bug,
            "--candidate", cell.variant.id,
            "--score",
        ]

    def score(self, cell: Cell, *, exit_code: int, stdout: str, stderr: str) -> CellResult:
        result = CellResult(
            cell_id=cell.id,
            job_type=self.name,
            target_id=cell.target.id,
            variant_id=cell.variant.id,
            axis=dict(cell.axis),
        )
        blob = f"{stdout}\n{stderr}"

        # Infra signals first — a harness failure is not a model verdict.
        if re.search(r"no such tool|worker.never.ran|host[_-]error|connection refused|provider 5\d\d", blob, re.I):
            result.verdict = "blocked"
            result.health = "infra:harness"
            result.notes = _first_line(blob)
            return result

        if exit_code == 0:
            # bench.py verify exits 0 ⇔ armed (RED→GREEN); score exits 0 ⇔ GREEN.
            result.verdict = "armed" if "verify" in blob.lower() or _looks_like_verify(blob) else "solved"
            result.health = "model:result"
        else:
            result.verdict = "failed"
            result.health = "model:result"
            result.notes = _first_line(blob)
        _extract_metrics(blob, result)
        return result


def _looks_like_verify(blob: str) -> bool:
    return bool(re.search(r"\bRED\b.*\bGREEN\b|armed|baseline.*fix", blob, re.I | re.S))


def _first_line(blob: str) -> str:
    for line in blob.splitlines():
        line = line.strip()
        if line:
            return line[:200]
    return ""


def _extract_metrics(blob: str, result: CellResult) -> None:
    m = re.search(r"cost_usd[\"']?\s*[:=]\s*([0-9.]+)", blob)
    if m:
        result.metrics["cost_usd"] = float(m.group(1))


base.register(BugfixPlugin())
