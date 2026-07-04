#!/usr/bin/env python3
"""Deterministic tests for the shared persona-QA completion contract."""

from __future__ import annotations

import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
sys.path.insert(0, str(ROOT))

from tools.persona_qa import from_product_journey_report


failures: list[str] = []


def check(label: str, got, want) -> None:
    if got != want:
        failures.append(f"{label}: got {got!r}, want {want!r}")


ready = from_product_journey_report({
    "review_status": "ready",
    "validation_status": "valid",
    "passed": 19,
    "warnings": 0,
    "failed": 0,
    "total": 19,
    "run_dir": ".artifacts/product-journey/ready",
})
check("ready state", ready.state, "completed")
check("ready verdict", ready.verdict, "solved")
check("ready health", ready.health, "model:result")

partial = from_product_journey_report({
    "review": {"status": "needs_evidence", "summary_counts": {"passed": 15, "warned": 3, "failed": 1, "total": 19}},
    "validation": {"status": "valid"},
    "scenario": {"id": "onboarding"},
    "attached_evidence": [{"path": "cassette://run/onboarding/tui"}],
    "scenario_outcomes_summary": {"scenarios": 5, "started": 1, "blocked": 0},
})
check("partial state", partial.state, "incomplete")
check("partial verdict", partial.verdict, "partial")
check("partial evidence refs", partial.evidence_refs, ["cassette://run/onboarding/tui"])

empty = from_product_journey_report({
    "review": {"status": "needs_evidence", "summary_counts": {"passed": 0, "warned": 0, "failed": 19, "total": 19}},
    "validation": {"status": "valid"},
})
check("empty verdict", empty.verdict, "failed")

if failures:
    print("FAIL: persona-qa completion contract")
    for failure in failures:
        print(f"  - {failure}")
    sys.exit(1)
print("PASS: persona-qa completion contract")
