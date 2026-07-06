#!/usr/bin/env python3
"""Generate the GLM-5.2 + BugSwarm bugfix comparison report.

This is an offline report builder. It reads committed benchmark evidence and
corpus metadata, then emits JSON + Markdown. It never queries BugSwarm, starts
Docker, or calls an LLM. Missing cells remain explicit `pending` records.
"""

from __future__ import annotations

import argparse
import json
import sys
from collections import Counter
from pathlib import Path
from typing import Any

try:
    import yaml  # type: ignore
except ModuleNotFoundError:
    sys.exit("glm52_bugswarm_report.py needs pyyaml")

REPO_ROOT = Path(__file__).resolve().parents[3]
DEFAULT_BAKEOFF_CELLS = REPO_ROOT / "tools/bugfix-bakeoff/results/cells"
DEFAULT_ARENA_ROUND1 = REPO_ROOT / "tools/arena/results/round-1/rollup.json"
DEFAULT_CORPUS = REPO_ROOT / "tools/arena/corpus/cost-bench.manifest.yaml"
DEFAULT_SOURCES = REPO_ROOT / "tools/arena/corpus/sources.yaml"

QUALITY_VALUES = ("solved", "partial", "failed", "pending", "blocked")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--generated-at", required=True, help="stable report timestamp")
    parser.add_argument("--json-out", required=True)
    parser.add_argument("--markdown-out", required=True)
    parser.add_argument("--bugswarm-source", default="", help="optional converted BugSwarm YAML source")
    parser.add_argument("--bugswarm-verification", default="", help="optional bugswarm_verify_source.py JSON report")
    parser.add_argument("--bugswarm-arena-rollup", default="", help="optional arena rollup with BugSwarm GLM-5.2 cells")
    parser.add_argument("--oss-arena-rollup", default="", help="optional arena rollup with OSS oracle GLM-5.2 cells")
    parser.add_argument("--bakeoff-cells", default=str(DEFAULT_BAKEOFF_CELLS))
    parser.add_argument("--arena-rollup", default=str(DEFAULT_ARENA_ROUND1))
    parser.add_argument("--corpus", default=str(DEFAULT_CORPUS))
    parser.add_argument("--sources", default=str(DEFAULT_SOURCES))
    args = parser.parse_args(argv)

    report = build_report(
        generated_at=args.generated_at,
        report_json=Path(args.json_out),
        bakeoff_cells=Path(args.bakeoff_cells),
        arena_rollup=Path(args.arena_rollup),
        corpus_path=Path(args.corpus),
        sources_path=Path(args.sources),
        bugswarm_source=Path(args.bugswarm_source) if args.bugswarm_source else None,
        bugswarm_verification=Path(args.bugswarm_verification) if args.bugswarm_verification else None,
        bugswarm_arena_rollup=Path(args.bugswarm_arena_rollup) if args.bugswarm_arena_rollup else None,
        oss_arena_rollup=Path(args.oss_arena_rollup) if args.oss_arena_rollup else None,
    )
    write_json(Path(args.json_out), report)
    write_text(Path(args.markdown_out), render_markdown(report))
    print(f"wrote {args.json_out} and {args.markdown_out}")
    return 0


def build_report(
    *,
    generated_at: str,
    report_json: Path,
    bakeoff_cells: Path,
    arena_rollup: Path,
    corpus_path: Path,
    sources_path: Path,
    bugswarm_source: Path | None = None,
    bugswarm_verification: Path | None = None,
    bugswarm_arena_rollup: Path | None = None,
    oss_arena_rollup: Path | None = None,
) -> dict[str, Any]:
    corpus = load_yaml(corpus_path)
    sources = load_yaml(sources_path)
    glm_cells = load_glm_bakeoff_cells(bakeoff_cells)
    arena_cells = load_arena_cells(arena_rollup)
    bugswarm_tasks = load_bugswarm_tasks(bugswarm_source)
    verification = load_verification(bugswarm_verification)
    bugswarm_cells = load_headline_arena_cells(bugswarm_arena_rollup, corpus_name="bugswarm")
    oss_arena_cells = load_headline_arena_cells(oss_arena_rollup, corpus_name="oss-oracle")

    matrix_rows = build_required_matrix(glm_cells + oss_arena_cells, bugswarm_tasks, bugswarm_cells)
    return {
        "kind": "glm52_bugswarm_bugfix_report",
        "version": 1,
        "generated_at": generated_at,
        "inputs": {
            "report_json": rel(report_json),
            "bakeoff_cells": rel(bakeoff_cells),
            "arena_rollup": rel(arena_rollup),
            "corpus": rel(corpus_path),
            "sources": rel(sources_path),
            "bugswarm_source": rel(bugswarm_source) if bugswarm_source else "",
            "bugswarm_verification": rel(bugswarm_verification) if bugswarm_verification else "",
            "bugswarm_arena_rollup": rel(bugswarm_arena_rollup) if bugswarm_arena_rollup else "",
            "oss_arena_rollup": rel(oss_arena_rollup) if oss_arena_rollup else "",
        },
        "corpora": corpus_summary(corpus, sources, bugswarm_tasks, verification),
        "glm52_bugfix_cells": glm_cells,
        "oss_glm52_arena_cells": oss_arena_cells,
        "bugswarm_glm52_arena_cells": bugswarm_cells,
        "required_glm52_matrix": matrix_rows,
        "rollups": {
            "glm52_by_corpus_treatment": rollup_required_matrix(matrix_rows),
            "supporting_oss_codex_round1": rollup_arena_cells(arena_cells),
        },
        "evidence_gaps": evidence_gaps(matrix_rows, bugswarm_tasks, verification),
        "interpretation": interpretation(matrix_rows, bugswarm_tasks, verification),
        "evidence_closure": evidence_closure(matrix_rows, bugswarm_tasks, verification),
    }


def load_glm_bakeoff_cells(cells_dir: Path) -> list[dict[str, Any]]:
    cells: list[dict[str, Any]] = []
    for path in sorted(cells_dir.glob("*glm-5.2*.json")):
        data = json.loads(path.read_text(encoding="utf-8"))
        outcome = data.get("outcome") or {}
        metrics = data.get("metrics") or {}
        cells.append({
            "source": "bugfix-bakeoff",
            "corpus": "oss-oracle",
            "task": str(data.get("bug") or ""),
            "candidate": str(data.get("candidate") or ""),
            "treatment": normalize_treatment(str(data.get("treatment") or "")),
            "model": str(data.get("model") or ""),
            "provider": str(data.get("provider") or ""),
            "quality": normalize_quality(str(outcome.get("quality") or "pending")),
            "oracle_status": str(outcome.get("oracle_status") or ""),
            "adjudicated": bool(outcome.get("adjudicated")),
            "total_tokens": as_int(metrics.get("total_tokens")),
            "cost_usd": as_float(metrics.get("cost_usd")),
            "evidence": rel(path),
            "notes": str(data.get("notes") or ""),
        })
    return cells


def load_arena_cells(rollup_path: Path) -> list[dict[str, Any]]:
    if not rollup_path.exists():
        return []
    data = json.loads(rollup_path.read_text(encoding="utf-8"))
    out: list[dict[str, Any]] = []
    for cell in data.get("cells", []):
        metrics = cell.get("metrics") or {}
        out.append({
            "task": str((cell.get("axis") or {}).get("task") or ""),
            "variant": str(cell.get("variant_id") or ""),
            "treatment": treatment_from_variant(str(cell.get("variant_id") or "")),
            "quality": normalize_quality(str(cell.get("verdict") or "pending")),
            "tokens": as_int(metrics.get("tokens")),
            "cost_usd": as_float(metrics.get("cost_usd")),
            "evidence": rel(rollup_path),
        })
    return out


def load_headline_arena_cells(rollup_path: Path | None, *, corpus_name: str) -> list[dict[str, Any]]:
    if rollup_path is None or not rollup_path.exists():
        return []
    data = json.loads(rollup_path.read_text(encoding="utf-8"))
    out: list[dict[str, Any]] = []
    for cell in data.get("cells", []):
        if not isinstance(cell, dict):
            continue
        metrics = cell.get("metrics") or {}
        task = str((cell.get("axis") or {}).get("task") or "")
        variant = str(cell.get("variant_id") or "")
        treatment = treatment_from_variant(variant)
        if "glm-5.2" not in variant.lower():
            continue
        out.append({
            "source": "arena-rollup",
            "corpus": corpus_name,
            "task": task,
            "candidate": "glm-5.2",
            "treatment": treatment,
            "model": "hf:zai-org/GLM-5.2",
            "provider": str(cell.get("job_type") or "paired-task"),
            "quality": normalize_quality(str(cell.get("verdict") or "pending")),
            "oracle_status": "",
            "adjudicated": False,
            "total_tokens": as_int(metrics.get("tokens")),
            "cost_usd": as_float(metrics.get("cost_usd")),
            "evidence": rel(rollup_path),
            "trace_ref": str(cell.get("trace_ref") or ""),
            "notes": str(cell.get("notes") or ""),
        })
    return [row for row in out if row["task"] and row["treatment"] in {"kitsoki", "raw-prompt"}]


def load_bugswarm_tasks(path: Path | None) -> list[dict[str, Any]]:
    if path is None or not path.exists():
        return []
    data = load_yaml(path)
    tasks = data.get("tasks") or []
    if not isinstance(tasks, list):
        return []
    out = []
    for task in tasks:
        if not isinstance(task, dict):
            continue
        out.append({
            "id": str(task.get("id") or ""),
            "repo": str(task.get("repo_label") or task.get("repo") or ""),
            "image_tag": str(task.get("image_tag") or ""),
            "verified_red": bool(task.get("verified_red")),
            "verified_green": bool(task.get("verified_green")),
        })
    return out


def load_verification(path: Path | None) -> dict[str, Any]:
    if path is None or not path.exists():
        return {}
    data = json.loads(path.read_text(encoding="utf-8"))
    return data if isinstance(data, dict) else {}


def build_required_matrix(
    glm_cells: list[dict[str, Any]],
    bugswarm_tasks: list[dict[str, Any]],
    bugswarm_cells: list[dict[str, Any]],
) -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    for treatment in ("kitsoki", "raw-prompt"):
        matching = [c for c in glm_cells if c["corpus"] == "oss-oracle" and c["treatment"] == treatment]
        if matching:
            rows.extend(matching)
        else:
            rows.append(pending_row("oss-oracle", "current-committed-glm52", treatment, "no committed GLM-5.2 cell"))

    bugswarm_by_key = {
        (cell["task"], cell["treatment"]): cell
        for cell in bugswarm_cells
    }
    task_ids = [task["id"] for task in bugswarm_tasks]
    if not task_ids:
        task_ids = sorted({cell["task"] for cell in bugswarm_cells})

    if task_ids:
        for task_id in task_ids:
            for treatment in ("kitsoki", "raw-prompt"):
                row = bugswarm_by_key.get((task_id, treatment))
                if row:
                    rows.append(row)
                else:
                    rows.append(pending_row("bugswarm", task_id, treatment, "BugSwarm task imported but no GLM-5.2 result cell"))
    else:
        for treatment in ("kitsoki", "raw-prompt"):
            rows.append(pending_row("bugswarm", "verified-subset", treatment, "no verified BugSwarm source imported"))
    return rows


def pending_row(corpus: str, task: str, treatment: str, reason: str) -> dict[str, Any]:
    return {
        "source": "pending",
        "corpus": corpus,
        "task": task,
        "candidate": "glm-5.2",
        "treatment": treatment,
        "model": "hf:zai-org/GLM-5.2",
        "provider": "synthetic.new",
        "quality": "pending",
        "oracle_status": "",
        "adjudicated": False,
        "total_tokens": None,
        "cost_usd": None,
        "evidence": "",
        "notes": reason,
    }


def corpus_summary(
    corpus: dict[str, Any],
    sources: dict[str, Any],
    bugswarm_tasks: list[dict[str, Any]],
    verification: dict[str, Any],
) -> dict[str, Any]:
    tasks = [t for t in corpus.get("tasks", []) if isinstance(t, dict)]
    repos = sorted({str(t.get("repo") or "") for t in tasks if t.get("repo")})
    source_rows = sources.get("sources") if isinstance(sources.get("sources"), list) else []
    bugswarm_source = next((s for s in source_rows if isinstance(s, dict) and s.get("id") == "bugswarm"), {})
    return {
        "oss_oracle": {
            "task_count": len(tasks),
            "repo_count": len(repos),
            "repos": repos,
            "bugfix_task_count": sum(1 for t in tasks if t.get("archetype") == "bugfix_test_repair"),
        },
        "bugswarm": {
            "source_status": str(bugswarm_source.get("status") or "missing"),
            "imported_task_count": len(bugswarm_tasks),
            "verified_task_count": sum(1 for t in bugswarm_tasks if t["verified_red"] and t["verified_green"]),
            "verification_report_count": int(verification.get("task_count") or 0),
            "verification_verified_count": int(verification.get("verified_count") or 0),
            "verification_mode": str(verification.get("mode") or ""),
            "source_catalog": rel(DEFAULT_SOURCES),
        },
    }


def rollup_required_matrix(rows: list[dict[str, Any]]) -> dict[str, Any]:
    buckets: dict[str, list[dict[str, Any]]] = {}
    for row in rows:
        key = f"{row['corpus']}|{row['treatment']}"
        buckets.setdefault(key, []).append(row)
    return {key: rollup_quality(rows) for key, rows in sorted(buckets.items())}


def rollup_arena_cells(cells: list[dict[str, Any]]) -> dict[str, Any]:
    buckets: dict[str, list[dict[str, Any]]] = {}
    for cell in cells:
        treatment = cell["treatment"]
        if treatment not in {"kitsoki", "raw-prompt"}:
            continue
        buckets.setdefault(treatment, []).append(cell)
    return {key: rollup_quality(rows, token_key="tokens") for key, rows in sorted(buckets.items())}


def rollup_quality(rows: list[dict[str, Any]], *, token_key: str = "total_tokens") -> dict[str, Any]:
    counts = Counter(row.get("quality", "pending") for row in rows)
    attempted = counts["solved"] + counts["partial"] + counts["failed"]
    solved = counts["solved"]
    token_values = [row.get(token_key) for row in rows if isinstance(row.get(token_key), int)]
    return {
        "n": len(rows),
        "attempted": attempted,
        "solved": solved,
        "partial": counts["partial"],
        "failed": counts["failed"],
        "pending": counts["pending"],
        "blocked": counts["blocked"],
        "success_rate": round(solved / attempted, 6) if attempted else None,
        "partial_rate": round(counts["partial"] / attempted, 6) if attempted else None,
        "total_tokens": sum(token_values) if token_values else None,
        "avg_tokens": round(sum(token_values) / len(token_values), 2) if token_values else None,
    }


def evidence_gaps(rows: list[dict[str, Any]], bugswarm_tasks: list[dict[str, Any]], verification: dict[str, Any]) -> list[str]:
    gaps: list[str] = []
    if any(row["corpus"] == "oss-oracle" and row["treatment"] == "raw-prompt" and row["quality"] == "pending" for row in rows):
        gaps.append("No committed raw-prompt GLM-5.2 result exists for the OSS oracle corpus.")
    if not bugswarm_tasks:
        gaps.append("No BugSwarm artifact source has been imported and RED/GREEN verified yet.")
    elif not any(t["verified_red"] and t["verified_green"] for t in bugswarm_tasks):
        gaps.append("BugSwarm artifacts have been imported but none are verified RED/GREEN yet.")
    if bugswarm_tasks and not verification:
        gaps.append("No BugSwarm verification report is attached to this generated report.")
    if any(row["corpus"] == "bugswarm" and row["quality"] == "pending" for row in rows):
        if bugswarm_tasks:
            gaps.append("Some imported BugSwarm tasks are missing committed GLM-5.2 Kitsoki or raw-prompt result cells.")
        else:
            gaps.append("No committed GLM-5.2 Kitsoki or raw-prompt result exists for BugSwarm.")
    return gaps


def evidence_closure(rows: list[dict[str, Any]], bugswarm_tasks: list[dict[str, Any]], verification: dict[str, Any]) -> dict[str, Any]:
    pending = [row for row in rows if row["quality"] == "pending"]
    by_corpus: dict[str, list[dict[str, Any]]] = {}
    for row in pending:
        by_corpus.setdefault(str(row["corpus"]), []).append(row)
    actions = [
        closure_action_oss(by_corpus.get("oss-oracle", [])),
        closure_action_bugswarm(by_corpus.get("bugswarm", []), bugswarm_tasks, verification),
    ]
    return {
        "pending_cell_count": len(pending),
        "pending_by_corpus": {corpus: len(items) for corpus, items in sorted(by_corpus.items())},
        "actions": actions,
    }


def closure_action_oss(rows: list[dict[str, Any]]) -> dict[str, Any]:
    if not rows:
        return {
            "corpus": "oss-oracle",
            "status": "complete",
            "pending_count": 0,
            "next": "No pending OSS oracle GLM-5.2 cells.",
        }
    return {
        "corpus": "oss-oracle",
        "status": "needs-spec",
        "pending_count": len(rows),
        "tasks": sorted({str(row["task"]) for row in rows}),
        "treatments": sorted({str(row["treatment"]) for row in rows}),
        "next": "Provide --oss-spec pointing at a paired-task spec with GLM-5.2 Kitsoki/raw-prompt variants.",
    }


def closure_action_bugswarm(rows: list[dict[str, Any]], bugswarm_tasks: list[dict[str, Any]], verification: dict[str, Any]) -> dict[str, Any]:
    if not rows:
        return {
            "corpus": "bugswarm",
            "status": "complete",
            "pending_count": 0,
            "next": "No pending BugSwarm GLM-5.2 cells.",
        }
    if not bugswarm_tasks:
        return {
            "corpus": "bugswarm",
            "status": "needs-import",
            "pending_count": len(rows),
            "next": "Import BugSwarm artifact metadata, execute RED/GREEN verification, then pass --bugswarm-source to glm52_gap_plan.py.",
        }
    if str(verification.get("mode") or "") != "execute" or int(verification.get("verified_count") or 0) == 0:
        return {
            "corpus": "bugswarm",
            "status": "needs-execute-verification",
            "pending_count": len(rows),
            "tasks": sorted({str(row["task"]) for row in rows}),
            "next": "Run bugswarm_verify_source.py --execute and apply the verification report before scheduling live GLM-5.2 cells.",
        }
    return {
        "corpus": "bugswarm",
        "status": "ready",
        "pending_count": len(rows),
        "tasks": sorted({str(row["task"]) for row in rows}),
        "treatments": sorted({str(row["treatment"]) for row in rows}),
        "next": "Run glm52_gap_plan.py with --bugswarm-source; it will generate split-backend BugSwarm live commands.",
    }


def interpretation(rows: list[dict[str, Any]], bugswarm_tasks: list[dict[str, Any]], verification: dict[str, Any]) -> list[str]:
    glm_kitsoki_attempts = [r for r in rows if r["corpus"] == "oss-oracle" and r["treatment"] == "kitsoki" and r["quality"] != "pending"]
    raw_prompt_attempts = [r for r in rows if r["treatment"] == "raw-prompt" and r["quality"] not in {"pending", "blocked"}]
    bugswarm_attempts = [r for r in rows if r["corpus"] == "bugswarm" and r["quality"] not in {"pending", "blocked"}]
    out = []
    if glm_kitsoki_attempts:
        tokens = sum(r["total_tokens"] or 0 for r in glm_kitsoki_attempts)
        out.append(
            f"Committed GLM-5.2 Kitsoki evidence contains {len(glm_kitsoki_attempts)} attempted OSS oracle cell(s), "
            f"{tokens} total tokens, and no solved cell yet."
        )
    if raw_prompt_attempts:
        tokens = sum(r["total_tokens"] or 0 for r in raw_prompt_attempts)
        out.append(f"Committed GLM-5.2 raw-prompt evidence contains {len(raw_prompt_attempts)} attempted cell(s) and {tokens} total tokens.")
    else:
        out.append("The GLM-5.2 raw-prompt arm remains pending; the report must not compute a token ratio from missing data.")
    if bugswarm_attempts:
        tokens = sum(r["total_tokens"] or 0 for r in bugswarm_attempts)
        out.append(f"Committed BugSwarm GLM-5.2 arena evidence contains {len(bugswarm_attempts)} attempted cell(s) and {tokens} total tokens.")
    if bugswarm_tasks:
        out.append(f"BugSwarm is reusable as an imported source with {len(bugswarm_tasks)} task(s) in the supplied source file.")
        if verification:
            out.append(
                f"BugSwarm verification report mode={verification.get('mode')} covers "
                f"{verification.get('task_count', 0)} task(s), with {verification.get('verified_count', 0)} verified."
            )
    else:
        out.append("BugSwarm is adapter-ready in the source catalog, but the committed report has no imported artifact subset yet.")
    return out


def render_markdown(report: dict[str, Any]) -> str:
    lines: list[str] = []
    corpora = report["corpora"]
    rollups = report["rollups"]["glm52_by_corpus_treatment"]
    cells = report["glm52_bugfix_cells"]
    oss_arena_cells = report["oss_glm52_arena_cells"]
    bugswarm_cells = report["bugswarm_glm52_arena_cells"]
    inputs = report["inputs"]
    lines.extend([
        "# BugSwarm + GLM-5.2 bugfix comparison report",
        "",
        f"Generated at: `{report['generated_at']}`.",
        "",
        "This report is generated offline from committed evidence. It does not call",
        "BugSwarm, Docker, or any LLM. Missing cells are reported as `pending`.",
        "",
        "## Research Question",
        "",
        "Compare the Kitsoki `bugfix` pipeline with raw prompts on GLM-5.2,",
        "using total token usage as the primary cost axis, and prepare the same",
        "success-rate comparison for a BugSwarm corpus alongside the existing",
        "OSS oracle corpus.",
        "",
        "The current committed evidence does not yet contain the full GLM-5.2",
        "matrix. This report therefore separates observed results from missing",
        "cells instead of imputing raw-prompt or BugSwarm numbers.",
        "",
        "## Method",
        "",
        "The headline matrix has one row per `(corpus, treatment)` bucket.",
        "A cell is counted as attempted only when its quality is `solved`,",
        "`partial`, or `failed`; `pending` and `blocked` are excluded from the",
        "model-quality denominator. Token totals are summed only from committed",
        "cell evidence that records real usage.",
        "",
        "Inputs:",
        "",
        f"- GLM-5.2 bakeoff cells: `{inputs['bakeoff_cells']}`.",
        f"- Arena supporting rollup: `{inputs['arena_rollup']}`.",
        f"- OSS oracle corpus: `{inputs['corpus']}`.",
        f"- Source catalog: `{inputs['sources']}`.",
        f"- OSS arena GLM rollup: `{inputs['oss_arena_rollup'] or 'not supplied'}`.",
        f"- BugSwarm source: `{inputs['bugswarm_source'] or 'not supplied'}`.",
        f"- BugSwarm verification report: `{inputs['bugswarm_verification'] or 'not supplied'}`.",
        f"- BugSwarm arena rollup: `{inputs['bugswarm_arena_rollup'] or 'not supplied'}`.",
        "",
        "Primary metrics:",
        "",
        "- success rate: `solved / (solved + partial + failed)`.",
        "- partial rate: reported separately because hidden oracles can be",
        "  implementation-coupled.",
        "- total tokens: provider-neutral primary cost measure.",
        "- USD cost: secondary; only shown where committed cell evidence provides",
        "  it.",
        "",
        "## Corpus Coverage",
        "",
        "| corpus | tasks | repositories | verified/imported status |",
        "|---|---:|---:|---|",
        f"| OSS oracle corpus | {corpora['oss_oracle']['task_count']} | {corpora['oss_oracle']['repo_count']} | frozen and locally validated |",
        f"| BugSwarm | {corpora['bugswarm']['imported_task_count']} | n/a | {corpora['bugswarm']['source_status']}; converted verified tasks: {corpora['bugswarm']['verified_task_count']}; verification report: {corpora['bugswarm']['verification_verified_count']}/{corpora['bugswarm']['verification_report_count']} ({corpora['bugswarm']['verification_mode'] or 'none'}) |",
        "",
        "The OSS oracle corpus remains the active internal benchmark source. It",
        "covers the pre-registered public OSS targets plus existing hidden-oracle",
        "bugfix fixtures. BugSwarm is represented separately in the source",
        "catalog, so its fail/pass CI artifact sampling process does not get",
        "collapsed into the OSS oracle denominator.",
        "",
        "BugSwarm source contract:",
        "",
        "- import explicit exported artifact metadata with",
        "  `tools/arena/scripts/bugswarm_to_arena.py`.",
        "- require `image_tag`, `repo`, `failed_job_id`, and `passed_job_id`.",
        "- treat the failed job as RED and the passed job as GREEN inside the",
        "  artifact image.",
        "- keep imported tasks unattempted until Docker verification proves both",
        "  sides still reproduce.",
        "- verify with `tools/arena/scripts/bugswarm_verify_source.py`; dry-run",
        "  mode records the Docker commands, while `--execute` runs each side in",
        "  separate fresh containers.",
        "",
        "## GLM-5.2 Headline Matrix",
        "",
        "| corpus | treatment | n | attempted | solved | partial | failed | pending | success rate | tokens |",
        "|---|---|---:|---:|---:|---:|---:|---:|---:|---:|",
    ])
    for key, bucket in sorted(rollups.items()):
        corpus, treatment = key.split("|", 1)
        lines.append(
            "| {corpus} | {treatment} | {n} | {attempted} | {solved} | {partial} | {failed} | {pending} | {rate} | {tokens} |".format(
                corpus=corpus,
                treatment=treatment,
                n=bucket["n"],
                attempted=bucket["attempted"],
                solved=bucket["solved"],
                partial=bucket["partial"],
                failed=bucket["failed"],
                pending=bucket["pending"],
                rate=format_rate(bucket["success_rate"]),
                tokens=format_int(bucket["total_tokens"]),
            )
        )
    lines.extend([
        "",
        "## Committed GLM-5.2 Cells",
        "",
        "| task | treatment | quality | tokens | cost | evidence |",
        "|---|---|---|---:|---:|---|",
    ])
    if cells:
        for cell in cells:
            lines.append(
                f"| {cell['task']} | {cell['treatment']} | {cell['quality']} | "
                f"{format_int(cell['total_tokens'])} | {format_cost(cell['cost_usd'])} | `{cell['evidence']}` |"
            )
    else:
        lines.append("| none | none | pending | n/a | n/a | n/a |")
    lines.extend([
        "",
        "## Committed OSS GLM-5.2 Arena Cells",
        "",
        "| task | treatment | quality | tokens | cost | evidence |",
        "|---|---|---|---:|---:|---|",
    ])
    if oss_arena_cells:
        for cell in oss_arena_cells:
            lines.append(
                f"| {cell['task']} | {cell['treatment']} | {cell['quality']} | "
                f"{format_int(cell['total_tokens'])} | {format_cost(cell['cost_usd'])} | `{cell['evidence']}` |"
            )
    else:
        lines.append("| none | none | pending | n/a | n/a | n/a |")
    lines.extend([
        "",
        "## Committed BugSwarm GLM-5.2 Arena Cells",
        "",
        "| task | treatment | quality | tokens | cost | evidence |",
        "|---|---|---|---:|---:|---|",
    ])
    if bugswarm_cells:
        for cell in bugswarm_cells:
            lines.append(
                f"| {cell['task']} | {cell['treatment']} | {cell['quality']} | "
                f"{format_int(cell['total_tokens'])} | {format_cost(cell['cost_usd'])} | `{cell['evidence']}` |"
            )
    else:
        lines.append("| none | none | pending | n/a | n/a | n/a |")
    lines.extend([
        "",
        "## Evidence Gaps",
        "",
    ])
    lines.extend(f"- {gap}" for gap in report["evidence_gaps"])
    lines.extend(render_evidence_closure_packet(report))
    lines.extend([
        "",
        "## Interpretation",
        "",
    ])
    lines.extend(f"- {item}" for item in report["interpretation"])
    lines.extend([
        "",
        "Bottom line: the committed GLM-5.2 evidence is not yet sufficient to",
        "claim Kitsoki beats or loses to raw prompts. The report is useful now as",
        "a reproducible evidence ledger and corpus scaffold; the headline",
        "comparison still requires raw-prompt GLM-5.2 cells and verified",
        "BugSwarm cells.",
    ])
    lines.extend([
        "",
        "## Supporting Codex-Native OSS Round",
        "",
        "The existing arena `round-1` results are supporting evidence for the",
        "Kitsoki-vs-raw-prompt harness and token accounting, but they are not",
        "GLM-5.2 cells. They should not be used to answer the GLM headline.",
        "",
    ])
    supporting = report["rollups"]["supporting_oss_codex_round1"]
    if supporting:
        lines.extend([
            "| treatment | n | attempted | solved | failed | success rate | tokens |",
            "|---|---:|---:|---:|---:|---:|---:|",
        ])
        for treatment, bucket in sorted(supporting.items()):
            lines.append(
                f"| {treatment} | {bucket['n']} | {bucket['attempted']} | {bucket['solved']} | "
                f"{bucket['failed']} | {format_rate(bucket['success_rate'])} | {format_int(bucket['total_tokens'])} |"
            )
    return "\n".join(lines) + "\n"


def render_evidence_closure_packet(report: dict[str, Any]) -> list[str]:
    inputs = report["inputs"]
    rows = [
        row for row in report["required_glm52_matrix"]
        if row["quality"] == "pending"
    ]
    if not rows:
        return [
            "",
            "## Evidence Closure Packet",
            "",
            "No pending GLM-5.2 headline cells remain in this report.",
        ]
    closure = report["evidence_closure"]
    commands = [
        "python3 tools/arena/scripts/glm52_gap_plan.py \\",
        f"  --report-json {inputs['report_json']} \\",
        "  --json-out .artifacts/arena/glm52-gap-plan.json \\",
        "  --markdown-out .artifacts/arena/glm52-gap-plan.md",
    ]
    if inputs.get("oss_arena_rollup"):
        commands.append("  # pass --oss-spec <paired-task spec> when scheduling missing OSS cells")
    if inputs.get("bugswarm_source"):
        commands[-1] += " \\"
        commands.append(f"  --bugswarm-source {inputs['bugswarm_source']}")
    else:
        commands.append("  # pass --bugswarm-source <execute-verified BugSwarm YAML> after importing artifacts")
    return [
        "",
        "## Evidence Closure Packet",
        "",
        "Generate the offline execution packet for the pending headline cells with:",
        "",
        "```bash",
        *commands,
        "```",
        "",
        "The packet emits no-spend `arena.py plan` / arming commands and, only",
        "after a spec passes audit, explicit `ARENA_PAIRED_TASK_ENABLE_CODEX=1",
        "... --live` commands for operator execution.",
        "",
        "| corpus | status | pending | next |",
        "|---|---|---:|---|",
        *[
            f"| {action['corpus']} | `{action['status']}` | {action['pending_count']} | {action['next']} |"
            for action in closure["actions"]
        ],
    ]


def load_yaml(path: Path) -> dict[str, Any]:
    if not path.exists():
        return {}
    data = yaml.safe_load(path.read_text(encoding="utf-8"))
    return data if isinstance(data, dict) else {}


def write_json(path: Path, payload: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def write_text(path: Path, text: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")


def normalize_treatment(value: str) -> str:
    value = value.strip().lower()
    if value in {"single", "single-briefed", "single-naive", "raw", "raw-prompt"}:
        return "raw-prompt"
    if value == "kitsoki":
        return "kitsoki"
    return value or "unknown"


def treatment_from_variant(variant: str) -> str:
    lowered = variant.lower()
    if lowered.startswith("kitsoki"):
        return "kitsoki"
    if lowered.startswith("single") or lowered.startswith("raw-prompt"):
        return "raw-prompt"
    return "unknown"


def normalize_quality(value: str) -> str:
    value = value.strip().lower()
    return value if value in QUALITY_VALUES else "pending"


def as_int(value: Any) -> int | None:
    return value if isinstance(value, int) else None


def as_float(value: Any) -> float | None:
    return float(value) if isinstance(value, (int, float)) else None


def format_int(value: Any) -> str:
    return f"{value:,}" if isinstance(value, int) else "n/a"


def format_cost(value: Any) -> str:
    return f"${value:.6f}" if isinstance(value, (int, float)) else "n/a"


def format_rate(value: Any) -> str:
    return f"{value:.3f}" if isinstance(value, (int, float)) else "n/a"


def rel(path: Path | None) -> str:
    if path is None:
        return ""
    try:
        return str(path.resolve().relative_to(REPO_ROOT))
    except ValueError:
        return str(path)


if __name__ == "__main__":
    raise SystemExit(main())
