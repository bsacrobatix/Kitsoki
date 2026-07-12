#!/usr/bin/env python3
"""No-LLM tests for arena's operator-facing CLI affordances."""

from __future__ import annotations

import json
import hashlib
import importlib.util
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
ARENA = HERE.parent / "arena.py"
TASK_OPTIMIZATION_PLAN_DECK = HERE.parent / "scripts" / "task_optimization_plan_deck.go"
REPO_ROOT = HERE.parent.parent.parent
sys.path.insert(0, str(HERE.parent))

failures: list[str] = []


def check(label: str, got, want) -> None:
    if got != want:
        failures.append(f"{label}: got {got!r}, want {want!r}")


def require(label: str, condition: bool) -> None:
    if not condition:
        failures.append(label)


def run(*argv: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        [sys.executable, str(ARENA), *argv],
        cwd=REPO_ROOT,
        text=True,
        capture_output=True,
    )


catalog = run("treatments", "--json")
check("treatments exits 0", catalog.returncode, 0)
rows = json.loads(catalog.stdout)
ids = sorted(row["id"] for row in rows)
check("catalog ids", ids, ["codex-codeact", "kitsoki-mcp", "kitsoki-mcp-codeact", "raw-codex"])
require("catalog names codeact surface", any(row["id"] == "codex-codeact" and row["action_surface"] == "kitsoki-codeact-mcp" for row in rows))

valid = run("validate", "--spec", "tools/arena/specs/codex-codeact-action-surface.yaml")
check("valid spec exits 0", valid.returncode, 0)
require("valid spec warns about live gate", "WARN:" in valid.stdout and "ARENA_PAIRED_TASK_ENABLE_CODEX" in valid.stdout)

with tempfile.TemporaryDirectory(prefix="arena-cli-") as td:
    bad = Path(td) / "bad-codeact.yaml"
    bad.write_text(
        """
job_type: paired-task
targets:
  - id: t
variants:
  - id: bad-codeact
    treatment: codex-codeact
    backend: codex
    model: gpt-5.5
    agent: kitsoki-mcp-driver
axes:
  task: [query-string-qs1-bugfix-test-repair]
options:
  capability_presets:
    repo_patch:
      fs: {read: ["**"], write: ["**"], max_bytes: 1048576}
      vcs: read
""",
        encoding="utf-8",
    )
    invalid = run("validate", "--spec", str(bad))
    check("invalid spec exits 1", invalid.returncode, 1)
    require("wrong agent rejected", "kitsoki-codeact-driver" in invalid.stderr)

    corpus = Path(td) / "corpus.lock.json"
    corpus.write_text(json.dumps({"tasks": [
        {"id": "kitsoki-exposed", "split": "learning", "task_sha256": "a"},
        {"id": "bugswarm-heldout", "split": "confirmation", "task_sha256": "b"},
    ]}), encoding="utf-8")
    study = Path(td) / "study.yaml"
    study.write_text(
        """
schema: task-optimization/v1
study_id: bugfix-codeact-v1
boundary: stories/bugfix
corpus_lock: corpus.lock.json
splits: {learning: exposed-plus-bugswarm, confirmation: bugswarm-holdout}
candidates:
  - {key: mini, profile: codex-mini, requested_model: gpt-5.4-mini, requested_effort: medium}
  - {key: oss, profile: syn-gpt-oss-120b, requested_model: hf:openai/gpt-oss-120b, requested_effort: n/a}
treatments: [raw-agent, strict-mcp-current]
repeats: {screening: 1, decision_boundary: 3}
stop: {max_versions: 4}
live_gate_env: KITSOKI_TASK_OPT_LIVE
""", encoding="utf-8")
    study_valid = run("task-optimization", "validate", "--study", str(study))
    check("task study validates", study_valid.returncode, 0)
    out = Path(td) / "out"
    plan = run("task-optimization", "plan", "--study", str(study), "--out", str(out))
    check("task plan exits 0", plan.returncode, 0)
    plan_json = json.loads((out / "plan.json").read_text(encoding="utf-8"))
    check("task plan has task x candidate x treatment cells", plan_json["cell_count"], 8)
    check("task plan starts planned", {cell["status"] for cell in plan_json["cells"]}, {"planned"})
    deck_path = out / "plan-deck.slidey.json"
    deck = subprocess.run(
        ["go", "run", str(TASK_OPTIMIZATION_PLAN_DECK), "--plan", str(out / "plan.json"), "--out", str(deck_path)],
        cwd=REPO_ROOT, text=True, capture_output=True,
    )
    check("task plan deck exits 0", deck.returncode, 0)
    deck_json = json.loads(deck_path.read_text(encoding="utf-8"))
    check("task plan deck title", deck_json["meta"]["title"], "Task optimization plan: bugfix-codeact-v1")
    check("task plan deck scene count", len(deck_json["scenes"]), 5)
    require("task plan deck declares no-provider source", "no provider" in deck_json["_comment"].lower())
    lock = json.loads((out / "study.lock.json").read_text(encoding="utf-8"))
    require("task lock pins study hash", bool(lock["study_manifest_sha256"]))
    require("task lock pins corpus hash", bool(lock["corpus_lock_sha256"]))
    arm_without_gate = run("task-optimization", "arm", "--study", str(study), "--out", str(out), "--live")
    check("task arm refuses without environment gate", arm_without_gate.returncode, 1)
    require("task arm calls no provider when gated", "no provider was called" in arm_without_gate.stderr)

    resolutions = Path(td) / "resolutions.json"
    resolutions.write_text(json.dumps({
        "mini": {"effective_model": "gpt-5.4-mini", "effective_effort": "medium", "provider": "openai", "backend": "codex", "profile_hash": "mini-profile", "launch_plan_hash": "mini-launch"},
        "oss": {"effective_model": "hf:openai/gpt-oss-120b", "effective_effort": "n/a", "provider": "synthetic", "backend": "openai", "profile_hash": "oss-profile", "launch_plan_hash": "oss-launch"},
    }), encoding="utf-8")
    preflight_dir = Path(td) / "preflight"
    preflight = run("task-optimization", "preflight", "--study", str(study), "--resolutions", str(resolutions), "--out", str(preflight_dir))
    check("task preflight exits 0", preflight.returncode, 0)
    preflight_json = json.loads((preflight_dir / "preflight.json").read_text(encoding="utf-8"))
    check("task preflight has effective receipts", {row["status"] for row in preflight_json["candidates"]}, {"ready"})

    attempts = Path(td) / "attempts"
    status_before = run("task-optimization", "status", "--plan", str(out / "plan.json"), "--preflight", str(preflight_dir / "preflight.json"), "--attempts", str(attempts), "--out", str(Path(td) / "status-before"))
    check("task status resumes unattempted cells", status_before.returncode, 0)
    before_json = json.loads((Path(td) / "status-before" / "status.json").read_text(encoding="utf-8"))
    check("task status starts learning", before_json["phase"], "learning")
    check("task status lists all resume cells", len(before_json["resume_cell_ids"]), 8)

    plan_digest = hashlib.sha256((json.dumps(plan_json, sort_keys=True, separators=(",", ":"), ensure_ascii=False) + "\n").encode("utf-8")).hexdigest()
    for n, cell_data in enumerate(plan_json["cells"]):
        receipt_path = Path(td) / f"attempt-{n}.json"
        receipt_path.write_text(json.dumps({
            "schema": "task-optimization/attempt/v1", "study_id": "bugfix-codeact-v1", "attempt_id": f"attempt-{n}",
            "cell_id": cell_data["id"], "candidate_id": cell_data["candidate_id"], "plan_sha256": plan_digest, "preflight_sha256": preflight_json["preflight_sha256"],
            "status": "scored", "verdict": "solved" if cell_data["candidate_id"] == "mini" else "failed",
            "metrics": {"total_tokens": 10 if cell_data["candidate_id"] == "mini" else 20},
        }), encoding="utf-8")
        recorded = run("task-optimization", "record", "--plan", str(out / "plan.json"), "--preflight", str(preflight_dir / "preflight.json"), "--attempts", str(attempts), "--receipt", str(receipt_path), "--out", str(attempts))
        check(f"task attempt {n} records", recorded.returncode, 0)
    champion_dir = Path(td) / "champion"
    selected = run("task-optimization", "select-champion", "--plan", str(out / "plan.json"), "--preflight", str(preflight_dir / "preflight.json"), "--attempts", str(attempts), "--out", str(champion_dir))
    check("task champion selects after learning", selected.returncode, 0)
    champion_json = json.loads((champion_dir / "champion.json").read_text(encoding="utf-8"))
    check("task champion uses learning scores", champion_json["candidate_id"], "mini")
    status_after = run("task-optimization", "status", "--plan", str(out / "plan.json"), "--preflight", str(preflight_dir / "preflight.json"), "--attempts", str(attempts), "--champion", str(champion_dir / "champion.json"), "--out", str(Path(td) / "status-after"))
    check("task status reaches complete after confirmation", status_after.returncode, 0)
    after_json = json.loads((Path(td) / "status-after" / "status.json").read_text(encoding="utf-8"))
    check("task status completes confirmed campaign", after_json["phase"], "complete")

    mismatched = Path(td) / "mismatched-attempt.json"
    mismatched.write_text(json.dumps({"schema": "task-optimization/attempt/v1", "study_id": "bugfix-codeact-v1", "attempt_id": "wrong-lock", "cell_id": plan_json["cells"][0]["id"], "candidate_id": plan_json["cells"][0]["candidate_id"], "plan_sha256": "wrong", "preflight_sha256": preflight_json["preflight_sha256"], "status": "scored"}), encoding="utf-8")
    rejected = run("task-optimization", "record", "--plan", str(out / "plan.json"), "--preflight", str(preflight_dir / "preflight.json"), "--attempts", str(attempts), "--receipt", str(mismatched), "--out", str(attempts))
    check("task record rejects altered plan", rejected.returncode, 1)

from arena.model import JobSpec  # noqa: E402
from arena.plugins import base as plugins  # noqa: E402

spec = JobSpec.from_dict({
    "job_type": "paired-task",
    "targets": [{"id": "target"}],
    "variants": [{"id": "raw", "treatment": "raw-codex"}],
    "axes": {"task": ["task"]},
})
cell = spec.cells()[0]
result = plugins.get("paired-task").score(
    cell,
    exit_code=1,
    stdout="",
    stderr='Failed to initialize: unable to resolve docker endpoint: context "desktop-linux": context not found',
)
check("docker context verdict", result.verdict, "blocked")
check("docker context health", result.health, "infra:harness")

result = plugins.get("paired-task").score(
    cell,
    exit_code=1,
    stdout="",
    stderr=(
        "docker: Error response from daemon: Sign in to continue using Docker Desktop. "
        "Membership in the [acronis] organization is required."
    ),
)
check("docker desktop sign-in verdict", result.verdict, "blocked")
check("docker desktop sign-in health", result.health, "infra:harness")

result = plugins.get("paired-task").score(cell, exit_code=0, stdout='{"verdict":"unsupported"}', stderr="")
check("unsupported is preserved", result.verdict, "unsupported")

spec_mod = importlib.util.spec_from_file_location("arena_cli", ARENA)
assert spec_mod and spec_mod.loader
arena_cli = importlib.util.module_from_spec(spec_mod)
spec_mod.loader.exec_module(arena_cli)


def fake_docker_run(cmd, *, text, capture_output, timeout):  # noqa: ANN001 - mirrors subprocess.run.
    del text, capture_output, timeout
    if cmd[:2] == ["docker", "version"]:
        return subprocess.CompletedProcess(cmd, 0, stdout="Docker version ok\n", stderr="")
    if cmd[:2] == ["docker", "ps"]:
        return subprocess.CompletedProcess(
            cmd,
            1,
            stdout="",
            stderr=(
                "Error response from daemon: Sign in to continue using Docker Desktop. "
                "Membership in the [acronis] organization is required."
            ),
        )
    if cmd[:3] == ["docker", "context", "ls"]:
        return subprocess.CompletedProcess(cmd, 0, stdout="NAME DESCRIPTION DOCKER ENDPOINT\n", stderr="")
    raise AssertionError(f"unexpected docker probe: {cmd}")


old_run = arena_cli.subprocess.run
try:
    arena_cli.subprocess.run = fake_docker_run
    doctor_error = arena_cli._check_docker()
finally:
    arena_cli.subprocess.run = old_run
require("doctor probes docker ps", "docker container API failed" in doctor_error)
require("doctor surfaces Docker Desktop sign-in", "Sign in to continue using Docker Desktop" in doctor_error)

if failures:
    print("FAIL: arena CLI UX")
    for f in failures:
        print(f"  - {f}")
    raise SystemExit(1)
print("PASS: arena CLI UX (catalog, validate, infra classification; no LLM)")
