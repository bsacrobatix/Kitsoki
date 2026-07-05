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


def write_issue_state(path: Path, issue_url: str) -> None:
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
        scenarios = [
            scenario
            for scenario in run.load_scenarios(run.SCENARIOS)
            if scenario.get("id") == "bugfix"
        ]
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
        scenario_id = run_json["scenarios"][0]["id"]
        filing_test.attach_bugfix_proof(run_dir, scenario_id)
        run.record_driver_event(
            run_dir,
            scenario_id,
            "replay",
            "captured",
            "Autonomous marathon smoke replay captured the scoped bugfix journey proof.",
            "session.open,session.trace,render.tui,visual.observe",
            str(run_dir / "test-evidence" / "trace-replay.md"),
            "",
            None,
        )
        run.record_finding(
            run_dir,
            "strength",
            "Scoped persona journey produced reviewable proof",
            "The marathon smoke attached local proof artifacts for the selected scenario only.",
            scenario_id,
            "low",
            str(run_dir / "test-evidence" / "oracle_result.md"),
            "observed",
            None,
        )
        run.record_finding(
            run_dir,
            "weakness",
            "Single-scenario marathon slice leaves broader coverage for cadence",
            "The smoke proves the autonomous loop for one scoped issue; full cadence expands the matrix.",
            scenario_id,
            "medium",
            str(run_dir / "driver-plan.md"),
            "open",
            None,
        )
        run.record_finding(
            run_dir,
            "issue",
            "Autonomous marathon should file and fix persona QA issues",
            "A credible persona QA issue should be filed with evidence, fixed by gh-agent, independently verified, and counted mechanically.",
            scenario_id,
            "high",
            str(run_dir / "test-evidence" / "trace-replay.md"),
            "open",
            None,
        )

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
            write_issue_state(issue_state, issue_urls[0])
        stats_output = tmp / "stats.json"
        stats = run.derive_stats(run.ARTIFACT_ROOT, str(issue_state), 0.82, 25, str(stats_output))
        report_path = Path(result.get("autonomous_fix_report_path", ""))
        report_text = report_path.read_text(encoding="utf-8") if report_path.exists() else ""
        claim_comment_url = result.get("gh_agent_claims", [{}])[0].get("comment_url", "")
        closeout_comment_url = result.get("issue_closeouts", [{}])[0].get("comment_url", "")
        weakness_routes = run.read_json(run_dir / "weakness-routes.json")
        deck_text = (run_dir / "deck.slidey.json").read_text(encoding="utf-8")

        check("marathon scoped run has one scenario",
              len(run_json.get("scenarios", [])) == 1 and scenario_id == "bugfix",
              failures)
        check("credible issue was filed",
              result.get("findings_filed_count") == 1 and len(issue_urls) == 1,
              failures)
        check("gh-agent fix drained with independent verification",
              result.get("gh_agent_drain_status") == "drained"
              and result.get("gh_agent_done_count") == 1
              and result.get("gh_agent_missing_verify_count") == 0
              and result.get("gh_agent_independent_verify_count") == 1,
              failures)
        check("autonomous gates are all green",
              result.get("autonomous_fix_status") == "autonomous_fix_valid"
              and result.get("autonomous_gate_summary") == "filing=pass, gh_agent=pass, review=pass, validation=pass",
              failures)
        check("human review artifacts link issue, run, fix, and verify",
              issue_urls and issue_urls[0] in report_text
              and "https://agent.example/run/job-" in report_text
              and "fix-report.md" in report_text
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
              and "PRD/design routes" in deck_text,
              failures)
        check("mechanical stats count found/filed/fixed",
              stats.get("findings_found_count") == 1
              and stats.get("findings_filed_count") == 1
              and stats.get("issues_fixed_count") == 1
              and stats.get("issues_reopened_count") == 0
              and stats.get("manual_stats_replaced") == "yes",
              failures)

        output = {
            "status": "passed" if not failures else "failed",
            "summary": "autonomous product-QA marathon smoke passed" if not failures else "autonomous product-QA marathon smoke failed",
            "run_id": run_json["run_id"],
            "run_dir": str(run_dir),
            "deck_path": str(run_dir / "deck.slidey.json"),
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
