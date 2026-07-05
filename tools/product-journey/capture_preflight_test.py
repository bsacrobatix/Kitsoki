#!/usr/bin/env python3
"""Runner-level test for --capture-preflight.

Run directly:  python3 tools/product-journey/capture_preflight_test.py

The real preflight uses kitsoki web-shot. This test injects tiny local commands
so it never launches Chromium, a live server, or an LLM.
"""

import importlib.util
import shlex
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


def main():
    with tempfile.TemporaryDirectory() as tmp:
        tmp = Path(tmp)
        run.ARTIFACT_ROOT = tmp / "product-journey"
        run.PREFLIGHT_ROOT = run.ARTIFACT_ROOT / "preflights"

        pass_script = tmp / "fake_webshot_pass.py"
        pass_script.write_text(
            "import os\n"
            "from pathlib import Path\n"
            "Path(os.environ['KITSOKI_CAPTURE_PREFLIGHT_OUT']).write_bytes(b'fake-png')\n"
            "print('captured')\n",
            encoding="utf-8",
        )
        result = run.capture_preflight("pass", f"{shlex.quote(sys.executable)} {shlex.quote(str(pass_script))}", 10)
        _check("pass status", result["status"] == "passed")
        _check("pass counts", result["passed"] == 4 and result["failed"] == 0)
        _check("writes preflight json", Path(result["preflight_path"]).exists())
        _check("writes preflight markdown", Path(result["markdown_path"]).exists())
        _check("writes webshot output", Path(result["webshot_output"]).exists())

        fail_script = tmp / "fake_webshot_fail.py"
        fail_script.write_text(
            "import sys\n"
            "print('capture failed', file=sys.stderr)\n"
            "sys.exit(7)\n",
            encoding="utf-8",
        )
        failed = run.capture_preflight("fail", f"{shlex.quote(sys.executable)} {shlex.quote(str(fail_script))}", 10)
        _check("fail status", failed["status"] == "failed")
        _check("fail records command exit", failed["exit_code"] == 7)
        _check("fail records missing output", any(check["id"] == "webshot-output" and check["status"] == "failed" for check in failed["checks"]))

    print("PASS")


if __name__ == "__main__":
    main()
