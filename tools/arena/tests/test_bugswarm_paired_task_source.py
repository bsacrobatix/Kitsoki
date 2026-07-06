#!/usr/bin/env python3
"""No-LLM/no-Docker tests for scheduling verified BugSwarm sources."""

from __future__ import annotations

import json
import os
import runpy
import subprocess
import sys
import tempfile
from pathlib import Path

import yaml  # type: ignore

HERE = Path(__file__).resolve().parent
ARENA_ROOT = HERE.parent
REPO_ROOT = ARENA_ROOT.parent.parent
CONVERT = REPO_ROOT / "tools/arena/scripts/bugswarm_to_arena.py"
APPLY = REPO_ROOT / "tools/arena/scripts/bugswarm_apply_verification.py"
GEN_SPEC = REPO_ROOT / "tools/arena/scripts/bugswarm_to_arena_spec.py"
RUNNER = REPO_ROOT / "tools/arena/lib/paired_task_runner.py"

sys.path.insert(0, str(ARENA_ROOT))
from arena.model import JobSpec  # noqa: E402
from arena.plugins import base as plugins  # noqa: E402

failures: list[str] = []


def check(label: str, got, want) -> None:
    if got != want:
        failures.append(f"{label}: got {got!r}, want {want!r}")


def require(label: str, condition: bool) -> None:
    if not condition:
        failures.append(label)


with tempfile.TemporaryDirectory() as tmp:
    tmpdir = Path(tmp)
    artifacts = tmpdir / "artifacts.json"
    source = tmpdir / "source.yaml"
    verification = tmpdir / "verification.json"
    verified_source = tmpdir / "verified.yaml"
    spec_path = tmpdir / "bugswarm-paired-task.yaml"

    artifacts.write_text(json.dumps({
        "artifacts": [
            {
                "image_tag": "square-okio-140452393",
                "repo": "square/okio",
                "failed_job_id": "140452393",
                "passed_job_id": "140452394",
            },
            {
                "image_tag": "square-okhttp-200000001",
                "repo": "square/okhttp",
                "failed_job_id": "200000001",
                "passed_job_id": "200000002",
            },
        ]
    }), encoding="utf-8")
    convert = subprocess.run(
        [sys.executable, str(CONVERT), "--in", str(artifacts), "--out", str(source)],
        cwd=REPO_ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    check("converter exits zero", convert.returncode, 0)
    verification.write_text(json.dumps({
        "kind": "arena_bugswarm_verification",
        "version": 1,
        "mode": "execute",
        "task_count": 2,
        "verified_count": 1,
        "results": [
            {
                "task_id": "bugswarm-square-okio-140452393",
                "image_tag": "square-okio-140452393",
                "verified_red": True,
                "verified_green": True,
                "failed_exit_code": 1,
                "passed_exit_code": 0,
            },
            {
                "task_id": "bugswarm-square-okhttp-200000001",
                "image_tag": "square-okhttp-200000001",
                "verified_red": False,
                "verified_green": False,
                "failed_exit_code": 0,
                "passed_exit_code": 0,
            },
        ],
    }), encoding="utf-8")
    apply = subprocess.run(
        [sys.executable, str(APPLY), "--source", str(source), "--verification", str(verification), "--out", str(verified_source)],
        cwd=REPO_ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    check("apply exits zero", apply.returncode, 0)

    generate = subprocess.run(
        [sys.executable, str(GEN_SPEC), "--source", str(verified_source), "--out", str(spec_path)],
        cwd=REPO_ROOT,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    check("spec generator exits zero", generate.returncode, 0)
    spec_payload = yaml.safe_load(spec_path.read_text(encoding="utf-8"))
    check("job type", spec_payload["job_type"], "paired-task")
    check("target corpus threaded", spec_payload["targets"][0]["corpus"], str(verified_source))
    check("verified-only task axis", spec_payload["axes"]["task"], ["bugswarm-square-okio-140452393"])
    check("variant treatments", [v["treatment"] for v in spec_payload["variants"]], ["kitsoki", "single-briefed"])
    check("candidate model", spec_payload["variants"][0]["model"], "glm-5.2")
    old_kitsoki_root = os.environ.get("KITSOKI_ROOT")
    os.environ["KITSOKI_ROOT"] = str(REPO_ROOT)
    try:
        runner_globals = runpy.run_path(str(RUNNER), run_name="paired_task_runner_test")
    finally:
        if old_kitsoki_root is None:
            os.environ.pop("KITSOKI_ROOT", None)
        else:
            os.environ["KITSOKI_ROOT"] = old_kitsoki_root
    check("glm profile mapping", runner_globals["MODEL_TO_PROFILE"].get("glm-5.2"), "synthetic-claude")

    spec = JobSpec.load(spec_path)
    cells = spec.cells()
    check("cell count", len(cells), 2)
    plugin = plugins.get("paired-task")
    argv = plugin.drive_command(cells[0], live=False)
    require("plugin passes corpus flag", "--corpus" in argv)
    check("plugin corpus value", argv[argv.index("--corpus") + 1], str(verified_source))
    check("plugin arm-only remains last", argv[-1], "--arm-only")
    relative_payload = dict(spec_payload)
    relative_payload["targets"] = [dict(spec_payload["targets"][0], corpus=".artifacts/bugswarm/verified-source.yaml")]
    relative_cell = JobSpec.from_dict(relative_payload).cells()[0]
    relative_argv = plugin.drive_command(relative_cell, live=False)
    check(
        "plugin maps relative corpus to container repo",
        relative_argv[relative_argv.index("--corpus") + 1],
        "/workspace/kitsoki/.artifacts/bugswarm/verified-source.yaml",
    )

    env = dict(os.environ)
    env["KITSOKI_ROOT"] = str(REPO_ROOT)
    arm = subprocess.run(
        [
            sys.executable,
            str(RUNNER),
            "--task",
            "bugswarm-square-okio-140452393",
            "--treatment",
            "kitsoki",
            "--target",
            "bugswarm-verified",
            "--corpus",
            str(verified_source),
            "--arm-only",
        ],
        cwd=REPO_ROOT,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    check("runner arm exits zero", arm.returncode, 0)
    payload = json.loads(arm.stdout.splitlines()[-1])
    check("runner verdict", payload["verdict"], "armed")
    check("runner tokens", payload["tokens"], 0)
    check("runner evidence", payload["evidence_refs"], [str(verified_source) + "#bugswarm-square-okio-140452393"])
    require("runner notes identify BugSwarm arm", "bugswarm_fail_pass_pair" in payload["notes"])

    unverified = subprocess.run(
        [
            sys.executable,
            str(RUNNER),
            "--task",
            "bugswarm-square-okhttp-200000001",
            "--treatment",
            "kitsoki",
            "--target",
            "bugswarm-verified",
            "--corpus",
            str(verified_source),
            "--arm-only",
        ],
        cwd=REPO_ROOT,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    check("unverified runner exits nonzero", unverified.returncode, 1)
    unverified_payload = json.loads(unverified.stdout.splitlines()[-1])
    check("unverified runner verdict", unverified_payload["verdict"], "failed")
    require("unverified notes include green=false", "green=False" in unverified_payload["notes"])

if failures:
    print("FAIL: BugSwarm paired-task source")
    for failure in failures:
        print(f"  - {failure}")
    sys.exit(1)
print("PASS: BugSwarm paired-task source scheduling (no LLM, no Docker)")
