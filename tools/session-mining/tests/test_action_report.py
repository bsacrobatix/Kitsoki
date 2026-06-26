#!/usr/bin/env python3
"""Tests for report.py action brief outputs. No LLM, no network."""

import json
import subprocess
import sys
import tempfile
from pathlib import Path


HERE = Path(__file__).resolve()
ROOT = HERE.parents[3]
TOOL = ROOT / "tools" / "session-mining" / "report.py"


def run():
    payload = {
        "schema_version": "1.0",
        "vocab_version": "test",
        "contributors": 2,
        "promote_min_contributors": 2,
        "reports_merged": 1,
        "patterns": [
            {
                "id": "fix-failing-tests",
                "occurrences": 5,
                "contributors": 2,
                "mechanical_fraction": 0.7,
                "pain": "high",
                "decision_points": ["code vs test"],
                "determinism_priority": 0.8,
                "example_signatures": ["go test -> edit -> go test"],
            },
            {
                "id": "explore-codebase",
                "occurrences": 3,
                "contributors": 1,
                "mechanical_fraction": 0.95,
                "pain": "low",
                "decision_points": [],
                "determinism_priority": 0.2,
                "example_signatures": [],
            },
        ],
        "novel_quarantine": [{"id": "visual-qc", "contributors": 1, "occurrences": 2}],
    }
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        src = tmp_path / "report.json"
        md = tmp_path / "BRIEF.md"
        summary = tmp_path / "brief.summary.json"
        deck = tmp_path / "deck.slidey.json"
        src.write_text(json.dumps(payload), encoding="utf-8")
        proc = subprocess.run(
            [
                sys.executable,
                str(TOOL),
                str(src),
                "--markdown",
                str(md),
                "--summary",
                str(summary),
                "--slidey-spec",
                str(deck),
            ],
            cwd=ROOT,
            text=True,
            capture_output=True,
            check=True,
        )
        if proc.stdout:
            raise AssertionError("report.py should not write stdout when --markdown is set")
        text = md.read_text(encoding="utf-8")
        if "# Session-mining action brief" not in text:
            raise AssertionError("markdown title missing")
        summary_json = json.loads(summary.read_text(encoding="utf-8"))
        if summary_json["patterns"][0]["verdict"] != "BUILD NOW":
            raise AssertionError("computed verdict missing from summary")
        if len(summary_json["candidates"]) != 1:
            raise AssertionError("candidate shortlist not computed")
        deck_json = json.loads(deck.read_text(encoding="utf-8"))
        if deck_json["meta"]["title"] != "Session-Mining Action Brief":
            raise AssertionError("deck title mismatch")
    print("PASS: action report renders markdown, summary JSON, and deterministic Slidey deck")
    return 0


if __name__ == "__main__":
    sys.exit(run())
