#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

for path in \
  "$root/docs/implementation-handoff.md" \
  "$root/pog/schemas/implementation-handoff-v0.md" \
  "$root/scripts/generate-implementation-handoff.sh"
do
  [[ -f "$path" ]] || { echo "missing $path" >&2; exit 1; }
done

out="$tmp/handoff.md"
"$root/scripts/generate-implementation-handoff.sh" \
  --changeset change-acceptance \
  --target-repo /tmp/target-repo \
  --acceptance "focused validation passes" \
  --context docs/pog-driver.md \
  --out "$out" >/dev/null

grep -q '^schema: implementation-handoff/v0$' "$out"
grep -q '^changeset: change-acceptance$' "$out"
grep -q '^target_repo: /tmp/target-repo$' "$out"
grep -q '^sanctioned_command: codex superagent' "$out"

echo "implementation handoff gate passed"
