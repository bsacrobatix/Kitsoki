#!/usr/bin/env python3
"""Regression for product-journey native gitops boundary validation."""

import importlib.util
import tempfile
from pathlib import Path

_spec = importlib.util.spec_from_file_location(
    "pj_run", str(Path(__file__).with_name("run.py"))
)
run = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(run)


def check(name, condition, failures):
    if condition:
        print(f"ok: {name}")
    else:
        print(f"FAIL: {name}")
        failures.append(name)


def main():
    failures = []
    with tempfile.TemporaryDirectory() as td:
        root = Path(td)
        good = root / "good.md"
        bad = root / "bad.md"
        readme = root / "readme.md"
        story_readme = root / "story-readme.md"
        good.write_text("Never file findings with raw `gh issue create`; use kitsoki gitops autonomous-fix.\n", encoding="utf-8")
        bad.write_text("File the finding with `gh issue create` after capture.\n", encoding="utf-8")
        readme.write_text("Use kitsoki gitops autonomous-fix for issue-to-fix gates.\n", encoding="utf-8")
        story_readme.write_text("Use the product-journey story autonomous_fix gate.\n", encoding="utf-8")

        original_driver = run.DRIVER_AGENT
        original_skill = run.PRODUCT_JOURNEY_SKILL
        original_readme = run.PRODUCT_JOURNEY_README
        try:
            run.DRIVER_AGENT = good
            run.PRODUCT_JOURNEY_SKILL = readme
            run.PRODUCT_JOURNEY_README = story_readme
            issues = []
            run.validate_native_gitops_boundaries(issues)
            check("explicit raw gh prohibition is allowed", not issues, failures)

            run.DRIVER_AGENT = bad
            issues = []
            run.validate_native_gitops_boundaries(issues)
            check(
                "raw gh filing guidance fails the corpus boundary",
                any(issue.get("id") == "native-gitops-boundary" for issue in issues),
                failures,
            )
        finally:
            run.DRIVER_AGENT = original_driver
            run.PRODUCT_JOURNEY_SKILL = original_skill
            run.PRODUCT_JOURNEY_README = original_readme

    if failures:
        raise SystemExit(1)
    print("PASS")


if __name__ == "__main__":
    main()
