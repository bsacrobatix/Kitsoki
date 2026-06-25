#!/usr/bin/env python3
"""Load and lint a punch-list/v1 manifest."""

from __future__ import annotations

import json
import sys

from punch_lib import default_state_path, load_yaml_or_json, normalize_manifest, write_state


def main() -> None:
    if len(sys.argv) < 2 or not sys.argv[1]:
        print(json.dumps({"items": [], "item_count": "0", "state_path": "", "error": "manifest_path is required"}))
        return
    manifest_path = sys.argv[1]
    state_path = sys.argv[2] if len(sys.argv) > 2 and sys.argv[2] else default_state_path(manifest_path)

    try:
        doc = load_yaml_or_json(manifest_path)
        items, errors, defaults = normalize_manifest(doc)
    except Exception as exc:  # noqa: BLE001
        print(json.dumps({"items": [], "item_count": "0", "state_path": state_path, "error": f"parse failed: {exc}"}))
        return

    state = {
        "manifest_path": manifest_path,
        "defaults": defaults,
        "items": items,
        "results": {"items": []},
        "error": "\n".join(errors),
    }
    try:
        write_state(state_path, state)
    except Exception as exc:  # noqa: BLE001
        print(json.dumps({"items": items, "item_count": str(len(items)), "state_path": state_path, "error": f"state write failed: {exc}"}))
        return

    print(json.dumps({"items": items, "item_count": str(len(items)), "state_path": state_path, "error": "\n".join(errors)}))


if __name__ == "__main__":
    main()
