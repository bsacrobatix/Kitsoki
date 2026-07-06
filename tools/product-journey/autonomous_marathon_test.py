#!/usr/bin/env python3
"""Runner-level test for --autonomous-marathon.

The fake KITSOKI_BIN comes from file_findings_test.py, so this test does not
call GitHub or a real LLM. It proves the marathon wrapper creates a bounded run
and later finalizes that run through the native issue filing and gh-agent gate.
"""

import importlib.util
import http.server
import os
import stat
import sys
import tempfile
import threading
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


class HealthHandler(http.server.BaseHTTPRequestHandler):
    body = b"ok"
    status = 200
    ready_body = b'{"status":"ready","repo":"o/r","public_base_url":"","worker":"test-worker","drain_enabled":true}'
    ready_status = 200

    def do_GET(self) -> None:
        if self.path == "/api/ready":
            self.send_response(self.ready_status)
            self.end_headers()
            self.wfile.write(self.ready_body)
            return
        if self.path != "/healthz":
            self.send_response(404)
            self.end_headers()
            return
        self.send_response(self.status)
        self.end_headers()
        self.wfile.write(self.body)

    def log_message(self, _format: str, *_args: object) -> None:
        return


def start_health_server(body: bytes = b"ok", status: int = 200, ready_body=None, ready_status: int = 200):
    attrs = {"body": body, "status": status, "ready_status": ready_status}
    if ready_body is not None:
        attrs["ready_body"] = ready_body
    handler = type("TestHealthHandler", (HealthHandler,), attrs)
    server = http.server.ThreadingHTTPServer(("127.0.0.1", 0), handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    return server, f"http://127.0.0.1:{server.server_port}"


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
            healthy_server, healthy_url = start_health_server()
            unhealthy_server, unhealthy_url = start_health_server(b"not-ok")
            wrong_repo_server, wrong_repo_url = start_health_server(
                ready_body=b'{"status":"ready","repo":"other/repo","public_base_url":"","worker":"test-worker","drain_enabled":true}'
            )

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
                "o/r",
                "",
                "stories/bugfix",
                healthy_url,
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
            expected_final_gates = [
                "autonomous_watchdog",
                "autonomous_fix ticket_repo=<owner/repo> gh_agent_public_base_url=<public-gh-agent-url>",
                "review",
                "validate",
            ]
            execution_plan = run.read_json(run_dir / "execution-plan.json")
            driver_plan = run.read_json(run_dir / "driver-plan.json")
            agent_brief = run.read_json(run_dir / "agent-brief.json")
            driver_handoff = run.read_json(run_dir / "driver-handoff.json")
            loaded_summary = run.summarize_run_bundle(run_dir)

            check("autonomous marathon creates a bounded run",
                  created["autonomous_marathon_status"] == "autonomous_marathon_ready_for_driver"
                  and run_json["mode"] == "autonomous-marathon"
                  and len(run_json["scenarios"]) == 1
                  and created["live_budget_minutes"] == 7
                  and created["gh_agent_health_status"] == "pass"
                  and created["gh_agent_readiness_status"] == "pass"
                  and "/api/ready" in created["gh_agent_readiness_summary"],
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
                  and control["watchdog"]["watchdog_minutes"] == 45
                  and control["final_gates"][:2] == ["autonomous_watchdog", "autonomous_fix"],
                  failures)
            check("creation writes a human-reviewable marathon report",
                  Path(created["autonomous_marathon_report_path"]).exists()
                  and "Autonomous Marathon Report" in Path(created["autonomous_marathon_report_path"]).read_text(encoding="utf-8"),
                  failures)
            check("generated driver contract advertises watchdog before autonomous fix",
                  execution_plan["finalize_commands"][-4:] == expected_final_gates
                  and driver_plan["final_gates"] == expected_final_gates
                  and agent_brief["finalize_commands"][-4:] == expected_final_gates
                  and driver_handoff["finalize_commands"] == expected_final_gates
                  and loaded_summary["driver_final_gates"] == expected_final_gates
                  and "4 final gates" in loaded_summary["driver_contract_summary"],
                  failures)

            live_ready = run.autonomous_marathon(
                catalog,
                github_targets,
                personas,
                scenarios,
                None,
                "vscode",
                "core-maintainer",
                "autonomous-marathon-live-driver",
                "bugfix",
                7,
                "o/r",
                "",
                "stories/bugfix",
                healthy_url,
                "",
                "",
                "",
                "none",
                "",
                "",
                "",
                0.82,
                25,
                "live",
                24,
                15,
                45,
                None,
            )
            check("live driver mode creates a story-dispatchable bundle",
                  live_ready["autonomous_marathon_status"] == "autonomous_marathon_ready_for_driver"
                  and live_ready["autonomous_driver_mode"] == "live"
                  and live_ready["autonomous_driver_status"] == "ready_for_dispatch"
                  and live_ready["autonomous_control_status"] == "ready_for_driver"
                  and "driver=ready" in live_ready["autonomous_gate_summary"]
                  and Path(live_ready["autonomous_marathon_report_path"]).exists(),
                  failures)
            live_dispatch = run.record_autonomous_driver_dispatch(
                Path(live_ready["run_dir"]),
                "live",
                "captured",
                "Story-owned live driver captured proof and recorded a credible issue.",
                5,
                1,
                str(Path(live_ready["run_dir"]) / "driver-trace.jsonl"),
                "",
            )
            live_summary = run.run_story_summary(Path(live_ready["run_dir"]))
            live_receipt_text = Path(live_summary["autonomous_driver_dispatch_markdown_path"]).read_text(encoding="utf-8")
            check("live driver dispatch writes durable review receipt",
                  live_dispatch["schema"] == "kitsoki/product-journey-autonomous-driver-dispatch/v1"
                  and Path(live_summary["autonomous_driver_dispatch_path"]).exists()
                  and Path(live_summary["autonomous_driver_dispatch_markdown_path"]).exists()
                  and live_summary["autonomous_driver_dispatch_status"] == "captured"
                  and live_summary["autonomous_driver_dispatch_trace"].endswith("driver-trace.jsonl")
                  and "Autonomous Driver Dispatch" in live_receipt_text,
                  failures)

            missing_ticket = run.autonomous_marathon(
                catalog,
                github_targets,
                personas,
                scenarios,
                None,
                "vscode",
                "core-maintainer",
                "autonomous-marathon-missing-ticket",
                "bugfix",
                7,
                "",
                "",
                "stories/bugfix",
                healthy_url,
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
            check("live pending marathon refuses missing ticket repo before handoff",
                  missing_ticket["autonomous_marathon_status"] == "autonomous_marathon_invalid"
                  and missing_ticket["validation_issue_summary"] == "ticket-repo-required"
                  and missing_ticket["autonomous_control_status"] == "not_run",
                  failures)

            unhealthy = run.autonomous_marathon(
                catalog,
                github_targets,
                personas,
                scenarios,
                None,
                "vscode",
                "core-maintainer",
                "autonomous-marathon-unhealthy-gh-agent",
                "bugfix",
                7,
                "o/r",
                "",
                "stories/bugfix",
                unhealthy_url,
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
            check("live pending marathon refuses unhealthy gh-agent before handoff",
                  unhealthy["autonomous_marathon_status"] == "autonomous_marathon_invalid"
                  and unhealthy["validation_issue_summary"] == "gh-agent-health"
                  and unhealthy["gh_agent_health_status"] == "fail"
                  and unhealthy["autonomous_control_status"] == "not_run",
                  failures)

            wrong_repo = run.autonomous_marathon(
                catalog,
                github_targets,
                personas,
                scenarios,
                None,
                "vscode",
                "core-maintainer",
                "autonomous-marathon-wrong-gh-agent",
                "bugfix",
                7,
                "o/r",
                "",
                "stories/bugfix",
                wrong_repo_url,
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
            check("live pending marathon refuses gh-agent for another repo before handoff",
                  wrong_repo["autonomous_marathon_status"] == "autonomous_marathon_invalid"
                  and wrong_repo["validation_issue_summary"] == "gh-agent-readiness"
                  and wrong_repo["gh_agent_health_status"] == "pass"
                  and wrong_repo["gh_agent_readiness_status"] == "fail"
                  and "/api/ready" in wrong_repo["gh_agent_readiness_summary"]
                  and wrong_repo["autonomous_control_status"] == "not_run",
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
                  and "finding-1->stories/prd" in finalized["weakness_route_summary"]
                  and finalized["prd_design_intake_count"] == 1
                  and "finding-1->stories/prd start" in finalized["prd_design_intake_summary"]
                  and Path(finalized["prd_design_intake_path"]).exists(),
                  failures)

            stale = run.autonomous_marathon(
                catalog,
                github_targets,
                personas,
                scenarios,
                None,
                "vscode",
                "core-maintainer",
                "autonomous-marathon-stale-watchdog",
                "bugfix",
                7,
                "o/r",
                "",
                "stories/bugfix",
                healthy_url,
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
            stale_dir = Path(stale["run_dir"])
            stale_run_json = run.read_json(stale_dir / "run.json")
            stale_scenario = stale_run_json["scenarios"][0]["id"]
            filing_test.attach_bugfix_proof(stale_dir, stale_scenario)
            run.record_finding(
                stale_dir,
                "issue",
                "Stale marathon must stop before autonomous fixing",
                "The marathon has a credible issue but no driver heartbeat inside the watchdog window.",
                stale_scenario,
                "high",
                str(stale_dir / "test-evidence" / "trace-replay.md"),
                "open",
                None,
            )
            stale_checked_at = (
                run.parse_iso_datetime(stale_run_json["created_at"]) + run.datetime.timedelta(minutes=46)
            ).isoformat(timespec="seconds")
            stale_db = tmp / "gh-agent-stale-watchdog.json"
            stale_finalized = run.autonomous_marathon(
                catalog,
                github_targets,
                personas,
                scenarios,
                stale_dir,
                "vscode",
                "core-maintainer",
                "ignored",
                "",
                7,
                "o/r",
                str(stale_db),
                "stories/bugfix",
                "https://agent.example",
                "",
                "",
                "",
                "none",
                "",
                str(run.ARTIFACT_ROOT),
                str(stale_dir / "autonomous-marathon-stats.json"),
                0.82,
                25,
                "pending",
                24,
                15,
                45,
                None,
                stale_checked_at,
            )
            stale_report_text = Path(stale_finalized["autonomous_marathon_report_path"]).read_text(encoding="utf-8")
            check("marathon finalization enforces watchdog before autonomous fix spend",
                  stale_finalized["autonomous_marathon_status"] == "autonomous_marathon_invalid"
                  and stale_finalized["autonomous_fix_status"] == "not_run"
                  and stale_finalized["autonomous_watchdog_status"] == "autonomous_watchdog_blocked"
                  and stale_finalized["validation_issue_summary"] == "autonomous-watchdog"
                  and stale_finalized["stats_status"] == "not_run"
                  and "watchdog=fail" in stale_finalized["autonomous_marathon_summary"]
                  and "autonomous-marathon-watchdog.md" in stale_report_text
                  and not stale_db.exists(),
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
            if "healthy_server" in locals():
                healthy_server.shutdown()
            if "unhealthy_server" in locals():
                unhealthy_server.shutdown()
            if "wrong_repo_server" in locals():
                wrong_repo_server.shutdown()
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
