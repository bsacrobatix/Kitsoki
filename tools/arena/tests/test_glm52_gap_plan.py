#!/usr/bin/env python3
"""No-LLM tests for the GLM-5.2 missing-cell execution packet."""

from __future__ import annotations

import json
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO_ROOT = HERE.parent.parent.parent
SCRIPT = REPO_ROOT / "tools/arena/scripts/glm52_gap_plan.py"

failures: list[str] = []


def check(label: str, got, want) -> None:
    if got != want:
        failures.append(f"{label}: got {got!r}, want {want!r}")


with tempfile.TemporaryDirectory() as tmp:
    out = Path(tmp)
    report = out / "report.json"
    packet_json = out / "packet.json"
    packet_md = out / "packet.md"
    report.write_text(json.dumps({
        "kind": "glm52_bugswarm_bugfix_report",
        "required_glm52_matrix": [
            {
                "corpus": "oss-oracle",
                "task": "query-string-qs1-bugfix-test-repair",
                "treatment": "raw-prompt",
                "quality": "pending",
            },
            {
                "corpus": "bugswarm",
                "task": "bugswarm-square-okio-140452393",
                "treatment": "kitsoki",
                "quality": "pending",
            },
            {
                "corpus": "bugswarm",
                "task": "bugswarm-square-okio-140452393",
                "treatment": "raw-prompt",
                "quality": "pending",
            },
            {
                "corpus": "oss-oracle",
                "task": "query-string-qs1-bugfix-test-repair",
                "treatment": "kitsoki",
                "quality": "solved",
            },
        ],
    }), encoding="utf-8")
    proc = subprocess.run(
        [
            sys.executable,
            str(SCRIPT),
            "--report-json",
            str(report),
            "--json-out",
            str(packet_json),
            "--markdown-out",
            str(packet_md),
            "--oss-spec",
            ".artifacts/arena/glm52-oss.yaml",
            "--bugswarm-source",
            ".artifacts/bugswarm/arena-source.verified.yaml",
        ],
        cwd=REPO_ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    check("planner exits zero", proc.returncode, 0)
    packet = json.loads(packet_json.read_text(encoding="utf-8"))
    check("packet kind", packet["kind"], "glm52_gap_execution_packet")
    check("pending count", packet["pending_cell_count"], 3)
    check("oss pending count", packet["pending_by_corpus"]["oss-oracle"], 1)
    check("bugswarm pending count", packet["pending_by_corpus"]["bugswarm"], 2)
    actions = {action["corpus"]: action for action in packet["actions"]}
    check("oss ready", actions["oss-oracle"]["status"], "ready")
    check("oss command uses supplied spec", ".artifacts/arena/glm52-oss.yaml" in "\n".join(actions["oss-oracle"]["commands"]), True)
    check("bugswarm needs live spec via generated no-LLM spec", actions["bugswarm"]["status"], "needs-live-spec")
    check("bugswarm spec generation command present", "bugswarm_to_arena_spec.py" in actions["bugswarm"]["commands"][0], True)
    check("generated bugswarm commands stay no-live", "--live" in "\n".join(actions["bugswarm"]["commands"]), False)
    check("bugswarm warns live backend", "GLM-capable backend" in "\n".join(actions["bugswarm"]["prerequisites"]), True)
    md = packet_md.read_text(encoding="utf-8")
    check("markdown names report arg", "--bugswarm-arena-rollup" in md, True)
    check("markdown includes no-llm plan command", "arena.py run --spec" in md, True)

with tempfile.TemporaryDirectory() as tmp:
    out = Path(tmp)
    report = out / "report.json"
    packet_json = out / "packet.json"
    packet_md = out / "packet.md"
    report.write_text(json.dumps({
        "required_glm52_matrix": [
            {
                "corpus": "oss-oracle",
                "task": "query-string-qs1-bugfix-test-repair",
                "treatment": "raw-prompt",
                "quality": "pending",
            }
        ]
    }), encoding="utf-8")
    proc = subprocess.run(
        [
            sys.executable,
            str(SCRIPT),
            "--report-json",
            str(report),
            "--json-out",
            str(packet_json),
            "--markdown-out",
            str(packet_md),
        ],
        cwd=REPO_ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    check("planner without specs exits zero", proc.returncode, 0)
    packet = json.loads(packet_json.read_text(encoding="utf-8"))
    actions = {action["corpus"]: action for action in packet["actions"]}
    check("oss needs spec", actions["oss-oracle"]["status"], "needs-spec")
    check("oss has prerequisite", bool(actions["oss-oracle"]["prerequisites"]), True)
    check("bugswarm complete when no pending rows", actions["bugswarm"]["status"], "complete")

with tempfile.TemporaryDirectory() as tmp:
    out = Path(tmp)
    report = out / "report.json"
    packet_json = out / "packet.json"
    packet_md = out / "packet.md"
    report.write_text(json.dumps({
        "required_glm52_matrix": [
            {
                "corpus": "bugswarm",
                "task": "bugswarm-square-okio-140452393",
                "treatment": "raw-prompt",
                "quality": "pending",
            }
        ]
    }), encoding="utf-8")
    proc = subprocess.run(
        [
            sys.executable,
            str(SCRIPT),
            "--report-json",
            str(report),
            "--json-out",
            str(packet_json),
            "--markdown-out",
            str(packet_md),
            "--bugswarm-spec",
            ".artifacts/bugswarm/bugswarm-glm52-live.yaml",
        ],
        cwd=REPO_ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    check("planner with supplied bugswarm spec exits zero", proc.returncode, 0)
    packet = json.loads(packet_json.read_text(encoding="utf-8"))
    actions = {action["corpus"]: action for action in packet["actions"]}
    check("supplied bugswarm spec ready", actions["bugswarm"]["status"], "ready")
    check("supplied bugswarm live command explicit", "--live" in actions["bugswarm"]["commands"][-1], True)
    check("supplied bugswarm live command gated", "ARENA_PAIRED_TASK_ENABLE_CODEX=1" in actions["bugswarm"]["commands"][-1], True)

if failures:
    print("FAIL: glm52 gap plan")
    for failure in failures:
        print(f"  - {failure}")
    sys.exit(1)
print("PASS: glm52 gap execution packet (no LLM, no Docker)")
