#!/usr/bin/env python3
"""Write deterministic implementation handoff report artifacts."""

from __future__ import annotations

import argparse
import datetime as dt
import json
import re
import subprocess
import sys
from pathlib import Path


def _slug(value: str) -> str:
    chars = []
    for ch in (value or "").lower():
        if ch.isalnum():
            chars.append(ch)
        elif ch in {"-", "_", ".", " ", ":"}:
            chars.append("-")
    return re.sub(r"-+", "-", "".join(chars).strip("-")) or "run"


def _build_summary(args: argparse.Namespace) -> dict:
    objectives = [
        {"label": "Understand task", "status": "done" if args.task_summary else "pending", "detail": args.task_title},
        {"label": "Write code", "status": "done" if args.code_summary else "pending", "detail": args.code_title},
        {"label": "Run tests", "status": "done" if args.test_status == "passed" else (args.test_status or "pending"), "detail": args.test_title},
        {"label": "Review implementation", "status": "done" if args.review_status == "approved" else (args.review_status or "pending"), "detail": args.review_title},
        {"label": "Prepare PR handoff", "status": "done", "detail": args.pr_title or args.code_title},
    ]
    artifacts = [
        {"label": "Task summary", "status": "done" if args.task_summary else "pending", "ref": args.task_summary},
        {"label": "Code artifact", "status": "done" if args.code_summary else "pending", "ref": args.code_summary},
        {"label": "Test artifact", "status": args.test_status or "pending", "ref": args.test_summary},
        {"label": "Review artifact", "status": args.review_status or "pending", "ref": args.review_summary},
        {"label": "Workdir", "status": "done" if args.workdir else "pending", "ref": args.workdir},
    ]
    items = [
        {"id": "task", "status": "done" if args.task_summary else "pending", "owner": "implementation", "artifact": args.task_title},
        {"id": "code", "status": "done" if args.code_summary else "pending", "owner": "implementation", "artifact": args.code_title},
        {"id": "test", "status": args.test_status or "pending", "owner": "implementation", "artifact": args.test_title},
        {"id": "review", "status": args.review_status or "pending", "owner": "implementation", "artifact": args.review_title},
    ]
    return {
        "title": f"Implementation Handoff: {args.ticket_id or args.ticket_title or 'Task'}",
        "summary": f"{args.ticket_id or 'Task'} ready for PR handoff: {args.pr_title or args.code_title}.",
        "objectives": objectives,
        "artifacts": artifacts,
        "items": items,
        "next_steps": [
            {"label": "Open PR", "detail": "Enter the pr-refinement tail with the generated PR title and body."},
            {"label": "Monitor merge", "detail": "Let pr-refinement own review comments, CI, and merge outcome."},
        ],
    }


def _write_markdown(path: Path, summary: dict) -> None:
    lines = ["# Implementation Handoff Report", "", summary["summary"], "", "## Objectives"]
    for item in summary.get("objectives", []):
        lines.append(f"- {item.get('label')}: {item.get('status')} - {item.get('detail')}")
    lines.extend(["", "## Artifacts"])
    for item in summary.get("artifacts", []):
        lines.append(f"- {item.get('label')}: `{item.get('ref', '')}`")
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--ticket-id", default="")
    ap.add_argument("--ticket-title", default="")
    ap.add_argument("--workdir", default="")
    ap.add_argument("--feature-branch", default="")
    ap.add_argument("--pr-title", default="")
    ap.add_argument("--task-title", default="")
    ap.add_argument("--task-summary", default="")
    ap.add_argument("--code-title", default="")
    ap.add_argument("--code-summary", default="")
    ap.add_argument("--test-title", default="")
    ap.add_argument("--test-summary", default="")
    ap.add_argument("--test-status", default="")
    ap.add_argument("--review-title", default="")
    ap.add_argument("--review-summary", default="")
    ap.add_argument("--review-status", default="")
    ap.add_argument("--artifact-root", default=".artifacts")
    ap.add_argument("--run-id", default="")
    args = ap.parse_args()

    run_id = _slug(args.run_id or args.ticket_id or dt.datetime.now(dt.timezone.utc).strftime("%Y%m%d-%H%M%SZ"))
    out_dir = Path(args.artifact_root) / "implementation" / run_id
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
