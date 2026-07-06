#!/usr/bin/env python3
"""No-LLM tests for GLM-5.2 report claim gating."""

from __future__ import annotations

import json
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO_ROOT = HERE.parent.parent.parent
SCRIPT = REPO_ROOT / "tools/arena/scripts/glm52_report_gate.py"
REPORT = REPO_ROOT / "docs/case-studies/bugswarm-glm52-bugfix-report.data.json"

failures: list[str] = []


def check(label: str, got, want) -> None:
    if got != want:
        failures.append(f"{label}: got {got!r}, want {want!r}")


def run_gate(path: Path) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, str(SCRIPT), "--report-json", str(path)],
        cwd=REPO_ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )


proc = run_gate(REPORT)
check("current report passes gate", proc.returncode, 0)
check("current report pass message", "PASS: GLM-5.2 report gate" in proc.stdout, True)

with tempfile.TemporaryDirectory() as tmp:
    out = Path(tmp)
    bad = out / "bad-comparison.json"
    report = json.loads(REPORT.read_text(encoding="utf-8"))
    report["comparisons"]["overall"]["status"] = "complete"
    report["comparisons"]["overall"]["success_rate_delta"] = 0.25
    report["comparisons"]["overall"]["token_ratio_kitsoki_to_raw"] = 10.0
    bad.write_text(json.dumps(report), encoding="utf-8")
    proc = run_gate(bad)
    check("bad pending comparison exits nonzero", proc.returncode, 1)
    check("bad pending comparison names ratio", "must not publish token ratio while pending" in proc.stdout, True)

with tempfile.TemporaryDirectory() as tmp:
    out = Path(tmp)
    bad = out / "bad-bugswarm-closure.json"
    report = json.loads(REPORT.read_text(encoding="utf-8"))
    action = next(action for action in report["evidence_closure"]["actions"] if action["corpus"] == "bugswarm")
    action["status"] = "ready"
    bad.write_text(json.dumps(report), encoding="utf-8")
    proc = run_gate(bad)
    check("bad bugswarm closure exits nonzero", proc.returncode, 1)
    check("bad bugswarm closure names execute gate", "needs-execute-verification" in proc.stdout, True)

with tempfile.TemporaryDirectory() as tmp:
    out = Path(tmp)
    bad = out / "bad-completion-audit.json"
    report = json.loads(REPORT.read_text(encoding="utf-8"))
    report["completion_audit"]["status"] = "complete"
    bad.write_text(json.dumps(report), encoding="utf-8")
    proc = run_gate(bad)
    check("bad completion audit exits nonzero", proc.returncode, 1)
    check("bad completion audit names pending evidence", "completion audit must remain incomplete" in proc.stdout, True)

with tempfile.TemporaryDirectory() as tmp:
    out = Path(tmp)
    bad = out / "bad-completion-next.json"
    report = json.loads(REPORT.read_text(encoding="utf-8"))
    missing = next(item for item in report["completion_audit"]["requirements"] if item["status"] == "missing")
    missing["next"] = ""
    bad.write_text(json.dumps(report), encoding="utf-8")
    proc = run_gate(bad)
    check("bad completion next exits nonzero", proc.returncode, 1)
    check("bad completion next names next step", "lacks next step" in proc.stdout, True)

with tempfile.TemporaryDirectory() as tmp:
    out = Path(tmp)
    bad = out / "bad-pending-tokens.json"
    report = json.loads(REPORT.read_text(encoding="utf-8"))
    row = next(row for row in report["required_glm52_matrix"] if row["quality"] == "pending")
    row["total_tokens"] = 123
    bad.write_text(json.dumps(report), encoding="utf-8")
    proc = run_gate(bad)
    check("bad pending tokens exits nonzero", proc.returncode, 1)
    check("bad pending tokens names problem", "pending row must not carry token evidence" in proc.stdout, True)

with tempfile.TemporaryDirectory() as tmp:
    out = Path(tmp)
    bad = out / "bad-references.json"
    report = json.loads(REPORT.read_text(encoding="utf-8"))
    report["references"]["bugswarm_seed"] = []
    bad.write_text(json.dumps(report), encoding="utf-8")
    proc = run_gate(bad)
    check("bad references exits nonzero", proc.returncode, 1)
    check("bad references names seed", "references missing BugSwarm seed provenance" in proc.stdout, True)

if failures:
    print("FAIL: glm52 report gate")
    for failure in failures:
        print(f"  - {failure}")
    sys.exit(1)
print("PASS: GLM-5.2 report gate tests (no LLM, no Docker)")
