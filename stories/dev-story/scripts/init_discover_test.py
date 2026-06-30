#!/usr/bin/env python3
"""No-LLM regression tests for the deterministic onboarding discovery script.

Guards two product-journey QA findings:
  * a Go checkout must get real `go build`/`go test` commands instead of
    "(not yet inferred)" — the deterministic profile should never be
    command-less for a stack with canonical commands;
  * `parse_target` must resolve an explicit target path (the `go_init`
    `target` slot vector) so onboarding can point at an external repo
    deterministically, not just the current checkout.

Pure stdlib, runs against throwaway temp dirs — never a live LLM.
"""

from __future__ import annotations

import importlib.util
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("init_discover", HERE / "init_discover.py")
mod = importlib.util.module_from_spec(SPEC)
assert SPEC and SPEC.loader
SPEC.loader.exec_module(mod)

failures: list[str] = []


def check(label: str, got, want) -> None:
    if got != want:
        failures.append(f"{label}: got {got!r}, want {want!r}")


def _mkrepo(files: dict[str, str]) -> Path:
    root = Path(tempfile.mkdtemp(prefix="kitsoki-discover-"))
    for rel, body in files.items():
        p = root / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(body, encoding="utf-8")
    return root


# 1. Go repo, no Makefile → canonical go commands, no dev server.
go_repo = _mkrepo({"go.mod": "module acme\ngo 1.22\n"})
prof = mod.discover(go_repo)
check("go stack", prof["stack"], "go project")
check("go build", prof["build_command"], "go build ./...")
check("go test", prof["test_command"], "go test ./...")
check("go dev (none)", prof["dev_command"], "")

# 2. Go repo WITH a Makefile → make targets win over the go defaults.
go_make = _mkrepo({"go.mod": "module acme\ngo 1.22\n", "Makefile": "build:\n\t:\ntest:\n\t:\n"})
prof = mod.discover(go_make)
check("go+make build", prof["build_command"], "make build")
check("go+make test", prof["test_command"], "make test")

# 3. Bare-path target (the go_init `target` slot) resolves to that path.
base = _mkrepo({})
other = _mkrepo({"go.mod": "module other\ngo 1.22\n"})
resolved = mod.parse_target(str(other), "", str(base))
check("target slot path", resolved, other.resolve())

# 4. Empty request falls back to the current checkout (unchanged behavior).
fallback = mod.parse_target("", "", str(base))
check("empty target fallback", fallback, base.resolve())

# 5. Natural-language onboard request still extracts its path.
nl = mod.parse_target(f"onboard {other}", "", str(base))
check("onboard <path>", nl, other.resolve())

if failures:
    print("FAIL: init_discover regression")
    for f in failures:
        print("  -", f)
    sys.exit(1)
print("PASS: init_discover regression (go defaults + target slot)")
