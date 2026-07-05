#!/usr/bin/env python3
"""Runner-level test for --scenario-qa-report (S4 of
docs/proposals/e2e-persona-qa-review.md, "all-transports fan-out + one
report").

stories/scenario-qa owns report.md itself (folded in Starlark,
scripts/build_report.star) and dispatches the driver/judge agents per
transport leg; this subcommand only owns deck.slidey.json -- the one derived
artifact that most benefits from this module's existing Slidey-deck-shape
validation. Covers:
  - scenario_qa_leg_counts()/scenario_qa_leg_level() over a mixed
    pass/fail/degraded-evidence leg set, including the vscode
    bridge-level label (never mistaken for editor-level coverage)
  - parse_scenario_qa_leg_results(): inline JSON, "@<path>" file JSON, and
    the empty/invalid-JSON error paths
  - render_scenario_qa_deck() produces a deck.slidey.json shape that passes
    this module's own validate_slidey_deck_shape() gate
  - the --scenario-qa-report CLI wiring end to end via run.main(), writing
    deck.slidey.json into an existing run dir and printing --json-output

This never calls a live LLM or GitHub; every check is local and deterministic.
Run directly:  python3 tools/product-journey/scenario_qa_report_test.py
"""

import contextlib
import importlib.util
import io
import json
import sys
import tempfile
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


def _expect_system_exit(name, fn, expected_text):
    try:
        fn()
    except SystemExit as exc:
        _check(name, expected_text in str(exc))
        return
    print(f"FAIL: {name}")
    sys.exit(1)


_LEG_RESULTS = {
    "items": [
        {"leg_id": "bugfix::tui", "scenario": "bugfix", "transport": "tui", "driver_status": "captured", "verdict": "pass", "verdict_summary": "TUI frame confirms the fix."},
        {"leg_id": "bugfix::web", "scenario": "bugfix", "transport": "web", "driver_status": "captured", "verdict": "pass", "verdict_summary": "Browser screenshot confirms the fix."},
        {"leg_id": "bugfix::vscode", "scenario": "bugfix", "transport": "vscode", "driver_status": "degraded-evidence", "verdict": "degraded-evidence", "verdict_summary": "IDE bridge came back JSON-degraded."},
    ]
}


def _test_leg_counts():
    items = run.scenario_qa_leg_items(_LEG_RESULTS)
    _check("scenario_qa_leg_items extracts the items list", len(items) == 3)
    _check("scenario_qa_leg_items tolerates a non-dict input", run.scenario_qa_leg_items(None) == [])
    _check("scenario_qa_leg_items tolerates a missing items key", run.scenario_qa_leg_items({}) == [])

    counts = run.scenario_qa_leg_counts(items)
    _check("counts total", counts["total"] == 3)
    _check("counts pass", counts["pass"] == 2)
    _check("counts degraded", counts["degraded"] == 1)
    _check("counts fail", counts["fail"] == 0)

    mixed = [
        {"verdict": "pass"},
        {"verdict": "fail"},
        {"verdict": "degraded-evidence"},
        {"verdict": "unjudged"},
        {"verdict": ""},
    ]
    mixed_counts = run.scenario_qa_leg_counts(mixed)
    _check("mixed counts total counts every item", mixed_counts["total"] == 5)
    _check("mixed counts pass", mixed_counts["pass"] == 1)
    _check("mixed counts degraded", mixed_counts["degraded"] == 1)
    _check("mixed counts fail excludes unjudged/empty verdicts", mixed_counts["fail"] == 1)

    summary = run.scenario_qa_report_summary("bugfix", counts)
    _check("summary names the pass/total ratio", "2 / 3 transport legs passed" in summary)
    _check("summary names the degraded count", "1 degraded-evidence" in summary)
    _check("summary omits a failed clause when there are no failures", "failed" not in summary)


def _test_leg_level():
    _check(
        "vscode leg with no explicit level falls back to bridge-level",
        run.scenario_qa_leg_level({"transport": "vscode"}) == "bridge-level",
    )
    _check(
        "tui leg with no explicit level falls back to frame-level",
        run.scenario_qa_leg_level({"transport": "tui"}) == "frame-level",
    )
    _check(
        "web leg with no explicit level falls back to frame-level",
        run.scenario_qa_leg_level({"transport": "web"}) == "frame-level",
    )
    _check(
        "an unknown transport with no contract has no level",
        run.scenario_qa_leg_level({"transport": "holodeck"}) == "",
    )
    _check(
        "an explicit evidence_level wins over the transport fallback",
        run.scenario_qa_leg_level({"transport": "tui", "evidence_level": "custom-level"}) == "custom-level",
    )
    _check(
        "a carried transport_evidence_contract.level wins over the bare transport fallback",
        run.scenario_qa_leg_level({"transport": "web", "transport_evidence_contract": {"level": "frame-level"}}) == "frame-level",
    )


def _test_parse_leg_results(tmp: Path):
    _check("empty raw returns an empty items list", run.parse_scenario_qa_leg_results("") == {"items": []})
    inline = json.dumps({"items": [{"transport": "tui"}]})
    _check("inline JSON parses", run.parse_scenario_qa_leg_results(inline) == {"items": [{"transport": "tui"}]})

    path = tmp / "leg-results.json"
    path.write_text(json.dumps({"items": [{"transport": "web"}]}), encoding="utf-8")
    _check("@path reads a JSON file", run.parse_scenario_qa_leg_results(f"@{path}") == {"items": [{"transport": "web"}]})

    _expect_system_exit(
        "invalid JSON raises a clear SystemExit",
        lambda: run.parse_scenario_qa_leg_results("{not json"),
        "not valid JSON",
    )
    _expect_system_exit(
        "a JSON scalar (not an object) is rejected",
        lambda: run.parse_scenario_qa_leg_results("[1, 2, 3]"),
        "must decode to a JSON object",
    )


def _test_render_deck():
    items = run.scenario_qa_leg_items(_LEG_RESULTS)
    counts = run.scenario_qa_leg_counts(items)
    deck = run.render_scenario_qa_deck("bugfix", "scenario-qa-run-all", items, counts)

    issues: list[dict] = []
    run.validate_slidey_deck_shape(deck, {"items": []}, issues)
    _check("the rendered deck passes this module's own Slidey deck-shape validator", issues == [])

    body_text = json.dumps(deck)
    _check("the deck names the scenario", "bugfix" in body_text)
    _check("the deck labels the vscode leg bridge-level", "bridge-level" in body_text)
    _check("the deck labels the tui leg frame-level", "frame-level" in body_text)
    _check("the deck carries the run id", "scenario-qa-run-all" in body_text)
    _check("the deck's summary scene names the pass/total ratio", "2 / 3 transport legs passed" in body_text)

    empty_deck = run.render_scenario_qa_deck("adhoc-thing", "run-empty", [], run.scenario_qa_leg_counts([]))
    empty_issues: list[dict] = []
    run.validate_slidey_deck_shape(empty_deck, {"items": []}, empty_issues)
    _check("a deck with zero recorded legs still passes deck-shape validation", empty_issues == [])


def _run_cli(tmp: Path, extra_args, expected_exit=None):
    out = io.StringIO()
    sys.argv = [
        "run.py",
        "--scenario-qa-report",
        "--json-output",
        "--run-dir",
        str(tmp),
        *extra_args,
    ]
    with contextlib.redirect_stdout(out):
        if expected_exit is None:
            run.main()
        else:
            try:
                run.main()
            except SystemExit as exc:
                _check(f"CLI exits {expected_exit}", exc.code == expected_exit or (expected_exit != 0 and exc.code))
                return None
            print("FAIL: expected SystemExit")
            sys.exit(1)
    return json.loads(out.getvalue())


def _test_cli(tmp: Path):
    run_dir = tmp / "run-cli"
    run_dir.mkdir()
    leg_results_json = json.dumps(_LEG_RESULTS)

    payload = _run_cli(
        run_dir,
        ["--scenario", "bugfix", "--leg-results-json", leg_results_json],
    )
    _check("CLI reports the built status", payload["status"] == "scenario_qa_deck_built")
    _check("CLI reports the run dir", payload["run_dir"] == str(run_dir))
    _check("CLI reports leg/pass/fail/degraded counts", (payload["leg_count"], payload["pass_count"], payload["fail_count"], payload["degraded_count"]) == (3, 2, 0, 1))
    deck_path = Path(payload["deck_path"])
    _check("CLI writes deck.slidey.json into the run dir", deck_path == run_dir / "deck.slidey.json")
    _check("CLI actually wrote the deck file", deck_path.exists())
    written = json.loads(deck_path.read_text(encoding="utf-8"))
    _check("the written deck names the scenario", written["scenes"][0]["subtitle"] == "bugfix")

    adhoc_dir = tmp / "run-cli-adhoc"
    adhoc_dir.mkdir()
    adhoc_payload = _run_cli(
        adhoc_dir,
        ["--scenario-description", "open the onboarding tour", "--leg-results-json", ""],
    )
    _check("CLI falls back to --scenario-description when there is no catalog scenario", adhoc_payload["leg_count"] == 0)
    adhoc_deck = json.loads((adhoc_dir / "deck.slidey.json").read_text(encoding="utf-8"))
    _check("an ad-hoc run names the description in the deck", adhoc_deck["scenes"][0]["subtitle"] == "open the onboarding tour")

    missing_run_dir_argv = [
        "run.py",
        "--scenario-qa-report",
        "--json-output",
    ]
    original_argv = sys.argv
    try:
        sys.argv = missing_run_dir_argv
        _expect_system_exit(
            "--scenario-qa-report without --run-dir raises a clear error",
            run.main,
            "requires --run-dir",
        )
    finally:
        sys.argv = original_argv


def main():
    _test_leg_counts()
    _test_leg_level()
    with tempfile.TemporaryDirectory() as tmp:
        tmp = Path(tmp)
        _test_parse_leg_results(tmp)
        _test_cli(tmp)
    _test_render_deck()
    print("PASS")


if __name__ == "__main__":
    main()
