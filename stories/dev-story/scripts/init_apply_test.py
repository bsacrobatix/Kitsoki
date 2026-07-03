#!/usr/bin/env python3
"""No-LLM regression tests for deterministic onboarding apply.

The story flow fixtures stub host.run, so they cannot prove the Python apply
script validates generated profiles before writing files. These tests run the
script against temp repos and fake `kitsoki project-profile validate` binaries.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
SCRIPT = HERE / "init_apply.py"

failures: list[str] = []


def check(label: str, condition: bool, detail: str = "") -> None:
    if not condition:
        failures.append(f"{label}{': ' + detail if detail else ''}")


def mkrepo() -> Path:
    root = Path(tempfile.mkdtemp(prefix="kitsoki-apply-"))
    (root / "go.mod").write_text("module acme\ngo 1.22\n", encoding="utf-8")
    return root


def fake_kitsoki(ok: bool) -> Path:
    root = Path(tempfile.mkdtemp(prefix="kitsoki-bin-"))
    path = root / "kitsoki"
    payload = {"ok": ok}
    if not ok:
        payload["schema"] = ["forced invalid profile"]
    path.write_text(
        "#!/bin/sh\n"
        "case \"$*\" in\n"
        "  *'project-profile validate'*)\n"
        f"    printf '%s\\n' {json.dumps(json.dumps(payload))}\n"
        f"    exit {0 if ok else 1}\n"
        "    ;;\n"
        "  *) echo unexpected command: \"$*\" >&2; exit 2 ;;\n"
        "esac\n",
        encoding="utf-8",
    )
    path.chmod(0o755)
    return path


def run_apply(repo: Path, validator: Path) -> subprocess.CompletedProcess[str]:
    env = os.environ.copy()
    env["KITSOKI_BIN"] = str(validator)
    return subprocess.run(
        [
            sys.executable,
            str(SCRIPT),
            str(repo),
            "acme",
            "Acme",
            "go project",
            "",
            "go test ./...",
            "go build ./...",
            "local defaults",
            "none",
        ],
        check=False,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        env=env,
    )


# 1. Validation failure is loud and happens before onboarding files are written.
repo = mkrepo()
proc = run_apply(repo, fake_kitsoki(False))
check("invalid exit", proc.returncode != 0, proc.stdout + proc.stderr)
check("invalid no config write", not (repo / ".kitsoki.yaml").exists())
check("invalid no profile write", not (repo / ".kitsoki" / "project-profile.yaml").exists())
try:
    invalid_report = json.loads(proc.stdout)
except json.JSONDecodeError as err:
    failures.append(f"invalid json: {err}: {proc.stdout!r}")
else:
    check("invalid status", invalid_report.get("status") == "profile-validation-failed", str(invalid_report))
    check("invalid carries diagnostics", invalid_report.get("profile_validation", {}).get("schema") == ["forced invalid profile"])

# 2. Validation success writes files and carries the validation report.
repo = mkrepo()
proc = run_apply(repo, fake_kitsoki(True))
check("valid exit", proc.returncode == 0, proc.stdout + proc.stderr)
check("valid config write", (repo / ".kitsoki.yaml").exists())
profile_path = repo / ".kitsoki" / "project-profile.yaml"
check("valid profile write", profile_path.exists())
try:
    valid_report = json.loads(proc.stdout)
except json.JSONDecodeError as err:
    failures.append(f"valid json: {err}: {proc.stdout!r}")
else:
    check("valid status", valid_report.get("status") == "applied", str(valid_report))
    check("valid profile validation", valid_report.get("profile_validation", {}).get("ok") is True)
if profile_path.exists():
    profile_text = profile_path.read_text(encoding="utf-8")
    check("valid setup plan present", "setup_plan:" in profile_text)
    check("valid setup writes instance", ".kitsoki/stories/acme-dev/app.yaml" in profile_text)
    check("valid setup gates build", "command: \"go build ./...\"" in profile_text)
    check("valid setup gates tests", "command: \"go test ./...\"" in profile_text)

if failures:
    print("FAIL: init_apply regression")
    for failure in failures:
        print("  -", failure)
    sys.exit(1)
print("PASS: init_apply regression (profile validation gates writes)")
