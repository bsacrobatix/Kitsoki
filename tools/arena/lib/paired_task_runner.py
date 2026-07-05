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
DRIVE_SH = KITSOKI_ROOT / "tools/mcp-drive/drive.sh"
BENCH_BUGFIX_STORY = KITSOKI_ROOT / "stories/bench-bugfix/app.yaml"

sys.path.insert(0, str(KITSOKI_ROOT / "tools/session-mining"))
from pricing import price_for  # noqa: E402  (path set above; single price table for the repo)

# Paired-task's `--model` axis names a WORKER model the same way
# bugfix-bakeoff/external/candidates.yaml does; this is the one place that
# maps that model name to the kitsoki harness `profile` bench-bugfix needs, so
# the kitsoki arm and the single-briefed arm use the IDENTICAL worker model —
# only the process (kitsoki pipeline vs raw codex exec) differs.
MODEL_TO_PROFILE = {
    "gpt-5.5": "codex-native",
}


def blended_cost_usd(model: str, tokens: int) -> tuple[float, bool]:
    """Rough USD cost from a TOTAL token count only.

    codex exec's summary reports one combined "tokens used" figure with no
    input/output split, so this can't be the exact per-bucket price_for()/
    message_cost() computation the mining tools use on real usage blocks.
    Blend input+output at 1:1 as a documented approximation; is_exact always
    false here regardless of the model's own is_estimate flag.
    """
    price, _ = price_for(model)
    blended_rate = (price.input + price.output) / 2
    return round(tokens * blended_rate / 1e6, 6), False


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
    parser.add_argument("--work-root", default=os.environ.get("ARENA_PAIRED_TASK_WORK_ROOT", "/workspace/kitsoki/.artifacts/arena/paired-task-work"))
    parser.add_argument(
        "--keep-workdir",
        action="store_true",
        default=os.environ.get("ARENA_PAIRED_TASK_KEEP_WORKDIR") == "1",
        help="keep the per-cell checkout under --work-root for debugging; default removes it after scoring",
    )
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
    notes = "; ".join(part for part in [dispatch.get("notes", ""), score.get("notes", "")] if part)
    if not args.keep_workdir:
        cleanup_cell_workdir(tree)
        notes = "; ".join(part for part in [notes, f"removed scratch workdir {container_path(str(tree))}"] if part)
    return emit(
        verdict=verdict,
        cost_usd=cost_usd,
        tokens=tokens,
        wall_s=elapsed(started),
        evidence_refs=[str(CORPUS) + "#" + str(task["id"]), score.get("evidence", "")],
        trace_ref=container_path(trace_ref),
        notes=notes,
        exit_code=0,
    )


def cleanup_cell_workdir(tree: Path) -> None:
    if not tree.exists():
        return
    for root, dirs, files in os.walk(tree):
        for name in dirs:
            if (Path(root) / name).is_symlink():
                continue
            try:
                os.chmod(Path(root) / name, 0o700)
            except OSError:
                pass
        for name in files:
            if (Path(root) / name).is_symlink():
                continue
            try:
                os.chmod(Path(root) / name, 0o600)
            except OSError:
                pass
    shutil.rmtree(tree, onerror=remove_readonly)


def remove_readonly(func: Any, path: str, _exc_info: Any) -> None:
    os.chmod(path, 0o700)
    func(path)


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
            # --no-hardlinks: inside the paired-task container, KITSOKI_ROOT is a
            # read-only bind mount and `tree` lives on the container's own
            # filesystem (e.g. /tmp) — a different device, so --local's default
            # hardlinking fails with "Invalid cross-device link". Force copies.
            run(
                ["git", "clone", "--local", "--no-hardlinks", "--no-checkout", "-q", str(KITSOKI_ROOT), str(tree)],
                cwd=KITSOKI_ROOT,
            )
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
    if args.treatment == "kitsoki":
        return dispatch_kitsoki(args, task, tree, trace_ref)
    return dispatch_single_prompt(args, task, tree, trace_ref)


def dispatch_single_prompt(args: argparse.Namespace, task: dict[str, Any], tree: Path, trace_ref: str) -> dict[str, Any]:
    """The naive baseline: one raw `codex exec` call, no kitsoki pipeline at all."""
    prompt = build_prompt(args, task)
    cmd = [
        "codex",
        "exec",
        "-C",
        str(tree),
        "--skip-git-repo-check",
        "--dangerously-bypass-approvals-and-sandbox",
        "-s",
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
    tokens = parse_tokens(proc.stdout + "\n" + proc.stderr)
    cost_usd, cost_exact = blended_cost_usd(args.model or "gpt-5.5", tokens)
    return {
        "blocked": proc.returncode != 0,
        "notes": f"codex exit={proc.returncode}: {first_line(proc.stdout + chr(10) + proc.stderr)}"
        + (f"; cost_usd={cost_usd} (blended estimate, not exact)" if not cost_exact else ""),
        "metrics": {"cost_usd": cost_usd, "tokens": tokens},
    }


def _orchestrator_backend_for(model: str) -> str:
    """Mirror drive.sh's own auto-detect: gpt-*/codex*/o3*/o4* -> codex, else
    claude. Kept in sync manually (drive.sh has no --print-backend to query)
    so dispatch_kitsoki's prompt tool-name spelling (see build_kitsoki_prompt)
    matches whichever backend MCP_DRIVE_BACKEND will actually select."""
    lowered = model.lower()
    if lowered.startswith(("gpt-", "codex", "o3", "o4")):
        return "codex"
    return "claude"


def dispatch_kitsoki(args: argparse.Namespace, task: dict[str, Any], tree: Path, trace_ref: str) -> dict[str, Any]:
    """Drive the REAL kitsoki bugfix pipeline (stories/bench-bugfix/app.yaml) via
    a headless codex orchestrator + the studio MCP — the same live-delegation
    primitive tools/bugfix-bakeoff/external/drive_cell.sh already proved out for
    the internal bake-off. `trace_ref` becomes the kitsoki session's own trace
    (session_new {trace: ...}), so cost/tokens below are read off REAL recorded
    agent-call usage, not a codex stdout summary line."""
    model = args.model or "gpt-5.5"
    profile = MODEL_TO_PROFILE.get(model)
    if not profile:
        return {
            "blocked": True,
            "notes": f"no kitsoki harness profile mapped for model {model!r}; add one to MODEL_TO_PROFILE",
            "metrics": {"cost_usd": 0.0, "tokens": 0},
        }
    try:
        bin_dir = ensure_kitsoki_binary()
        branch = f"paired-task-{safe_name(task['id'])}"
        run(["git", "checkout", "-q", "-B", branch], cwd=tree)
        test_cmd = test_cmd_for(task)
    except (Exception, SystemExit) as exc:  # noqa: BLE001 - report, don't crash the sweep
        return {
            "blocked": True,
            "notes": f"kitsoki dispatch setup failed: {exc}",
            "metrics": {"cost_usd": 0.0, "tokens": 0},
        }

    orchestrator_model = os.environ.get("ARENA_PAIRED_TASK_ORCHESTRATOR_MODEL", "gpt-5.5")
    orchestrator_backend = os.environ.get("ARENA_PAIRED_TASK_ORCHESTRATOR_BACKEND") or _orchestrator_backend_for(orchestrator_model)

    thread = Path(trace_ref).with_suffix(".thread.md")
    prompt = build_kitsoki_prompt(args, task, tree, trace_ref, thread, profile, branch, test_cmd, orchestrator_backend)
    prompt_file = Path(trace_ref).with_suffix(".prompt.md")
    prompt_file.write_text(prompt, encoding="utf-8")

    env = dict(os.environ)
    env["PATH"] = f"{bin_dir}:{env.get('PATH', '')}"
    env["MCP_DRIVE_BACKEND"] = orchestrator_backend
    env["MCP_DRIVE_MODEL"] = orchestrator_model
    # codex does not forward the parent env to the MCP servers it spawns; the
    # kitsoki MCP process forks the WORKER (codex-native) and needs the same
    # ChatGPT-subscription auth the orchestrator itself is using, AND the same
    # PATH (verified live: without PATH forwarded, the worker-side codex
    # backend's `exec.LookPath("codex")` fails inside the spawned kitsoki MCP
    # subprocess even though codex is on PATH for THIS process — internal/host's
    # ResolveBin then reported "codex binary not found", proving the pipeline's
    # own harness-profile routing was correct; only env propagation was missing).
    env["MCP_DRIVE_FORWARD_ENV"] = "CODEX_HOME,HOME,PATH"

    cmd = [str(DRIVE_SH), "--prompt-file", str(prompt_file)]
    try:
        proc = subprocess.run(
            cmd,
            cwd=KITSOKI_ROOT,
            text=True,
            capture_output=True,
            env=env,
            timeout=int(os.environ.get("ARENA_CODEX_TIMEOUT_S", "1800")),
        )
    except subprocess.TimeoutExpired as exc:
        return {
            "blocked": True,
            "notes": f"drive.sh timed out after {exc.timeout}s",
            "metrics": {"cost_usd": 0.0, "tokens": 0},
        }

    drive_log = Path(trace_ref).with_suffix(".drive-log.json")
    drive_log.write_text(json.dumps({
        "cmd": redact_cmd(cmd),
        "returncode": proc.returncode,
        "stdout_tail": proc.stdout[-4000:],
        "stderr_tail": proc.stderr[-4000:],
    }, indent=2), encoding="utf-8")

    try:
        metrics = real_trace_metrics(trace_ref, model)
    except (Exception, SystemExit) as exc:  # noqa: BLE001 - trace may be absent on an early failure
        metrics = {"cost_usd": 0.0, "tokens": 0, "cost_note": f"trace metrics unavailable: {exc}"}
    notes = f"drive.sh exit={proc.returncode}: {first_line(proc.stdout + chr(10) + proc.stderr)}"
    if metrics.get("cost_note"):
        notes += f"; {metrics['cost_note']}"
    return {
        "blocked": proc.returncode != 0,
        "notes": notes,
        "metrics": {"cost_usd": metrics["cost_usd"], "tokens": metrics["tokens"]},
    }


def ensure_kitsoki_binary() -> Path:
    """Build the kitsoki CLI once and return its containing directory.

    The paired-task image is built ONCE (Dockerfile.paired-task), but the repo
    checkout is bind-mounted at container-RUN time — whatever's on the host at
    that moment, not what was baked into the image. So the binary has to be
    built at run time, not image-build time. `make build-bin` writes to the
    checkout's own (gitignored) bin/, which lives on the host side of the bind
    mount, so a build here is naturally cached across ephemeral `--rm`
    containers as long as they share the same mounted checkout — no cp of a
    locally-built binary (see MEMORY cp-binary-invalidates-codesign; that
    specific failure is macOS ad-hoc-signing, not applicable to this Linux
    image, but building fresh in-place sidesteps the whole class of problem).
    """
    which = shutil.which("kitsoki")
    if which:
        return Path(which).parent
    binary = KITSOKI_ROOT / "bin" / "kitsoki"
    if binary.exists():
        return binary.parent
    run(["make", "build-bin"], cwd=KITSOKI_ROOT)
    if not binary.exists():
        raise RuntimeError("make build-bin did not produce bin/kitsoki")
    return binary.parent


def test_cmd_for(task: dict[str, Any]) -> str:
    """The real test command for this task's tree, or a deliberate no-op.

    github_content tasks arm a repo-history content oracle (no test suite is
    part of the corpus for those repos); "true" is an explicit always-pass
    no-op, NOT an empty string — an empty test_cmd falls back to `go test
    ./...` in stories/bugfix (internal/host/local_ci.go), which is wrong (and
    slow/broken) for a non-Go tree. external_bakeoff tasks DO have a real
    project test_cmd (bench.py meta), the same one bugfix-bakeoff drives.
    """
    oracle = task.get("oracle") or {}
    if oracle.get("kind") == "external_bakeoff":
        meta = json.loads(
            run(
                ["python3", str(BENCH), "meta", "--project", str(oracle["project"]), "--bug", str(oracle["bug"])],
                cwd=KITSOKI_ROOT,
                capture=True,
            ).stdout
        )
        return meta.get("test_cmd") or "true"
    return "true"


def build_kitsoki_prompt(
    args: argparse.Namespace,
    task: dict[str, Any],
    tree: Path,
    trace_ref: str,
    thread: Path,
    profile: str,
    branch: str,
    test_cmd: str,
    orchestrator_backend: str,
) -> str:
    """Adapted from drive_cell.sh's "Drive ONE kitsoki bug-fix pipeline cell"
    template — same shape, but paired-task tasks come from cost-bench.manifest
    (no candidates.yaml/manifest.yaml plumbing to thread through).

    Tool-name spelling MUST match the orchestrator backend: codex's
    tool_search indexes the kitsoki MCP server's own dotted names (studio.ping,
    session.new, ...) so reusing them verbatim works; the CLAUDE backend
    client-side-renames every dotted MCP tool name to mcp__kitsoki__session_new
    (underscored), so a literal "session.new" in the prompt names a tool that
    doesn't exist under claude. dispatch_kitsoki derives orchestrator_backend
    from the chosen orchestrator model (see _orchestrator_backend_for) and
    passes it here so the two stay in sync."""
    tool = (lambda dotted: dotted) if orchestrator_backend == "codex" else (
        lambda dotted: "mcp__kitsoki__" + dotted.replace(".", "_")
    )
    ticket_title = f"{task['id']} ({task['archetype']}): {task.get('ticket', '')}"
    return "\n".join([
        "Drive ONE kitsoki bug-fix pipeline cell to completion via the kitsoki studio MCP.",
        f"The fix MUST be generated by the live worker model inside the session (profile "
        f"**{profile}**); you (orchestrator) only click studio tools — do NOT edit source.",
        "",
        f"1. {tool('studio.ping')}.",
        f"2. {tool('session.new')} EXACTLY:",
        f'   - story_path: "{BENCH_BUGFIX_STORY}"',
        '   - harness: "live"',
        f'   - profile: "{profile}"',
        f'   - trace: "{trace_ref}"',
        "   - initial_world:",
        f'       ticket_id: "{task["id"]}"',
        f'       thread: "{thread}"',
        f'       ticket_title: "{ticket_title}"',
        f'       workdir: "{tree}"',
        '       workspace_id: ""',
        f'       feature_branch: "{branch}"',
        f'       base_branch: "{branch}"',
        '       bugfix_mode: "full"',
        '       judge_mode: "llm"',
        f'       test_cmd: "{test_cmd}"',
        "       bf_autostart_attempted: true",
        "       escalate_low_value: true",
        "   (workspace_id EMPTY ⇒ implementer edits the prepared workdir directly + commits.)",
        "3. Drive **full_pipeline** ONCE, then only advance explicit gates (accept/continue/",
        "   confirm/proceed) and answer ask-gates affirmatively (\"looks correct, proceed\").",
        "   Do NOT re-drive start — the LLM judge auto-emits accept/refine. Give each",
        "   on_enter step time (the worker profile does the real work there).",
        "4. STOP at a terminal state, ~25 forward turns, or a repeated stuck state. If a",
        "   host_error bounces you to idle, read world.last_error, report it verbatim, STOP.",
        "5. Then inspect the session status/world/trace through MCP. Do not use shell,",
        "   filesystem, git, GitHub, or non-kitsoki tools during the delegated drive.",
        "Report: final state; trace path; source modified (y/n) + fix SHA; 1-line fix;",
        "reproduction bug_verified (t/f); forward turns; last_error if any.",
    ])


def real_trace_metrics(trace_ref: str, model: str) -> dict[str, Any]:
    """Cost/tokens off the REAL kitsoki session trace, read directly (NOT via
    bench.py's `cost` command / read_trace_metrics — that shared reader sums
    input_tokens but DISCARDS cached_input_tokens, then callers price every
    token at the full input rate).

    Verified live on two real kitsoki-codex-native cells: a codex-native
    worker's calls are 90-96% cache reads across a long multi-turn agent
    session (bf__reproducer/bf__implementer each re-read a large, mostly
    unchanged context every tool turn). Pricing that at the full input rate
    instead of pricing.py's cache_read rate (0.1x input) overstated real cost
    by >20x — a genuine cost-reporting bug, not a genuine kitsoki cost gap.
    codex-native runs on ChatGPT-subscription auth (unmetered: recorded
    cost_usd stays 0 even though token counts are real), so this always
    falls back to a cache-aware estimate computed directly from the trace's
    own per-call usage breakdown — the same pricing table method_cost() uses
    on Claude usage blocks, applied here to codex's usage shape."""
    if not os.path.exists(trace_ref):
        return {"cost_usd": 0.0, "tokens": 0, "cost_note": "no trace usage recorded"}
    total_in = total_cached = total_out = 0
    metered_cost = 0.0
    for line in Path(trace_ref).read_text(encoding="utf-8").splitlines():
        try:
            entry = json.loads(line)
        except (json.JSONDecodeError, ValueError):
            continue
        if entry.get("kind") != "agent.call.complete":
            continue
        meta = (entry.get("payload") or {}).get("meta") or {}
        usage = meta.get("usage") or {}
        total_in += usage.get("input_tokens", 0) or 0
        total_cached += usage.get("cached_input_tokens", 0) or 0
        total_out += usage.get("output_tokens", 0) or 0
        c = meta.get("cost_usd")
        if isinstance(c, (int, float)):
            metered_cost += c
    tokens = total_in + total_out
    if metered_cost > 0:
        return {"cost_usd": round(metered_cost, 6), "tokens": tokens, "cost_note": "cost_usd from real recorded trace usage (metered)"}
    if tokens == 0:
        return {"cost_usd": 0.0, "tokens": 0, "cost_note": "no trace usage recorded"}
    price, _ = price_for(model)
    fresh_input = max(total_in - total_cached, 0)
    cost = (fresh_input * price.input + total_cached * price.cache_read + total_out * price.output) / 1e6
    cost = round(cost, 6)
    return {
        "cost_usd": cost,
        "tokens": tokens,
        "cost_note": (
            f"cost_usd={cost} (cache-aware estimate over REAL trace usage: "
            f"{fresh_input} fresh input + {total_cached} cache-read input + {total_out} output tokens; "
            "subscription auth reports no metered cost)"
        ),
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
            "evidence": container_path(target),
            "notes": f"github_content oracle green={green}",
        }
    if oracle.get("kind") == "external_bakeoff":
        out = tree.parent / f"{tree.name}-score.json"
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
        return {"verdict": verdict, "evidence": container_path(out), "notes": notes}
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


def parse_tokens(blob: str) -> int:
    lines = [line.strip() for line in blob.splitlines()]
    for idx, line in enumerate(lines):
        if line.lower() == "tokens used" and idx + 1 < len(lines):
            try:
                return int(lines[idx + 1].replace(",", ""))
            except ValueError:
                return 0
    return 0


def safe_name(value: str) -> str:
    return "".join(ch if ch.isalnum() or ch in "._-" else "-" for ch in value)[:180]


def container_path(path: str | Path) -> str:
    """Translate a /workspace/kitsoki-rooted path to its host-visible path.

    Cell JSONs are read back on the host after the container exits; without
    this they'd carry a container-only prefix nothing on the host can open.
    ARENA_HOST_REPO_ROOT is the host side of the same bind mount (set by
    DockerBackend.run from the cell's own mounts, so it's exact per-run — no
    guessing at the mount source).
    """
    text = str(path)
    host_root = os.environ.get("ARENA_HOST_REPO_ROOT")
    if host_root and text.startswith(str(KITSOKI_ROOT)):
        return host_root.rstrip("/") + text[len(str(KITSOKI_ROOT)):]
    return text


def redact_cmd(cmd: list[str]) -> list[str]:
    # Prompt text can include task details; keep command shape without bloating trace.
    if cmd:
        return cmd[:-1] + ["<prompt>"]
    return cmd


if __name__ == "__main__":
    raise SystemExit(main())
