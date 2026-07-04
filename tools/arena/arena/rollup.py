"""Arena's rollup — a thin delegating shim over the shared reporting module.

The bucket/build_rollup/write_rollup logic used to live here as a private copy
that mirrored product-journey's own rollup implementation without importing
it — two rollup brains drifting independently. It has been extracted to
`tools/persona_qa/reporting.py` as the single shared implementation
(generalized over completion-state-shaped records for both the bugfix and
persona-qa shapes); this module now only adapts arena's call sites to it,
keeping arena's title ("Arena rollup") and default axes for byte-compatible
output. See tools/persona_qa/tests/test_shared_rollup.py for the golden test
proving that byte-compatibility.
"""

from __future__ import annotations

from pathlib import Path
from typing import Any

import sys

# tools/arena/arena/rollup.py -> parents[3] is the repo root, where
# tools/persona_qa lives as a sibling package to tools/arena.
_REPO_ROOT = Path(__file__).resolve().parents[3]
if str(_REPO_ROOT) not in sys.path:
    sys.path.insert(0, str(_REPO_ROOT))

from tools.persona_qa.reporting import build_rollup as _shared_build_rollup  # noqa: E402
from tools.persona_qa.reporting import write_rollup as _shared_write_rollup  # noqa: E402
from tools.persona_qa.reporting import _markdown as _shared_markdown  # noqa: E402

from .model import CellResult

_TITLE = "Arena rollup"


def build_rollup(results: list[CellResult]) -> dict[str, Any]:
    return _shared_build_rollup(results)


def write_rollup(results: list[CellResult], out_dir: str | Path) -> dict[str, str]:
    return _shared_write_rollup(results, out_dir, title=_TITLE)


def _markdown(rollup: dict[str, Any]) -> str:
    # Kept for any existing direct callers/tests of arena's private markdown
    # helper; delegates to the shared renderer with arena's title.
    return _shared_markdown(rollup, title=_TITLE)
