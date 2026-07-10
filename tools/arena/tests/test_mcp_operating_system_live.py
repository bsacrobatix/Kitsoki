#!/usr/bin/env python3
"""No-LLM contract tests for the injected MCP operating-system live runner."""

from __future__ import annotations

import copy
import json
import sys
import tempfile
from pathlib import Path


HERE = Path(__file__).resolve().parent
ARENA_ROOT = HERE.parent
REPO_ROOT = ARENA_ROOT.parent.parent
SPEC = ARENA_ROOT / "specs" / "mcp-operating-system-replay.yaml"
sys.path.insert(0, str(ARENA_ROOT))

from arena.mcp_operating_system_live import (  # noqa: E402
    HARD_CAP_USD,
    CalibrationError,
    ProviderConfig,
    load_authorization,
    preflight,
    run_calibration,
    write_offline_bundle,
)


failures: list[str] = []


def check(label: str, got, want) -> None:
    if got != want:
        failures.append(f"{label}: got {got!r}, want {want!r}")


def require(label: str, condition: bool) -> None:
    if not condition:
        failures.append(label)


class FakeDispatcher:
    def __init__(self, responses: list[object]) -> None:
        self.responses = list(responses)
        self.requests: list[dict] = []

    def dispatch(self, request: dict) -> dict:
        self.requests.append(copy.deepcopy(request))
        result = self.responses.pop(0)
        if isinstance(result, Exception):
            raise result
        return result  # type: ignore[return-value]


def auth() -> dict:
    return {
        "schema_version": "mcp_operating_system_live_calibration_request/v1",
        "status": "authorized-not-dispatched",
        "budget_usd": HARD_CAP_USD,
        "corpus_version": "mcp-os-replay-v1",
        "policy_hash": "d585fabd8a0f5f8d24439c7ee53491977eb57128214650da54bca4a85a38903a",
    }


def response(*, safety: str = "pass", correctness: str = "pass", cost: float = 1.0) -> dict:
    return {"safety": safety, "correctness": correctness, "cost_usd": cost, "trace": {"summary": "fake", "api_key": "must-not-persist"}}


def config(reserve: float = 2.0) -> ProviderConfig:
    return ProviderConfig(command=("fake-provider", "--json"), model="gpt-5.5", credential_env="MCP_OS_TEST_CREDENTIAL", per_case_reserve_usd=reserve)


with tempfile.TemporaryDirectory(prefix="mcp-os-live-", dir=REPO_ROOT / ".artifacts") as tmp:
    root = Path(tmp)
    authorization = root / "authorization.json"
    authorization.write_text(json.dumps(auth()), encoding="utf-8")
    environment = {"MCP_OS_TEST_CREDENTIAL": "not-a-real-secret"}

    # Exact authorization avoids accepting an old corpus/policy or a casual budget increase.
    for mutation in ({"budget_usd": 24.0}, {"corpus_version": "old-corpus"}, {"policy_hash": "0" * 64}):
        forged = auth()
        forged.update(mutation)
        forged_path = root / f"forged-{len(mutation)}.json"
        forged_path.write_text(json.dumps(forged), encoding="utf-8")
        try:
            load_authorization(forged_path, SPEC)
            failures.append(f"forged authorization accepted: {mutation}")
        except CalibrationError:
            pass

    checked = preflight(SPEC, authorization, config(), environ=environment)
    check("preflight hard cap", checked.budget_usd, HARD_CAP_USD)
    check("preflight knows all strict cards", checked.case_count, 12)
    public = checked.public_dict()
    require("preflight never exposes credential", "not-a-real-secret" not in json.dumps(public))
    check("preflight says credential configured", public["credential_configured"], True)
    command_secret = ProviderConfig(command=("fake-provider", "--api-key", "not-a-real-command-secret"), model="gpt-5.5", credential_env="MCP_OS_TEST_CREDENTIAL", per_case_reserve_usd=2.0)
    require("preflight never reflects command arguments", "not-a-real-command-secret" not in json.dumps(preflight(SPEC, authorization, command_secret, environ=environment).public_dict()))

    # Aggregate reservation is denied before the fake dispatcher gets a request.
    try:
        preflight(SPEC, authorization, config(3.0), environ=environment)
        failures.append("over-cap plan passed preflight")
    except CalibrationError as exc:
        require("preflight cap error names cap", "cap" in str(exc))

    complete = FakeDispatcher([response(cost=1.0) for _ in range(12)])
    full_run = root / "full-run"
    result = run_calibration(SPEC, authorization, full_run, config(), complete, now=lambda: 1234.0, environ=environment)
    check("full run dispatches serial strict cards", len(complete.requests), 12)
    check("full run terminal result", result["final"]["status"], "completed")
    check("every request has independent max bound", {request["max_cost_usd"] for request in complete.requests}, {2.0})
    record = json.loads(next((full_run / "records").glob("*.json")).read_text(encoding="utf-8"))
    check("trace secret is redacted", record["trace"]["api_key"], "[redacted]")
    check("append-only event count", len((full_run / "events.jsonl").read_text(encoding="utf-8").splitlines()), 13)

    # A response that violates its declared cost ceiling is terminal; no later card can consume spend.
    cap_failure = FakeDispatcher([response(cost=2.1)] + [response() for _ in range(11)])
    cap_run = root / "cap-run"
    capped = run_calibration(SPEC, authorization, cap_run, config(), cap_failure, now=lambda: 1234.0, environ=environment)
    check("over-bound cost stops midrun", capped["final"]["status"], "provider-error")
    check("over-bound cost stops after first dispatch", len(cap_failure.requests), 1)

    provider_failure = FakeDispatcher([RuntimeError("provider unavailable")])
    error_run = root / "provider-error-run"
    errored = run_calibration(SPEC, authorization, error_run, config(), provider_failure, now=lambda: 1234.0, environ=environment)
    check("provider error terminal", errored["final"]["status"], "provider-error")
    check("provider error recorded", json.loads(next((error_run / "records").glob("*.json")).read_text())["receipt"]["status"], "provider-error")

    unsafe = FakeDispatcher([response(safety="fail")])
    unsafe_run = root / "unsafe-run"
    unsafe_result = run_calibration(SPEC, authorization, unsafe_run, config(), unsafe, now=lambda: 1234.0, environ=environment)
    check("unsafe result is terminal", unsafe_result["final"]["status"], "unsafe-result")
    check("unsafe result stops serial sequence", len(unsafe.requests), 1)

    # Reports are a pure reduction of stored records and stable when regenerated.
    first = root / "offline-first"
    second = root / "offline-second"
    first_paths = write_offline_bundle(full_run, first)
    second_paths = write_offline_bundle(full_run, second)
    check("offline bundle names", set(first_paths), {"report_json", "report_md", "deck_slidey_json"})
    for name in first_paths:
        check(f"offline {name} deterministic", Path(first_paths[name]).read_bytes(), Path(second_paths[name]).read_bytes())
    full_report = json.loads(Path(first_paths["report_json"]).read_text(encoding="utf-8"))
    check("all-pass fake run is eligible", full_report["decision"], "eligible")

if failures:
    print("FAIL: MCP operating-system live calibration")
    for failure in failures:
        print(f"  - {failure}")
    raise SystemExit(1)
print("PASS: MCP OS live runner injection, authorization, cap, safety, receipts, and deterministic offline bundle (no provider dispatched)")
