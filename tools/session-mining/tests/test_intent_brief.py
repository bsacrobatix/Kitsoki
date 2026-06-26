#!/usr/bin/env python3
"""Tests for intent_brief.py artifact outputs. No LLM, no network."""

import json
import subprocess
import sys
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parents[3]
TOOL = ROOT / "tools" / "session-mining" / "intent_brief.py"


def run():
    intents = {
        "job": "intent-job",
        "total_intents": 1,
        "tags": {"action": {"fix": 1}, "surface": {"cli": 1}, "scope": {"test": 1}},
        "intents": [{
            "instance_id": "i1",
            "user_text": "fix the failing test",
            "tags": {"action": ["fix"], "surface": ["cli"]},
        }],
    }
    analysis = {
        "instances": [{
            "instance_id": "i1",
            "determinism": "agent-gated",
            "measured": {"tool_calls": 2, "edit_rerun_cycles": 1, "retries": 0},
            "grounding": {"actions_cited": 2, "actions_validated": 1},
            "actions": [{"signature": "go test"}, {"signature": "edit file"}],
            "agent_gates": [{"decision": "code vs test", "validator": "regression goes green"}],
        }],
        "clusters": [{"count": 2, "key": "fix failing tests"}],
    }
    with tempfile.TemporaryDirectory() as tmp:
        tmp_path = Path(tmp)
        intents_path = tmp_path / "intents.json"
        analysis_path = tmp_path / "analysis.json"
        md = tmp_path / "BRIEF.md"
        summary = tmp_path / "intent.summary.json"
        deck = tmp_path / "deck.slidey.json"
        intents_path.write_text(json.dumps(intents), encoding="utf-8")
        analysis_path.write_text(json.dumps(analysis), encoding="utf-8")
        proc = subprocess.run(
            [
                sys.executable,
                str(TOOL),
                "--intents", str(intents_path),
                "--analysis", str(analysis_path),
                "--markdown", str(md),
                "--summary", str(summary),
                "--slidey-spec", str(deck),
            ],
            cwd=ROOT,
            text=True,
            capture_output=True,
            check=True,
        )
        if proc.stdout:
            raise AssertionError("intent_brief.py should not write stdout when --markdown is set")
        if "# Session-mining intent brief" not in md.read_text(encoding="utf-8"):
            raise AssertionError("markdown title missing")
        summary_json = json.loads(summary.read_text(encoding="utf-8"))
        if summary_json["determinism_counts"]["agent-gated"] != 1:
            raise AssertionError("determinism count missing")
        if summary_json["intents"][0]["agent_gates"] != ["code vs test — regression goes green"]:
            raise AssertionError("agent gate summary missing")
        deck_json = json.loads(deck.read_text(encoding="utf-8"))
        if deck_json["meta"]["title"] != "Session-Mining Intent Brief":
            raise AssertionError("deck title mismatch")
    print("PASS: intent brief renders markdown, summary JSON, and deterministic Slidey deck")
    return 0


if __name__ == "__main__":
    sys.exit(run())
