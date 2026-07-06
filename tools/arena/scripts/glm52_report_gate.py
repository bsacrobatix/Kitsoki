#!/usr/bin/env python3
"""Validate the GLM-5.2 + BugSwarm report's research claims.

The report generator is allowed to publish partial evidence, but it must not
turn missing cells into apparent wins, token ratios, or BugSwarm readiness. This
gate is offline and deterministic: it reads the JSON report and checks the
claim-level invariants that make the Markdown safe to cite.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any

QUALITY_ATTEMPTED = {"solved", "partial", "failed"}
REQUIRED_COMPARISON_SCOPES = {"overall", "oss-oracle", "bugswarm"}
REQUIRED_CORPUS_TREATMENTS = {
    "oss-oracle|kitsoki",
    "oss-oracle|raw-prompt",
    "bugswarm|kitsoki",
    "bugswarm|raw-prompt",
}


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--report-json", required=True)
    args = parser.parse_args(argv)

    report = json.loads(Path(args.report_json).read_text(encoding="utf-8"))
    problems = validate_report(report)
    if problems:
        print("FAIL: GLM-5.2 report gate")
        for problem in problems:
            print(f"  - {problem}")
        return 1
    print("PASS: GLM-5.2 report gate")
    return 0


def validate_report(report: dict[str, Any]) -> list[str]:
    problems: list[str] = []
    problems.extend(require_top_level(report))
    rows = [row for row in report.get("required_glm52_matrix", []) if isinstance(row, dict)]
    problems.extend(require_matrix(rows))
    problems.extend(require_rollups(report, rows))
    problems.extend(require_comparisons(report))
    problems.extend(require_closure(report, rows))
    problems.extend(require_references(report))
    return problems


def require_top_level(report: dict[str, Any]) -> list[str]:
    problems: list[str] = []
    if report.get("kind") != "glm52_bugswarm_bugfix_report":
        problems.append(f"unexpected report kind: {report.get('kind')!r}")
    for key in ("corpora", "rollups", "comparisons", "evidence_closure", "references"):
        if not isinstance(report.get(key), dict):
            problems.append(f"missing mapping: {key}")
    return problems


def require_matrix(rows: list[dict[str, Any]]) -> list[str]:
    problems: list[str] = []
    seen = {f"{row.get('corpus')}|{row.get('treatment')}" for row in rows}
    missing = sorted(REQUIRED_CORPUS_TREATMENTS - seen)
    if missing:
        problems.append("required GLM matrix missing corpus/treatment bucket(s): " + ", ".join(missing))
    for row in rows:
        if row.get("candidate") != "glm-5.2":
            problems.append(f"matrix row {row.get('corpus')}:{row.get('task')}:{row.get('treatment')} is not glm-5.2")
        quality = row.get("quality")
        tokens = row.get("total_tokens")
        if quality in QUALITY_ATTEMPTED and not isinstance(tokens, int):
            problems.append(f"attempted row lacks token evidence: {row.get('corpus')} {row.get('task')} {row.get('treatment')}")
        if quality == "pending" and tokens is not None:
            problems.append(f"pending row must not carry token evidence: {row.get('corpus')} {row.get('task')} {row.get('treatment')}")
    return problems


def require_rollups(report: dict[str, Any], rows: list[dict[str, Any]]) -> list[str]:
    problems: list[str] = []
    rollups = report.get("rollups") if isinstance(report.get("rollups"), dict) else {}
    by_corpus = rollups.get("glm52_by_corpus_treatment")
    overall = rollups.get("glm52_by_treatment_overall")
    if not isinstance(by_corpus, dict):
        problems.append("missing glm52_by_corpus_treatment rollup")
    elif set(by_corpus) != REQUIRED_CORPUS_TREATMENTS:
        problems.append("glm52_by_corpus_treatment keys do not match required matrix buckets")
    if not isinstance(overall, dict):
        problems.append("missing glm52_by_treatment_overall rollup")
    else:
        for treatment in ("kitsoki", "raw-prompt"):
            if treatment not in overall:
                problems.append(f"overall rollup missing treatment {treatment}")
        pending_rows = sum(1 for row in rows if row.get("quality") == "pending")
        pending_rollup = sum(int(bucket.get("pending") or 0) for bucket in overall.values() if isinstance(bucket, dict))
        if pending_rows != pending_rollup:
            problems.append(f"overall pending count {pending_rollup} does not match matrix pending count {pending_rows}")
    return problems


def require_comparisons(report: dict[str, Any]) -> list[str]:
    comparisons = report.get("comparisons") if isinstance(report.get("comparisons"), dict) else {}
    problems: list[str] = []
    missing = sorted(REQUIRED_COMPARISON_SCOPES - set(comparisons))
    if missing:
        problems.append("missing comparison scope(s): " + ", ".join(missing))
    for scope, comparison in comparisons.items():
        if not isinstance(comparison, dict):
            problems.append(f"comparison {scope!r} is not a mapping")
            continue
        kitsoki = comparison.get("kitsoki") if isinstance(comparison.get("kitsoki"), dict) else {}
        raw = comparison.get("raw_prompt") if isinstance(comparison.get("raw_prompt"), dict) else {}
        complete = kitsoki.get("attempted", 0) > 0 and raw.get("attempted", 0) > 0
        if complete:
            if comparison.get("status") != "complete":
                problems.append(f"comparison {scope} should be complete when both arms have attempted cells")
            if comparison.get("success_rate_delta") is None:
                problems.append(f"comparison {scope} is complete but lacks success_rate_delta")
            if kitsoki.get("total_tokens") is not None and raw.get("total_tokens") is not None and comparison.get("token_ratio_kitsoki_to_raw") is None:
                problems.append(f"comparison {scope} is complete but lacks token_ratio_kitsoki_to_raw")
        else:
            if comparison.get("status") != "pending":
                problems.append(f"comparison {scope} must remain pending until both arms have attempted cells")
            if comparison.get("success_rate_delta") is not None:
                problems.append(f"comparison {scope} must not publish success delta while pending")
            if comparison.get("token_ratio_kitsoki_to_raw") is not None:
                problems.append(f"comparison {scope} must not publish token ratio while pending")
    return problems


def require_closure(report: dict[str, Any], rows: list[dict[str, Any]]) -> list[str]:
    closure = report.get("evidence_closure") if isinstance(report.get("evidence_closure"), dict) else {}
    actions = closure.get("actions") if isinstance(closure.get("actions"), list) else []
    action_by_corpus = {action.get("corpus"): action for action in actions if isinstance(action, dict)}
    problems: list[str] = []
    pending_rows = [row for row in rows if row.get("quality") == "pending"]
    if closure.get("pending_cell_count") != len(pending_rows):
        problems.append("evidence closure pending_cell_count does not match matrix")
    for corpus in ("oss-oracle", "bugswarm"):
        if corpus not in action_by_corpus:
            problems.append(f"evidence closure missing action for {corpus}")
    bugswarm = report.get("corpora", {}).get("bugswarm", {}) if isinstance(report.get("corpora"), dict) else {}
    bugswarm_action = action_by_corpus.get("bugswarm", {})
    if bugswarm.get("imported_task_count", 0) > 0 and bugswarm.get("verified_task_count", 0) == 0:
        if bugswarm_action.get("status") != "needs-execute-verification":
            problems.append("BugSwarm imported-but-unverified source must be gated as needs-execute-verification")
    return problems


def require_references(report: dict[str, Any]) -> list[str]:
    refs = report.get("references") if isinstance(report.get("references"), dict) else {}
    problems: list[str] = []
    local_paths = {ref.get("path") for ref in refs.get("local_evidence", []) if isinstance(ref, dict)}
    for required in ("tools/bugfix-bakeoff/results/cells", "tools/arena/corpus/cost-bench.manifest.yaml", "tools/arena/corpus/sources.yaml"):
        if required not in local_paths:
            problems.append(f"references missing local evidence path {required}")
    upstream_urls = {ref.get("url") for ref in refs.get("upstream", []) if isinstance(ref, dict)}
    for required in ("https://www.bugswarm.org/", "https://github.com/BugSwarm/client", "https://www.bugswarm.org/docs/toolset/bugswarm-rest-api/"):
        if required not in upstream_urls:
            problems.append(f"references missing upstream URL {required}")
    if not refs.get("bugswarm_seed"):
        problems.append("references missing BugSwarm seed provenance")
    return problems


if __name__ == "__main__":
    raise SystemExit(main())
