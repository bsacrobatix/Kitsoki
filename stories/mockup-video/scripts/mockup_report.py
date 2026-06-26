#!/usr/bin/env python3
"""Write deterministic mockup-video report artifacts."""

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
    feature = args.feature or "Mockup walkthrough"
    render_ref = args.video_path or args.video_handle
    chapters_ref = args.chapters_path or args.chapters_handle
    source_ref = args.source_spec or ""
    media = [{
        "title": "Accepted walkthrough",
        "caption": f"{feature} ({args.medium or 'medium unknown'})",
        "src": render_ref,
        "chapters": chapters_ref or "auto",
    }]
    phases = [
        {"label": "Brief", "status": "done" if feature else "pending", "detail": feature},
        {"label": "Author source", "status": "done" if source_ref else "pending", "detail": args.source_summary or args.source_kind},
        {"label": "Render walkthrough", "status": "done" if render_ref else "pending", "detail": render_ref},
        {"label": "Review and refine", "status": "done", "detail": f"{args.cycle} refine cycle(s)."},
        {"label": "Report deck", "status": "done", "detail": "Generated from structured story state."},
    ]
    artifacts = [
        {"label": "Source spec", "status": "done" if source_ref else "pending", "ref": source_ref},
        {"label": "Walkthrough video", "status": "done" if render_ref else "pending", "ref": render_ref},
        {"label": "Chapters sidecar", "status": "done" if chapters_ref else "pending", "ref": chapters_ref},
    ]
    next_steps = [
        {"label": "Review accepted video", "detail": "Open the walkthrough artifact and confirm it communicates the intended scenarios."},
        {"label": "Promote source if useful", "detail": "Keep mockup source under the run artifact folder unless it becomes product documentation."},
    ]
    return {
        "title": "Mockup Video Report",
        "summary": f"{feature}: accepted {args.medium or 'mockup'} walkthrough after {args.cycle} refine cycle(s).",
        "feature": feature,
        "medium": args.medium,
        "phases": phases,
        "media": media,
        "artifacts": artifacts,
        "next_steps": next_steps,
    }


def _write_markdown(path: Path, summary: dict[str, Any]) -> None:
    lines = [
        "# Mockup Video Report",
        "",
        summary["summary"],
        "",
        "## Artifacts",
    ]
    for artifact in summary.get("artifacts", []):
        lines.append(f"- {artifact.get('label')}: `{artifact.get('ref', '')}`")
    lines.extend(["", "## Next steps"])
    for step in summary.get("next_steps", []):
        lines.append(f"- {step.get('label')}: {step.get('detail')}")
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--feature", default="")
    ap.add_argument("--medium", default="")
    ap.add_argument("--source-kind", default="")
    ap.add_argument("--source-spec", default="")
    ap.add_argument("--source-summary", default="")
    ap.add_argument("--video-handle", default="")
    ap.add_argument("--chapters-handle", default="")
    ap.add_argument("--video-path", default="")
    ap.add_argument("--chapters-path", default="")
    ap.add_argument("--cycle", type=int, default=0)
    ap.add_argument("--artifact-root", default=".artifacts")
    ap.add_argument("--run-id", default="")
    args = ap.parse_args()

    run_id = _slug(args.run_id or dt.datetime.now(dt.timezone.utc).strftime("%Y%m%d-%H%M%SZ"))
    out_dir = Path(args.artifact_root) / "mockup-video" / run_id
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
        "--kind", "feature-demo",
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
