#!/usr/bin/env python3
"""Write fleet fan-out review artifacts.

The fleet story already owns the authoritative brief board in a JSON sidecar.
This script turns that board into deterministic review artifacts under
.artifacts/fleet/<run-id>/ and asks tools/report-deck for the standardized
Slidey structure. No LLM is called here.
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import re
import subprocess
import sys
from pathlib import Path
from typing import Any


def _slug(value: str) -> str:
    out = []
    for ch in (value or "").lower():
        if ch.isalnum():
            out.append(ch)
        elif ch in {"-", "_", ".", " ", ":"}:
            out.append("-")
    text = "".join(out).strip("-")
    return re.sub(r"-+", "-", text) or "run"


def _load_board(path: str) -> list[dict[str, Any]]:
    if not path:
        return []
    try:
        with open(path, "r", encoding="utf-8") as fh:
            doc = json.load(fh) or {}
    except Exception:  # noqa: BLE001 - report best effort from current world if file is gone.
        return []
    briefs = doc.get("fleet_briefs") or []
    return briefs if isinstance(briefs, list) else []


def _normalize_status(value: str) -> str:
    value = (value or "pending").lower()
    if value == "shipped":
        return "succeeded"
    if value == "parked":
        return "failed"
    if value in {"pending", "running", "blocked", "failed", "succeeded", "retried", "skipped"}:
        return value
    return "pending"


def _build_summary(args: argparse.Namespace) -> dict[str, Any]:
    briefs = _load_board(args.state_path)
    items = []
    for brief in briefs:
        brief_id = str(brief.get("id") or "brief")
        status = _normalize_status(str(brief.get("status") or "pending"))
        detail = str(brief.get("last_error") or brief.get("gate_command") or "")
        items.append({
            "id": brief_id,
            "label": brief_id,
            "status": status,
            "attempts": brief.get("attempts", 1 if status in {"succeeded", "failed"} else 0),
            "owner": "ship-it",
            "artifact": f".worktrees/{brief_id}",
            "detail": detail,
            "gate_command": brief.get("gate_command", ""),
        })

    succeeded = sum(1 for item in items if item["status"] == "succeeded")
    failed = sum(1 for item in items if item["status"] == "failed")
    pending = sum(1 for item in items if item["status"] in {"pending", "running", "blocked"})
    next_steps = []
    if failed:
        next_steps.append({"label": "Review parked briefs", "detail": f"{failed} brief(s) need operator follow-up."})
    if pending:
        next_steps.append({"label": "Resume fan-out", "detail": f"{pending} brief(s) remain pending."})
    if not next_steps:
        next_steps.append({"label": "Review integrations", "detail": "Inspect shipped worktrees and merge evidence."})

    return {
        "title": "Fleet Fan-out Report",
        "decomposition_path": args.decomposition_path,
        "state_path": args.state_path,
        "fleet_summary": f"{succeeded} shipped, {failed} parked.",
        "items": items,
        "counts": {"succeeded": succeeded, "failed": failed, "pending": pending},
        "next_steps": next_steps,
    }


def _write_markdown(path: Path, summary: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    lines = [
        "# Fleet Fan-out Report",
        "",
        summary.get("fleet_summary", ""),
        "",
        f"- Decomposition: `{summary.get('decomposition_path', '')}`",
        f"- State: `{summary.get('state_path', '')}`",
        "",
        "## Briefs",
    ]
    for item in summary.get("items", []):
        detail = item.get("detail") or item.get("gate_command") or ""
        lines.append(f"- {item.get('id')}: {item.get('status')} -> `{item.get('artifact')}`" + (f" ({detail})" if detail else ""))
    lines.extend(["", "## Next steps"])
    for step in summary.get("next_steps", []):
        lines.append(f"- {step.get('label')}: {step.get('detail')}")
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--state-path", required=True)
    ap.add_argument("--decomposition-path", default="")
    ap.add_argument("--artifact-root", default=".artifacts")
    ap.add_argument("--run-id", default="")
    args = ap.parse_args()

    run_id = _slug(args.run_id or dt.datetime.now(dt.timezone.utc).strftime("%Y%m%d-%H%M%SZ"))
    out_dir = Path(args.artifact_root) / "fleet" / run_id
    summary_path = out_dir / "summary.json"
    report_path = out_dir / "report.md"
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
        "--kind", "fanout",
        "--input", str(summary_path),
        "--out", str(deck_path),
    ], check=True, capture_output=True, text=True)

    print(json.dumps({
        "report_path": str(report_path),
        "report_summary_path": str(summary_path),
        "report_deck_path": str(deck_path),
        "fleet_summary": summary["fleet_summary"],
    }))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
