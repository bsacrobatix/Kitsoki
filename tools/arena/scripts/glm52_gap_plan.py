#!/usr/bin/env python3
"""Build an offline execution packet for missing GLM-5.2 headline cells.

The report generator is intentionally honest about missing cells; this script
turns those `pending` rows into a concrete no-spend plan and, when specs are
provided, explicit paid `arena run --live` commands. It never runs Docker or an
LLM.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Any

try:
    import yaml  # type: ignore
except ModuleNotFoundError:
    yaml = None


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--report-json", required=True, help="JSON from glm52_bugswarm_report.py")
    parser.add_argument("--json-out", required=True)
    parser.add_argument("--markdown-out", required=True)
    parser.add_argument("--oss-spec", default="", help="paired-task arena spec for OSS GLM-5.2 cells")
    parser.add_argument("--bugswarm-spec", default="", help="paired-task arena spec for BugSwarm GLM-5.2 cells")
    parser.add_argument("--bugswarm-source", default="", help="verified BugSwarm source; used when --bugswarm-spec must be generated")
    parser.add_argument("--oss-out", default=".artifacts/arena/glm52-oss")
    parser.add_argument("--bugswarm-out", default=".artifacts/arena/bugswarm-glm52")
    args = parser.parse_args(argv)

    report = json.loads(Path(args.report_json).read_text(encoding="utf-8"))
    packet = build_packet(
        report,
        report_json=args.report_json,
        oss_spec=args.oss_spec,
        bugswarm_spec=args.bugswarm_spec,
        bugswarm_source=args.bugswarm_source,
        oss_out=args.oss_out,
        bugswarm_out=args.bugswarm_out,
    )
    write_json(Path(args.json_out), packet)
    write_text(Path(args.markdown_out), render_markdown(packet))
    print(f"wrote {args.json_out} and {args.markdown_out}")
    return 0


def build_packet(
    report: dict[str, Any],
    *,
    report_json: str,
    oss_spec: str,
    bugswarm_spec: str,
    bugswarm_source: str,
    oss_out: str,
    bugswarm_out: str,
) -> dict[str, Any]:
    pending = [
        row for row in report.get("required_glm52_matrix", [])
        if isinstance(row, dict) and row.get("quality") == "pending"
    ]
    by_corpus: dict[str, list[dict[str, Any]]] = {}
    for row in pending:
        by_corpus.setdefault(str(row.get("corpus") or "unknown"), []).append(row)

    actions: list[dict[str, Any]] = []
    actions.append(corpus_action(
        corpus="oss-oracle",
        rows=by_corpus.get("oss-oracle", []),
        spec=oss_spec,
        out_dir=oss_out,
        report_arg="--oss-arena-rollup",
        source="",
    ))
    actions.append(corpus_action(
        corpus="bugswarm",
        rows=by_corpus.get("bugswarm", []),
        spec=bugswarm_spec,
        out_dir=bugswarm_out,
        report_arg="--bugswarm-arena-rollup",
        source=bugswarm_source,
    ))
    return {
        "kind": "glm52_gap_execution_packet",
        "version": 1,
        "source_report": report_json,
        "pending_cell_count": len(pending),
        "pending_by_corpus": {corpus: len(rows) for corpus, rows in sorted(by_corpus.items())},
        "actions": actions,
        "notes": [
            "Commands are emitted for operator execution only; this planner never runs Docker or an LLM.",
            "Run each no-LLM plan command before any --live command.",
            "After live runs land, regenerate glm52_bugswarm_report.py with the emitted report_arg rollup paths.",
        ],
    }


def corpus_action(
    *,
    corpus: str,
    rows: list[dict[str, Any]],
    spec: str,
    out_dir: str,
    report_arg: str,
    source: str,
) -> dict[str, Any]:
    treatments = sorted({str(row.get("treatment") or "") for row in rows if row.get("treatment")})
    tasks = sorted({str(row.get("task") or "") for row in rows if row.get("task")})
    action: dict[str, Any] = {
        "corpus": corpus,
        "pending_count": len(rows),
        "tasks": tasks,
        "treatments": treatments,
        "status": "complete" if not rows else "needs-spec",
        "prerequisites": [],
        "commands": [],
        "rollup": str(Path(out_dir) / "rollup.json"),
        "report_arg": report_arg,
    }
    if not rows:
        return action
    if not spec:
        if corpus == "bugswarm" and source:
            spec = ".artifacts/bugswarm/bugswarm-glm52.yaml"
            action["prerequisites"].append(
                "Generate and inspect the BugSwarm paired-task spec from the verified source."
            )
            action["prerequisites"].append(
                "Do not run --live until the spec uses a verified GLM-capable backend for both Kitsoki and raw-prompt arms."
            )
            action["commands"].append(
                f"python3 tools/arena/scripts/bugswarm_to_arena_spec.py --source {source} --out {spec}"
            )
            action["commands"].extend([
                f"python3 tools/arena/arena.py plan --spec {spec}",
                f"python3 tools/arena/arena.py run --spec {spec} --out {out_dir}",
            ])
            action["status"] = "needs-live-spec"
            action["spec"] = spec
            return action
        action["prerequisites"].append(
            f"Provide a paired-task arena spec for {corpus} with kitsoki-glm-5.2 and raw-prompt-glm-5.2 variants."
        )
        return action
    audit = audit_spec(spec, tasks=tasks, treatments=treatments)
    action["spec_audit"] = audit
    if not audit["ok"]:
        action["status"] = "needs-spec-fix"
        action["spec"] = spec
        action["prerequisites"].extend(audit["problems"])
        action["commands"].append(f"python3 tools/arena/arena.py plan --spec {spec}")
        return action
    action["status"] = "ready"
    action["spec"] = spec
    action["commands"].extend([
        f"python3 tools/arena/arena.py plan --spec {spec}",
        f"python3 tools/arena/arena.py run --spec {spec} --out {out_dir}",
        f"ARENA_PAIRED_TASK_ENABLE_CODEX=1 python3 tools/arena/arena.py run --spec {spec} --out {out_dir} --live",
    ])
    return action


def audit_spec(spec: str, *, tasks: list[str], treatments: list[str]) -> dict[str, Any]:
    problems: list[str] = []
    if yaml is None:
        return {
            "ok": False,
            "problems": ["Cannot inspect supplied spec because PyYAML is not installed."],
        }
    path = Path(spec)
    if not path.exists():
        return {
            "ok": False,
            "problems": [f"Supplied spec does not exist: {spec}"],
        }
    try:
        data = yaml.safe_load(path.read_text(encoding="utf-8"))
    except Exception as exc:  # noqa: BLE001 - report parse failure in the packet.
        return {
            "ok": False,
            "problems": [f"Cannot parse supplied spec {spec}: {exc}"],
        }
    if not isinstance(data, dict):
        return {"ok": False, "problems": [f"Supplied spec is not a mapping: {spec}"]}
    if data.get("job_type") != "paired-task":
        problems.append(f"Spec job_type must be paired-task, got {data.get('job_type')!r}.")
    spec_tasks = {str(task) for task in (data.get("axes") or {}).get("task", [])}
    missing_tasks = sorted(set(tasks) - spec_tasks)
    if missing_tasks:
        problems.append(f"Spec is missing pending task(s): {', '.join(missing_tasks)}.")
    variants = [variant for variant in data.get("variants", []) if isinstance(variant, dict)]
    variants_by_treatment: dict[str, list[dict[str, Any]]] = {}
    for variant in variants:
        treatment = normalize_treatment(str(variant.get("treatment") or variant.get("id") or ""))
        variants_by_treatment.setdefault(treatment, []).append(variant)
    for treatment in treatments:
        matching = variants_by_treatment.get(treatment, [])
        if not matching:
            problems.append(f"Spec has no variant for treatment {treatment!r}.")
            continue
        if not any(is_glm_variant(variant) for variant in matching):
            ids = ", ".join(str(variant.get("id") or "<unnamed>") for variant in matching)
            problems.append(f"Spec treatment {treatment!r} is not GLM-5.2 labeled (variant(s): {ids}).")
    return {
        "ok": not problems,
        "problems": problems,
        "tasks": sorted(spec_tasks),
        "variants": [str(variant.get("id") or "") for variant in variants],
    }


def normalize_treatment(value: str) -> str:
    lowered = value.strip().lower()
    if lowered.startswith("kitsoki"):
        return "kitsoki"
    if lowered.startswith("single") or lowered.startswith("raw-prompt") or lowered in {"raw", "raw-prompt"}:
        return "raw-prompt"
    return lowered


def is_glm_variant(variant: dict[str, Any]) -> bool:
    haystack = " ".join(
        str(variant.get(key) or "")
        for key in ("id", "candidate", "model", "profile")
    ).lower()
    return "glm-5.2" in haystack or "glm52" in haystack


def render_markdown(packet: dict[str, Any]) -> str:
    lines = [
        "# GLM-5.2 Headline Evidence Execution Packet",
        "",
        f"Source report: `{packet['source_report']}`.",
        f"Pending headline cells: {packet['pending_cell_count']}.",
        "",
        "This packet is generated offline. It does not run Docker or any LLM.",
        "",
        "## Actions",
        "",
    ]
    for action in packet["actions"]:
        lines.extend([
            f"### {action['corpus']}",
            "",
            f"- status: `{action['status']}`",
            f"- pending cells: {action['pending_count']}",
            f"- treatments: {', '.join(action['treatments']) if action['treatments'] else 'none'}",
            f"- rollup for report: `{action['rollup']}`",
            f"- report argument: `{action['report_arg']} {action['rollup']}`",
            "",
        ])
        if action["prerequisites"]:
            lines.append("Prerequisites:")
            lines.extend(f"- {item}" for item in action["prerequisites"])
            lines.append("")
        if action["commands"]:
            lines.append("Commands:")
            lines.append("")
            lines.append("```bash")
            lines.extend(action["commands"])
            lines.append("```")
            lines.append("")
    lines.extend(["## Notes", ""])
    lines.extend(f"- {note}" for note in packet["notes"])
    return "\n".join(lines) + "\n"


def write_json(path: Path, payload: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def write_text(path: Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")


if __name__ == "__main__":
    raise SystemExit(main())
