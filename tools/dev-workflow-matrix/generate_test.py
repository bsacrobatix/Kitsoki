#!/usr/bin/env python3
"""Tests for the dev-workflow-matrix generator.

Run directly:  python3 tools/dev-workflow-matrix/generate_test.py

Covers: seeded manifest -> expected markdown structure, missing verdict ->
honest "no standing verdict", standing verdict file -> verdict + freshness,
manifest validation (missing cell / bad status / bad check_type), and that
the real checked-in manifest loads and stays in sync with the committed
docs/testing/dev-workflow-matrix.md.
"""
from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
from pathlib import Path

_spec = importlib.util.spec_from_file_location(
    "dwm_generate", str(Path(__file__).with_name("generate.py"))
)
gen = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(gen)

import yaml  # noqa: E402


def _check(name, cond):
    if not cond:
        print(f"FAIL: {name}")
        sys.exit(1)
    print(f"ok: {name}")


def seed_manifest(**cell_overrides) -> dict:
    cell = {
        "workflow": "wf1",
        "surface": "s1",
        "repo": "r1",
        "status": "proof-thin",
        "reason": "works but proof thin",
    }
    cell.update(cell_overrides)
    return {
        "schema_version": "1.0.0",
        "plan": ".context/plan.md",
        "workflows": [{"id": "wf1", "title": "Workflow One"}],
        "surfaces": [{"id": "s1", "title": "Surface One"}],
        "repos": [{"id": "r1", "title": "Repo One"}],
        "cells": [cell],
    }


def load(manifest: dict, tmp: Path) -> dict:
    path = tmp / "manifest.yaml"
    path.write_text(yaml.safe_dump(manifest), encoding="utf-8")
    return gen.load_manifest(path)


def main():
    with tempfile.TemporaryDirectory() as tmpdir:
        tmp = Path(tmpdir)

        # 1. seeded manifest -> expected markdown
        manifest = load(seed_manifest(), tmp)
        md = gen.render(manifest, tmp)
        _check("has DO-NOT-HAND-EDIT header", "DO NOT HAND-EDIT" in md)
        _check("points at the manifest", "tools/dev-workflow-matrix/manifest.yaml" in md)
        _check("repo section rendered", "## Repo One" in md)
        _check("table row rendered", "| **Workflow One** | 🟡 proof-thin |" in md)
        _check("reason in cell detail", "works but proof thin" in md)

        # 2. no verdict pointer -> honest "no standing verdict" for BOTH classes
        _check(
            "both proof classes say no standing verdict",
            "  - mechanical: no standing verdict" in md
            and "  - experience: no standing verdict" in md,
        )

        # 3. pointer to an absent file -> still honest, names the pointer
        manifest = load(
            seed_manifest(
                verdicts={
                    "mechanical": {"check_type": "replay", "path": "gone.json"}
                }
            ),
            tmp,
        )
        md = gen.render(manifest, tmp)
        _check(
            "absent verdict file named",
            "no standing verdict (pointer `gone.json` absent)" in md,
        )

        # 4. standing verdict file -> verdict + check_type + freshness date
        verdict_path = tmp / "verdicts" / "cell.json"
        verdict_path.parent.mkdir(parents=True)
        verdict_path.write_text(
            json.dumps(
                {
                    "schema_version": "1.0.0",
                    "verdict": "solved",
                    "health": "model:result",
                    "metrics": {},
                    "evidence_refs": [],
                }
            ),
            encoding="utf-8",
        )
        manifest = load(
            seed_manifest(
                verdicts={
                    "mechanical": {"check_type": "replay", "path": "verdicts/cell.json"},
                    "experience": {
                        "check_type": "journey-verdict",
                        "path": "verdicts/cell.json",
                    },
                }
            ),
            tmp,
        )
        md = gen.render(manifest, tmp)
        _check("mechanical verdict shown", "**solved** (replay, as of " in md)
        _check("experience verdict shown", "**solved** (journey-verdict, as of " in md)

        # 5. unreadable verdict file is flagged, not crashed on
        bad = tmp / "bad.json"
        bad.write_text("{not json", encoding="utf-8")
        manifest = load(
            seed_manifest(
                verdicts={"mechanical": {"check_type": "replay", "path": "bad.json"}}
            ),
            tmp,
        )
        md = gen.render(manifest, tmp)
        _check("unreadable verdict flagged", "unreadable verdict at `bad.json`" in md)

        # 6. validation: missing cell of the cross product
        m = seed_manifest()
        m["surfaces"].append({"id": "s2", "title": "Surface Two"})
        try:
            load(m, tmp)
            _check("missing cell rejected", False)
        except gen.ManifestError as err:
            _check("missing cell rejected", "missing cells" in str(err))

        # 7. validation: unknown status
        try:
            load(seed_manifest(status="meh"), tmp)
            _check("bad status rejected", False)
        except gen.ManifestError as err:
            _check("bad status rejected", "status" in str(err))

        # 8. validation: experience check_type must be a judged type
        try:
            load(
                seed_manifest(
                    verdicts={
                        "experience": {"check_type": "replay", "path": "x.json"}
                    }
                ),
                tmp,
            )
            _check("replay rejected as experience check_type", False)
        except gen.ManifestError as err:
            _check("replay rejected as experience check_type", "check_type" in str(err))

        # 9. validation: duplicate cell
        m = seed_manifest()
        m["cells"].append(dict(m["cells"][0]))
        try:
            load(m, tmp)
            _check("duplicate cell rejected", False)
        except gen.ManifestError as err:
            _check("duplicate cell rejected", "duplicate" in str(err))

    # 10. the real checked-in manifest loads and matches the committed matrix
    real = gen.load_manifest(gen.DEFAULT_MANIFEST)
    _check(
        "real manifest covers 5x4x2 = 40 cells",
        len(real["cells"]) == 40,
    )
    rendered = gen.render(real, gen.REPO_ROOT)
    committed = gen.DEFAULT_OUT
    if committed.exists():
        _check(
            "committed matrix is fresh (rerun make dev-workflow-matrix if not)",
            committed.read_text(encoding="utf-8") == rendered,
        )
    else:
        print("skip: committed matrix not present yet")

    print("PASS")


if __name__ == "__main__":
    main()
