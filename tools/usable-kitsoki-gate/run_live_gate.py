#!/usr/bin/env python3
"""run_live_gate.py — Task 3.3's LIVE half (docs/proposals/
usable-kitsoki-release-gate.md). The gated, cost-bearing counterpart to
`flow_gate_runner.py`'s no-LLM flow-replay path: instead of replaying a
compiled flow fixture through `stories/scenario-foundry-harness`'s stub
`ask` room, this drives a mined scenario's turns into a REAL `workbench:`
room (`stories/dev-story`'s `landing` room, migrated onto `workbench:` by
room-workbench Task 3.1) via a REAL spawned agent process, closing the
"honest gap" `flow_gate_runner.py`'s docstring documents for the no-LLM
path.

## Structural gating (mirrors tools/swarm/tiers/liveExplorerCli.ts exactly)

This module is NEVER executed by any test in this repo -- grep
`tools/arena/tests/` and `tools/usable-kitsoki-gate/` for this file's name;
it will only turn up in docstrings/README references and in
`usable_kitsoki_gate.py`'s `drive_command(cell, live=True)` branch (which
itself is only reachable when an operator passes `arena run --live`
explicitly -- see `tools/arena/arena.py`'s `--live` flag and README's
"Live... gated behind --live, cost-bearing, never run in CI").

The ONE gate lives in `assert_live_gate_allowed`: `live_gate` must be
`is True`, and the only way this module's own `main()` ever sets that to
`True` is a LITERAL `--live-gate` flag in `sys.argv` -- not an env var, not
a config default (mirrors `liveExplorerCli.ts`'s `parseArgs().liveExplorers`
contract, and `bugfix.py`'s `--live`-gated `drive_cell.sh` path). Deleting
the flag from the invocation makes `parse_args` return `live_gate=False`,
which makes `main` refuse before spawning any agent or touching a real
session -- tested directly in
`tools/arena/tests/test_usable_kitsoki_gate_live_gate.py` by calling
`parse_args`/`assert_live_gate_allowed` themselves, never by invoking this
file's `main()`.

## What ONE live cell does (only reachable via `--live-gate`, manual only)

1. Loads the scenario IR document (`GATE_SCENARIO_CORPUS`/`GATE_SCENARIO_ID`,
   the identical env contract `_gate_cli.py` reads for the no-LLM path).
2. Snapshots the mtimes of every trace file already sitting under this
   app's `~/.kitsoki/sessions/<app-slug>/` directory
   (`internal/store/trace_path.go`'s `SessionsDir()`/`DefaultTracePath`
   convention) so a freshly-written trace can be told apart from a stale one
   without needing the spawned agent to report its own session id reliably.
3. Builds a briefing prompt (mirrors `liveExplorerCli.ts`'s `buildPrompt`)
   instructing the agent to mint a real kitsoki session against
   `LIVE_TARGET_APP` via the studio MCP toolbox (`session_new`,
   `session_submit`/`session_drive`) and submit the scenario's mined turns,
   IN ORDER, as free text -- real routing, real dispatch, real LLM spend.
   `AskUserQuestion` is hard-denied for headless agents (AGENTS.md); the
   prompt tells the agent to proceed on its own judgment rather than stall.
4. Spawns that agent (`--agent-cmd`, default `claude -p`) via
   `subprocess.run` -- real LLM spend happens here, exactly once per cell.
5. Diffs the sessions directory's mtimes to find the trace file the run
   just wrote, parses it as the SAME raw `store.Event` JSONL
   `flow_gate_runner.run_scenario_flow`'s `--trace-out` produces (one line
   per event, `{"kind": "turn.end", "payload": {...}}` for the ones that
   matter), and re-uses `usable_kitsoki_gate.py`'s `extract_turn_signals` /
   `build_parity_record` verbatim -- the SAME S1 x S4 join the no-LLM path
   performs, so a live and a no-LLM record can never silently drift onto
   different join logic.
6. Writes `{"records": [<record>]}` to `GATE_RESULTS_PATH` and prints the
   `[usable-kitsoki-gate] wrote <path> (N records)` pointer line
   `usable_kitsoki_gate.py`'s `score()` scans for -- the identical contract
   the no-LLM harnesses honor, so the plugin's `score()` needs no
   live-vs-no-LLM branch of its own.

## Why this is not run in CI, ever

Every invocation costs real LLM tokens (a spawned `claude -p` process) and
depends on an operator's own `claude`/agent credentials being present in the
environment -- exactly the two properties AGENTS.md's "Automated testing
should never use a real LLM or incur costs" rule forbids in any CI job. The
release-candidate workflow (`.github/workflows/usable-kitsoki-gate.yml`'s
`release-candidate-live-gate` job) documents the intended cadence (an
explicit tag or `workflow_dispatch`, never `pull_request`/`push: main`) but
still never actually invokes this file with `--live-gate` from within the
repo -- that remains an operator's own `arena run --live` invocation per
`tools/arena/README.md`'s existing "Live... never run in CI" convention.
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import time
from pathlib import Path
from typing import Any

sys.path.insert(0, str(Path(__file__).resolve().parent))
import flow_gate_runner as runner  # noqa: E402

_ARENA_ROOT = runner.REPO_ROOT / "tools" / "arena"
if str(_ARENA_ROOT) not in sys.path:
    sys.path.insert(0, str(_ARENA_ROOT))

from arena.plugins import usable_kitsoki_gate as gate_plugin  # noqa: E402

# The real `workbench:` room this live path drives -- unlike the no-LLM
# path's `stories/scenario-foundry-harness` stub, `stories/dev-story`'s
# `landing` room IS a `workbench:` room (room-workbench Task 3.1: "app:
# migrate dev-story's landing.yaml onto workbench:"), so
# `workbenchGateSignal` actually fires for real turns driven here.
LIVE_TARGET_APP = runner.REPO_ROOT / "stories" / "dev-story" / "app.yaml"
LIVE_TARGET_APP_SLUG = "dev-story"

DEFAULT_AGENT_CMD = "claude"
LIVE_AGENT_TIMEOUT_SECONDS = 1800  # 30 minutes -- a real multi-turn agent drive, not a flow replay


class LiveGateNotAllowedError(Exception):
    """Raised by `assert_live_gate_allowed` when `--live-gate` was not passed
    literally on the command line. The ONE gate this module has -- see the
    module docstring's "Structural gating" section."""


def assert_live_gate_allowed(*, live_gate: bool) -> None:
    if live_gate is not True:
        raise LiveGateNotAllowedError(
            "run_live_gate.py refuses to spend real LLM tokens without an explicit "
            "--live-gate flag on the command line (no env var fallback, no default) -- "
            "see this module's docstring and tools/swarm/tiers/liveExplorerCli.ts for the "
            "identical pattern this mirrors"
        )


def parse_args(argv: list[str]) -> argparse.Namespace:
    """Parses argv. `live_gate` is `True` ONLY if `--live-gate` is literally
    present -- no env var fallback, no implicit default (mirrors
    `liveExplorerCli.ts`'s `parseArgs().liveExplorers` contract exactly).
    """
    parser = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument(
        "--live-gate", action="store_true", dest="live_gate",
        help="required: opt in to real LLM spend for this cell (never set this from CI)",
    )
    parser.add_argument("--agent-cmd", default=DEFAULT_AGENT_CMD, help="agent binary to spawn (default: claude)")
    parser.add_argument(
        "--target-app", default=str(LIVE_TARGET_APP),
        help="path to the app.yaml whose workbench room this cell drives (default: stories/dev-story/app.yaml)",
    )
    return parser.parse_args(argv)


def _sessions_dir_for(app_slug: str) -> Path:
    home = Path(os.environ.get("HOME") or Path.home())
    return home / ".kitsoki" / "sessions" / app_slug


def _snapshot_trace_files(sessions_dir: Path) -> dict[Path, float]:
    if not sessions_dir.is_dir():
        return {}
    return {p: p.stat().st_mtime for p in sessions_dir.glob("*.jsonl")}


def _find_new_trace_file(sessions_dir: Path, before: dict[Path, float]) -> Path | None:
    """The trace file the just-completed agent run wrote: newly created, or
    modified more recently than every file that already existed before the
    agent was spawned. Ties broken by newest mtime -- exactly one real
    session should have been minted per live cell."""
    if not sessions_dir.is_dir():
        return None
    candidates: list[tuple[float, Path]] = []
    for p in sessions_dir.glob("*.jsonl"):
        prior_mtime = before.get(p)
        mtime = p.stat().st_mtime
        if prior_mtime is None or mtime > prior_mtime:
            candidates.append((mtime, p))
    if not candidates:
        return None
    candidates.sort()
    return candidates[-1][1]


def build_live_prompt(scenario: dict[str, Any], target_app: Path) -> str:
    """Mirrors `liveExplorerCli.ts`'s `buildPrompt` -- a persona-briefed,
    scenario-grounded prompt telling the spawned agent exactly what real
    turns to submit and reminding it `AskUserQuestion` is unavailable
    headless (AGENTS.md)."""
    turns = scenario.get("turns") or []
    turn_lines = "\n".join(
        f"  {i + 1}. {t.get('text', '')}" for i, t in enumerate(turns) if isinstance(t, dict)
    )
    return "\n".join([
        f"You are driving a real kitsoki session against {target_app} as part of the",
        "usable-kitsoki-gate live parity harness (docs/proposals/usable-kitsoki-release-gate.md).",
        f"Persona: {scenario.get('persona', '')}",
        f"Goal: {scenario.get('goal', '')}",
        "",
        "Use kitsoki's MCP tools (session_new against the app above, then session_submit /",
        "session_drive) to mint ONE new session and submit the following turns, IN ORDER,",
        "as free text -- do not skip, reorder, or paraphrase them:",
        turn_lines,
        "",
        "AskUserQuestion is not available to you headless -- proceed on your own judgment",
        "for anything ambiguous rather than stopping to ask.",
        "When every turn has been submitted, stop; do not close or delete the session.",
    ])


def run_live_cell(
    scenario_id: str,
    corpus_dir: Path,
    surface: str,
    *,
    agent_cmd: str,
    target_app: Path,
    evidence_dir: Path,
) -> dict[str, Any]:
    """The real work `main()` performs once `--live-gate` has been checked.
    Never called from anywhere else in this repo (see module docstring)."""
    scenario = runner.load_scenario(corpus_dir, scenario_id)
    sessions_dir = _sessions_dir_for(LIVE_TARGET_APP_SLUG)
    before = _snapshot_trace_files(sessions_dir)

    prompt = build_live_prompt(scenario, target_app)
    proc = subprocess.run(
        [agent_cmd, "-p", prompt],
        cwd=str(runner.REPO_ROOT),
        capture_output=True,
        text=True,
        timeout=LIVE_AGENT_TIMEOUT_SECONDS,
    )

    # Give the trace writer a moment to flush after the agent process exits
    # (best-effort; a real operator run is not latency-sensitive here).
    time.sleep(0.5)
    trace_path = _find_new_trace_file(sessions_dir, before)

    trace_events: list[dict[str, Any]] = []
    notes_parts: list[str] = []
    if proc.returncode != 0:
        blob = (proc.stdout + "\n" + proc.stderr).strip()
        notes_parts.append(f"live agent exited {proc.returncode}: {blob[:300]}")
    if trace_path is None:
        notes_parts.append(
            "no new session trace file was found under "
            f"{sessions_dir} after the live agent run -- treating as no signal captured, "
            "not fabricating one"
        )
    else:
        for line in trace_path.read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if line:
                trace_events.append(json.loads(line))

    turn_signals = gate_plugin.extract_turn_signals(trace_events)

    evidence_dir.mkdir(parents=True, exist_ok=True)
    evidence_path = evidence_dir / f"{scenario_id}.{surface}.live.trace.jsonl"
    evidence_path.write_text(
        "\n".join(json.dumps(e, sort_keys=True) for e in trace_events)
        + ("\n" if trace_events else ""),
        encoding="utf-8",
    )
    evidence_refs = [str(evidence_path)]
    if trace_path is not None:
        evidence_refs.append(str(trace_path))

    return gate_plugin.build_parity_record(
        scenario=scenario,
        surface=surface,
        turn_signals=turn_signals,
        evidence_refs=evidence_refs,
        notes="; ".join(notes_parts),
    )


def main(argv: list[str]) -> int:
    args = parse_args(argv)
    try:
        assert_live_gate_allowed(live_gate=args.live_gate)
    except LiveGateNotAllowedError as exc:
        print(f"[usable-kitsoki-gate] {exc}", file=sys.stderr)
        return 2

    scenario_corpus = os.environ.get("GATE_SCENARIO_CORPUS") or str(runner.DEFAULT_CORPUS)
    corpus_dir = Path(scenario_corpus)
    if not corpus_dir.is_absolute():
        corpus_dir = runner.REPO_ROOT / corpus_dir

    scenario_id = os.environ.get("GATE_SCENARIO_ID")
    if not scenario_id:
        print("[usable-kitsoki-gate] GATE_SCENARIO_ID is required (one cell = one scenario)", file=sys.stderr)
        return 2

    surface = os.environ.get("GATE_SURFACE") or gate_plugin.DEFAULT_SURFACE
    run_id = os.environ.get("GATE_RUN_ID") or "local-live"
    results_path_env = os.environ.get("GATE_RESULTS_PATH")
    if results_path_env:
        results_path = Path(results_path_env)
        if not results_path.is_absolute():
            results_path = runner.REPO_ROOT / results_path
    else:
        results_path = runner.REPO_ROOT / ".artifacts" / "usable-kitsoki-gate" / run_id / "parity-records.json"

    evidence_dir = results_path.parent / "evidence"
    target_app = Path(args.target_app)
    if not target_app.is_absolute():
        target_app = runner.REPO_ROOT / target_app

    try:
        record = run_live_cell(
            scenario_id, corpus_dir, surface,
            agent_cmd=args.agent_cmd, target_app=target_app, evidence_dir=evidence_dir,
        )
    except runner.ScenarioNotFound as exc:
        print(f"[usable-kitsoki-gate] {exc}", file=sys.stderr)
        return 2

    results_path.parent.mkdir(parents=True, exist_ok=True)
    results_path.write_text(json.dumps({"run_id": run_id, "records": [record]}, indent=2), encoding="utf-8")
    print(f"[usable-kitsoki-gate] wrote {results_path} (1 record)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
