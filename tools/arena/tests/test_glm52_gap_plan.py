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
    oss_spec = out / "oss-glm52-live.yaml"
    oss_spec.write_text(
        "\n".join([
            "job_type: paired-task",
            "targets:",
            "  - id: cost-bench",
            "variants:",
            "  - id: raw-prompt-glm-5.2",
            "    treatment: single-briefed",
            "    candidate: glm-5.2",
            "    backend: claude",
            "    model: glm-5.2",
            "axes:",
            "  task:",
            "    - query-string-qs1-bugfix-test-repair",
            "",
        ]),
        encoding="utf-8",
    )
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
            str(oss_spec),
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
    check("oss raw prompt ready", actions["oss-oracle"]["status"], "ready")
    check("oss command uses supplied spec", str(oss_spec) in "\n".join(actions["oss-oracle"]["commands"]), True)
    check("oss raw prompt live command present", "--live" in "\n".join(actions["oss-oracle"]["commands"]), True)
    check("bugswarm needs live spec via generated no-LLM spec", actions["bugswarm"]["status"], "needs-live-spec")
    check("bugswarm spec generation command present", "bugswarm_to_arena_spec.py" in actions["bugswarm"]["commands"][0], True)
    check("generated bugswarm commands stay no-live", "--live" in "\n".join(actions["bugswarm"]["commands"]), False)
    check("bugswarm warns live backend", "backend=claude" in "\n".join(actions["bugswarm"]["prerequisites"]), True)
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
    bugswarm_spec = out / "bugswarm-glm52-live.yaml"
    bugswarm_spec.write_text(
        "\n".join([
            "job_type: paired-task",
            "targets:",
            "  - id: bugswarm-verified",
            "    corpus: .artifacts/bugswarm/arena-source.verified.yaml",
            "variants:",
            "  - id: raw-prompt-glm-5.2",
            "    treatment: single-briefed",
            "    candidate: glm-5.2",
            "    backend: codex",
            "    model: glm-5.2",
            "axes:",
            "  task:",
            "    - bugswarm-square-okio-140452393",
            "",
        ]),
        encoding="utf-8",
    )
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
            str(bugswarm_spec),
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
    check("supplied bugswarm raw spec needs claude backend", actions["bugswarm"]["status"], "needs-spec-fix")
    check("supplied bugswarm raw spec audit fails", actions["bugswarm"]["spec_audit"]["ok"], False)
    check("supplied bugswarm raw spec no live command", "--live" in "\n".join(actions["bugswarm"]["commands"]), False)
    check("supplied bugswarm raw spec names claude backend", "requires backend 'claude'" in "\n".join(actions["bugswarm"]["prerequisites"]), True)

with tempfile.TemporaryDirectory() as tmp:
    out = Path(tmp)
    report = out / "report.json"
    packet_json = out / "packet.json"
    packet_md = out / "packet.md"
    bugswarm_spec = out / "bugswarm-raw-glm52-live.yaml"
    bugswarm_spec.write_text(
        "\n".join([
            "job_type: paired-task",
            "targets:",
            "  - id: bugswarm-verified",
            "    corpus: .artifacts/bugswarm/arena-source.verified.yaml",
            "variants:",
            "  - id: raw-prompt-glm-5.2",
            "    treatment: single-briefed",
            "    candidate: glm-5.2",
            "    backend: claude",
            "    model: glm-5.2",
            "axes:",
            "  task:",
            "    - bugswarm-square-okio-140452393",
            "",
        ]),
        encoding="utf-8",
    )
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
            str(bugswarm_spec),
        ],
        cwd=REPO_ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    check("planner with supplied raw claude bugswarm spec exits zero", proc.returncode, 0)
    packet = json.loads(packet_json.read_text(encoding="utf-8"))
    actions = {action["corpus"]: action for action in packet["actions"]}
    check("supplied bugswarm raw claude spec needs runner adapter", actions["bugswarm"]["status"], "needs-runner-adapter")
    check("supplied bugswarm raw claude spec audit ok", actions["bugswarm"]["spec_audit"]["ok"], True)
    check("supplied bugswarm raw claude no live command", "--live" in "\n".join(actions["bugswarm"]["commands"]), False)
    check("supplied bugswarm raw claude names materializer gap", "BugSwarm --live materialization" in "\n".join(actions["bugswarm"]["prerequisites"]), True)

with tempfile.TemporaryDirectory() as tmp:
    out = Path(tmp)
    report = out / "report.json"
    packet_json = out / "packet.json"
    packet_md = out / "packet.md"
    bugswarm_spec = out / "bugswarm-kitsoki-glm52-live.yaml"
    bugswarm_spec.write_text(
        "\n".join([
            "job_type: paired-task",
            "targets:",
            "  - id: bugswarm-verified",
            "    corpus: .artifacts/bugswarm/arena-source.verified.yaml",
            "variants:",
            "  - id: kitsoki-glm-5.2",
            "    treatment: kitsoki",
            "    candidate: glm-5.2",
            "    backend: codex",
            "    model: glm-5.2",
            "axes:",
            "  task:",
            "    - bugswarm-square-okio-140452393",
            "",
        ]),
        encoding="utf-8",
    )
    report.write_text(json.dumps({
        "required_glm52_matrix": [
            {
                "corpus": "bugswarm",
                "task": "bugswarm-square-okio-140452393",
                "treatment": "kitsoki",
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
            str(bugswarm_spec),
        ],
        cwd=REPO_ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    check("planner with kitsoki-only bugswarm spec exits zero", proc.returncode, 0)
    packet = json.loads(packet_json.read_text(encoding="utf-8"))
    actions = {action["corpus"]: action for action in packet["actions"]}
    check("supplied bugswarm kitsoki spec needs runner adapter", actions["bugswarm"]["status"], "needs-runner-adapter")
    check("supplied bugswarm kitsoki spec audit ok", actions["bugswarm"]["spec_audit"]["ok"], True)
    check("supplied bugswarm kitsoki no live command", "--live" in "\n".join(actions["bugswarm"]["commands"]), False)
    check("supplied bugswarm kitsoki names materializer gap", "BugSwarm --live materialization" in "\n".join(actions["bugswarm"]["prerequisites"]), True)

with tempfile.TemporaryDirectory() as tmp:
    out = Path(tmp)
    report = out / "report.json"
    packet_json = out / "packet.json"
    packet_md = out / "packet.md"
    bad_spec = out / "oss-codex.yaml"
    bad_spec.write_text(
        "\n".join([
            "job_type: paired-task",
            "targets:",
            "  - id: cost-bench",
            "variants:",
            "  - id: raw-prompt-codex-native",
            "    treatment: single-briefed",
            "    candidate: codex-native",
            "    backend: codex",
            "    model: gpt-5.5",
            "axes:",
            "  task:",
            "    - query-string-qs1-bugfix-test-repair",
            "",
        ]),
        encoding="utf-8",
    )
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
            "--oss-spec",
            str(bad_spec),
        ],
        cwd=REPO_ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    check("planner with bad oss spec exits zero", proc.returncode, 0)
    packet = json.loads(packet_json.read_text(encoding="utf-8"))
    actions = {action["corpus"]: action for action in packet["actions"]}
    check("bad oss spec needs fix", actions["oss-oracle"]["status"], "needs-spec-fix")
    check("bad oss spec audit fails", actions["oss-oracle"]["spec_audit"]["ok"], False)
    check("bad oss spec no live command", "--live" in "\n".join(actions["oss-oracle"]["commands"]), False)
    check("bad oss spec names GLM problem", "not GLM-5.2" in "\n".join(actions["oss-oracle"]["prerequisites"]), True)

if failures:
    print("FAIL: glm52 gap plan")
    for failure in failures:
        print(f"  - {failure}")
    sys.exit(1)
print("PASS: glm52 gap execution packet (no LLM, no Docker)")
