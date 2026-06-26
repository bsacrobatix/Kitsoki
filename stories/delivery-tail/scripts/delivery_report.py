#!/usr/bin/env python3
"""Write deterministic delivery-tail shipped report artifacts."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import re
import subprocess
import sys
from pathlib import Path
from typing import Any


def _slug(value: str) -> str:
    chars = []
    for ch in (value or "").lower():
        if ch.isalnum():
            chars.append(ch)
        elif ch in {"-", "_", ".", " ", ":"}:
            chars.append("-")
    return re.sub(r"-+", "-", "".join(chars).strip("-")) or "run"


def _build_summary(args: argparse.Namespace) -> dict[str, Any]:
    summary = f"Shipped {args.workspace_branch or 'branch'} at {args.shipped_sha or args.integrated_sha}."
    objectives = [
        {"label": "Integrate branch", "status": "done" if args.integrate_outcome == "integrated" else "failed", "detail": args.integrated_sha},
        {"label": "Verify merged commit", "status": "done" if args.verify_ok == "true" else "failed", "detail": args.verify_output[:240]},
        {"label": "Cleanup worktree", "status": "done" if args.cleanup_outcome == "cleaned" else "pending", "detail": args.worktree_path},
        {"label": "Report deck", "status": "done", "detail": "Generated from delivery-tail world state."},
    ]
    artifacts = [
        {"label": "Merged commit", "status": "done" if args.shipped_sha else "pending", "ref": args.shipped_sha},
        {"label": "Gate command", "status": "done" if args.gate_command else "pending", "ref": args.gate_command},
        {"label": "Maker worktree", "status": "done" if args.worktree_path else "pending", "ref": args.worktree_path},
    ]
    items = [
        {"id": "branch", "status": args.integrate_outcome or "unknown", "owner": args.workspace_branch, "artifact": args.integrated_sha},
        {"id": "gate", "status": "green" if args.verify_ok == "true" else "red", "owner": "delivery-tail", "artifact": args.gate_command},
        {"id": "cleanup", "status": args.cleanup_outcome or "unknown", "owner": "delivery-tail", "artifact": args.worktree_path},
    ]
    return {
        "title": "Delivery Tail Report",
        "summary": summary,
        "brief": args.brief,
        "objectives": objectives,
        "artifacts": artifacts,
        "items": items,
        "next_steps": [
            {"label": "Review shipped SHA", "detail": "Confirm downstream parent story records the shipped commit."},
            {"label": "Follow parent workflow", "detail": "Continue any PR, issue, or release handling owned by the importing story."},
        ],
    }


def _write_markdown(path: Path, summary: dict[str, Any]) -> None:
    lines = [
        "# Delivery Tail Report",
        "",
        summary["summary"],
        "",
        f"- Brief: {summary.get('brief', '')}",
        "",
        "## Objectives",
    ]
    for item in summary.get("objectives", []):
        lines.append(f"- {item.get('label')}: {item.get('status')} - {item.get('detail')}")
    lines.extend(["", "## Artifacts"])
    for item in summary.get("artifacts", []):
        lines.append(f"- {item.get('label')}: `{item.get('ref', '')}`")
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--brief", default="")
    ap.add_argument("--workspace-branch", default="")
    ap.add_argument("--worktree-path", default="")
    ap.add_argument("--gate-command", default="")
    ap.add_argument("--integrate-outcome", default="")
    ap.add_argument("--integrated-sha", default="")
    ap.add_argument("--verify-ok", default="")
    ap.add_argument("--verify-output", default="")
    ap.add_argument("--cleanup-outcome", default="")
    ap.add_argument("--shipped-sha", default="")
    ap.add_argument("--artifact-root", default=".artifacts")
    ap.add_argument("--run-id", default="")
    args = ap.parse_args()

    run_id = _slug(args.run_id or args.shipped_sha or dt.datetime.now(dt.timezone.utc).strftime("%Y%m%d-%H%M%SZ"))
    out_dir = Path(args.artifact_root) / "delivery-tail" / run_id
    report_path = out_dir / "report.md"
    summary_path = out_dir / "summary.json"
    deck_path = out_dir / "deck.slidey.json"

    summary = _build_summary(args)
    summary["report_path"] = str(report_path)
    summary["summary_path"] = str(summary_path)
    summary["deck_path"] = str(deck_path)

    out_dir.mkdir(parents=True, exist_ok=True)
    summary_path.write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")
    _write_markdown(report_path, summary)

    deck_tool = Path(__file__).resolve().parents[3] / "tools" / "report-deck" / "deterministic_deck.py"
    subprocess.run([
        sys.executable,
        str(deck_tool),
        "--kind", "workflow",
        "--input", str(summary_path),
        "--out", str(deck_path),
    ], check=True, capture_output=True, text=True)

    print(json.dumps({
        "report_path": str(report_path),
        "summary_path": str(summary_path),
        "deck_path": str(deck_path),
        "summary": summary["summary"],
    }))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
