#!/usr/bin/env python3
"""Runner-level test for --autonomous-marathon.

The fake KITSOKI_BIN comes from file_findings_test.py, so this test does not
call GitHub or a real LLM. It proves the marathon wrapper creates a bounded run
and later finalizes that run through the native issue filing and gh-agent gate.
"""

import importlib.util
import os
import stat
import sys
import tempfile
from pathlib import Path

_spec = importlib.util.spec_from_file_location(
    "pj_run", str(Path(__file__).with_name("run.py"))
)
run = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(run)

_filing_spec = importlib.util.spec_from_file_location(
    "file_findings_test", str(Path(__file__).with_name("file_findings_test.py"))
)
filing_test = importlib.util.module_from_spec(_filing_spec)
_filing_spec.loader.exec_module(filing_test)


def check(label: str, condition: bool, failures: list[str]) -> None:
    if condition:
        print(f"ok: {label}")
    else:
        print(f"not ok: {label}")
        failures.append(label)


def main() -> int:
    failures: list[str] = []
    with tempfile.TemporaryDirectory() as tmp_name:
        tmp = Path(tmp_name)
        run.ARTIFACT_ROOT = tmp / "product-journey"
        run.ARTIFACT_ROOT.mkdir(parents=True)

        fake = tmp / "fake_kitsoki.py"
        fake.write_text(filing_test.FAKE_KITSOKI, encoding="utf-8")
        fake.chmod(fake.stat().st_mode | stat.S_IXUSR)

        old_env = {
            "KITSOKI_BIN": os.environ.get("KITSOKI_BIN"),
            "KITSOKI_GITOPS_AUTOFIX_USE_KITSOKI_BIN_FAKE": os.environ.get("KITSOKI_GITOPS_AUTOFIX_USE_KITSOKI_BIN_FAKE"),
        }
        os.environ["KITSOKI_BIN"] = f"{sys.executable} {fake}"
        os.environ["KITSOKI_GITOPS_AUTOFIX_USE_KITSOKI_BIN_FAKE"] = "1"
        try:
            catalog = run.load_catalog(run.CATALOG)
            github_targets = run.load_github_targets(run.GITHUB_TARGETS)
            personas = run.load_personas(run.PERSONAS)
            scenarios = run.load_scenarios(run.SCENARIOS)

            created = run.autonomous_marathon(
                catalog,
                github_targets,
                personas,
                scenarios,
                None,
                "vscode",
                "core-maintainer",
                "autonomous-marathon-test",
                "bugfix",
                7,
                "",
                "",
                "stories/bugfix",
                "",
                "",
                "",
                "",
                "none",
                "",
                "",
                "",
                0.82,
                25,
                "pending",
                24,
                15,
                45,
                None,
            )
            run_dir = Path(created["run_dir"])
            run_json = run.read_json(run_dir / "run.json")
            scenario_id = run_json["scenarios"][0]["id"]
            control_path = Path(created["autonomous_control_path"])
            control_markdown_path = Path(created["autonomous_control_markdown_path"])
            control = run.read_json(control_path)

            check("autonomous marathon creates a bounded run",
                  created["autonomous_marathon_status"] == "autonomous_marathon_ready_for_driver"
                  and run_json["mode"] == "autonomous-marathon"
                  and len(run_json["scenarios"]) == 1
                  and created["live_budget_minutes"] == 7,
                  failures)
            check("creation writes standing-loop control metadata",
                  created["autonomous_control_status"] == "ready_for_driver"
                  and control_path.exists()
                  and control_markdown_path.exists()
                  and "Autonomous Marathon Control" in control_markdown_path.read_text(encoding="utf-8")
                  and control["cadence"]["hours"] == 24
                  and control["budget"]["per_scenario_live_minutes"] == 7
                  and control["budget"]["manual_glue_steps_target"] == 0
                  and control["watchdog"]["heartbeat_minutes"] == 15
                  and control["watchdog"]["watchdog_minutes"] == 45,
                  failures)
            check("creation writes a human-reviewable marathon report",
                  Path(created["autonomous_marathon_report_path"]).exists()
                  and "Autonomous Marathon Report" in Path(created["autonomous_marathon_report_path"]).read_text(encoding="utf-8"),
                  failures)

            filing_test.attach_bugfix_proof(run_dir, scenario_id)
            run.record_driver_event(
                run_dir,
                scenario_id,
                "replay",
                "captured",
                "Autonomous marathon test replay captured proof for the bugfix journey.",
                "session.open,session.trace,render.tui,visual.observe",
                str(run_dir / "test-evidence" / "trace-replay.md"),
                "",
                None,
            )
            run.record_finding(
                run_dir,
                "weakness",
                "Scoped marathon still needs wider cadence",
                "A single bounded scenario proves the gate while broader cadence expands coverage.",
                scenario_id,
                "medium",
                str(run_dir / "driver-plan.md"),
                "open",
                None,
            )
            run.record_finding(
                run_dir,
                "issue",
                "Autonomous marathon should file and fix issues",
                "Credible persona QA issues should be filed, queued for gh-agent, verified, and included in stats.",
                scenario_id,
                "high",
                str(run_dir / "test-evidence" / "trace-replay.md"),
                "open",
                None,
            )

            finalized = run.autonomous_marathon(
                catalog,
                github_targets,
                personas,
                scenarios,
                run_dir,
                "vscode",
                "core-maintainer",
                "ignored",
                "",
                7,
                "o/r",
                str(tmp / "gh-agent.json"),
                "stories/bugfix",
                "https://agent.example",
                "",
                "",
                "",
                "none",
                "",
                str(run.ARTIFACT_ROOT),
                str(run_dir / "autonomous-marathon-stats.json"),
                0.82,
                25,
                "pending",
                24,
                15,
                45,
                None,
            )
            report = Path(finalized["autonomous_marathon_report_path"])
            report_text = report.read_text(encoding="utf-8") if report.exists() else ""

            check("finalization runs native autonomous fix gate",
                  finalized["autonomous_marathon_status"] == "autonomous_marathon_valid"
                  and finalized["autonomous_fix_status"] == "autonomous_fix_valid"
                  and finalized["autonomous_gate_summary"] == "filing=pass, gh_agent=pass, independent_verify=pass, review=pass, validation=pass",
                  failures)
            check("marathon report links fix evidence and stats",
                  "autonomous-fix-report.md" in report_text
                  and "autonomous-marathon-stats.json" in report_text
                  and "Stats gate: `pass` - pass" in report_text
                  and "Stats current run scanned: `yes`" in report_text
                  and finalized["stats_found_count"] == 1
                  and finalized["stats_filed_count"] == 1,
                  failures)
            check("weaknesses route to PRD/design during finalization",
                  finalized["weakness_route_count"] == 1
                  and "finding-1->stories/prd" in finalized["weakness_route_summary"],
                  failures)

            missing_stats = run.autonomous_marathon(
                catalog,
                github_targets,
                personas,
                scenarios,
                None,
                "vscode",
                "core-maintainer",
                "autonomous-marathon-missing-stats",
                "bugfix",
                7,
                "o/r",
                str(tmp / "gh-agent-missing-stats.json"),
                "stories/bugfix",
                "https://agent.example",
                "",
                "",
                "",
                "none",
                "",
                str(tmp / "empty-stats-root"),
                "",
                0.82,
                25,
                "replay",
                24,
                15,
                45,
                None,
            )
            check("marathon fails closed when derived stats do not cover the run",
                  missing_stats["autonomous_marathon_status"] == "autonomous_marathon_invalid"
                  and missing_stats["autonomous_fix_status"] == "autonomous_fix_valid"
                  and missing_stats["stats_found_count"] == 0
                  and missing_stats["stats_gate_status"] == "fail"
                  and missing_stats["stats_current_run_scanned"] == "no"
                  and "stats=fail" in missing_stats["autonomous_marathon_summary"],
                  failures)

            unrelated_stats_root = tmp / "unrelated-stats-root"
            unrelated_run = unrelated_stats_root / "run-unrelated"
            unrelated_run.mkdir(parents=True)
            run.write_json(unrelated_run / "findings.json", {
                "items": [
                    {
                        "id": "finding-unrelated",
                        "kind": "issue",
                        "title": "Unrelated fixed persona QA issue",
                        "summary": "This fixed issue belongs to a different run and must not satisfy the current marathon stats gate.",
                        "scenario": "bugfix",
                        "severity": "high",
                        "origin": "observed",
                        "status": "fixed",
                        "github_issue": {
                            "url": "https://github.com/o/r/issues/999",
                            "repo": "o/r",
                            "number": "999",
                            "state": "closed",
                            "comments": [{"body": "kitsoki-fixed-in: unrelated"}],
                        },
                    }
                ]
            })
            unrelated_stats = run.autonomous_marathon(
                catalog,
                github_targets,
                personas,
                scenarios,
                None,
                "vscode",
                "core-maintainer",
                "autonomous-marathon-unrelated-stats",
                "bugfix",
                7,
                "o/r",
                str(tmp / "gh-agent-unrelated-stats.json"),
                "stories/bugfix",
                "https://agent.example",
                "",
                "",
                "",
                "none",
                "",
                str(unrelated_stats_root),
                "",
                0.82,
                25,
                "replay",
                24,
                15,
                45,
                None,
            )
            check("marathon fails closed when aggregate stats omit the current run",
                  unrelated_stats["autonomous_marathon_status"] == "autonomous_marathon_invalid"
                  and unrelated_stats["autonomous_fix_status"] == "autonomous_fix_valid"
                  and unrelated_stats["stats_found_count"] == 1
                  and unrelated_stats["stats_fixed_count"] == 1
                  and unrelated_stats["stats_gate_status"] == "fail"
                  and unrelated_stats["stats_current_run_scanned"] == "no"
                  and "current_run_scanned=no" in unrelated_stats["autonomous_marathon_summary"],
                  failures)

            replay = run.autonomous_marathon(
                catalog,
                github_targets,
                personas,
                scenarios,
                None,
                "vscode",
                "core-maintainer",
                "autonomous-marathon-replay",
                "bugfix",
                7,
                "o/r",
                str(tmp / "gh-agent-replay.json"),
                "stories/bugfix",
                "https://agent.example",
                "",
                "",
                "",
                "none",
                "",
                str(run.ARTIFACT_ROOT),
                "",
                0.82,
                25,
                "replay",
                24,
                15,
                45,
                None,
            )
            replay_dir = Path(replay["run_dir"])
            replay_report = Path(replay["autonomous_marathon_report_path"])
            replay_report_text = replay_report.read_text(encoding="utf-8") if replay_report.exists() else ""
            check("replay mode creates, captures, and finalizes without external attachment",
                  replay["autonomous_marathon_status"] == "autonomous_marathon_valid"
                  and replay["autonomous_driver_status"] == "captured"
                  and replay["autonomous_driver_evidence_count"] >= 5
                  and replay["autonomous_fix_status"] == "autonomous_fix_valid"
                  and replay["stats_filed_count"] >= 1,
                  failures)
            check("replay mode records armed marathon control",
                  replay["autonomous_control_status"] == "armed"
                  and Path(replay["autonomous_control_path"]).exists()
                  and "manual_glue_steps_target" in Path(replay["autonomous_control_path"]).read_text(encoding="utf-8"),
                  failures)
            check("replay mode leaves human-reviewable driver and fix artifacts",
                  (replay_dir / "driver-journal.md").exists()
                  and "autonomous-replay-evidence" in (replay_dir / "driver-journal.md").read_text(encoding="utf-8")
                  and "autonomous-fix-report.md" in replay_report_text
                  and "Autonomous driver: `replay` / `captured`" in replay_report_text,
                  failures)
        finally:
            for key, value in old_env.items():
                if value is None:
                    os.environ.pop(key, None)
                else:
                    os.environ[key] = value

    if failures:
        print("FAIL")
        for failure in failures:
            print(f"- {failure}")
        return 1
    print("PASS")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
