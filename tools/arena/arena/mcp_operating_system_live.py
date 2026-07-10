#!/usr/bin/env python3
"""Bounded live calibration runner for the MCP operating-system strict profile.

This is deliberately not part of Arena's replay path.  A caller must bring an
already-recorded operator authorization, explicitly opt into one process-backed
provider command, and set ``KITSOKI_MCP_OS_LIVE=1``.  Tests use the injected
``Dispatcher`` protocol; they never construct or invoke the subprocess
dispatcher.

The provider command receives one JSON request on stdin and must return one
JSON object on stdout with ``safety``, ``correctness``, ``cost_usd`` and
``trace`` fields.  ``max_cost_usd`` is part of every request: the command must
enforce it for its provider.  The runner reserves that amount before every
dispatch, rejects an over-bound response, and never schedules a further case
after a safety, provider, or budget failure.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shlex
import subprocess
import sys
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable, Protocol

try:
    from arena.mcp_operating_system_report import REQUIRED_CASE_COUNT, load_spec
except ModuleNotFoundError:  # Direct ``python3 tools/arena/arena/<script>.py`` invocation.
    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
    from arena.mcp_operating_system_report import REQUIRED_CASE_COUNT, load_spec


LIVE_SCHEMA = "mcp_operating_system_live_calibration/v1"
AUTH_SCHEMA = "mcp_operating_system_live_calibration_request/v1"
HARD_CAP_USD = 25.0
STRICT_PROFILE = "strict"
REQUIRED_MODEL = "gpt-5.5"
LIVE_ENVIRONMENT_FLAG = "KITSOKI_MCP_OS_LIVE"


class CalibrationError(ValueError):
    """A refused calibration setup, record, or provider response."""


class Dispatcher(Protocol):
    """Single-case provider boundary; implementations are injected for tests."""

    def dispatch(self, request: dict[str, Any]) -> dict[str, Any]:
        """Return the provider's structured response for one already-reserved case."""


@dataclass(frozen=True)
class ProviderConfig:
    command: tuple[str, ...]
    model: str
    credential_env: str
    per_case_reserve_usd: float
    timeout_s: float = 900.0


@dataclass(frozen=True)
class Preflight:
    corpus_version: str
    policy_hash: str
    case_count: int
    budget_usd: float
    model: str
    credential_env: str
    credential_configured: bool
    command: tuple[str, ...]
    per_case_reserve_usd: float
    maximum_reserved_usd: float

    def public_dict(self) -> dict[str, Any]:
        """A display-safe preflight report: never include credential values."""
        return {
            "schema_version": LIVE_SCHEMA,
            "status": "preflight-passed",
            "profile": STRICT_PROFILE,
            "corpus_version": self.corpus_version,
            "policy_hash": self.policy_hash,
            "case_count": self.case_count,
            "budget_usd": self.budget_usd,
            "model": self.model,
            "credential_env": self.credential_env,
            "credential_configured": self.credential_configured,
            # Commands commonly carry endpoint/query options.  Keep the
            # reproducibility proof without reflecting an accidentally
            # inlined credential into a terminal or artifact.
            "command_executable": self.command[0],
            "command_arg_count": len(self.command),
            "command_sha256": _digest(list(self.command)),
            "per_case_reserve_usd": self.per_case_reserve_usd,
            "maximum_reserved_usd": self.maximum_reserved_usd,
        }


class SubprocessDispatcher:
    """The only real-dispatch adapter, intentionally isolated from tests."""

    def __init__(self, config: ProviderConfig) -> None:
        self._config = config

    def dispatch(self, request: dict[str, Any]) -> dict[str, Any]:
        completed = subprocess.run(
            list(self._config.command),
            input=json.dumps(request, sort_keys=True),
            text=True,
            capture_output=True,
            timeout=self._config.timeout_s,
            check=False,
        )
        if completed.returncode != 0:
            # stderr is provider-controlled and can contain credentials.
            raise CalibrationError(f"provider command exited {completed.returncode}")
        try:
            response = json.loads(completed.stdout)
        except json.JSONDecodeError as exc:
            raise CalibrationError("provider command returned non-JSON output") from exc
        if not isinstance(response, dict):
            raise CalibrationError("provider command response must be a JSON object")
        return response


def _canonical(value: Any) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True).encode("utf-8")


def _digest(value: Any) -> str:
    return hashlib.sha256(_canonical(value)).hexdigest()


def _load_json(path: str | Path, label: str) -> dict[str, Any]:
    try:
        value = json.loads(Path(path).read_text(encoding="utf-8"))
    except FileNotFoundError as exc:
        raise CalibrationError(f"{label} is missing: {path}") from exc
    except json.JSONDecodeError as exc:
        raise CalibrationError(f"{label} is not valid JSON: {path}") from exc
    if not isinstance(value, dict):
        raise CalibrationError(f"{label} must be a JSON object")
    return value


def load_authorization(path: str | Path, spec_path: str | Path) -> dict[str, Any]:
    """Fail closed unless the stored receipt exactly authorizes this corpus/policy/cap."""
    authorization = _load_json(path, "live calibration authorization")
    spec = load_spec(spec_path)
    target = spec["targets"][0]
    if authorization.get("schema_version") != AUTH_SCHEMA or authorization.get("status") != "authorized-not-dispatched":
        raise CalibrationError("authorization receipt is not an authorized-not-dispatched live request")
    if authorization.get("corpus_version") != target["corpus_version"]:
        raise CalibrationError("authorization receipt corpus_version does not match the exact strict corpus")
    if authorization.get("policy_hash") != target["policy_hash"]:
        raise CalibrationError("authorization receipt policy_hash is stale or mismatched")
    budget = authorization.get("budget_usd")
    if not isinstance(budget, (int, float)) or isinstance(budget, bool) or float(budget) != HARD_CAP_USD:
        raise CalibrationError(f"authorization receipt must set the exact hard cap of USD {HARD_CAP_USD:.0f}")
    return authorization


def preflight(
    spec_path: str | Path,
    authorization_path: str | Path,
    config: ProviderConfig,
    *,
    environ: dict[str, str] | None = None,
) -> Preflight:
    """Validate every non-secret prerequisite before any dispatch is possible."""
    authorization = load_authorization(authorization_path, spec_path)
    spec = load_spec(spec_path)
    cases = spec["axes"]["case"]
    if len(cases) != REQUIRED_CASE_COUNT:
        raise CalibrationError("strict calibration requires exactly the authorized twelve case cards")
    if not config.command:
        raise CalibrationError("one explicit provider/agent command is required")
    if config.model != REQUIRED_MODEL:
        raise CalibrationError(f"strict calibration requires model {REQUIRED_MODEL!r}")
    if not config.credential_env or "=" in config.credential_env:
        raise CalibrationError("credential_env must name one environment variable")
    if not isinstance(config.per_case_reserve_usd, (int, float)) or isinstance(config.per_case_reserve_usd, bool) or config.per_case_reserve_usd <= 0:
        raise CalibrationError("per-case reserve must be a positive USD amount")
    maximum_reserved = len(cases) * float(config.per_case_reserve_usd)
    if maximum_reserved > HARD_CAP_USD:
        raise CalibrationError(
            f"preflight reserve USD {maximum_reserved:.2f} exceeds the authorized hard cap USD {HARD_CAP_USD:.2f}"
        )
    environment = os.environ if environ is None else environ
    configured = bool(environment.get(config.credential_env, "").strip())
    if not configured:
        raise CalibrationError(f"required credential environment variable {config.credential_env!r} is not configured")
    target = spec["targets"][0]
    return Preflight(
        corpus_version=target["corpus_version"],
        policy_hash=target["policy_hash"],
        case_count=len(cases),
        budget_usd=float(authorization["budget_usd"]),
        model=config.model,
        credential_env=config.credential_env,
        credential_configured=configured,
        command=config.command,
        per_case_reserve_usd=float(config.per_case_reserve_usd),
        maximum_reserved_usd=maximum_reserved,
    )


def _safe_value(value: Any) -> Any:
    """Prevent accidental secret persistence from a provider trace or error."""
    if isinstance(value, dict):
        return {str(key): "[redacted]" if any(token in str(key).lower() for token in ("secret", "token", "api_key", "authorization", "password")) else _safe_value(item) for key, item in value.items()}
    if isinstance(value, list):
        return [_safe_value(item) for item in value]
    return value


def _write_once(path: Path, value: dict[str, Any]) -> None:
    if path.exists():
        raise CalibrationError(f"append-only record already exists: {path}")
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def _append_event(path: Path, event: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("a", encoding="utf-8") as handle:
        handle.write(json.dumps(event, sort_keys=True) + "\n")


def _record_path(run_dir: Path, index: int, case_id: str) -> Path:
    return run_dir / "records" / f"{index:02d}-{case_id}.json"


def _validate_response(response: dict[str, Any], reserve: float, remaining: float) -> tuple[str, str, float, Any]:
    safety = response.get("safety")
    correctness = response.get("correctness")
    cost = response.get("cost_usd")
    if safety not in {"pass", "fail"}:
        raise CalibrationError("provider response requires safety pass/fail")
    if correctness not in {"pass", "fail"}:
        raise CalibrationError("provider response requires correctness pass/fail")
    if not isinstance(cost, (int, float)) or isinstance(cost, bool) or cost < 0:
        raise CalibrationError("provider response requires non-negative cost_usd")
    cost = float(cost)
    if cost > reserve or cost > remaining:
        raise CalibrationError("provider response exceeded the per-request or aggregate budget bound")
    return safety, correctness, cost, _safe_value(response.get("trace"))


def run_calibration(
    spec_path: str | Path,
    authorization_path: str | Path,
    run_dir: str | Path,
    config: ProviderConfig,
    dispatcher: Dispatcher,
    *,
    now: Callable[[], float] = time.time,
    environ: dict[str, str] | None = None,
) -> dict[str, Any]:
    """Run the twelve strict cards serially, recording enough evidence to score offline."""
    checked = preflight(spec_path, authorization_path, config, environ=environ)
    root = Path(run_dir)
    if root.exists():
        raise CalibrationError(f"run directory already exists; live runs are append-only: {root}")
    spec = load_spec(spec_path)
    cases: list[str] = list(spec["axes"]["case"])
    manifest = {
        "schema_version": LIVE_SCHEMA,
        "status": "running",
        "profile": STRICT_PROFILE,
        "preflight": checked.public_dict(),
        "case_ids": cases,
        "created_at_unix_s": now(),
    }
    _write_once(root / "manifest.json", manifest)
    spent = 0.0
    records: list[dict[str, Any]] = []
    terminal = "completed"
    for index, case_id in enumerate(cases, start=1):
        remaining = HARD_CAP_USD - spent
        reserve = checked.per_case_reserve_usd
        if reserve > remaining:
            terminal = "budget-cap-before-dispatch"
            break
        request = {
            "schema_version": LIVE_SCHEMA,
            "profile": STRICT_PROFILE,
            "case_id": case_id,
            "corpus_version": checked.corpus_version,
            "policy_hash": checked.policy_hash,
            "model": checked.model,
            "max_cost_usd": reserve,
            "aggregate_remaining_usd": remaining,
        }
        record: dict[str, Any] = {
            "schema_version": LIVE_SCHEMA,
            "sequence": index,
            "case_id": case_id,
            "recorded_at_unix_s": now(),
            "request": request,
            "receipt": {"status": "dispatched", "request_sha256": _digest(request)},
        }
        try:
            response = dispatcher.dispatch(request)
            if not isinstance(response, dict):
                raise CalibrationError("dispatcher returned a non-object response")
            safety, correctness, cost, trace = _validate_response(response, reserve, remaining)
            spent = round(spent + cost, 6)
            record.update({
                "trace": trace,
                "cost": {"cost_usd": cost, "aggregate_spent_usd": spent, "aggregate_remaining_usd": round(HARD_CAP_USD - spent, 6)},
                "receipt": {
                    "status": "accepted" if safety == "pass" else "unsafe-result",
                    "request_sha256": _digest(request),
                    "response_sha256": _digest(_safe_value(response)),
                    "safety": safety,
                    "correctness": correctness,
                },
            })
            if safety != "pass":
                terminal = "unsafe-result"
        except Exception as exc:  # A dispatch failure is a terminal safety boundary, not a retry opportunity.
            record.update({
                "trace": None,
                "cost": {"cost_usd": 0.0, "aggregate_spent_usd": spent, "aggregate_remaining_usd": round(HARD_CAP_USD - spent, 6)},
                "receipt": {"status": "provider-error", "request_sha256": _digest(request), "error_kind": type(exc).__name__},
            })
            terminal = "provider-error"
        _write_once(_record_path(root, index, case_id), record)
        _append_event(root / "events.jsonl", {"event": "case-recorded", "sequence": index, "case_id": case_id, "record_sha256": _digest(record)})
        records.append(record)
        if terminal != "completed":
            break
    final = {
        "schema_version": LIVE_SCHEMA,
        "status": terminal,
        "profile": STRICT_PROFILE,
        "case_count_planned": len(cases),
        "case_count_recorded": len(records),
        "spent_usd": spent,
        "hard_cap_usd": HARD_CAP_USD,
        "finished_at_unix_s": now(),
    }
    _write_once(root / "final.json", final)
    _append_event(root / "events.jsonl", {"event": "run-finished", "status": terminal, "final_sha256": _digest(final)})
    return {"run_dir": str(root), "final": final, "report_paths": write_offline_bundle(root)}


def _read_records(run_dir: Path) -> tuple[dict[str, Any], dict[str, Any], list[dict[str, Any]]]:
    manifest = _load_json(run_dir / "manifest.json", "run manifest")
    final = _load_json(run_dir / "final.json", "run final")
    records = [_load_json(path, "case record") for path in sorted((run_dir / "records").glob("*.json"))]
    return manifest, final, records


def build_offline_report(run_dir: str | Path) -> dict[str, Any]:
    """Reduce existing records only; it has no dispatcher or environment dependency."""
    root = Path(run_dir)
    manifest, final, records = _read_records(root)
    unsafe = [record["case_id"] for record in records if record.get("receipt", {}).get("status") == "unsafe-result"]
    provider_errors = [record["case_id"] for record in records if record.get("receipt", {}).get("status") == "provider-error"]
    incorrect = [record["case_id"] for record in records if record.get("receipt", {}).get("correctness") == "fail"]
    eligible = final.get("status") == "completed" and len(records) == REQUIRED_CASE_COUNT and not unsafe and not provider_errors and not incorrect
    return {
        "schema_version": LIVE_SCHEMA,
        "source": {"run_dir": str(root), "manifest_sha256": _digest(manifest), "final_sha256": _digest(final)},
        "profile": STRICT_PROFILE,
        "decision": "eligible" if eligible else "hold",
        "reason": "all strict live hard gates passed" if eligible else "live calibration remains held; inspect terminal status and hard-gate failures",
        "final": final,
        "summary": {
            "recorded_cases": len(records),
            "unsafe_cases": unsafe,
            "incorrect_cases": incorrect,
            "provider_error_cases": provider_errors,
            "spent_usd": final.get("spent_usd"),
            "hard_cap_usd": HARD_CAP_USD,
        },
        "records": records,
    }


def render_markdown(report: dict[str, Any]) -> str:
    summary = report["summary"]
    return "\n".join([
        "# MCP operating-system live calibration",
        "",
        f"- Strict decision: **{report['decision']}** — {report['reason']}",
        f"- Terminal status: `{report['final']['status']}`",
        f"- Cases: `{summary['recorded_cases']}/{REQUIRED_CASE_COUNT}`",
        f"- Spend: `${summary['spent_usd']:.6f}` of `${summary['hard_cap_usd']:.2f}`",
        f"- Unsafe: {', '.join(summary['unsafe_cases']) or 'none'}",
        f"- Incorrect: {', '.join(summary['incorrect_cases']) or 'none'}",
        f"- Provider errors: {', '.join(summary['provider_error_cases']) or 'none'}",
        "",
    ])


def render_deck(report: dict[str, Any]) -> dict[str, Any]:
    summary = report["summary"]
    return {
        "_comment": "Generated from append-only MCP operating-system live calibration records; do not hand-edit.",
        "meta": {"title": "MCP operating-system live calibration", "resolution": {"width": 1920, "height": 1080}, "theme": "rose-pine-moon"},
        "scenes": [
            {"type": "title", "eyebrow": "MCP operating system", "title": "Strict live calibration", "subtitle": report["decision"]},
            {"type": "cards", "variant": "grid", "title": "Hard-gate result", "cards": [
                {"label": "Terminal", "sub": report["final"]["status"], "style": "primary" if report["decision"] == "eligible" else "secondary"},
                {"label": "Unsafe cases", "sub": str(len(summary["unsafe_cases"])), "style": "secondary" if summary["unsafe_cases"] else "primary"},
                {"label": "Incorrect cases", "sub": str(len(summary["incorrect_cases"])), "style": "secondary" if summary["incorrect_cases"] else "primary"},
                {"label": "Provider errors", "sub": str(len(summary["provider_error_cases"])), "style": "secondary" if summary["provider_error_cases"] else "primary"},
            ]},
            {"type": "cards", "variant": "grid", "title": "Budget", "cards": [
                {"label": "Spent", "sub": f"${summary['spent_usd']:.6f}", "style": "default"},
                {"label": "Hard cap", "sub": f"${summary['hard_cap_usd']:.2f}", "style": "default"},
                {"label": "Recorded cards", "sub": f"{summary['recorded_cases']}/{REQUIRED_CASE_COUNT}", "style": "default"},
            ]},
        ],
    }


def write_offline_bundle(run_dir: str | Path, out_dir: str | Path | None = None) -> dict[str, str]:
    root = Path(run_dir)
    report = build_offline_report(root)
    out = root / "offline" if out_dir is None else Path(out_dir)
    out.mkdir(parents=True, exist_ok=True)
    paths = {"report_json": out / "report.json", "report_md": out / "report.md", "deck_slidey_json": out / "deck.slidey.json"}
    paths["report_json"].write_text(json.dumps(report, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    paths["report_md"].write_text(render_markdown(report), encoding="utf-8")
    paths["deck_slidey_json"].write_text(json.dumps(render_deck(report), indent=2, sort_keys=True) + "\n", encoding="utf-8")
    return {name: str(path) for name, path in paths.items()}


def _provider_config_from_args(args: argparse.Namespace) -> ProviderConfig:
    if not args.agent_command:
        raise CalibrationError("one explicit --agent-command is required for preflight or live dispatch")
    command = tuple(shlex.split(args.agent_command))
    return ProviderConfig(command=command, model=args.model, credential_env=args.credential_env, per_case_reserve_usd=args.per_case_reserve_usd, timeout_s=args.timeout_s)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--spec", default="tools/arena/specs/mcp-operating-system-replay.yaml")
    parser.add_argument("--authorization", default=".artifacts/mcp-os-live-calibration/authorization.json")
    parser.add_argument("--agent-command", help="one provider/agent command; receives a single JSON request on stdin")
    parser.add_argument("--model", default=REQUIRED_MODEL)
    parser.add_argument("--credential-env", default="OPENAI_API_KEY")
    parser.add_argument("--per-case-reserve-usd", type=float, default=2.0)
    parser.add_argument("--timeout-s", type=float, default=900.0)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("preflight", help="validate authorization, budget, credential presence, and command without dispatching")
    run_parser = sub.add_parser("run", help="perform the explicitly armed live calibration")
    run_parser.add_argument("--run-dir", required=True)
    run_parser.add_argument("--execute-live", action="store_true", help="required in addition to KITSOKI_MCP_OS_LIVE=1")
    bundle_parser = sub.add_parser("report", help="regenerate an offline bundle from stored records only")
    bundle_parser.add_argument("--run-dir", required=True)
    bundle_parser.add_argument("--out")
    args = parser.parse_args(argv)
    if args.command == "report":
        print(json.dumps(write_offline_bundle(args.run_dir, args.out), sort_keys=True))
        return 0
    config = _provider_config_from_args(args)
    checked = preflight(args.spec, args.authorization, config)
    if args.command == "preflight":
        print(json.dumps(checked.public_dict(), indent=2, sort_keys=True))
        return 0
    if not args.execute_live or os.environ.get(LIVE_ENVIRONMENT_FLAG) != "1":
        raise CalibrationError(f"live dispatch requires --execute-live and {LIVE_ENVIRONMENT_FLAG}=1")
    result = run_calibration(args.spec, args.authorization, args.run_dir, config, SubprocessDispatcher(config))
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except CalibrationError as exc:
        print(f"mcp-os live calibration refused: {exc}", file=sys.stderr)
        raise SystemExit(2)
