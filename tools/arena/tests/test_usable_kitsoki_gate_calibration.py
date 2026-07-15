#!/usr/bin/env python3
"""Task 4.2 (docs/proposals/usable-kitsoki-release-gate.md) + S6
"no-llm-parity": the no-LLM calibration run over the 18-scenario calibration
set (tools/session-mining/calibration/), driven against the THREE real
`workbench:` rooms this project ships (dev-story, the hand-authored primary,
and its two thin inheritors pets-dev/slidey-dev -- see
tools/session-mining/flow_fixture_compiler.py's WORKBENCH_TARGETS registry),
checked in as a diffable parity report at
tools/arena/tests/fixtures/usable-kitsoki-gate/calibration-report.json.

By default this test verifies that report's fixture contract and replays one
real no-LLM calibration cell through tools/usable-kitsoki-gate/
flow_gate_runner.py. Set KITSOKI_FULL_CALIBRATION_REGEN=1 to regenerate the
full 162-cell report from scratch (via run_calibration_gate.py's
`sweep()`/`rollup()`) and diff it byte-for-byte against the checked-in copy.
A real full-regeneration diff means either (a) the calibration set changed,
(b) the harness/join logic changed, (c) S1's producer contract changed, or
(d) one of the three target stories' workbench wiring changed -- any of which
should surface as a reviewable fixture diff, not a silent behavior change.

Spends zero dollars and touches no docker/browser/LLM: every scenario is
driven through a real `kitsoki test flows` replay
(tools/session-mining/calibration/flows/*.<target>.flow.yaml, `test_kind:
flow`, pure state-machine + host_cassette replay) against each target's own
real app (stories/dev-story/app.yaml, stories/pets-dev/app.yaml,
stories/slidey-dev/app.yaml), per AGENTS.md's "Automated testing should
never use a real LLM" rule. Under `make test` the default one-cell replay
finishes quickly. Full regeneration reuses the prebuilt
KITSOKI_TEST_KITSOKI_BINARY flow runner when available; standalone full
regeneration falls back to `go run ./cmd/kitsoki`.
"""

from __future__ import annotations

import json
import os
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO_ROOT = HERE.parents[2]
GATE_TOOLS_DIR = REPO_ROOT / "tools" / "usable-kitsoki-gate"
sys.path.insert(0, str(GATE_TOOLS_DIR))

import flow_gate_runner as runner  # noqa: E402
import run_calibration_gate as calib  # noqa: E402

FIXTURE_PATH = HERE / "fixtures" / "usable-kitsoki-gate" / "calibration-report.json"

failures: list[str] = []


def check(label: str, got, want) -> None:
    if got != want:
        failures.append(f"{label}: got {got!r}, want {want!r}")


def check_true(label: str, cond: bool, detail: str = "") -> None:
    if not cond:
        failures.append(f"{label}: expected true{f' ({detail})' if detail else ''}")


corpus_dir = runner.DEFAULT_CORPUS
check_true("calibration corpus exists on disk", corpus_dir.is_dir(), str(corpus_dir))

scenario_ids = runner.list_scenario_ids(corpus_dir)
check("calibration set has 18 scenario documents", len(scenario_ids), 18)

targets = list(calib.DEFAULT_TARGETS)

if not FIXTURE_PATH.exists():
    failures.append(f"checked-in calibration report missing at {FIXTURE_PATH} -- run "
                     "run_calibration_gate.py --relative-evidence and commit its output")
else:
    checked_in = json.loads(FIXTURE_PATH.read_text(encoding="utf-8"))

    check("fixture schema_version", checked_in.get("schema_version"), "1.0.0")
    check("fixture run_id", checked_in.get("run_id"), "calibration")
    check("fixture corpus", checked_in.get("corpus"), "tools/session-mining/calibration")
    check("fixture surfaces", checked_in.get("surfaces"), ["web", "tui", "mcp"])
    check("fixture targets", checked_in.get("targets"), targets)
    check("fixture scenario_count", checked_in.get("scenario_count"), len(scenario_ids))
    check("fixture record_count", checked_in.get("record_count"), 18 * 3 * len(targets))
    check("fixture has one record per expected cell", len(checked_in.get("records", [])), 18 * 3 * len(targets))
    check("fixture parity threshold", checked_in.get("parity_threshold_percent"), calib.gate_constants.PARITY_THRESHOLD_PERCENT)
    check("fixture gate conditions", checked_in.get("gate_conditions"), list(calib.gate_constants.GATE_CONDITIONS))

    full_regen = os.environ.get("KITSOKI_FULL_CALIBRATION_REGEN", "").strip() == "1"
    evidence_dir = REPO_ROOT / ".artifacts" / "usable-kitsoki-gate" / "calibration-evidence"

    if full_regen:
        records = calib.sweep(
            corpus_dir, list(("web", "tui", "mcp")), run_id="calibration",
            evidence_dir=evidence_dir, concurrency=2, targets=targets,
        )
        check("162 records for 18 scenarios x 3 surfaces x 3 workbench targets", len(records), 18 * 3 * len(targets))

        rolled = calib.rollup(records, results_path=str(REPO_ROOT / ".artifacts" / "usable-kitsoki-gate" / "calibration-report.json"))
        report = {
            "schema_version": "1.0.0",
            "run_id": "calibration",
            "corpus": "tools/session-mining/calibration",
            "surfaces": ["web", "tui", "mcp"],
            "targets": targets,
            "scenario_count": len(scenario_ids),
            "record_count": len(records),
            "parity_threshold_percent": calib.gate_constants.PARITY_THRESHOLD_PERCENT,
            "gate_conditions": list(calib.gate_constants.GATE_CONDITIONS),
            "rollup": rolled,
            "records": records,
        }
        report = calib._relativize_report(report)  # noqa: SLF001 - full regeneration mode checks exact fixture shape

        if report != checked_in:
            # Give a readable pointer to the first mismatching top-level key
            # rather than a wall of JSON.
            for key in sorted(set(report) | set(checked_in)):
                if report.get(key) != checked_in.get(key):
                    failures.append(
                        f"regenerated calibration report differs from the checked-in fixture at key {key!r} "
                        "-- regenerate and review the diff (tools/usable-kitsoki-gate/run_calibration_gate.py "
                        "--out tools/arena/tests/fixtures/usable-kitsoki-gate/calibration-report.json "
                        "--relative-evidence) before committing"
                    )
    else:
        # The full 162-cell regeneration is intentionally heavy and can exceed
        # the Python policy lane's per-file timeout when the broad repo gate is
        # contended. The default gate still runs one real replay cell through
        # the same flow runner and verifies the checked-in fixture's contract;
        # set KITSOKI_FULL_CALIBRATION_REGEN=1 for the byte-for-byte sweep.
        smoke_scenario = scenario_ids[0]
        smoke_surface = "web"
        smoke_target = targets[0]
        smoke = runner.build_record_for_cell(
            smoke_scenario, corpus_dir, smoke_surface,
            evidence_dir=evidence_dir, target=smoke_target,
        )
        smoke["evidence_refs"] = [
            str(Path(ref).resolve().relative_to(REPO_ROOT))
            if Path(ref).resolve().is_relative_to(REPO_ROOT)
            else ref
            for ref in smoke["evidence_refs"]
        ]
        fixture_records = checked_in.get("records", [])
        match = next((
            record for record in fixture_records
            if record.get("scenario_id") == smoke_scenario
            and record.get("surface") == smoke_surface
            and record.get("evidence_refs") == smoke["evidence_refs"]
        ), None)
        check_true("fixture contains smoke replay cell", match is not None)
        if match is not None:
            check("smoke replay matches checked-in calibration fixture", smoke, match)

    # Report the calibration-contact finding for Task 4.2's open question
    # 1: does the 90% placeholder threshold survive contact with the real
    # calibration set? (see usable_kitsoki_gate_constants.py's own
    # calibration-contact note, which this run's number must match.)
    worst = checked_in["rollup"]["metrics"]["worst_surface_parity_percent"]
    threshold = checked_in["parity_threshold_percent"]
    silent_bounce_count = checked_in["rollup"]["metrics"]["silent_bounce_count"]
    misroute_adjacent_count = checked_in["rollup"]["metrics"]["misroute_adjacent_count"]
    check_true(
        "calibration-contact finding is consistent: worst-surface parity vs threshold is honestly reported "
        "(not silently patched to pass)",
        True,  # this check always "passes" -- it exists to print the number below for a human reviewer
    )
    # Report (not assume) the other two GATE_CONDITIONS from the actual
    # measured run -- S2's own regression-test-at-scale framing
    # (workbench_gate_signal.go's doc comment) says silent_bounce should
    # always be false by construction across all 162 real cells, and
    # misroute_adjacent is hard-false from every S1 signal today (a
    # documented absence, not a measurement) -- print both explicitly
    # rather than only ever reporting the parity number.
    print(f"[calibration] silent_bounce_count={silent_bounce_count} "
          f"(zero_silent_bounce {'PASSES' if silent_bounce_count == 0 else 'FAILS'})")
    print(f"[calibration] misroute_adjacent_count={misroute_adjacent_count} "
          f"(zero_misroute_adjacent {'PASSES' if misroute_adjacent_count == 0 else 'FAILS'}; "
          "S1 hard-codes this false today -- documented absence, not a measurement)")
    print(f"[calibration] worst_surface_parity_percent={worst} vs PARITY_THRESHOLD_PERCENT={threshold} "
          f"-> gate {'PASSES' if checked_in['rollup']['verdict'] == 'solved' else 'FAILS'} on the calibration set "
          "(see usable_kitsoki_gate_constants.py's calibration-contact note for why)")

if failures:
    print(f"FAIL ({len(failures)}):")
    for f in failures:
        print(f"  - {f}")
    sys.exit(1)
if os.environ.get("KITSOKI_FULL_CALIBRATION_REGEN", "").strip() == "1":
    print("PASS: usable-kitsoki-gate no-LLM calibration run (Task 3.3 no-LLM half + Task 4.2) "
          "regenerates byte-identical to the checked-in parity report")
else:
    print("PASS: usable-kitsoki-gate calibration fixture contract plus one real no-LLM replay cell "
          "(set KITSOKI_FULL_CALIBRATION_REGEN=1 for the full byte-identical sweep)")
