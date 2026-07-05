#!/usr/bin/env python3
"""generate.py — render the dev-workflow surface matrix from its manifest.

WS-F F1 of .context/dev-workflows-surface-matrix-plan.md: the 5-workflow ×
4-surface × 2-repo matrix is a GENERATED artifact seeded from a hand-edited
manifest (tools/dev-workflow-matrix/manifest.yaml), so progress is visible
and regressions bite. This generator reads the manifest plus any standing
completion-state verdict files (schemas/completion-state.schema.json) the
cells point at, and emits the matrix as markdown with BOTH proof-class
verdicts per cell (mechanical = check_type `replay`; experience = one of the
judged check types) and each verdict's freshness (verdict-file mtime date).

A cell without a verdict pointer — or whose pointed file is missing — renders
as "no standing verdict". That is the honest steady state today; the WS-F
arena gate later fills these in.

Usage:
  python3 tools/dev-workflow-matrix/generate.py                 # print to stdout
  python3 tools/dev-workflow-matrix/generate.py --out PATH      # write file
  python3 tools/dev-workflow-matrix/generate.py --manifest PATH --repo-root DIR

The output is deterministic given the manifest + verdict files (no generation
timestamp), so regenerating without changes produces no diff.
"""
from __future__ import annotations

import argparse
import datetime as _dt
import json
import sys
from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_MANIFEST = Path(__file__).resolve().parent / "manifest.yaml"
DEFAULT_OUT = REPO_ROOT / "docs" / "testing" / "dev-workflow-matrix.md"

STATUS_EMOJI = {
    "works": "✅",
    "proof-thin": "🟡",
    "gap": "🔴",
    "out-of-scope": "⬜",
}

# Proof classes and the completion-state check_type values each accepts
# (plan §2 principle 1 / WS-G G1; schemas/completion-state.schema.json).
MECHANICAL_CHECK_TYPES = {"replay"}
EXPERIENCE_CHECK_TYPES = {"docs-fidelity", "ux-heuristic", "journey-verdict"}
PROOF_CLASSES = ("mechanical", "experience")


class ManifestError(ValueError):
    """Raised when the manifest is structurally invalid."""


def load_manifest(path: Path) -> dict:
    with open(path, encoding="utf-8") as fh:
        manifest = yaml.safe_load(fh)
    if not isinstance(manifest, dict):
        raise ManifestError(f"{path}: manifest must be a mapping")
    validate_manifest(manifest, path)
    return manifest


def _ids(manifest: dict, key: str) -> list[str]:
    entries = manifest.get(key) or []
    out = []
    for entry in entries:
        if not isinstance(entry, dict) or "id" not in entry or "title" not in entry:
            raise ManifestError(f"{key}: every entry needs id + title, got {entry!r}")
        out.append(entry["id"])
    if not out:
        raise ManifestError(f"{key}: must be non-empty")
    return out


def validate_manifest(manifest: dict, path: Path) -> None:
    workflows = _ids(manifest, "workflows")
    surfaces = _ids(manifest, "surfaces")
    repos = _ids(manifest, "repos")

    cells = manifest.get("cells") or []
    seen: set[tuple[str, str, str]] = set()
    for cell in cells:
        key = (cell.get("workflow"), cell.get("surface"), cell.get("repo"))
        if key in seen:
            raise ManifestError(f"{path}: duplicate cell {key}")
        seen.add(key)
        if cell.get("workflow") not in workflows:
            raise ManifestError(f"{path}: unknown workflow in cell {key}")
        if cell.get("surface") not in surfaces:
            raise ManifestError(f"{path}: unknown surface in cell {key}")
        if cell.get("repo") not in repos:
            raise ManifestError(f"{path}: unknown repo in cell {key}")
        status = cell.get("status")
        if status not in STATUS_EMOJI:
            raise ManifestError(
                f"{path}: cell {key} status {status!r} not in {sorted(STATUS_EMOJI)}"
            )
        if not cell.get("reason"):
            raise ManifestError(f"{path}: cell {key} needs a one-line reason")
        verdicts = cell.get("verdicts") or {}
        for proof_class, spec in verdicts.items():
            if proof_class not in PROOF_CLASSES:
                raise ManifestError(
                    f"{path}: cell {key} verdict class {proof_class!r} not in {PROOF_CLASSES}"
                )
            allowed = (
                MECHANICAL_CHECK_TYPES if proof_class == "mechanical" else EXPERIENCE_CHECK_TYPES
            )
            check_type = (spec or {}).get("check_type")
            if check_type not in allowed:
                raise ManifestError(
                    f"{path}: cell {key} {proof_class} check_type {check_type!r} "
                    f"must be one of {sorted(allowed)}"
                )
            if not (spec or {}).get("path"):
                raise ManifestError(
                    f"{path}: cell {key} {proof_class} verdict needs a repo-relative path "
                    "(omit the verdicts entry entirely when there is no standing verdict)"
                )

    missing = [
        (w, s, r)
        for r in repos
        for w in workflows
        for s in surfaces
        if (w, s, r) not in seen
    ]
    if missing:
        raise ManifestError(f"{path}: missing cells: {missing}")


def read_verdict(spec: dict | None, repo_root: Path) -> str:
    """One proof-class verdict summary line: verdict + check_type + freshness."""
    if not spec:
        return "no standing verdict"
    verdict_path = repo_root / spec["path"]
    if not verdict_path.exists():
        return f"no standing verdict (pointer `{spec['path']}` absent)"
    try:
        payload = json.loads(verdict_path.read_text(encoding="utf-8"))
        verdict = payload["verdict"]
    except (json.JSONDecodeError, KeyError, OSError) as err:
        return f"unreadable verdict at `{spec['path']}` ({type(err).__name__})"
    fresh = _dt.date.fromtimestamp(verdict_path.stat().st_mtime).isoformat()
    return f"**{verdict}** ({spec['check_type']}, as of {fresh})"


def render(manifest: dict, repo_root: Path) -> str:
    workflows = manifest["workflows"]
    surfaces = manifest["surfaces"]
    repos = manifest["repos"]
    cells = {
        (c["workflow"], c["surface"], c["repo"]): c for c in manifest["cells"]
    }

    manifest_rel = "tools/dev-workflow-matrix/manifest.yaml"
    lines = [
        "<!-- GENERATED FILE — DO NOT HAND-EDIT.",
        f"     Source of truth: {manifest_rel}",
        "     Regenerate with: make dev-workflow-matrix -->",
        "",
        "# Dev-workflow surface matrix",
        "",
        f"Generated from `{manifest_rel}` by `tools/dev-workflow-matrix/generate.py`.",
        f"Plan: `{manifest.get('plan', '')}` (§1 target matrix, WS-F F1).",
        "",
        "Legend: ✅ works + no-LLM proven · 🟡 works but proof thin or reliability"
        " suspect · 🔴 gap · ⬜ intentionally out of scope for the surface.",
        "",
        "Every cell carries two proof-class verdicts from"
        " `schemas/completion-state.schema.json`: **mechanical** (check_type"
        " `replay`; no-LLM, per-commit) and **experience** (check_type"
        " `docs-fidelity` / `ux-heuristic` / `journey-verdict`; persona-judged,"
        " budgeted — WS-G). \"no standing verdict\" is the honest state until the"
        " arena gate feeds this matrix.",
        "",
    ]

    for repo in repos:
        lines.append(f"## {repo['title']}")
        lines.append("")
        header = "| Workflow | " + " | ".join(s["title"] for s in surfaces) + " |"
        lines.append(header)
        lines.append("|---|" + "---|" * len(surfaces))
        for wf in workflows:
            row = [f"**{wf['title']}**"]
            for sf in surfaces:
                cell = cells[(wf["id"], sf["id"], repo["id"])]
                row.append(f"{STATUS_EMOJI[cell['status']]} {cell['status']}")
            lines.append("| " + " | ".join(row) + " |")
        lines.append("")
        lines.append("### Cell detail")
        lines.append("")
        for wf in workflows:
            for sf in surfaces:
                cell = cells[(wf["id"], sf["id"], repo["id"])]
                verdicts = cell.get("verdicts") or {}
                lines.append(
                    f"- **{wf['title']} × {sf['title']}** — "
                    f"{STATUS_EMOJI[cell['status']]} {cell['status']}: {cell['reason']}"
                )
                lines.append(
                    f"  - mechanical: {read_verdict(verdicts.get('mechanical'), repo_root)}"
                )
                lines.append(
                    f"  - experience: {read_verdict(verdicts.get('experience'), repo_root)}"
                )
        lines.append("")

    return "\n".join(lines).rstrip() + "\n"


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--manifest", type=Path, default=DEFAULT_MANIFEST)
    parser.add_argument("--repo-root", type=Path, default=REPO_ROOT)
    parser.add_argument("--out", type=Path, default=None, help="write here instead of stdout")
    args = parser.parse_args(argv)

    try:
        manifest = load_manifest(args.manifest)
    except ManifestError as err:
        print(f"[dev-workflow-matrix] manifest invalid: {err}", file=sys.stderr)
        return 2

    markdown = render(manifest, args.repo_root)
    if args.out:
        args.out.parent.mkdir(parents=True, exist_ok=True)
        args.out.write_text(markdown, encoding="utf-8")
        print(f"[dev-workflow-matrix] wrote {args.out}")
    else:
        sys.stdout.write(markdown)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
