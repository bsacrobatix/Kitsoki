#!/usr/bin/env bash
# Installed as .kitsoki/bin/{claude,codex}. It is intentionally tiny: policy
# resolution stays in `kitsoki agent launch`, while this wrapper preserves the
# exact native CLI argv that the caller supplied.
set -euo pipefail

backend="$(basename "$0")"
case "$backend" in claude|codex) ;; *) echo "unsupported Kitsoki launcher shim: $backend" >&2; exit 2 ;; esac

upper="${backend^^}"
override="KITSOKI_AGENT_${upper}_BIN"
real_override="KITSOKI_AGENT_${upper}_REAL_BIN"

if [ "${KITSOKI_AGENT_LAUNCH_SHIM_ACTIVE:-}" = "1" ]; then
  real="${!real_override:-}"
  if [ -z "$real" ]; then
    shim_dir="$(cd "$(dirname "$0")" && pwd -P)"
    old_path="$PATH"
    PATH="$(printf '%s' "$PATH" | awk -v shim="$shim_dir" -F: '{for (i=1;i<=NF;i++) if ($i != shim) printf "%s%s", sep, $i; sep=":"}')"
    real="$(command -v "$backend" || true)"
    PATH="$old_path"
  fi
  [ -n "$real" ] || { echo "Kitsoki launcher shim: real $backend binary not found; set $real_override" >&2; exit 127; }
  exec "$real" "$@"
fi

kitsoki_bin="${KITSOKI_BIN:-kitsoki}"
args=(agent launch --raw --interactive --backend "$backend" --working-dir "$PWD")
for arg in "$@"; do args+=(--raw-arg "$arg"); done
KITSOKI_AGENT_LAUNCH_SHIM_ACTIVE=1 "$kitsoki_bin" "${args[@]}"
