"""usable-kitsoki-gate job-type plugin — one arena cell = one (persona x
surface) run of the mined scenario corpus, per
docs/proposals/usable-kitsoki-release-gate.md Task 2.

Cell shape: `scenario_id x persona x surface` per the proposal's own
Event/format model, but — exactly like `swarm.py` — one CELL drives the
WHOLE mined corpus for one persona x surface combination (N scenarios run
inside the cell), not one cell per scenario; the parity verdict record
(`usable_kitsoki_gate_schema.json`) is emitted once per scenario x persona x
surface, i.e. many records per cell.

`drive_command()` dispatches on `axis["surface"]`:
  - "web" reuses the existing swarm-style harness convention: cd into
    tools/runstatus and run a Playwright spec via npx, mirroring
    `swarm.py`'s SPEC/RUNSTATUS_DIR pattern exactly.
  - "tui" / "mcp" dispatch into a workbench-driving runner script under
    `tools/usable-kitsoki-gate/` instead of a browser spec.

S1 (workbench producer contract) and S4 (scenario foundry / mined corpus)
have not landed yet as of this commit, so none of the three concrete
harness entry points referenced below
(`tests/playwright/usable-kitsoki-gate-web.spec.ts`,
`tools/usable-kitsoki-gate/run_tui_gate.py`,
`tools/usable-kitsoki-gate/run_mcp_gate.py`) exist on disk yet — this
plugin's job today is to get the argv/env composition and scoring contract
right and PROVEN by test, so S1/S4 land against a stable, already-tested
seam rather than the plugin being re-designed once those pieces show up.
`arena plan`/`arena run` will simply fail to find those scripts until S1/S3
wire them in (Task 3, out of scope here) — that failure mode is
indistinguishable from any other "harness script missing" infra failure and
is handled the same way `score()` handles any other harness crash: an
`infra:*` health, never a fabricated model verdict.

Scoring never regexes stdout for a verdict. It only reads the
`[usable-kitsoki-gate] wrote <path>` pointer line the harness is expected to
print (mirrors swarm.py's `[swarm] wrote <path>` convention), loads the
parity-records bundle at that path, validates each record against
`usable_kitsoki_gate_schema.json`, and reduces the records into the three
GATE_CONDITIONS from `usable_kitsoki_gate_constants.py`. The reduction here
is PER CELL, i.e. per (persona, surface) in production (one cell drives one
surface, so grouping by surface is normally a no-op) -- but it groups
records by their own `surface` field and gates on the MINIMUM per-surface
parity (WORST_SURFACE_GATING), not a flat aggregate, so a hand-written
bundle spanning more than one surface (golden regression fixtures, Task 4.1
-- see tests/fixtures/usable-kitsoki-gate/) is still reduced correctly. True
cross-CELL worst-surface gating (comparing separately-run cells against each
other across a real S1/S4 sweep) is still a higher rollup layer (Task 3,
gated on S1/S4 landing, explicitly out of scope for this plugin).
"""

from __future__ import annotations

import json
import re
import shlex
import sys
from pathlib import Path
from typing import Any

# tools/arena/arena/plugins/usable_kitsoki_gate.py -> parents[4] == REPO_ROOT
# (mirrors bugfix.py's / swarm.py's / persona_qa.py's REPO_ROOT derivation).
REPO_ROOT = Path(__file__).resolve().parents[4]
if str(REPO_ROOT) not in sys.path:
    sys.path.insert(0, str(REPO_ROOT))

from ..model import Cell, CellResult  # noqa: E402
from . import base  # noqa: E402
from . import usable_kitsoki_gate_constants as gate_constants  # noqa: E402

try:
    import jsonschema  # type: ignore
except ImportError:  # pragma: no cover - advisory only, mirrors test_usable_kitsoki_gate_schema.py
    jsonschema = None  # type: ignore[assignment]

# Path *inside the container* where the kitsoki checkout is mounted (mirrors
# bugfix.py's / swarm.py's / persona_qa.py's KITSOKI_MNT convention).
KITSOKI_MNT = "/workspace/kitsoki"
RUNSTATUS_DIR = f"{KITSOKI_MNT}/tools/runstatus"

# Not built yet (S1/S4 gated) -- see module docstring. Names fixed here so the
# eventual harness has one obvious place to land.
WEB_SPEC = "tests/playwright/usable-kitsoki-gate-web.spec.ts"
TUI_RUNNER = f"{KITSOKI_MNT}/tools/usable-kitsoki-gate/run_tui_gate.py"
MCP_RUNNER = f"{KITSOKI_MNT}/tools/usable-kitsoki-gate/run_mcp_gate.py"

SURFACES = ("web", "tui", "mcp")
DEFAULT_SURFACE = "web"
DEFAULT_SCENARIO_CORPUS = "docs/proposals/scenario-foundry-corpus.json"

SCHEMA_PATH = Path(__file__).resolve().parent / "usable_kitsoki_gate_schema.json"

# `[usable-kitsoki-gate] wrote /abs/path/parity-records-<run_id>.json (N records)`
# -- the ONE line of stdout this plugin reads; everything else comes off disk.
_RESULTS_LINE_RE = re.compile(r"\[usable-kitsoki-gate\]\s+wrote\s+(\S+\.json)")

# Infra signals -- a harness crash (or a not-yet-built S1/S4 entry point)
# before any results file was ever written is not a model verdict. Kept ONLY
# as the fallback for when no results-path pointer is found in stdout at all.
_INFRA_RE = re.compile(
    r"traceback|no such file|permission denied|connection refused|"
    r"command not found|econnrefused|provider 5\d\d|modulenotfounderror",
    re.I,
)


class UsableKitsokiGatePlugin:
    name = "usable-kitsoki-gate"

    def image(self, cell: Cell) -> str:
        # Web/TUI surfaces are Playwright-driven (or, for TUI, a
        # workbench-driving spec that may still shell to a browser-hosted
        # runstatus terminal) and need the browser-capable image; MCP is a
        # headless stdio surface and does not. A `meta.image` override is
        # the escape hatch either way (mirrors swarm.py/persona_qa.py).
        override = cell.target.meta.get("image") or cell.variant.meta.get("image")
        if override:
            return str(override)
        surface = self._coords(cell)["surface"]
        if surface == "mcp":
            return "kitsoki-arena-repo-runtime:latest"
        return "kitsoki-arena-repo-runtime-browser:latest"

    def _coords(self, cell: Cell) -> dict[str, str]:
        surface = (
            cell.axis.get("surface")
            or str(cell.variant.meta.get("surface", ""))
            or DEFAULT_SURFACE
        )
        return {
            "surface": surface,
            "persona": cell.axis.get("persona") or str(cell.variant.meta.get("persona", "")),
            "scenario_corpus": (
                cell.axis.get("scenario_corpus")
                or str(cell.variant.meta.get("scenario_corpus", ""))
                or DEFAULT_SCENARIO_CORPUS
            ),
            "run_id": cell.axis.get("run_id") or _sanitize(cell.id),
        }

    def drive_command(self, cell: Cell, *, live: bool) -> list[str]:
        # No separate paid path today: neither surface driver exists yet (see
        # module docstring), so there is nothing live-specific to branch on.
        # Accepted for interface parity with the other job-type plugins, same
        # as swarm.py's `live` no-op.
        del live
        coords = self._coords(cell)
        surface = coords["surface"]
        results_path = (
            f"{KITSOKI_MNT}/.artifacts/usable-kitsoki-gate/{coords['run_id']}/parity-records.json"
        )

        env: list[str] = [
            f"GATE_SURFACE={shlex.quote(surface)}",
            f"GATE_SCENARIO_CORPUS={shlex.quote(coords['scenario_corpus'])}",
            f"GATE_RUN_ID={shlex.quote(coords['run_id'])}",
            f"GATE_RESULTS_PATH={shlex.quote(results_path)}",
        ]
        if coords["persona"]:
            env.append(f"GATE_PERSONA={shlex.quote(coords['persona'])}")

        if surface == "web":
            script = (
                f"set -euo pipefail; cd {shlex.quote(RUNSTATUS_DIR)} && "
                f"{' '.join(env)} npx playwright test {shlex.quote(WEB_SPEC)}"
            )
        else:
            runner = TUI_RUNNER if surface == "tui" else MCP_RUNNER
            script = (
                f"set -euo pipefail; cd {shlex.quote(KITSOKI_MNT)} && "
                f"{' '.join(env)} python3 {shlex.quote(runner)}"
            )
        return ["bash", "-lc", script]

    def score(self, cell: Cell, *, exit_code: int, stdout: str, stderr: str) -> CellResult:
        result = CellResult(
            cell_id=cell.id,
            job_type=self.name,
            target_id=cell.target.id,
            variant_id=cell.variant.id,
            axis=dict(cell.axis),
        )
        blob = f"{stdout}\n{stderr}"
        results_path = _extract_results_path(stdout)
        if results_path is None:
            if _INFRA_RE.search(blob):
                result.verdict = "blocked"
                result.health = "infra:harness"
                result.notes = _first_line(blob)
            else:
                result.verdict = "blocked"
                result.health = "infra:missing-results-path"
                result.notes = (
                    f"no usable-kitsoki-gate results path found in stdout (exit_code={exit_code}); "
                    "the harness did not honor the '[usable-kitsoki-gate] wrote <path>' contract"
                )
            return result

        host_path = _host_results_path(results_path)
        try:
            data = json.loads(host_path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            result.verdict = "blocked"
            result.health = "infra:results-malformed"
            result.notes = f"could not load usable-kitsoki-gate results at {host_path}: {exc}"
            return result

        records = data.get("records") if isinstance(data, dict) else data
        if not isinstance(records, list):
            result.verdict = "blocked"
            result.health = "infra:results-malformed"
            result.notes = f"usable-kitsoki-gate results at {host_path} did not contain a 'records' array"
            return result

        return _rollup_from_records(result, records, str(host_path))


def _rollup_from_records(result: CellResult, records: list[Any], results_path: str) -> CellResult:
    """Reduce the cell's parity verdict records into the three
    GATE_CONDITIONS (usable_kitsoki_gate_constants.py) for THIS cell's
    (persona, surface) combination.

    In production a single cell's records all share one `surface` value (one
    cell = one persona x surface run, per module docstring), so grouping by
    surface here is a no-op superset of the plain aggregate. But nothing stops
    a hand-written parity-records bundle (golden regression fixtures, Task 4.1)
    or a future harness change from putting more than one surface's records in
    one bundle, and `usable_kitsoki_gate_constants.WORST_SURFACE_GATING` is a
    fixed design decision (never average across surfaces) -- so this reduction
    groups by `surface` and gates on the MINIMUM per-surface parity, not the
    flat aggregate, to be correct either way. True cross-CELL worst-surface
    gating (comparing separately-run cells against each other) is still a
    higher-layer (arena rollup) concern -- see module docstring -- this is
    only the within-bundle reduction.
    """
    schema_errors: list[str] = []
    if jsonschema is not None and SCHEMA_PATH.exists():
        schema = json.loads(SCHEMA_PATH.read_text(encoding="utf-8"))
        validator = jsonschema.Draft7Validator(schema)
        for i, record in enumerate(records):
            for err in validator.iter_errors(record):
                schema_errors.append(f"record[{i}]: {err.message}")

    if schema_errors:
        result.verdict = "blocked"
        result.health = "infra:results-malformed"
        result.evidence_refs = [results_path]
        result.notes = (
            f"{len(schema_errors)} parity record(s) failed schema validation: "
            + "; ".join(schema_errors[:5])
        )
        return result

    silent_bounce_count = sum(1 for r in records if r.get("silent_bounce"))
    misroute_adjacent_count = sum(1 for r in records if r.get("misroute_adjacent"))
    source_completed_count = sum(1 for r in records if r.get("source_completed"))
    both_completed_count = sum(
        1 for r in records if r.get("source_completed") and r.get("candidate_completed")
    )
    parity_pct = gate_constants.parity_percent(both_completed_count, source_completed_count)

    # Per-surface breakdown -- the gate always reduces with min(), never
    # average, per WORST_SURFACE_GATING (usable_kitsoki_gate_constants.py).
    per_surface_totals: dict[str, dict[str, int]] = {}
    for r in records:
        surface = str(r.get("surface", ""))
        bucket = per_surface_totals.setdefault(surface, {"source": 0, "both": 0})
        if r.get("source_completed"):
            bucket["source"] += 1
            if r.get("candidate_completed"):
                bucket["both"] += 1
    per_surface_parity_percent = {
        surface: gate_constants.parity_percent(totals["both"], totals["source"])
        for surface, totals in per_surface_totals.items()
    }
    worst_surface_parity_pct = (
        min(per_surface_parity_percent.values()) if per_surface_parity_percent else 100.0
    )

    passes = gate_constants.gate_passes(
        silent_bounce_count=silent_bounce_count,
        misroute_adjacent_count=misroute_adjacent_count,
        worst_surface_parity_percent=worst_surface_parity_pct,
    )

    evidence_refs = {results_path}
    for r in records:
        for ref in r.get("evidence_refs") or []:
            evidence_refs.add(str(ref))

    result.verdict = "solved" if passes else "failed"
    result.health = "model:result"
    result.trace_ref = results_path
    result.evidence_refs = sorted(evidence_refs)
    result.metrics.update({
        "record_count": len(records),
        "silent_bounce_count": silent_bounce_count,
        "misroute_adjacent_count": misroute_adjacent_count,
        "source_completed_count": source_completed_count,
        "both_completed_count": both_completed_count,
        "parity_percent": parity_pct,
        "worst_surface_parity_percent": worst_surface_parity_pct,
        "per_surface_parity_percent": per_surface_parity_percent,
    })
    result.notes = (
        f"{result.verdict}: {len(records)} record(s), parity={parity_pct:.1f}% overall, "
        f"worst_surface_parity={worst_surface_parity_pct:.1f}% "
        f"(threshold {gate_constants.PARITY_THRESHOLD_PERCENT:.1f}%), "
        f"silent_bounce={silent_bounce_count}, misroute_adjacent={misroute_adjacent_count}"
    )
    return result


def _extract_results_path(stdout: str) -> str | None:
    """Pull the results-file path out of the harness's own
    `[usable-kitsoki-gate] wrote <path> (...)` stdout line -- a path, not a
    verdict (see module docstring).
    """
    match = None
    for line in stdout.splitlines():
        m = _RESULTS_LINE_RE.search(line)
        if m:
            match = m.group(1)
    return match


def _host_results_path(container_path: str) -> Path:
    """Map a results path as printed INSIDE the container back to the host
    path this process can read (mirrors swarm.py's `_host_results_path`
    convention: for `local` placement the executor mounts REPO_ROOT at
    KITSOKI_MNT, so a file written under KITSOKI_MNT/... is readable here at
    the equivalent REPO_ROOT/... path). Paths that don't carry the
    KITSOKI_MNT prefix (e.g. a test calling score() directly against a temp
    dir) are returned unchanged.
    """
    if container_path.startswith(KITSOKI_MNT + "/"):
        return REPO_ROOT / container_path[len(KITSOKI_MNT) + 1:]
    return Path(container_path)


def _sanitize(value: str) -> str:
    """A cell id (e.g. `kitsoki--core-maintainer--surface:web`) contains `:`
    and other path-unfriendly characters; turn it into a safe path segment
    for use in the container results path.
    """
    return re.sub(r"[^A-Za-z0-9_.-]+", "-", value).strip("-") or "cell"


def _first_line(blob: str) -> str:
    for line in blob.splitlines():
        line = line.strip()
        if line:
            return line[:200]
    return ""


base.register(UsableKitsokiGatePlugin())
