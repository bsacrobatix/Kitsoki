#!/usr/bin/env python3
"""Runner-level test for active/draft product-journey corpus handling.

Run directly:  python3 tools/product-journey/active_corpus_test.py

The test reads local corpus metadata only. It never calls a live LLM or GitHub.
"""

import importlib.util
import sys
from pathlib import Path

_spec = importlib.util.spec_from_file_location(
    "pj_run", str(Path(__file__).with_name("run.py"))
)
run = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(run)


def _check(name, cond):
    if not cond:
        print(f"FAIL: {name}")
        sys.exit(1)
    print(f"ok: {name}")


def main():
    personas = run.load_personas(run.PERSONAS)
    scenarios = run.load_scenarios(run.SCENARIOS)
    github_targets = run.load_github_targets(run.GITHUB_TARGETS)
    active_personas = run.active_personas(personas)
    active_scenarios = run.active_scenarios(scenarios)

    expected_active_scenarios = {
        "product-discovery",
        "project-onboarding",
        "tui-slash-commands",
        "bugfix",
        "prd-design",
        "feature-implementation",
        "evidence-backed-product-bug",
    }

    _check("full persona corpus keeps draft backlog", len(personas) == 11)
    _check("full scenario corpus keeps mined backlog", len(scenarios) == 25)
    _check("active persona corpus is runnable", len(active_personas) == 5)
    _check("active scenario corpus is runnable", len(active_scenarios) == 7)
    _check("active scenarios are the natural-use contract", {item["id"] for item in active_scenarios} == expected_active_scenarios)
    _check("mined scenarios are draft only", not any(item["id"].startswith("mined-scn-") for item in active_scenarios))

    result = run.validate_journey_corpus(personas, scenarios, github_targets)
    _check("full corpus validation passes with draft warnings", result["status"] == "valid")
    _check("full corpus validation has no errors", result["errors"] == 0)
    _check("validation reports active personas", result["personas"] == 5)
    _check("validation reports active scenarios", result["scenarios"] == 7)
    _check("validation reports all personas", result["all_personas"] == 11)
    _check("validation reports all scenarios", result["all_scenarios"] == 25)
    _check("validation reports draft personas", result["draft_personas"] == 6)
    _check("validation reports draft scenarios", result["draft_scenarios"] == 18)
    _check("validation includes draft warnings", {"draft-personas", "draft-scenarios"} <= {issue["id"] for issue in result["issues"]})

    print("PASS")


if __name__ == "__main__":
    main()
