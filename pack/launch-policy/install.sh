#!/usr/bin/env bash
# Install K1 launch-policy layers into a Git repository.
set -euo pipefail

pack_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
force=0
siblings=1
target=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --force) force=1; shift ;;
    --no-siblings) siblings=0; shift ;;
    -h|--help) echo "usage: install.sh <target-repo> [--force] [--no-siblings]"; exit 0 ;;
    -*) echo "unknown flag: $1" >&2; exit 2 ;;
    *) [ -z "$target" ] || { echo "one target repository is required" >&2; exit 2; }; target="$1"; shift ;;
  esac
done
[ -n "$target" ] || { echo "usage: install.sh <target-repo> [--force] [--no-siblings]" >&2; exit 2; }
git -C "$target" rev-parse --is-inside-work-tree >/dev/null 2>&1 || { echo "$target is not a Git repository" >&2; exit 2; }
target="$(git -C "$target" rev-parse --show-toplevel)"

changed=0
install_file() {
  local src="$1" dst="$2" mode="$3"
  if [ -e "$dst" ] && cmp -s "$src" "$dst"; then return; fi
  if [ -e "$dst" ] && [ "$force" -ne 1 ]; then
    echo "SKIP $dst differs locally (use --force after review)" >&2
    return 3
  fi
  mkdir -p "$(dirname "$dst")"
  cp "$src" "$dst"
  chmod "$mode" "$dst"
  changed=$((changed + 1))
  echo "installed ${dst#$target/}"
}

roots="    - ."
if [ "$siblings" -eq 1 ]; then
  parent="$(cd "$target/.." && pwd -P)"
  for sibling in "$parent"/*; do
    [ -d "$sibling/.git" ] || continue
    sibling="$(cd "$sibling" && pwd -P)"
    [ "$sibling" = "$target" ] && continue
    roots="$roots\n    - ../$(basename "$sibling")"
  done
fi
policy_tmp="$(mktemp)"
trap 'rm -f "$policy_tmp"' EXIT
sed "s|{{PROTECTED_ROOTS}}|$roots|" "$pack_dir/templates/agent-launch-policy.yaml" > "$policy_tmp"
install_file "$policy_tmp" "$target/.kitsoki.local.yaml" 0644
install_file "$pack_dir/.claude/hooks/block-bare-checkout.sh" "$target/.claude/hooks/block-bare-checkout.sh" 0755
install_file "$pack_dir/templates/settings.json" "$target/.claude/settings.json" 0644
install_file "$pack_dir/scripts/launch-policy-gate.sh" "$target/scripts/launch-policy-gate.sh" 0755

hooks_dir="$(git -C "$target" rev-parse --git-path hooks)"
case "$hooks_dir" in /*) ;; *) hooks_dir="$target/$hooks_dir" ;; esac
install_file "$pack_dir/git-hooks/reference-transaction" "$hooks_dir/reference-transaction" 0755

mkdir -p "$target/.capsules/workspaces"
if [ "$changed" -eq 0 ]; then echo "launch-policy install: no changes"; else echo "launch-policy install: $changed file(s) installed"; fi
