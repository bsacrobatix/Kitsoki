#!/usr/bin/env bash
# Hermetic install + K1 red-team proof. The temporary repo is intentional: this
# test verifies Git hook installation and exact Git command behavior.
set -euo pipefail
pack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
repo="$tmp/repo"
git init -q -b main "$repo"
git -C "$repo" config user.name "Launch Policy Gate"
git -C "$repo" config user.email "launch-policy@example.invalid"
git -C "$repo" commit -q --allow-empty -m init

"$pack_dir/install.sh" "$repo" --no-siblings
test -x "$repo/.claude/hooks/block-bare-checkout.sh"
node -e 'JSON.parse(require("fs").readFileSync(process.argv[1], "utf8"))' "$repo/.claude/settings.json"
grep -q 'require_capsule: false' "$repo/.kitsoki.local.yaml"
grep -q '\.capsules/workspaces' "$repo/.kitsoki.local.yaml"

if git -C "$repo" checkout -q -b should-be-blocked 2>/dev/null; then
  echo "FAIL: reference-transaction did not pin primary checkout" >&2
  exit 1
fi
(cd "$repo" && scripts/launch-policy-gate.sh)

out="$("$pack_dir/install.sh" "$repo" --no-siblings)"
grep -q 'no changes' <<<"$out" || { echo "FAIL: installer was not idempotent" >&2; exit 1; }
echo "PASS: launch-policy pack install and red-team gate"
