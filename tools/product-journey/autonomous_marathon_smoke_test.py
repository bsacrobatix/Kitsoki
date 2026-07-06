#!/usr/bin/env python3
"""No-LLM autonomous product-QA marathon smoke.

This is the smallest deterministic version of the standing marathon loop:
create a scoped persona run, attach cassette/local driver proof, record a
credible issue, run the native gitops autonomous-fix facade, and derive stats
from the filed issue state. It uses the same fake Kitsoki backend as the
runner-level filing tests, so it never calls a real LLM or GitHub.
"""

import importlib.util
import json
import os
import stat
import subprocess
import sys
import tempfile
from pathlib import Path

_run_spec = importlib.util.spec_from_file_location(
    "pj_run", str(Path(__file__).with_name("run.py"))
)
run = importlib.util.module_from_spec(_run_spec)
_run_spec.loader.exec_module(run)

_filing_spec = importlib.util.spec_from_file_location(
    "pj_file_findings_test", str(Path(__file__).with_name("file_findings_test.py"))
)
filing_test = importlib.util.module_from_spec(_filing_spec)
_filing_spec.loader.exec_module(filing_test)


def check(name: str, cond: bool, failures: list[str]) -> None:
    if cond:
        print(f"ok: {name}")
        return
    print(f"FAIL: {name}")
    failures.append(name)


def write_issue_state(path: Path, issue_urls: list[str]) -> None:
    payload = [
        {
            "url": issue_url,
            "state": "closed",
            "comments": [
                {
                    "body": (
                        "Fixed in:\n\n"
                        "- `abc1234` Autonomous marathon smoke\n\n"
                        "<!-- kitsoki-fixed-in\n"
                        "commits:\n"
                        "  - abc1234\n"
                        "verified:\n"
                        "  - independent verify passed\n"
                        "-->"
                    )
                }
            ],
        }
        for issue_url in issue_urls
    ]
    path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def main() -> int:
    failures: list[str] = []
    with tempfile.TemporaryDirectory() as tmp_name:
        tmp = Path(tmp_name)
        run.ARTIFACT_ROOT = tmp / "product-journey"
        run.ARTIFACT_ROOT.mkdir(parents=True)

        fake = tmp / "fake_kitsoki.py"
        fake.write_text(filing_test.FAKE_KITSOKI, encoding="utf-8")
        fake.chmod(fake.stat().st_mode | stat.S_IXUSR)

        catalog = run.load_catalog(run.CATALOG)
        personas = run.load_personas(run.PERSONAS)
        scenarios = run.select_scenarios(
            run.active_scenarios(run.load_scenarios(run.SCENARIOS)),
            "core-use-cases",
        )
        run_dir, run_json = run.build_run_bundle(
            catalog,
            run.load_github_targets(run.GITHUB_TARGETS),
            personas,
            scenarios,
            "vscode",
            "core-maintainer",
            "autonomous-marathon-smoke",
            "dry-run",
            None,
        )
        driver_result = run.attach_autonomous_marathon_replay_driver(run_dir, run_json, None)

        proc = subprocess.run(
            [
                "go", "run", "./cmd/kitsoki", "gitops", "autonomous-fix",
                "--json",
                "--report-invalid-autonomous-fix",
                "--run-dir", str(run_dir),
                "--ticket-repo", "o/r",
                "--agent-db", str(tmp / "autonomous-marathon-gh-agent.json"),
                "--public-base-url", "https://agent.example",
            ],
            cwd=run.ROOT,
            env={
                **os.environ,
                "KITSOKI_BIN": f"{sys.executable} {fake}",
                "KITSOKI_GITOPS_AUTOFIX_USE_KITSOKI_BIN_FAKE": "1",
            },
            text=True,
            capture_output=True,
            check=False,
        )
        check("native gitops autonomous-fix facade exits cleanly", proc.returncode == 0, failures)
        if proc.returncode != 0:
            print(proc.stdout)
            print(proc.stderr)
            return 1
        result = json.loads(proc.stdout)

        findings = run.read_json(run_dir / "findings.json")
        issue_urls = [
            item.get("github_issue", {}).get("url", "")
            for item in findings.get("items", [])
            if item.get("kind") == "issue" and item.get("origin", "observed") != "seeded"
        ]
        issue_urls = [url for url in issue_urls if url]
        issue_state = tmp / "issue-state.json"
        if issue_urls:
            write_issue_state(issue_state, issue_urls)
        stats_output = tmp / "stats.json"
        stats = run.derive_stats(run.ARTIFACT_ROOT, str(issue_state), 0.82, 25, str(stats_output))
        report_path = Path(result.get("autonomous_fix_report_path", ""))
        report_text = report_path.read_text(encoding="utf-8") if report_path.exists() else ""
        claim_comment_url = result.get("gh_agent_claims", [{}])[0].get("comment_url", "")
        closeout_comment_url = result.get("issue_closeouts", [{}])[0].get("comment_url", "")
        weakness_routes = run.read_json(run_dir / "weakness-routes.json")
        prd_intake = run.read_json(run_dir / "prd-design-intake.json")
        deck_text = (run_dir / "deck.slidey.json").read_text(encoding="utf-8")

        scenario_ids = [scenario.get("id") for scenario in run_json.get("scenarios", [])]
        check("marathon scoped run covers core use cases",
              scenario_ids == ["project-onboarding", "prd-design", "bugfix"],
              failures)
        check("autonomous replay captured every core use case",
              driver_result.get("autonomous_driver_status") == "captured"
              and driver_result.get("autonomous_driver_issue_count") == 3
              and driver_result.get("autonomous_driver_evidence_count", 0) >= 9,
              failures)
        check("credible issue was filed",
              result.get("findings_filed_count") == 3 and len(issue_urls) == 3,
              failures)
        check("gh-agent fix drained with independent verification",
              result.get("gh_agent_drain_status") == "drained"
              and result.get("gh_agent_done_count") == 3
              and result.get("gh_agent_missing_verify_count") == 0
              and result.get("gh_agent_independent_verify_count") == 3
              and result.get("gh_agent_missing_triage_count") == 0
              and result.get("gh_agent_triage_evidence_count") == 3,
              failures)
        check("autonomous gates are all green",
              result.get("autonomous_fix_status") == "autonomous_fix_valid"
              and result.get("autonomous_gate_summary") == "filing=pass, gh_agent=pass, independent_verify=pass, review=pass, validation=pass",
              failures)
        check("human review artifacts link issue, run, fix, and verify",
              issue_urls and issue_urls[0] in report_text
              and "https://agent.example/run/job-" in report_text
              and "fix-report.md" in report_text
              and "triage-verdict.md" in report_text
              and "independent-verify.md" in report_text,
              failures)
        check("human review artifacts link gh-agent claim evidence",
              result.get("gh_agent_claim_status") == "claimed"
              and claim_comment_url
              and claim_comment_url in report_text
              and claim_comment_url in deck_text
              and "Claims: claimed" in deck_text,
              failures)
        check("review deck links issue close-out evidence",
              result.get("issue_closeout_status") == "closed"
              and closeout_comment_url
              and closeout_comment_url in deck_text
              and "Issue close-out: closed" in deck_text,
              failures)
        check("weakness routed to PRD/design review artifact",
              weakness_routes.get("summary", {}).get("routed") == 1
              and weakness_routes.get("items", [{}])[0].get("target_story") == "stories/prd"
              and prd_intake.get("summary", {}).get("intake_count") == 1
              and prd_intake.get("items", [{}])[0].get("story_intent") == "start"
              and prd_intake.get("items", [{}])[0].get("persona_lens", {}).get("evidence_emphasis")
              and "weakness-routes.md" in prd_intake.get("items", [{}])[0].get("story_slots", {}).get("upstream_paths", "")
              and "prd-design-intake.md" in deck_text
              and "PRD/design routes" in deck_text,
              failures)
        check("mechanical stats count found/filed/fixed",
              stats.get("findings_found_count") == 3
              and stats.get("findings_filed_count") == 3
              and stats.get("issues_fixed_count") == 3
              and stats.get("issues_reopened_count") == 0
              and stats.get("manual_stats_replaced") == "yes",
              failures)

        output = {
            "status": "passed" if not failures else "failed",
            "summary": "core use-case autonomous product-QA marathon smoke passed" if not failures else "core use-case autonomous product-QA marathon smoke failed",
            "run_id": run_json["run_id"],
            "run_dir": str(run_dir),
            "deck_path": str(run_dir / "deck.slidey.json"),
            "scenario_ids": scenario_ids,
            "driver_journal_path": str(run_dir / "driver-journal.md"),
            "autonomous_fix_report_path": str(report_path),
            "issue_state_file": str(issue_state),
            "stats_output": str(stats_output),
            "stats_summary": stats.get("stats_summary", ""),
            "filed_issue_count": result.get("findings_filed_count", 0),
            "gh_agent_done_count": result.get("gh_agent_done_count", 0),
            "gh_agent_claim_comment_url": claim_comment_url,
            "gh_agent_independent_verify_count": result.get("gh_agent_independent_verify_count", 0),
            "issue_closeout_status": result.get("issue_closeout_status", ""),
            "issue_closeout_comment_url": closeout_comment_url,
            "autonomous_gate_summary": result.get("autonomous_gate_summary", ""),
            "failures": failures,
        }
        (tmp / "autonomous-marathon-smoke.json").write_text(json.dumps(output, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        if failures:
            print(json.dumps(output, sort_keys=True))
            return 1
        print("PASS")
        return 0


if __name__ == "__main__":
    raise SystemExit(main())
