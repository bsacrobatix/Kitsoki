"""Shared completion-state contract for persona-QA runs.

The product-journey runner has rich native artifacts (`review.json`,
`validation`, `scenario-outcomes.json`, driver journals, decks). The arena needs
one stable, job-agnostic object it can score without knowing those artifact
details. This module is that bridge: product-journey, stories, MCP adapters, and
arena plugins can all exchange the same compact completion state.
"""

from __future__ import annotations

import json
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Any


STATES = ("completed", "incomplete", "blocked", "error")

# The versioned contract this module emits — schemas/completion-state.schema.json.
# Bump the major segment only on a breaking field change; arena's plugins and
# bench.py's writer share the same version string.
SCHEMA_VERSION = "1.0.0"


@dataclass(frozen=True)
class CompletionState:
    """Normalized result for a persona-QA cell or run bundle.

    Conforms to the shared arena/persona-QA contract at
    schemas/completion-state.schema.json — `to_dict()`/`to_json()` are safe to
    hand to any consumer of that schema (e.g. an arena job-type plugin), and
    `schema_version` lets a consumer reject a payload written against a future,
    incompatible version of the contract.
    """

    state: str
    verdict: str
    health: str
    summary: str
    schema_version: str = SCHEMA_VERSION
    run_dir: str = ""
    deck_path: str = ""
    review_status: str = "not_reviewed"
    validation_status: str = "unknown"
    checks_passed: int = 0
    checks_warned: int = 0
    checks_failed: int = 0
    checks_total: int = 0
    scenarios_total: int = 0
    scenarios_started: int = 0
    scenarios_blocked: int = 0
    proof_minimum_evidence_count: int = 0
    minimum_evidence_count: int = 0
    evidence_refs: list[str] = field(default_factory=list)
    blockers: list[str] = field(default_factory=list)
    # Which proof class this state grades (schemas/completion-state.schema.json
    # `check_type`, WS-G G1). "" (the default) means "not applicable" — the
    # product-journey report bridge below never sets it (a run bundle IS the
    # journey, not a graded check over one). Adapters that grade an already-
    # judged artifact (e.g. a kitsoki-ui-qa/ui-review verdict.json) set this
    # explicitly to "journey-verdict"/"ux-heuristic" so an arena check-suite
    # consumer knows which check produced the verdict.
    check_type: str = ""

    def __post_init__(self) -> None:
        if self.state not in STATES:
            raise ValueError(f"unknown completion state {self.state!r}; want one of {STATES}")

    def to_dict(self) -> dict[str, Any]:
        data = asdict(self)
        if not self.check_type:
            # Omit rather than emit "" — mirrors CellResult.to_dict()'s
            # omit-the-default convention so a bare completion-state (no
            # declared check_type) stays byte-identical to pre-G6 payloads.
            data.pop("check_type", None)
        # The shared contract (schemas/completion-state.schema.json) requires a
        # `metrics` object on every completion-state, bugfix and persona-qa
        # alike. persona-qa's "metrics" are its check/scenario/proof counters —
        # nest them here so a generic consumer (e.g. an arena job-type plugin)
        # finds them at the same key regardless of workload; the flat fields
        # stay too, for callers that already read them directly.
        data["metrics"] = {
            "checks_passed": self.checks_passed,
            "checks_warned": self.checks_warned,
            "checks_failed": self.checks_failed,
            "checks_total": self.checks_total,
            "scenarios_total": self.scenarios_total,
            "scenarios_started": self.scenarios_started,
            "scenarios_blocked": self.scenarios_blocked,
            "proof_minimum_evidence_count": self.proof_minimum_evidence_count,
            "minimum_evidence_count": self.minimum_evidence_count,
        }
        return data

    def to_json(self) -> str:
        return json.dumps(self.to_dict(), sort_keys=True)


def from_product_journey_report(report: dict[str, Any]) -> CompletionState:
    """Build a completion state from a product-journey report or story summary."""

    review = report.get("review", {}) or {}
    validation = report.get("validation", {}) or {}
    run = report.get("run", {}) or {}
    scenario = report.get("scenario", {}) or {}
    scenario_id = scenario if isinstance(scenario, str) else scenario.get("id", "")

    review_status = (
        report.get("review_status")
        or review.get("review_status")
        or review.get("status")
        or "not_reviewed"
    )
    validation_status = (
        report.get("validation_status")
        or validation.get("status")
        or validation.get("run", {}).get("status")
        or "unknown"
    )
    counts = review.get("summary_counts", {}) or {}
    passed = int(report.get("review_passed", report.get("passed", review.get("review_passed_count", counts.get("passed", 0)))) or 0)
    warned = int(report.get("review_warnings", report.get("warnings", review.get("review_warning_count", counts.get("warned", 0)))) or 0)
    failed = int(report.get("review_failed", report.get("failed", review.get("review_failed_count", counts.get("failed", 0)))) or 0)
    total = int(report.get("review_total", report.get("total", review.get("review_total_count", counts.get("total", 0)))) or 0)

    scenario_summary = report.get("scenario_outcomes_summary", review.get("scenario_outcomes_summary", {})) or {}
    scenarios_total = int(scenario_summary.get("scenarios", 0) or 0)
    scenarios_started = int(scenario_summary.get("started", 0) or 0)
    scenarios_blocked = int(scenario_summary.get("blocked", 0) or 0)

    quality_gates = report.get("quality_gates") or review.get("quality_gates") or []
    proof_minimum = sum(int(gate.get("proof_minimum_evidence_count", 0) or 0) for gate in quality_gates)
    minimum = sum(int(gate.get("minimum_evidence_count", 0) or 0) for gate in quality_gates)
    blockers = _blockers(report, quality_gates)
    evidence_refs = _evidence_refs(report)

    if validation_status == "error" or report.get("status") == "failed" and not review_status:
        state = "error"
        verdict = "blocked"
        health = "infra:harness"
    elif review_status == "ready" and validation_status in {"valid", "unknown"}:
        state = "completed"
        verdict = "solved"
        health = "model:result"
    elif blockers and failed == 0 and scenarios_blocked:
        state = "blocked"
        verdict = "blocked"
        health = "model:result"
    else:
        state = "incomplete"
        verdict = "partial" if evidence_refs or scenarios_started or passed else "failed"
        health = "model:result"

    summary = _summary(
        state=state,
        review_status=review_status,
        validation_status=validation_status,
        passed=passed,
        total=total,
        warned=warned,
        failed=failed,
        scenario_id=scenario_id,
    )
    return CompletionState(
        state=state,
        verdict=verdict,
        health=health,
        summary=summary,
        run_dir=report.get("run_dir") or run.get("run_dir", ""),
        deck_path=report.get("deck_path") or run.get("deck_path", ""),
        review_status=review_status,
        validation_status=validation_status,
        checks_passed=passed,
        checks_warned=warned,
        checks_failed=failed,
        checks_total=total,
        scenarios_total=scenarios_total,
        scenarios_started=scenarios_started,
        scenarios_blocked=scenarios_blocked,
        proof_minimum_evidence_count=proof_minimum,
        minimum_evidence_count=minimum,
        evidence_refs=evidence_refs,
        blockers=blockers,
    )


def load_product_journey_run(run_dir: str | Path) -> CompletionState:
    """Load a run bundle from disk and derive its completion state."""

    path = Path(run_dir)
    report = {
        "run": {
            "run_dir": str(path),
            "deck_path": str(path / "deck.slidey.json"),
        }
    }
    for name, key in [
        ("review.json", "review"),
        ("scenario-outcomes.json", "scenario_outcomes"),
        ("driver-handoff.json", "driver_handoff"),
    ]:
        candidate = path / name
        if candidate.exists():
            report[key] = json.loads(candidate.read_text(encoding="utf-8"))
    if "scenario_outcomes" in report:
        report["scenario_outcomes_summary"] = report["scenario_outcomes"].get("summary", {})
    handoff = report.get("driver_handoff", {})
    status = handoff.get("status", {})
    if status:
        report["quality_gates"] = handoff.get("quality_gates", [])
        report["proof_minimum_evidence_count"] = status.get("proof_minimum_evidence_count", 0)
        report["minimum_evidence_count"] = status.get("minimum_evidence_count", 0)
    return from_product_journey_report(report)


def _blockers(report: dict[str, Any], quality_gates: list[dict[str, Any]]) -> list[str]:
    blockers = []
    for gate in quality_gates:
        if gate.get("blocked"):
            blockers.append(str(gate.get("scenario") or gate.get("label") or "blocked"))
    handoff = report.get("driver_handoff", {}) or {}
    for item in handoff.get("missing_proof_evidence", []) or []:
        scenario = item.get("scenario") or item.get("kind") or "missing-proof"
        blockers.append(f"missing proof: {scenario}")
    return sorted(set(blockers))


def _evidence_refs(report: dict[str, Any]) -> list[str]:
    refs = []
    for item in report.get("attached_evidence", []) or []:
        ref = item.get("path") or item.get("evidence_path")
        if ref:
            refs.append(str(ref))
    for item in report.get("driver_journal_events", []) or []:
        refs.extend(str(ref) for ref in item.get("evidence_refs", []) if ref)
    return sorted(set(refs))


def _summary(
    *,
    state: str,
    review_status: str,
    validation_status: str,
    passed: int,
    total: int,
    warned: int,
    failed: int,
    scenario_id: str,
) -> str:
    scenario = f" scenario={scenario_id};" if scenario_id else ""
    checks = f"{passed}/{total}" if total else "0/0"
    return (
        f"{state}:{scenario} review={review_status}; validation={validation_status}; "
        f"checks={checks} passed, {warned} warnings, {failed} failures"
    )
