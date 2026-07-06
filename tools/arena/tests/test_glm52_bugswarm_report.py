#!/usr/bin/env python3
"""No-LLM tests for the generated GLM-5.2 + BugSwarm report."""

from __future__ import annotations

import json
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO_ROOT = HERE.parent.parent.parent
SCRIPT = REPO_ROOT / "tools/arena/scripts/glm52_bugswarm_report.py"
CONVERT = REPO_ROOT / "tools/arena/scripts/bugswarm_to_arena.py"
VERIFY = REPO_ROOT / "tools/arena/scripts/bugswarm_verify_source.py"
APPLY = REPO_ROOT / "tools/arena/scripts/bugswarm_apply_verification.py"

failures: list[str] = []


def check(label: str, got, want) -> None:
    if got != want:
        failures.append(f"{label}: got {got!r}, want {want!r}")


with tempfile.TemporaryDirectory() as tmp:
    out = Path(tmp)
    json_out = out / "report.json"
    md_out = out / "report.md"
    proc = subprocess.run(
        [
            sys.executable,
            str(SCRIPT),
            "--generated-at",
            "2026-07-06T00:00:00Z",
            "--json-out",
            str(json_out),
            "--markdown-out",
            str(md_out),
        ],
        cwd=REPO_ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    check("generator exits zero", proc.returncode, 0)
    report = json.loads(json_out.read_text(encoding="utf-8"))
    check("report kind", report.get("kind"), "glm52_bugswarm_bugfix_report")
    check("oss corpus task count", report["corpora"]["oss_oracle"]["task_count"], 26)
    check("bugswarm source status", report["corpora"]["bugswarm"]["source_status"], "adapter-ready")
    check("bugswarm imported count", report["corpora"]["bugswarm"]["imported_task_count"], 0)
    closure = {action["corpus"]: action for action in report["evidence_closure"]["actions"]}
    check("closure oss needs spec", closure["oss-oracle"]["status"], "needs-spec")
    check("closure bugswarm needs import", closure["bugswarm"]["status"], "needs-import")

    glm_cells = report["glm52_bugfix_cells"]
    check("one committed glm cell", len(glm_cells), 1)
    check("glm cell treatment", glm_cells[0]["treatment"], "kitsoki")
    check("glm cell quality", glm_cells[0]["quality"], "partial")
    check("glm cell tokens", glm_cells[0]["total_tokens"], 2890980)

    headline = report["rollups"]["glm52_by_corpus_treatment"]
    check("oss kitsoki attempted", headline["oss-oracle|kitsoki"]["attempted"], 1)
    check("oss kitsoki success rate", headline["oss-oracle|kitsoki"]["success_rate"], 0.0)
    check("oss raw pending", headline["oss-oracle|raw-prompt"]["pending"], 1)
    check("bugswarm raw pending", headline["bugswarm|raw-prompt"]["pending"], 1)

    md = md_out.read_text(encoding="utf-8")
    check("markdown names pending raw arm", "oss-oracle | raw-prompt" in md, True)
    check("markdown warns no token ratio", "must not compute a token ratio" in md, True)
    check("markdown includes closure packet", "## Evidence Closure Packet" in md, True)
    check("markdown includes gap planner", "glm52_gap_plan.py" in md, True)
    check("markdown tells bugswarm source handoff", "--bugswarm-source <execute-verified BugSwarm YAML>" in md, True)
    check("markdown closure table includes import", "| bugswarm | `needs-import`" in md, True)

with tempfile.TemporaryDirectory() as tmp:
    out = Path(tmp)
    artifacts = out / "artifacts.json"
    source = out / "bugswarm-source.yaml"
    verification = out / "bugswarm-verification.json"
    json_out = out / "with-bugswarm.json"
    md_out = out / "with-bugswarm.md"
    artifacts.write_text(json.dumps({
        "artifacts": [
            {
                "image_tag": "square-okio-140452393",
                "repo": "square/okio",
                "failed_job_id": "140452393",
                "passed_job_id": "140452394",
            }
        ]
    }), encoding="utf-8")
    subprocess.run([sys.executable, str(CONVERT), "--in", str(artifacts), "--out", str(source)], cwd=REPO_ROOT, check=True)
    subprocess.run([sys.executable, str(VERIFY), "--source", str(source), "--out", str(verification), "--dry-run"], cwd=REPO_ROOT, check=True)
    proc = subprocess.run(
        [
            sys.executable,
            str(SCRIPT),
            "--generated-at",
            "2026-07-06T00:00:00Z",
            "--json-out",
            str(json_out),
            "--markdown-out",
            str(md_out),
            "--bugswarm-source",
            str(source),
            "--bugswarm-verification",
            str(verification),
        ],
        cwd=REPO_ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    check("generator with bugswarm exits zero", proc.returncode, 0)
    report = json.loads(json_out.read_text(encoding="utf-8"))
    check("bugswarm imported with source", report["corpora"]["bugswarm"]["imported_task_count"], 1)
    check("bugswarm verification mode", report["corpora"]["bugswarm"]["verification_mode"], "dry-run")
    check("bugswarm verification count", report["corpora"]["bugswarm"]["verification_report_count"], 1)
    check("bugswarm dry-run verified count", report["corpora"]["bugswarm"]["verification_verified_count"], 0)
    closure = {action["corpus"]: action for action in report["evidence_closure"]["actions"]}
    check("closure bugswarm needs execute verification", closure["bugswarm"]["status"], "needs-execute-verification")
    md = md_out.read_text(encoding="utf-8")
    check("bugswarm source closure command", f"--bugswarm-source {source}" in md, True)

with tempfile.TemporaryDirectory() as tmp:
    out = Path(tmp)
    artifacts = out / "artifacts.json"
    source = out / "bugswarm-source.yaml"
    verification = out / "bugswarm-verification.json"
    verified_source = out / "bugswarm-verified.yaml"
    rollup = out / "bugswarm-rollup.json"
    json_out = out / "with-bugswarm-rollup.json"
    md_out = out / "with-bugswarm-rollup.md"
    artifacts.write_text(json.dumps({
        "artifacts": [
            {
                "image_tag": "square-okio-140452393",
                "repo": "square/okio",
                "failed_job_id": "140452393",
                "passed_job_id": "140452394",
            }
        ]
    }), encoding="utf-8")
    verification.write_text(json.dumps({
        "kind": "arena_bugswarm_verification",
        "version": 1,
        "mode": "execute",
        "task_count": 1,
        "verified_count": 1,
        "results": [
            {
                "task_id": "bugswarm-square-okio-140452393",
                "image_tag": "square-okio-140452393",
                "verified_red": True,
                "verified_green": True,
                "failed_exit_code": 1,
                "passed_exit_code": 0,
            }
        ],
    }), encoding="utf-8")
    subprocess.run([sys.executable, str(CONVERT), "--in", str(artifacts), "--out", str(source)], cwd=REPO_ROOT, check=True)
    subprocess.run(
        [
            sys.executable,
            str(APPLY),
            "--source",
            str(source),
            "--verification",
            str(verification),
            "--out",
            str(verified_source),
        ],
        cwd=REPO_ROOT,
        check=True,
    )
    rollup.write_text(json.dumps({
        "cells": [
            {
                "axis": {"task": "bugswarm-square-okio-140452393"},
                "cell_id": "bugswarm-verified--kitsoki-glm-5.2--task:bugswarm-square-okio-140452393",
                "evidence_refs": [str(verified_source) + "#bugswarm-square-okio-140452393"],
                "health": "model:result",
                "job_type": "paired-task",
                "metrics": {"cost_usd": 0.15, "tokens": 1000, "wall_s": 60.0},
                "notes": "synthetic fixture: oracle solved",
                "target_id": "bugswarm-verified",
                "trace_ref": "traces/kitsoki.jsonl",
                "variant_id": "kitsoki-glm-5.2",
                "verdict": "solved",
            },
            {
                "axis": {"task": "bugswarm-square-okio-140452393"},
                "cell_id": "bugswarm-verified--raw-prompt-glm-5.2--task:bugswarm-square-okio-140452393",
                "evidence_refs": [str(verified_source) + "#bugswarm-square-okio-140452393"],
                "health": "model:result",
                "job_type": "paired-task",
                "metrics": {"cost_usd": 0.03, "tokens": 200, "wall_s": 20.0},
                "notes": "synthetic fixture: oracle failed",
                "target_id": "bugswarm-verified",
                "trace_ref": "traces/raw.jsonl",
                "variant_id": "raw-prompt-glm-5.2",
                "verdict": "failed",
            },
        ]
    }), encoding="utf-8")
    proc = subprocess.run(
        [
            sys.executable,
            str(SCRIPT),
            "--generated-at",
            "2026-07-06T00:00:00Z",
            "--json-out",
            str(json_out),
            "--markdown-out",
            str(md_out),
            "--bugswarm-source",
            str(verified_source),
            "--bugswarm-verification",
            str(verification),
            "--bugswarm-arena-rollup",
            str(rollup),
        ],
        cwd=REPO_ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    check("generator with bugswarm rollup exits zero", proc.returncode, 0)
    report = json.loads(json_out.read_text(encoding="utf-8"))
    check("bugswarm cell count", len(report["bugswarm_glm52_arena_cells"]), 2)
    headline = report["rollups"]["glm52_by_corpus_treatment"]
    check("bugswarm kitsoki attempted", headline["bugswarm|kitsoki"]["attempted"], 1)
    check("bugswarm kitsoki success", headline["bugswarm|kitsoki"]["success_rate"], 1.0)
    check("bugswarm kitsoki tokens", headline["bugswarm|kitsoki"]["total_tokens"], 1000)
    check("bugswarm raw attempted", headline["bugswarm|raw-prompt"]["attempted"], 1)
    check("bugswarm raw success", headline["bugswarm|raw-prompt"]["success_rate"], 0.0)
    check("bugswarm raw tokens", headline["bugswarm|raw-prompt"]["total_tokens"], 200)
    gaps = "\n".join(report["evidence_gaps"])
    check("bugswarm result gap absent", "Some imported BugSwarm tasks are missing" in gaps, False)
    md = md_out.read_text(encoding="utf-8")
    check("markdown includes bugswarm arena section", "Committed BugSwarm GLM-5.2 Arena Cells" in md, True)
    check("markdown includes bugswarm rollup input", "BugSwarm arena rollup" in md, True)

with tempfile.TemporaryDirectory() as tmp:
    out = Path(tmp)
    rollup = out / "oss-glm-rollup.json"
    json_out = out / "with-oss-rollup.json"
    md_out = out / "with-oss-rollup.md"
    rollup.write_text(json.dumps({
        "cells": [
            {
                "axis": {"task": "query-string-qs1-bugfix-test-repair"},
                "cell_id": "cost-bench-round2--kitsoki-glm-5.2--task:query-string-qs1-bugfix-test-repair",
                "evidence_refs": ["tools/arena/corpus/cost-bench.manifest.yaml#query-string-qs1-bugfix-test-repair"],
                "health": "model:result",
                "job_type": "paired-task",
                "metrics": {"cost_usd": 0.5, "tokens": 3000, "wall_s": 300.0},
                "notes": "synthetic fixture: oracle solved",
                "target_id": "cost-bench-round2",
                "trace_ref": "traces/oss-kitsoki.jsonl",
                "variant_id": "kitsoki-glm-5.2",
                "verdict": "solved",
            },
            {
                "axis": {"task": "query-string-qs1-bugfix-test-repair"},
                "cell_id": "cost-bench-round2--raw-prompt-glm-5.2--task:query-string-qs1-bugfix-test-repair",
                "evidence_refs": ["tools/arena/corpus/cost-bench.manifest.yaml#query-string-qs1-bugfix-test-repair"],
                "health": "model:result",
                "job_type": "paired-task",
                "metrics": {"cost_usd": 0.08, "tokens": 700, "wall_s": 80.0},
                "notes": "synthetic fixture: oracle failed",
                "target_id": "cost-bench-round2",
                "trace_ref": "traces/oss-raw.jsonl",
                "variant_id": "raw-prompt-glm-5.2",
                "verdict": "failed",
            },
            {
                "axis": {"task": "query-string-qs2-bugfix-test-repair"},
                "cell_id": "cost-bench-round2--kitsoki-codex-native--task:query-string-qs2-bugfix-test-repair",
                "health": "model:result",
                "job_type": "paired-task",
                "metrics": {"cost_usd": 0.1, "tokens": 999, "wall_s": 20.0},
                "target_id": "cost-bench-round2",
                "variant_id": "kitsoki-codex-native",
                "verdict": "solved",
            },
        ]
    }), encoding="utf-8")
    proc = subprocess.run(
        [
            sys.executable,
            str(SCRIPT),
            "--generated-at",
            "2026-07-06T00:00:00Z",
            "--json-out",
            str(json_out),
            "--markdown-out",
            str(md_out),
            "--oss-arena-rollup",
            str(rollup),
        ],
        cwd=REPO_ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    check("generator with oss rollup exits zero", proc.returncode, 0)
    report = json.loads(json_out.read_text(encoding="utf-8"))
    check("oss arena glm cell count", len(report["oss_glm52_arena_cells"]), 2)
    headline = report["rollups"]["glm52_by_corpus_treatment"]
    check("oss kitsoki includes bakeoff plus arena", headline["oss-oracle|kitsoki"]["attempted"], 2)
    check("oss kitsoki token total includes both", headline["oss-oracle|kitsoki"]["total_tokens"], 2893980)
    check("oss raw attempted from arena", headline["oss-oracle|raw-prompt"]["attempted"], 1)
    check("oss raw tokens from arena", headline["oss-oracle|raw-prompt"]["total_tokens"], 700)
    md = md_out.read_text(encoding="utf-8")
    check("markdown includes oss arena section", "Committed OSS GLM-5.2 Arena Cells" in md, True)
    check("markdown includes oss rollup input", "OSS arena GLM rollup" in md, True)

if failures:
    print("FAIL: glm52 bugswarm report")
    for failure in failures:
        print(f"  - {failure}")
    sys.exit(1)
print("PASS: glm52 bugswarm report generator (no LLM, no Docker)")
