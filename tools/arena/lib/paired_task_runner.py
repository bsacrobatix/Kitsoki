#!/usr/bin/env python3
"""Run one frozen paired-task arena cell.

The runner is intentionally small: it materializes the pinned task baseline,
optionally dispatches a live worker, then scores the candidate tree with the
task's frozen deterministic oracle. `--arm-only` never calls a model; `--live`
only calls Codex when ARENA_PAIRED_TASK_ENABLE_CODEX=1 is set.
"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any

try:
    import yaml  # type: ignore
except ModuleNotFoundError:
    sys.exit("paired_task_runner.py needs pyyaml")

KITSOKI_ROOT = Path(os.environ.get("KITSOKI_ROOT", "/workspace/kitsoki")).resolve()
CORPUS = KITSOKI_ROOT / "tools/arena/corpus/cost-bench.manifest.yaml"
BENCH = KITSOKI_ROOT / "tools/bugfix-bakeoff/external/bench.py"


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--task", required=True)
    parser.add_argument("--treatment", required=True)
    parser.add_argument("--target", required=True)
    parser.add_argument("--backend", default="")
    parser.add_argument("--model", default="")
    parser.add_argument("--effort", default="")
    parser.add_argument("--live", action="store_true")
    parser.add_argument("--arm-only", action="store_true")
    parser.add_argument("--work-root", default=os.environ.get("ARENA_PAIRED_TASK_WORK_ROOT", "/workspace/arena-paired-task"))
    args = parser.parse_args(argv)

    if args.live == args.arm_only:
        return emit(
            verdict="blocked",
            notes="exactly one of --live or --arm-only is required",
            exit_code=2,
        )

    task = load_task(args.task)
    if args.arm_only:
        return arm_task(task)
    return run_live(args, task)


def load_task(task_id: str) -> dict[str, Any]:
    corpus = yaml.safe_load(CORPUS.read_text(encoding="utf-8"))
    for task in corpus.get("tasks", []):
        if task.get("id") == task_id:
            return task
    sys.exit(f"unknown corpus task: {task_id}")


def arm_task(task: dict[str, Any]) -> int:
    oracle = task.get("oracle") or {}
    kind = oracle.get("kind")
    started = time.monotonic()
    if kind == "github_content":
        baseline = fetch_raw(oracle["repo"], task["baseline_sha"], oracle["file"])
        fixed = fetch_raw(oracle["repo"], task["fix_sha"], oracle["file"])
        red = baseline is None or oracle["required_text"] not in baseline
        green = fixed is not None and oracle["required_text"] in fixed
        return emit(
            verdict="armed" if red and green else "failed",
            cost_usd=0.0,
            tokens=0,
            wall_s=elapsed(started),
            evidence_refs=[str(CORPUS) + "#" + str(task["id"])],
            trace_ref="",
            notes=f"arm github_content red={red} green={green}",
        )
    if kind == "external_bakeoff":
        cmd = [
            "python3",
            str(BENCH),
            "verify",
            "--project",
            str(oracle["project"]),
            "--bug",
            str(oracle["bug"]),
        ]
        if str(oracle.get("project")) == "kitsoki":
            cmd.extend(["--repo-dir", str(KITSOKI_ROOT)])
        proc = subprocess.run(cmd, cwd=KITSOKI_ROOT, text=True, capture_output=True)
        return emit(
            verdict="armed" if proc.returncode == 0 else "failed",
            cost_usd=0.0,
            tokens=0,
            wall_s=elapsed(started),
            evidence_refs=[str(CORPUS) + "#" + str(task["id"])],
            trace_ref="",
            notes=first_line(proc.stdout + "\n" + proc.stderr),
            exit_code=0 if proc.returncode == 0 else 1,
        )
    return emit(verdict="blocked", notes=f"unknown oracle kind {kind!r}", exit_code=2)


def run_live(args: argparse.Namespace, task: dict[str, Any]) -> int:
    started = time.monotonic()
    work_root = Path(args.work_root)
    work_root.mkdir(parents=True, exist_ok=True)
    cell_id = safe_name(f"{task['id']}--{args.treatment}--{args.backend or 'backend'}--{args.model or 'model'}")
    trace_ref = str(work_root / f"{cell_id}.jsonl")
    tree = work_root / cell_id
    if tree.exists():
        shutil.rmtree(tree)

    materialize_baseline(task, tree)
    dispatch = dispatch_worker(args, task, tree, trace_ref)
    score = score_tree(task, tree)
    metrics = dispatch.get("metrics", {})
    cost_usd = metrics.get("cost_usd")
    if not isinstance(cost_usd, (int, float)):
        cost_usd = 0.0
    tokens = metrics.get("tokens")
    if not isinstance(tokens, int):
        tokens = 0
    verdict = score["verdict"]
    if dispatch.get("blocked"):
        verdict = "blocked"
    return emit(
        verdict=verdict,
        cost_usd=cost_usd,
        tokens=tokens,
        wall_s=elapsed(started),
        evidence_refs=[str(CORPUS) + "#" + str(task["id"]), score.get("evidence", "")],
        trace_ref=trace_ref,
        notes="; ".join(part for part in [dispatch.get("notes", ""), score.get("notes", "")] if part),
        exit_code=0,
    )


def materialize_baseline(task: dict[str, Any], tree: Path) -> None:
    oracle = task.get("oracle") or {}
    if oracle.get("kind") == "github_content":
        repo = str(oracle["repo"])
        run(["git", "init", "-q", str(tree)], cwd=KITSOKI_ROOT)
        run(["git", "remote", "add", "origin", f"https://github.com/{repo}.git"], cwd=tree)
        run(["git", "fetch", "-q", "--depth", "1", "origin", str(task["baseline_sha"])], cwd=tree)
        run(["git", "checkout", "-q", "FETCH_HEAD"], cwd=tree)
        return
    if oracle.get("kind") == "external_bakeoff":
        project = str(oracle["project"])
        bug = str(oracle["bug"])
        meta = json.loads(
            run(
                ["python3", str(BENCH), "meta", "--project", project, "--bug", bug],
                cwd=KITSOKI_ROOT,
                capture=True,
            ).stdout
        )
        repo = meta.get("repo") or "."
        if repo == ".":
            run(["git", "clone", "--local", "--no-checkout", "-q", str(KITSOKI_ROOT), str(tree)], cwd=KITSOKI_ROOT)
        else:
            run(["git", "clone", "-q", "--no-checkout", str(repo), str(tree)], cwd=KITSOKI_ROOT)
        run(["git", "checkout", "-q", str(meta["baseline_sha"])], cwd=tree)
        return
    raise SystemExit(f"cannot materialize oracle kind {oracle.get('kind')!r}")


def dispatch_worker(args: argparse.Namespace, task: dict[str, Any], tree: Path, trace_ref: str) -> dict[str, Any]:
    if args.backend == "synthetic":
        return {"notes": "synthetic backend: no live model call; baseline scored directly", "metrics": {"cost_usd": 0.0, "tokens": 0}}
    if args.backend != "codex":
        return {"blocked": True, "notes": f"unsupported live backend {args.backend!r}", "metrics": {"cost_usd": 0.0, "tokens": 0}}
    if os.environ.get("ARENA_PAIRED_TASK_ENABLE_CODEX") != "1":
        return {
            "blocked": True,
            "notes": "codex live dispatch disabled; set ARENA_PAIRED_TASK_ENABLE_CODEX=1 to spend",
            "metrics": {"cost_usd": 0.0, "tokens": 0},
        }
    prompt = build_prompt(args, task)
    cmd = [
        "codex",
        "exec",
        "--cd",
        str(tree),
        "--ask-for-approval",
        "never",
        "--sandbox",
        "danger-full-access",
    ]
    if args.model:
        cmd.extend(["--model", args.model])
    cmd.append(prompt)
    proc = subprocess.run(cmd, cwd=tree, text=True, capture_output=True, timeout=int(os.environ.get("ARENA_CODEX_TIMEOUT_S", "900")))
    Path(trace_ref).write_text(json.dumps({
        "cmd": redact_cmd(cmd),
        "returncode": proc.returncode,
        "stdout_tail": proc.stdout[-4000:],
        "stderr_tail": proc.stderr[-4000:],
    }, indent=2), encoding="utf-8")
    return {
        "blocked": proc.returncode != 0,
        "notes": f"codex exit={proc.returncode}: {first_line(proc.stdout + chr(10) + proc.stderr)}",
        "metrics": {"cost_usd": 0.0, "tokens": 0},
    }


def build_prompt(args: argparse.Namespace, task: dict[str, Any]) -> str:
    return "\n".join([
        "You are fixing one benchmark task in a checked-out repository baseline.",
        "Do not ask questions. Make the smallest correct change and run focused checks if obvious.",
        f"Task id: {task['id']}",
        f"Archetype: {task['archetype']}",
        f"Treatment: {args.treatment}",
        "",
        str(task.get("ticket", "")),
    ])


def score_tree(task: dict[str, Any], tree: Path) -> dict[str, str]:
    oracle = task.get("oracle") or {}
    if oracle.get("kind") == "github_content":
        target = tree / str(oracle["file"])
        text = target.read_text(encoding="utf-8", errors="replace") if target.exists() else ""
        green = str(oracle["required_text"]) in text
        return {
            "verdict": "solved" if green else "failed",
            "evidence": str(target),
            "notes": f"github_content oracle green={green}",
        }
    if oracle.get("kind") == "external_bakeoff":
        out = Path(tempfile.mkdtemp(prefix="arena-score-")) / "cell.json"
        cmd = [
            "python3",
            str(BENCH),
            "score",
            "--project",
            str(oracle["project"]),
            "--bug",
            str(oracle["bug"]),
            "--tree",
            str(tree),
            "--out",
            str(out),
            "--candidate",
            "paired-task",
            "--treatment",
            "paired-task",
        ]
        proc = subprocess.run(cmd, cwd=KITSOKI_ROOT, text=True, capture_output=True)
        verdict = "failed"
        notes = first_line(proc.stdout + "\n" + proc.stderr)
        if out.exists():
            cell = json.loads(out.read_text(encoding="utf-8"))
            verdict = str(((cell.get("outcome") or {}).get("quality")) or verdict)
            notes = notes or json.dumps(cell.get("outcome", {}), sort_keys=True)
        return {"verdict": verdict, "evidence": str(out), "notes": notes}
    return {"verdict": "blocked", "evidence": "", "notes": f"unknown oracle kind {oracle.get('kind')!r}"}


def fetch_raw(repo: str, sha: str, file_path: str) -> str | None:
    url = f"https://raw.githubusercontent.com/{repo}/{sha}/{urllib.parse.quote(file_path)}"
    req = urllib.request.Request(url, headers={"User-Agent": "kitsoki-paired-task-runner"})
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            return resp.read().decode("utf-8", errors="replace")
    except Exception:
        return None


def run(cmd: list[str], *, cwd: Path, capture: bool = False) -> subprocess.CompletedProcess[str]:
    proc = subprocess.run(cmd, cwd=cwd, text=True, capture_output=True)
    if proc.returncode != 0:
        sys.stderr.write(proc.stdout[-2000:] + proc.stderr[-2000:])
        raise SystemExit(f"command failed: {' '.join(cmd)}")
    return proc


def emit(
    *,
    verdict: str,
    cost_usd: float | None = None,
    tokens: int | None = None,
    wall_s: float | None = None,
    evidence_refs: list[str] | None = None,
    trace_ref: str = "",
    notes: str = "",
    exit_code: int = 0,
) -> int:
    payload = {
        "verdict": verdict,
        "cost_usd": cost_usd,
        "tokens": tokens,
        "wall_s": wall_s,
        "evidence_refs": [ref for ref in (evidence_refs or []) if ref],
        "trace_ref": trace_ref,
        "notes": notes,
    }
    print(json.dumps(payload, sort_keys=True))
    return exit_code


def elapsed(started: float) -> float:
    return round(time.monotonic() - started, 3)


def first_line(blob: str) -> str:
    for line in blob.splitlines():
        line = line.strip()
        if line:
            return line[:300]
    return ""


def safe_name(value: str) -> str:
    return "".join(ch if ch.isalnum() or ch in "._-" else "-" for ch in value)[:180]


def redact_cmd(cmd: list[str]) -> list[str]:
    # Prompt text can include task details; keep command shape without bloating trace.
    if cmd:
        return cmd[:-1] + ["<prompt>"]
    return cmd


if __name__ == "__main__":
    raise SystemExit(main())
