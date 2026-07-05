#!/usr/bin/env python3
"""flow_gate_runner.py — the no-LLM half of Task 3.3
(docs/proposals/usable-kitsoki-release-gate.md): drive one mined scenario's
S4-compiled flow fixture through a REAL `kitsoki test flows` run and turn the
resulting trace into a parity-verdict record via the S1 x S4 join that
already lives in `tools/arena/arena/plugins/usable_kitsoki_gate.py`
(`extract_turn_signals` / `build_parity_record`).

## Why `kitsoki test flows --trace-out`, not a hand-rolled Go harness

`cmd/kitsoki/test_flows.go`'s `--trace-out` flag already writes the run's
authoritative JSONL trace (the exact `store.Event` stream
`internal/testrunner/flows_workbench_smoke_trace_test.go` reads via
`OnRigClose`) to a file. That JSONL is byte-compatible with what
`extract_turn_signals` expects (`{"kind": "turn.end", "payload": {...}}` per
line) -- no new Go code, no new trace format, just the existing CLI flag
pointed at each scenario's compiled `.flow.yaml`.

## The honest gap this run makes visible (do not paper over it)

`stories/scenario-foundry-harness`'s `desk` room is a plain room, not a
`workbench:` room (`stories/scenario-foundry-harness/rooms/desk.yaml` has no
`workbench:` key) -- it exists ONLY to give `flow_fixture_compiler.py`
somewhere to project a mined utterance's free text onto a fixed `ask` intent
(see that compiler's own module docstring). `internal/orchestrator
/workbench_gate_signal.go`'s `workbenchGateSignal` returns nil for any turn
whose dispatching state is not a `workbench:` room, so replaying these
compiled fixtures through the harness app produces **zero**
`usable_kitsoki_gate` turn signals, every time, by construction. This is not
a bug in this runner or in the harness app -- it is the true, current state
of the no-LLM join: nothing in this codebase yet compiles a mined scenario's
turns onto a REAL app's `workbench:` room (that would need a per-target-app
projection this compiler explicitly does not attempt, per its own
docstring). `build_parity_record` reports this honestly (`candidate_completed
= False`, notes say "no usable_kitsoki_gate turn signal was captured") rather
than fabricating a signal -- see the calibration report and
`usable_kitsoki_gate_constants.py`'s calibration-contact note for what this
means for `PARITY_THRESHOLD_PERCENT`.

## Surface handling

All three surfaces (`web`, `tui`, `mcp`) drive the IDENTICAL flow-replay
mechanism today -- there is no per-surface app/state-machine differentiation
in `stories/scenario-foundry-harness` (it has one room, no surface-specific
routing), and no real browser/PTY/MCP-stdio driver exists yet for the
no-LLM path (`WEB_SPEC`/`TUI_RUNNER`/`MCP_RUNNER` in
`usable_kitsoki_gate.py` document that gap). This is a deliberate,
documented simplification: the no-LLM contract test proves the schema /
join / rollup machinery end to end across all three surface tags, while a
REAL per-surface driver (a Playwright-driven web session, a PTY-driven TUI
session) remains later, larger, separately-gated work. Every record's
`surface` field is still set correctly per the cell it was built for, so
the rollup's WORST_SURFACE_GATING reduction is exercised for real, even
though the three surfaces' underlying runs are, today, the same run.
"""

from __future__ import annotations

import json
import subprocess
import sys
import tempfile
from pathlib import Path
from typing import Any

# tools/usable-kitsoki-gate/flow_gate_runner.py -> parents[2] == REPO_ROOT
REPO_ROOT = Path(__file__).resolve().parents[2]
# `tools/usable-kitsoki-gate` has a hyphen in it and can't be a dotted
# import path itself; reach the plugin the same way
# test_usable_kitsoki_gate_corpus.py does -- put tools/arena on sys.path and
# import its `arena` top-level package directly, rather than pretending
# `tools` is an importable namespace package.
_ARENA_ROOT = REPO_ROOT / "tools" / "arena"
if str(_ARENA_ROOT) not in sys.path:
    sys.path.insert(0, str(_ARENA_ROOT))

from arena.plugins import usable_kitsoki_gate as gate_plugin  # noqa: E402

HARNESS_APP = REPO_ROOT / "stories" / "scenario-foundry-harness" / "app.yaml"
DEFAULT_CORPUS = REPO_ROOT / gate_plugin.DEFAULT_SCENARIO_CORPUS
SURFACES = gate_plugin.SURFACES

# `kitsoki test flows` is invoked via `go run` (never a pre-built binary --
# AGENTS.md: "avoid generating a binary of kitsoki for testing"). A single
# invocation is ~1-1.5s warm; a calibration sweep bounds concurrency (see
# run_calibration_gate.py) rather than firing all cells at once.
FLOW_TIMEOUT_SECONDS = 120


class ScenarioNotFound(Exception):
    pass


def load_scenario(corpus_dir: Path, scenario_id: str) -> dict[str, Any]:
    path = corpus_dir / f"{scenario_id}.json"
    if not path.exists():
        raise ScenarioNotFound(f"no scenario IR document for {scenario_id!r} at {path}")
    return json.loads(path.read_text(encoding="utf-8"))


def flow_fixture_path(corpus_dir: Path, scenario_id: str) -> Path:
    return corpus_dir / "flows" / f"{scenario_id}.flow.yaml"


def list_scenario_ids(corpus_dir: Path) -> list[str]:
    """Every `scn-*.json` document directly under `corpus_dir` (mirrors
    `arena.model.load_targets_from_corpus`'s directory branch), sorted for
    determinism -- a calibration sweep's cell order must not depend on
    filesystem iteration order.
    """
    return sorted(p.stem for p in corpus_dir.glob("scn-*.json"))


def run_scenario_flow(
    scenario_id: str,
    corpus_dir: Path,
    *,
    kitsoki_root: Path = REPO_ROOT,
) -> tuple[subprocess.CompletedProcess, list[dict[str, Any]]]:
    """Replay one scenario's compiled flow fixture through a real
    `kitsoki test flows` run and return (process result, parsed trace
    events). Never touches an LLM or a network call -- `test_kind: flow`
    fixtures are pure state-machine + `host_cassette` replay
    (AGENTS.md: "Automated testing should never use a real LLM").
    """
    flow_path = flow_fixture_path(corpus_dir, scenario_id)
    if not flow_path.exists():
        raise ScenarioNotFound(f"no compiled flow fixture for {scenario_id!r} at {flow_path}")

    with tempfile.TemporaryDirectory() as tmp:
        trace_out = Path(tmp) / f"{scenario_id}.trace.jsonl"
        proc = subprocess.run(
            [
                "go", "run", "./cmd/kitsoki", "test", "flows",
                str(HARNESS_APP),
                "--flows", str(flow_path),
                "--trace-out", str(trace_out),
            ],
            cwd=str(kitsoki_root),
            capture_output=True,
            text=True,
            timeout=FLOW_TIMEOUT_SECONDS,
        )
        trace_events: list[dict[str, Any]] = []
        if trace_out.exists():
            for line in trace_out.read_text(encoding="utf-8").splitlines():
                line = line.strip()
                if not line:
                    continue
                trace_events.append(json.loads(line))
        return proc, trace_events


def build_record_for_cell(
    scenario_id: str,
    corpus_dir: Path,
    surface: str,
    *,
    evidence_dir: Path,
    kitsoki_root: Path = REPO_ROOT,
) -> dict[str, Any]:
    """Drive one (scenario, surface) cell end to end: replay the flow, pull
    the turn signals off the real trace, and join them against the
    scenario's own `abandoned` oracle via `build_parity_record` -- the exact
    join `usable_kitsoki_gate.py`'s `score()` performs for a raw-signal
    bundle, just invoked directly here instead of through an arena cell.
    """
    scenario = load_scenario(corpus_dir, scenario_id)
    proc, trace_events = run_scenario_flow(scenario_id, corpus_dir, kitsoki_root=kitsoki_root)
    turn_signals = gate_plugin.extract_turn_signals(trace_events)

    evidence_dir.mkdir(parents=True, exist_ok=True)
    evidence_path = evidence_dir / f"{scenario_id}.{surface}.trace.jsonl"
    evidence_path.write_text(
        "\n".join(json.dumps(e, sort_keys=True) for e in trace_events)
        + ("\n" if trace_events else ""),
        encoding="utf-8",
    )

    notes = ""
    if proc.returncode != 0:
        blob = (proc.stdout + "\n" + proc.stderr).strip()
        notes = f"kitsoki test flows exited {proc.returncode}: {blob[:300]}"

    return gate_plugin.build_parity_record(
        scenario=scenario,
        surface=surface,
        turn_signals=turn_signals,
        evidence_refs=[str(evidence_path)],
        notes=notes,
    )
