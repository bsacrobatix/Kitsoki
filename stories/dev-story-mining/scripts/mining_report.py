#!/usr/bin/env python3
"""Write deterministic dev-story-mining report artifacts."""

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
    chars: list[str] = []
    for ch in (value or "").lower():
        if ch.isalnum():
            chars.append(ch)
        elif ch in {"-", "_", ".", " ", ":", "/"}:
            chars.append("-")
    return re.sub(r"-+", "-", "".join(chars).strip("-")) or "run"


def _obj(value: str) -> dict[str, Any]:
    if not (value or "").strip():
        return {}
    try:
        loaded = json.loads(value)
    except json.JSONDecodeError:
        return {}
    return loaded if isinstance(loaded, dict) else {}


def _title(obj: dict[str, Any], fallback: str) -> str:
    return str(obj.get("summary_title") or obj.get("brief_path") or obj.get("summary_markdown") or fallback)[:240]


def _summary(args: argparse.Namespace) -> dict[str, Any]:
    mine = _obj(args.mine_artifact)
    mapped = _obj(args.map_artifact)
    decide = _obj(args.decide_artifact)
    author = _obj(args.author_artifact)
    record = _obj(args.record_artifact)
    selected = args.selected_theme or ", ".join(str(x.get("theme", "")) for x in decide.get("selected", []) if isinstance(x, dict))
    summary = f"Mining job {args.job} completed for {args.stories_dir}."
    if selected:
        summary += f" Selected: {selected}."
    objectives = [
        {"label": "Mine transcripts", "status": "done" if mine else "pending", "detail": f"{mine.get('intent_count', '')} intents; {mine.get('brief_path', '')}"},
        {"label": "Map story gates", "status": "done" if mapped else "pending", "detail": _title(mapped, "No map artifact")},
        {"label": "Select enrichment", "status": "done" if decide else "pending", "detail": selected},
        {"label": "Author gate", "status": "done" if author.get("flows_green") else "pending", "detail": _title(author, "No author artifact")},
        {"label": "Record ladder review", "status": "done" if record else "pending", "detail": _title(record, "No record artifact")},
    ]
    artifacts = [
        {"label": "Mining brief", "status": "done" if mine.get("brief_path") else "pending", "ref": mine.get("brief_path", "")},
        {"label": "Selected theme", "status": "done" if selected else "pending", "ref": selected},
        {"label": "Files changed", "status": "done" if author.get("files_changed") else "pending", "ref": json.dumps(author.get("files_changed", []), sort_keys=True)},
        {"label": "Ladder moves", "status": "done" if record else "pending", "ref": json.dumps(record.get("ladder_moves", []), sort_keys=True)},
    ]
    items = [
        {"id": "mine", "status": "done" if mine else "missing", "owner": "miner", "artifact": mine.get("brief_path", "")},
        {"id": "map", "status": "done" if mapped else "missing", "owner": "mapper", "artifact": _title(mapped, "")},
        {"id": "decide", "status": "done" if decide else "missing", "owner": "ranker", "artifact": selected},
        {"id": "author", "status": "green" if author.get("flows_green") else "needs-review", "owner": "author", "artifact": _title(author, "")},
        {"id": "record", "status": "done" if record else "missing", "owner": "recorder", "artifact": _title(record, "")},
    ]
    return {
        "title": "Dev Story Mining Report",
        "summary": summary,
        "job": args.job,
        "stories_dir": args.stories_dir,
        "objectives": objectives,
        "artifacts": artifacts,
        "items": items,
        "next_steps": [
            {"label": "Review authored gate", "detail": "Inspect files_changed and the generated flow fixture."},
            {"label": "Re-run after adoption", "detail": "Use recorded gate decisions to reassess the determinism ladder."},
        ],
    }


def _write_markdown(path: Path, summary: dict[str, Any]) -> None:
    lines = ["# Dev Story Mining Report", "", summary["summary"], "", "## Objectives"]
    for item in summary.get("objectives", []):
        lines.append(f"- {item.get('label')}: {item.get('status')} - {item.get('detail')}")
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--job", default="")
    ap.add_argument("--stories-dir", default="")
    ap.add_argument("--selected-theme", default="")
    ap.add_argument("--mine-artifact", default="")
    ap.add_argument("--map-artifact", default="")
    ap.add_argument("--decide-artifact", default="")
    ap.add_argument("--author-artifact", default="")
    ap.add_argument("--record-artifact", default="")
    ap.add_argument("--artifact-root", default=".artifacts")
    ap.add_argument("--run-id", default="")
    args = ap.parse_args()

    run_id = _slug(args.run_id or args.job or dt.datetime.now(dt.timezone.utc).strftime("%Y%m%d-%H%M%SZ"))
    out_dir = Path(args.artifact_root) / "dev-story-mining" / run_id
    report_path = out_dir / "report.md"
    summary_path = out_dir / "summary.json"
    deck_path = out_dir / "deck.slidey.json"

    summary = _summary(args)
    summary["report_path"] = str(report_path)
    summary["summary_path"] = str(summary_path)
    summary["deck_path"] = str(deck_path)

    out_dir.mkdir(parents=True, exist_ok=True)
    summary_path.write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")
    _write_markdown(report_path, summary)
    deck_tool = Path(__file__).resolve().parents[3] / "tools" / "report-deck" / "deterministic_deck.py"
    subprocess.run([sys.executable, str(deck_tool), "--kind", "workflow", "--input", str(summary_path), "--out", str(deck_path)], check=True, capture_output=True, text=True)
    print(json.dumps({"report_path": str(report_path), "summary_path": str(summary_path), "deck_path": str(deck_path), "summary": summary["summary"]}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
