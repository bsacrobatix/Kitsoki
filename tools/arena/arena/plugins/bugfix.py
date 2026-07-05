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

Scoring reads the unified completion-state contract (schemas/completion-
state.schema.json) that bench.py writes to `--completion-state <path>`, instead
of regexing the container's stdout/stderr. The container and this (host-side)
process share the same repo checkout (the executor mounts REPO_ROOT at
KITSOKI_MNT for `local` placement), so the file bench.py wrote inside the
container is readable here at the equivalent host path once the run completes.
Stdout/stderr infra-signal detection remains ONLY as a fallback for when that
file is absent — e.g. a live cell driven through `drive_cell.sh`, which does not
yet forward --completion-state to its embedded `bench.py score` call.
"""

from __future__ import annotations

import json
import re
from pathlib import Path

from ..model import Cell, CellResult, VERDICTS
from . import base

# Path *inside the container* where the kitsoki checkout (incl. the bakeoff
# harness) is mounted. The executor's mounts map the host repo here.
KITSOKI_MNT = "/workspace/kitsoki"
BENCH = f"{KITSOKI_MNT}/tools/bugfix-bakeoff/external/bench.py"

# Host-side repo root: tools/arena/arena/plugins/bugfix.py -> parents[4] == REPO_ROOT.
# For `local` placement this is the same tree the container mounts at KITSOKI_MNT,
# so a file bench.py wrote under KITSOKI_MNT/.artifacts/... is readable here at
# REPO_ROOT/.artifacts/....
REPO_ROOT = Path(__file__).resolve().parents[4]
COMPLETION_STATE_DIR = ".artifacts/arena/completion-state"

_HEALTH_RE = re.compile(r"^(infra:[a-z0-9_-]+|model:result|incomplete)$")

# Infra signals — a harness failure is not a model verdict. Kept ONLY as the
# fallback for a missing completion-state file (see module docstring).
_INFRA_RE = re.compile(
    r"no such tool|worker.never.ran|host[_-]error|connection refused|provider 5\d\d",
    re.I,
)


class BugfixPlugin:
    name = "bugfix"

    def image(self, cell: Cell) -> str:
        # The repo-runtime image carries go/node/python/rust + test runners. One
        # image per project keeps deps cached; falls back to a shared default.
        return cell.target.meta.get("image") or f"kitsoki-arena-repo/{cell.target.id}:latest"

    def completion_state_path(self, cell: Cell, *, container: bool = False) -> str:
        """Where bench.py writes (and this plugin reads) the cell's completion-state.

        `container=True` returns the path as seen INSIDE the container (passed to
        bench.py's --completion-state); the default returns the host-side path this
        process reads back after the container run completes.
        """
        rel = f"{COMPLETION_STATE_DIR}/{cell.id}.json"
        return f"{KITSOKI_MNT}/{rel}" if container else str(REPO_ROOT / rel)

    def drive_command(self, cell: Cell, *, live: bool) -> list[str]:
        project = cell.target.id
        bug = cell.axis.get("bug", "")
        state_path = self.completion_state_path(cell, container=True)
        if not live:
            # No-LLM arming proof: verify RED@baseline, GREEN@fix for this bug.
            return ["python3", BENCH, "verify", "--project", project, "--bug", bug,
                    "--completion-state", state_path]
        # Paid path: drive a candidate then score it (drive_cell.sh handles both).
        # NOTE: drive_cell.sh does not yet forward --completion-state to its
        # embedded `bench.py score` call, so a live cell currently falls back to
        # the infra-signal detection below until that wiring lands.
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
        state_path = Path(self.completion_state_path(cell))
        if state_path.exists():
            return self._score_from_completion_state(result, state_path)
        return self._score_from_infra_fallback(result, exit_code=exit_code,
                                                stdout=stdout, stderr=stderr)

    def _score_from_completion_state(self, result: CellResult, state_path: Path) -> CellResult:
        try:
            data = json.loads(state_path.read_text())
        except (OSError, json.JSONDecodeError) as exc:
            result.verdict = "blocked"
            result.health = "infra:completion-state-malformed"
            result.notes = f"completion-state at {state_path} is not valid JSON: {exc}"
            return result

        required = ("schema_version", "verdict", "health", "metrics", "evidence_refs")
        missing = [k for k in required if k not in data]
        if missing:
            result.verdict = "blocked"
            result.health = "infra:completion-state-malformed"
            result.notes = f"completion-state at {state_path} missing field(s): {', '.join(missing)}"
            return result
        if data["verdict"] not in VERDICTS:
            result.verdict = "blocked"
            result.health = "infra:completion-state-malformed"
            result.notes = f"completion-state at {state_path} has unknown verdict {data['verdict']!r}"
            return result
        if not _HEALTH_RE.match(str(data["health"])):
            result.verdict = "blocked"
            result.health = "infra:completion-state-malformed"
            result.notes = f"completion-state at {state_path} has unrecognized health {data['health']!r}"
            return result
        if not isinstance(data.get("metrics"), dict) or not isinstance(data.get("evidence_refs"), list):
            result.verdict = "blocked"
            result.health = "infra:completion-state-malformed"
            result.notes = f"completion-state at {state_path} has malformed metrics/evidence_refs"
            return result

        result.verdict = data["verdict"]
        result.health = data["health"]
        result.metrics = dict(data.get("metrics") or {})
        result.evidence_refs = list(data.get("evidence_refs") or [])
        result.trace_ref = data.get("trace_ref", "") or ""
        result.notes = data.get("notes") or data.get("summary") or ""
        return result

    def _score_from_infra_fallback(self, result: CellResult, *, exit_code: int,
                                    stdout: str, stderr: str) -> CellResult:
        blob = f"{stdout}\n{stderr}"
        if _INFRA_RE.search(blob):
            result.verdict = "blocked"
            result.health = "infra:harness"
            result.notes = _first_line(blob)
            return result
        # No completion-state file and no recognized infra signal: the contract
        # was not honored by whatever produced this run. Surface that plainly
        # rather than guessing a model verdict from exit code/stdout shape.
        result.verdict = "blocked"
        result.health = "infra:missing-completion-state"
        result.notes = (
            f"no completion-state file written (exit_code={exit_code}); "
            "the driver did not honor the arena/bugfix completion-state contract"
        )
        return result


def _first_line(blob: str) -> str:
    for line in blob.splitlines():
        line = line.strip()
        if line:
            return line[:200]
    return ""


base.register(BugfixPlugin())
