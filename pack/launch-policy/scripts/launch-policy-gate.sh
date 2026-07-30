#!/usr/bin/env bash
# Red-team K1 enforcement gate. Run from an installed consumer repository.
set -euo pipefail
root="$(git rev-parse --show-toplevel)"
hook="$root/.claude/hooks/block-bare-checkout.sh"
[ -x "$hook" ] || { echo "FAIL: missing executable Claude guard" >&2; exit 1; }

attempt_blocked() {
  local label="$1" command="$2"
  if CLAUDE_PROJECT_DIR="$root" "$hook" <<EOF
{"tool_name":"Bash","tool_input":{"command":"$command","cwd":"$root"}}
EOF
  then
    echo "FAIL: $label was allowed" >&2
    exit 1
  fi
  echo "PASS: $label blocked"
}

attempt_allowed() {
  local label="$1" command="$2" cwd="$3"
  if CLAUDE_PROJECT_DIR="$root" "$hook" <<EOF
{"tool_name":"Bash","tool_input":{"command":"$command","cwd":"$cwd"}}
EOF
  then
    echo "PASS: $label allowed"
  else
    echo "FAIL: $label was blocked" >&2
    exit 1
  fi
}

attempt_blocked "bare checkout" "git checkout -b main-adjacent"
attempt_blocked "direct main commit" "git commit -m direct-main"
attempt_blocked "bare agent launch" "claude -p unsafe"

# The sanctioned agent work trees are exempt. `unlimited` is explicitly a
# direct-git mode, so `.worktrees/<name>` must not be guarded like the
# protected checkout. Probe paths are physical (`pwd -P`) and need not exist:
# the hook falls back to the literal cwd string when it cannot `cd` there, and
# a logical path would then not string-match its own resolved root.
root_phys="$(cd "$root" && pwd -P)"
attempt_allowed "capsule workspace commit" "git commit -m work" "$root_phys/.capsules/workspaces/gate-probe"
attempt_allowed "unlimited worktree commit" "git commit -m work" "$root_phys/.worktrees/gate-probe"
attempt_allowed "unlimited worktree checkout" "git checkout -b feature" "$root_phys/.worktrees/gate-probe"

parent="$(cd "$root/.." && pwd -P)"
sibling="$parent/launch-policy-sibling-gate"
cleanup() { rm -rf "$sibling"; }
trap cleanup EXIT
git init -q "$sibling"
attempt_blocked "sibling repository write" "printf nope > ../$(basename "$sibling")/blocked.txt"

echo "PASS: K1 launch policy red-team gate"
