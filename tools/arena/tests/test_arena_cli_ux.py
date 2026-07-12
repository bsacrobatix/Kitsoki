#!/usr/bin/env python3
"""No-LLM tests for arena's operator-facing CLI affordances."""

from __future__ import annotations

import json
import hashlib
import importlib.util
import subprocess
import sys
import tempfile
from datetime import datetime, timedelta, timezone
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


def corpus_lock(tasks: list[dict[str, str]]) -> dict:
    """Build a minimal but self-authenticating BugSwarm corpus lock fixture."""
    payload = {
        "schema": "arena_bugswarm_corpus_lock/v1",
        "status": "ready",
        "source": "verified-source.yaml",
        "source_sha256": "a" * 64,
        "selection": {"algorithm": "test", "learning_count": 1, "confirmation_count": 1, "repository_separated": True},
        "tasks": [
            {
                "id": task["id"], "split": task["split"], "repository": task["repository"],
                "image_digest": "repo@sha256:" + "b" * 64,
                "commits": {"failed": "c" * 40, "passed": "d" * 40},
                "verification": {"receipt": "verify.json", "receipt_sha256": "e" * 64, "red": True, "green": True},
                "public_task": {"text": "ticket", "sha256": "f" * 64},
                "hidden_oracle": {"reference": "oracle", "sha256": "0" * 64},
            }
            for task in tasks
        ],
    }
    payload["lock_sha256"] = hashlib.sha256(
        json.dumps(payload, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("utf-8")
    ).hexdigest()
    return payload


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
check("catalog ids", ids, [
    "codex-codeact",
    "kitsoki-mcp",
    "kitsoki-mcp-codeact",
    "raw-codex",
    "strict-mcp-codeact-decomposed",
    "strict-mcp-decomposed-fallback",
])
require("catalog names codeact surface", any(row["id"] == "codex-codeact" and row["action_surface"] == "kitsoki-codeact-mcp" for row in rows))
require("catalog names strict fallback surface", any(
    row["id"] == "strict-mcp-decomposed-fallback"
    and row["action_surface"] == "kitsoki-studio-mcp+codeact-decomposed-fallback"
    for row in rows
))

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
    corpus.write_text(json.dumps(corpus_lock([
        {"id": "kitsoki-exposed", "split": "learning", "repository": "example/exposed"},
        {"id": "bugswarm-heldout", "split": "confirmation", "repository": "example/heldout"},
    ])), encoding="utf-8")
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
    tampered_lock = corpus_lock([
        {"id": "kitsoki-exposed", "split": "learning", "repository": "example/exposed"},
        {"id": "bugswarm-heldout", "split": "confirmation", "repository": "example/heldout"},
    ])
    tampered_lock["tasks"][0]["image_digest"] = "forged"
    corpus.write_text(json.dumps(tampered_lock), encoding="utf-8")
    tampered = run("task-optimization", "validate", "--study", str(study))
    check("task study rejects tampered corpus lock", tampered.returncode, 1)
    require("tampered corpus error is explicit", "lock_sha256" in tampered.stderr)
    corpus.write_text(json.dumps(corpus_lock([
        {"id": "kitsoki-exposed", "split": "learning", "repository": "example/exposed"},
        {"id": "bugswarm-heldout", "split": "confirmation", "repository": "example/heldout"},
    ])), encoding="utf-8")
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

    # The campaign preflight resolves the actual native dry-run launch plan;
    # it no longer trusts an operator-authored requested/effective mapping.
    profile_config = Path(td) / "profiles.yaml"
    profile_config.write_text("""
harness_profiles:
  codex-mini:
    backend: codex
    model: gpt-5.4-mini
    models: [gpt-5.4-mini]
    effort: medium
    efforts: [medium]
  syn-gpt-oss-120b:
    backend: claude
    model: hf:openai/gpt-oss-120b
    models: [hf:openai/gpt-oss-120b]
""", encoding="utf-8")
    preflight_dir = Path(td) / "preflight"
    preflight = run("task-optimization", "preflight", "--study", str(study), "--config", str(profile_config), "--working-dir", str(REPO_ROOT), "--out", str(preflight_dir))
    check("task preflight exits 0", preflight.returncode, 0)
    preflight_json = json.loads((preflight_dir / "preflight.json").read_text(encoding="utf-8"))
    check("task preflight has effective receipts", {row["status"] for row in preflight_json["candidates"]}, {"ready"})
    require("task preflight has native launch plans", all(row.get("launch_plan", {}).get("dry_run") for row in preflight_json["candidates"]))
    require("task preflight reports redacted auth readiness", all(row.get("auth", {}).get("status") == "ambient-unverified" for row in preflight_json["candidates"]))
    ready_candidates = {row["candidate_id"]: row for row in preflight_json["candidates"] if row.get("status") == "ready"}
    check("task preflight readies every planned candidate", set(ready_candidates), {cell["candidate_id"] for cell in plan_json["cells"]})
    require("ready profile boundary has stable hashes", all(
        isinstance(row.get("profile_hash"), str) and isinstance(row.get("launch_plan_hash"), str)
        for row in ready_candidates.values()
    ))
    unsupported_config = Path(td) / "unsupported-profiles.yaml"
    unsupported_config.write_text("""
harness_profiles:
  codex-mini:
    backend: codex
    model: gpt-5.4-mini
    models: [gpt-5.4-mini]
    effort: medium
    efforts: [medium]
""", encoding="utf-8")
    unsupported_dir = Path(td) / "unsupported-preflight"
    unsupported = run("task-optimization", "preflight", "--study", str(study), "--config", str(unsupported_config), "--working-dir", str(REPO_ROOT), "--out", str(unsupported_dir))
    check("task preflight allows explicit unsupported profile", unsupported.returncode, 0)
    unsupported_json = json.loads((unsupported_dir / "preflight.json").read_text(encoding="utf-8"))
    check("task preflight emits unsupported profile", {row["candidate_id"]: row["status"] for row in unsupported_json["candidates"]}["oss"], "unsupported")
    model_mismatch_config = Path(td) / "model-mismatch.yaml"
    model_mismatch_config.write_text("""
harness_profiles:
  codex-mini:
    backend: codex
    model: gpt-5.4
    models: [gpt-5.4]
    effort: medium
    efforts: [medium]
""", encoding="utf-8")
    model_mismatch_dir = Path(td) / "model-mismatch-preflight"
    model_mismatch = run("task-optimization", "preflight", "--study", str(study), "--config", str(model_mismatch_config), "--working-dir", str(REPO_ROOT), "--out", str(model_mismatch_dir))
    check("task preflight rejects unavailable requested model", model_mismatch.returncode, 1)
    model_mismatch_json = json.loads((model_mismatch_dir / "preflight.json").read_text(encoding="utf-8"))
    require("task preflight preserves model mismatch reason", "fallback is forbidden" in {row["candidate_id"]: row.get("reason", "") for row in model_mismatch_json["candidates"]}["mini"])
    effort_mismatch_config = Path(td) / "effort-mismatch.yaml"
    effort_mismatch_config.write_text("""
harness_profiles:
  codex-mini:
    backend: codex
    model: gpt-5.4-mini
    models: [gpt-5.4-mini]
    effort: high
    efforts: [high]
""", encoding="utf-8")
    effort_mismatch_dir = Path(td) / "effort-mismatch-preflight"
    effort_mismatch = run("task-optimization", "preflight", "--study", str(study), "--config", str(effort_mismatch_config), "--working-dir", str(REPO_ROOT), "--out", str(effort_mismatch_dir))
    check("task preflight rejects unsupported effort", effort_mismatch.returncode, 1)
    effort_mismatch_json = json.loads((effort_mismatch_dir / "preflight.json").read_text(encoding="utf-8"))
    require("task preflight preserves effort mismatch reason", "effective_effort differs" in {row["candidate_id"]: row.get("reason", "") for row in effort_mismatch_json["candidates"]}["mini"])
    invalid_dir = Path(td) / "invalid-context-preflight"
    invalid_context = run("task-optimization", "preflight", "--study", str(study), "--config", str(profile_config), "--working-dir", str(Path(td) / "missing-context"), "--out", str(invalid_dir))
    check("task preflight rejects missing context", invalid_context.returncode, 1)
    invalid_context_json = json.loads((invalid_dir / "preflight.json").read_text(encoding="utf-8"))
    check("task preflight records invalid context", {row["status"] for row in invalid_context_json["candidates"]}, {"invalid"})

    attempts = Path(td) / "attempts"
    status_before = run("task-optimization", "status", "--plan", str(out / "plan.json"), "--preflight", str(preflight_dir / "preflight.json"), "--attempts", str(attempts), "--out", str(Path(td) / "status-before"))
    check("task status resumes unattempted cells", status_before.returncode, 0)
    before_json = json.loads((Path(td) / "status-before" / "status.json").read_text(encoding="utf-8"))
    check("task status starts learning", before_json["phase"], "learning")
    check("task status lists all resume cells", len(before_json["resume_cell_ids"]), 8)

    plan_digest = hashlib.sha256((json.dumps(plan_json, sort_keys=True, separators=(",", ":"), ensure_ascii=False) + "\n").encode("utf-8")).hexdigest()
    run_started = datetime.now(timezone.utc).replace(microsecond=0)
    run_finished = run_started + timedelta(seconds=60)
    produced_at = (run_started + timedelta(seconds=1)).isoformat().replace("+00:00", "Z")
    started_at = run_started.isoformat().replace("+00:00", "Z")
    finished_at = run_finished.isoformat().replace("+00:00", "Z")
    def sha256(path):
        return hashlib.sha256(path.read_bytes()).hexdigest()

    def evidence(path, attempt_id, *, passed=None):
        payload = {"path": str(path), "sha256": sha256(path), "attempt_id": attempt_id,
                   "produced_at": produced_at}
        if passed is not None:
            payload["passed"] = passed
        return payload

    for n, cell_data in enumerate(plan_json["cells"]):
        attempt_id = f"attempt-{n}"
        solved = cell_data["candidate_id"] == "mini"
        trace = Path(td) / f"{attempt_id}.jsonl"
        trace.write_text('{"event":"fixture"}\n', encoding="utf-8")
        oracle = Path(td) / f"{attempt_id}.oracle.json"
        oracle.write_text(json.dumps({"passed": solved}), encoding="utf-8")
        suite = Path(td) / f"{attempt_id}.suite.json"
        suite.write_text(json.dumps({"passed": solved}), encoding="utf-8")
        report = Path(td) / f"{attempt_id}.agentbench.json"
        report_metrics = {
            "input_tokens": 6 if solved else 12, "output_tokens": 2,
            "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0,
            "total_tokens": 8 if solved else 14, "cost_usd": 0.0,
            "wall_seconds": 1.5, "accounting_status": "complete",
            "agent_calls_started": 1, "agent_calls_finished": 1,
            "agent_calls_errored": 0, "agent_calls_in_flight": 0,
        }
        report.write_text(json.dumps({"trace": str(trace), "passed": solved,
                                      "outcome": "solved" if solved else "failed",
                                      "metrics": report_metrics}), encoding="utf-8")
        candidate = ready_candidates[cell_data["candidate_id"]]
        receipt_path = Path(td) / f"attempt-{n}.json"
        receipt_path.write_text(json.dumps({
            "schema": "task-optimization/attempt/v1", "study_id": "bugfix-codeact-v1", "attempt_id": attempt_id,
            "cell_id": cell_data["id"], "candidate_id": cell_data["candidate_id"], "plan_sha256": plan_digest, "preflight_sha256": preflight_json["preflight_sha256"],
            "status": "scored", "verdict": "solved" if solved else "failed",
            "metrics": {"input_tokens": report_metrics["input_tokens"], "output_tokens": report_metrics["output_tokens"],
                        "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0,
                        "total_tokens": report_metrics["total_tokens"], "cost_usd": 0.0, "wall_s": 1.5},
            "runtime": {"status": "exited", "exit_code": 0 if solved else 1, "runner_commit": "fixture",
                        "image_digest": "sha256:fixture", "started_at": started_at, "finished_at": finished_at},
            "boundary": {"profile_hash": candidate["profile_hash"], "launch_plan_hash": candidate["launch_plan_hash"],
                         "capability_hash": "sha256:fixture", "sandbox_kind": "fake", "sandbox_identity": "fixture"},
            "leakage": {"verdict": "clean", "checker": "fixture", "policy_hash": "sha256:fixture"},
            "artifacts": {"agentbench_report": evidence(report, attempt_id), "trace": evidence(trace, attempt_id),
                          "oracle": evidence(oracle, attempt_id, passed=solved), "suite": evidence(suite, attempt_id, passed=solved)},
            "score": {"schema": "task-optimization/score/v1", "agentbench_report_sha256": sha256(report), "trace_sha256": sha256(trace)},
        }), encoding="utf-8")
        recorded = run("task-optimization", "record", "--plan", str(out / "plan.json"), "--preflight", str(preflight_dir / "preflight.json"), "--attempts", str(attempts), "--receipt", str(receipt_path), "--out", str(attempts))
        check(f"task attempt {n} records", recorded.returncode, 0)
    champion_dir = Path(td) / "champion"
    selected = run("task-optimization", "select-champion", "--plan", str(out / "plan.json"), "--preflight", str(preflight_dir / "preflight.json"), "--attempts", str(attempts), "--out", str(champion_dir))
    check("task champion selects after learning", selected.returncode, 0)
    champion_json = json.loads((champion_dir / "champion.json").read_text(encoding="utf-8"))
    check("task champion uses learning scores", champion_json["candidate_id"], "mini")
    check("task champion freezes treatment", champion_json["treatment"], "raw-agent")
    analysis_dir = Path(td) / "analysis"
    analyzed = run("task-optimization", "analyze", "--plan", str(out / "plan.json"), "--preflight", str(preflight_dir / "preflight.json"), "--attempts", str(attempts), "--out", str(analysis_dir))
    check("task analysis is materialized", analyzed.returncode, 0)
    analysis_json = json.loads((analysis_dir / "analysis.json").read_text(encoding="utf-8"))
    check("task analysis exposes treatment arms", {(row["candidate_id"], row["treatment"]) for row in analysis_json["arms"]}, {("mini", "raw-agent"), ("mini", "strict-mcp-current"), ("oss", "raw-agent"), ("oss", "strict-mcp-current")})
    comparison_dir = Path(td) / "comparison"
    compared = run("task-optimization", "compare", "--plan", str(out / "plan.json"), "--preflight", str(preflight_dir / "preflight.json"), "--attempts", str(attempts), "--out", str(comparison_dir))
    check("task comparison is materialized", compared.returncode, 0)
    comparison_json = json.loads((comparison_dir / "comparison.json").read_text(encoding="utf-8"))
    check("task comparison has every arm pair", len(comparison_json["comparisons"]), 6)
    frozen = run("task-optimization", "freeze", "--plan", str(out / "plan.json"), "--preflight", str(preflight_dir / "preflight.json"), "--attempts", str(attempts), "--out", str(champion_dir))
    check("task freeze alias is immutable", frozen.returncode, 0)
    confirmation_dir = Path(td) / "confirmation"
    confirmed = run("task-optimization", "confirm", "--plan", str(out / "plan.json"), "--preflight", str(preflight_dir / "preflight.json"), "--attempts", str(attempts), "--champion", str(champion_dir / "champion.json"), "--out", str(confirmation_dir))
    check("task confirmation is materialized", confirmed.returncode, 0)
    confirmation_json = json.loads((confirmation_dir / "confirmation.json").read_text(encoding="utf-8"))
    check("task confirmation is complete", confirmation_json["complete"], True)
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
