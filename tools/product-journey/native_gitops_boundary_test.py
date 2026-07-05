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
        readme = root / "readme.md"
        story_readme = root / "story-readme.md"
        good.write_text(
            "Never file findings with raw `gh issue create`; use kitsoki gitops autonomous-fix.\n"
            "Do not run `gh issue comment` or `gh issue close`; use kitsoki gitops issue-comment "
            "and kitsoki gitops issue-transition.\n",
            encoding="utf-8",
        )
        bad_create = root / "bad-create.md"
        bad_comment = root / "bad-comment.md"
        bad_close = root / "bad-close.md"
        bad_intent = root / "bad-intent.md"
        bad_create.write_text("File the finding with `gh issue create` after capture.\n", encoding="utf-8")
        bad_comment.write_text("Post the closeout with `gh issue comment` after the fix lands.\n", encoding="utf-8")
        bad_close.write_text("Close the issue with `gh issue close` after verification.\n", encoding="utf-8")
        bad_intent.write_text("Use issue_comment and issue_transition directly from the driver.\n", encoding="utf-8")
        readme.write_text(
            "Use kitsoki gitops autonomous-fix for issue-to-fix gates, or kitsoki gitops "
            "issue-comment / issue-transition for explicit native ticket mutations.\n",
            encoding="utf-8",
        )
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

            cases = [
                ("raw gh filing guidance fails the corpus boundary", bad_create),
                ("raw gh comment guidance fails the corpus boundary", bad_comment),
                ("raw gh close guidance fails the corpus boundary", bad_close),
                ("standalone mutation intent guidance fails the corpus boundary", bad_intent),
            ]
            for label, path in cases:
                run.DRIVER_AGENT = path
                issues = []
                run.validate_native_gitops_boundaries(issues)
                check(
                    label,
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
