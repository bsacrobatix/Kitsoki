#!/usr/bin/env bash
# Focused regression tests for scripts/dev-workspace.sh. These tests create
# disposable repositories because the behavior under test is repository
# creation/merge/teardown itself.
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
dev_workspace="$script_dir/dev-workspace.sh"

tmp="$(mktemp -d "${TMPDIR:-/tmp}/kitsoki-dev-workspace-test.XXXXXX")"
cleanup() {
  rm -rf "$tmp"
}
trap cleanup EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

gitc() {
  git -C "$1" -c user.name="Test User" -c user.email="test@example.com" "${@:2}"
}

write_merge_helper() {
  local repo="$1"
  mkdir -p "$repo/scripts"
  cat >"$repo/scripts/merge-to-main.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
branch="${1:?branch required}"
[ "$(git branch --show-current)" = "main" ] || { echo "not on main" >&2; exit 1; }
git merge --ff-only "$branch"
SH
  chmod +x "$repo/scripts/merge-to-main.sh"
}

source_repo="$tmp/source"
mkdir -p "$source_repo"
git -C "$source_repo" init --quiet --initial-branch=main
write_merge_helper "$source_repo"
printf 'one\n' >"$source_repo/README.md"
gitc "$source_repo" add -A
gitc "$source_repo" commit --quiet -m initial

root="$tmp/workspaces"
create_json="$("$dev_workspace" create --repo "$source_repo" --root "$root" --id case-1 --branch agent/case-1 --base main --json)"
workspace="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["path"])' <<<"$create_json")"
[ -d "$workspace/.git" ] || fail "create did not produce a git workspace"
[ -f "$workspace/.kitsoki-capsule" ] || fail "missing capsule sentinel"
[ -f "$workspace/capsule-manifest.json" ] || fail "missing capsule manifest"
[ -f "$workspace/.kitsoki-clone" ] || fail "missing clone sentinel"
[ "$(git -C "$workspace" branch --show-current)" = "agent/case-1" ] || fail "workspace branch mismatch"

printf 'two\n' >"$workspace/feature.txt"
"$dev_workspace" commit --repo "$source_repo" --root "$root" "$workspace" --message 'add feature' >/dev/null
"$dev_workspace" merge --repo "$source_repo" --root "$root" "$workspace" --teardown >/dev/null
[ ! -e "$workspace" ] || fail "merge --teardown left workspace behind"
[ "$(cat "$source_repo/feature.txt")" = "two" ] || fail "merge did not fast-forward source repo"

dirty_json="$("$dev_workspace" create --repo "$source_repo" --root "$root" --id dirty --branch agent/dirty --base main --json)"
dirty_workspace="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["path"])' <<<"$dirty_json")"
printf 'dirty\n' >"$dirty_workspace/untracked.txt"
if "$dev_workspace" close --repo "$source_repo" --root "$root" "$dirty_workspace" >/tmp/kitsoki-dev-workspace-close.log 2>&1; then
  fail "close succeeded on a dirty workspace"
fi
"$dev_workspace" close --repo "$source_repo" --root "$root" --force "$dirty_workspace" >/dev/null
[ ! -e "$dirty_workspace" ] || fail "close --force left workspace behind"

echo "dev-workspace tests passed"
