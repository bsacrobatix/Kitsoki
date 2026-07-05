#!/usr/bin/env python3
"""Unit tests for flow_fixture_compiler.py (task 3.1), over SYNTHETIC scenario
IR documents (no LLM, no real corpus). Exercises: turn/episode count parity,
display_input fidelity, canned-answer derivation (corrective_ops /
expected_effects / generic), determinism, and the kind:conversation guard.

Run:  python3 tools/session-mining/tests/test_flow_fixture_compiler.py
(exits 0 on success, non-zero with a diagnostic on failure)
"""
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
TOOL = os.path.dirname(HERE)
sys.path.insert(0, TOOL)

import flow_fixture_compiler as ffc

FAILURES = []


def check(name, cond, detail=""):
    if not cond:
        FAILURES.append("%s: %s" % (name, detail))


def _scenario(**over):
    base = {
        "schema_version": "1.0",
        "kind": "conversation",
        "id": "scn-synthetic-0000",
        "source": "mined",
        "provenance": {"corpus": "claude-code", "session_id": "synthetic", "span_idx": 0},
        "persona": "explorer",
        "goal": "do the thing",
        "turns": [{"role": "user", "text": "do the thing", "corrected": False}],
        "expected_effects": [],
        "abandoned": False,
    }
    base.update(over)
    return base


def test_single_turn_no_correction():
    scenario = _scenario()
    flow_yaml, cassette_yaml = ffc.compile_scenario(scenario)
    flow_text = flow_yaml.decode("utf-8")
    cas_text = cassette_yaml.decode("utf-8")
    check("single_turn: one 'ask' turn emitted", flow_text.count("name: ask") == 1, flow_text)
    check("single_turn: display_input carries verbatim text", '"do the thing"' in flow_text)
    check("single_turn: one cassette episode", cas_text.count("converse_") == 1, cas_text)
    check("single_turn: generic canned answer (no correction, no effects)", "acknowledged" in cas_text and "expected effects" not in cas_text)


def test_correction_turn_and_final_effects():
    scenario = _scenario(
        turns=[
            {"role": "user", "text": "rebase this onto main", "corrected": True,
             "corrective_ops": ["git rebase --abort"], "followup_text_head": "no wait, stash first"},
            {"role": "user", "text": "ok stash my changes then rebase", "corrected": False},
        ],
        expected_effects=["git.rebase completed", "no uncommitted changes lost"],
    )
    flow_yaml, cassette_yaml = ffc.compile_scenario(scenario)
    flow_text = flow_yaml.decode("utf-8")
    check("two turns -> two 'ask' entries", flow_text.count("name: ask") == 2, flow_text)
    check("two turns -> two cassette episodes", cassette_yaml.decode("utf-8").count("converse_") == 2)
    turn0_answer = ffc.canned_answer(scenario, 0)
    turn1_answer = ffc.canned_answer(scenario, 1)
    check("corrected turn cites its corrective_ops", "git rebase --abort" in turn0_answer, turn0_answer)
    check("final turn cites expected_effects", "git.rebase completed" in turn1_answer, turn1_answer)
    check("corrected turn and final turn answers differ", turn0_answer != turn1_answer)


def test_abandoned_scenario_last_turn():
    scenario = _scenario(
        turns=[{"role": "user", "text": "do the risky thing", "corrected": True,
                "corrective_ops": [], "followup_text_head": "[Request interrupted by user]"}],
        expected_effects=[],
        abandoned=True,
    )
    answer = ffc.canned_answer(scenario, 0)
    # corrected=True but no corrective_ops -> falls through past the
    # correction branch; abandoned+last -> the abandonment branch.
    check("abandoned last turn with no ops -> abandonment phrasing", "unresolved" in answer, answer)


def test_determinism():
    scenario = _scenario()
    a = ffc.compile_scenario(scenario)
    b = ffc.compile_scenario(scenario)
    check("compile_scenario is a pure function (byte-identical reruns)", a == b)


def test_rejects_non_conversation_kind():
    scenario = _scenario(kind="trace")
    try:
        ffc.compile_scenario(scenario)
        check("rejects kind != conversation", False, "expected ValueError, got none")
    except ValueError:
        pass


def test_rejects_empty_turns():
    scenario = _scenario(turns=[])
    try:
        ffc.compile_scenario(scenario)
        check("rejects empty turns", False, "expected ValueError, got none")
    except ValueError:
        pass


def test_yaml_string_escaping_is_safe():
    # A turn containing a double quote and a newline must not break the
    # generated YAML's double-quoted scalar (json.dumps produces valid YAML
    # double-quoted-scalar escaping for both).
    scenario = _scenario(turns=[{"role": "user", "text": 'say "hi"\nplease', "corrected": False}])
    flow_yaml, _ = ffc.compile_scenario(scenario)
    text = flow_yaml.decode("utf-8")
    check("embedded quote/newline round-trips via json.dumps escaping", '\\"hi\\"' in text and "\\n" in text, text)


def main():
    tests = [v for k, v in sorted(globals().items()) if k.startswith("test_") and callable(v)]
    for t in tests:
        t()
    if FAILURES:
        print("FAILED (%d):" % len(FAILURES))
        for f in FAILURES:
            print(" -", f)
        return 1
    print("OK: %d test functions passed" % len(tests))
    return 0


if __name__ == "__main__":
    sys.exit(main())
