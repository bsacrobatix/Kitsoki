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
source_dir=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --source-dir)
      source_dir="${2:?--source-dir requires a value}"
      shift 2
      ;;
    --gate)
      shift 2
      ;;
    --force)
      shift
      ;;
    *)
      if [ "$1" = "$branch" ]; then
        shift
      else
        shift
      fi
      ;;
  esac
done
[ "$(git branch --show-current)" = "main" ] || { echo "not on main" >&2; exit 1; }
if [ -n "$source_dir" ]; then
  git fetch "$source_dir" "$branch:refs/heads/capsule/test-main-land" >/dev/null
  branch="capsule/test-main-land"
fi
git merge --ff-only "$branch"
SH
  chmod +x "$repo/scripts/merge-to-main.sh"
}

source_repo="$tmp/source"
mkdir -p "$source_repo"
git -C "$source_repo" init --quiet --initial-branch=main
write_merge_helper "$source_repo"
printf '.kitsoki.local.yaml\n' >"$source_repo/.gitignore"
cat >"$source_repo/Makefile" <<'MK'
bootstrap-workspace:
	@true

bootstrap-worktree: bootstrap-workspace
MK
printf 'one\n' >"$source_repo/README.md"
printf 'harness_profiles:\n  local:\n    backend: codex\n' >"$source_repo/.kitsoki.local.yaml"
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

"$dev_workspace" create --repo "$source_repo" --root "$root" --id local-config --branch agent/local-config --base main --bootstrap >/dev/null
local_config_workspace="$root/local-config"
[ -f "$local_config_workspace/.kitsoki.local.yaml" ] || fail "bootstrap did not copy .kitsoki.local.yaml"
cmp "$source_repo/.kitsoki.local.yaml" "$local_config_workspace/.kitsoki.local.yaml" >/dev/null || fail "bootstrap copied the wrong .kitsoki.local.yaml content"
"$dev_workspace" close --repo "$source_repo" --root "$root" "$local_config_workspace" >/dev/null

printf 'local scratch\n' >"$source_repo/untracked-primary.txt"
printf 'two\n' >"$workspace/feature.txt"
"$dev_workspace" commit --repo "$source_repo" --root "$root" "$workspace" --message 'add feature' >/dev/null
"$dev_workspace" merge --repo "$source_repo" --root "$root" "$workspace" --teardown >/dev/null
[ ! -e "$workspace" ] || fail "merge --teardown left workspace behind"
[ ! -e "$source_repo/feature.txt" ] || fail "default merge should not modify primary main working tree"
[ "$(git -C "$source_repo" show staging/local:feature.txt)" = "two" ] || fail "merge did not fast-forward staging/local"
[ "$(git -C "$source_repo" branch --show-current)" = "main" ] || fail "default merge moved primary checkout off main"
rm "$source_repo/untracked-primary.txt"

from_staging_json="$("$dev_workspace" create --repo "$source_repo" --root "$root" --id from-staging --branch agent/from-staging --base staging/local --json)"
from_staging_workspace="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["path"])' <<<"$from_staging_json")"
[ "$(cat "$from_staging_workspace/feature.txt")" = "two" ] || fail "create --base staging/local did not start from staging branch"
"$dev_workspace" close --repo "$source_repo" --root "$root" "$from_staging_workspace" >/dev/null

second_json="$("$dev_workspace" create --repo "$source_repo" --root "$root" --id case-2 --branch agent/case-2 --base main --json)"
second_workspace="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["path"])' <<<"$second_json")"
printf 'three\n' >"$second_workspace/feature2.txt"
"$dev_workspace" commit --repo "$source_repo" --root "$root" "$second_workspace" --message 'add second feature' >/dev/null
"$dev_workspace" merge --repo "$source_repo" --root "$root" "$second_workspace" --teardown >/dev/null
[ ! -e "$second_workspace" ] || fail "second merge --teardown left workspace behind"
[ "$(git -C "$source_repo" show staging/local:feature.txt)" = "two" ] || fail "second merge dropped existing staging/local content"
[ "$(git -C "$source_repo" show staging/local:feature2.txt)" = "three" ] || fail "second merge did not advance existing staging/local"

main_json="$("$dev_workspace" create --repo "$source_repo" --root "$root" --id case-main --branch agent/case-main --base main --target main --json)"
main_workspace="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["path"])' <<<"$main_json")"
[ "$(python3 -c 'import json,sys; print(json.load(sys.stdin)["target"])' <"$main_workspace/.kitsoki-dev-workspace.json")" = "main" ] || fail "create --target main was not recorded in workspace manifest"
printf 'main landing\n' >"$main_workspace/main-feature.txt"
"$dev_workspace" commit --repo "$source_repo" --root "$root" "$main_workspace" --message 'add main feature' >/dev/null
"$dev_workspace" merge --repo "$source_repo" --root "$root" "$main_workspace" --target main --gate true --teardown >/dev/null
[ ! -e "$main_workspace" ] || fail "merge --target main --teardown left workspace behind"
[ "$(cat "$source_repo/main-feature.txt")" = "main landing" ] || fail "explicit main merge did not update primary checkout"

unrelated_json="$("$dev_workspace" create --repo "$source_repo" --root "$root" --id unrelated-dirty --branch agent/unrelated-dirty --base main --json)"
unrelated_workspace="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["path"])' <<<"$unrelated_json")"
printf 'branch\n' >"$unrelated_workspace/branch-only.txt"
"$dev_workspace" commit --repo "$source_repo" --root "$root" "$unrelated_workspace" --message 'add branch-only file' >/dev/null
printf 'local\n' >"$source_repo/local-only.txt"
"$dev_workspace" merge --repo "$source_repo" --root "$root" "$unrelated_workspace" --teardown >/dev/null
[ ! -e "$unrelated_workspace" ] || fail "merge with unrelated dirty primary left workspace behind"
[ "$(git -C "$source_repo" show staging/local:branch-only.txt)" = "branch" ] || fail "merge skipped branch-only file"
[ "$(cat "$source_repo/local-only.txt")" = "local" ] || fail "merge clobbered unrelated dirty primary file"

overlap_json="$("$dev_workspace" create --repo "$source_repo" --root "$root" --id overlapping-dirty --branch agent/overlapping-dirty --base main --target main --json)"
overlap_workspace="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["path"])' <<<"$overlap_json")"
printf 'branch\n' >"$overlap_workspace/conflict.txt"
"$dev_workspace" commit --repo "$source_repo" --root "$root" "$overlap_workspace" --message 'add conflict file' >/dev/null
printf 'local\n' >"$source_repo/conflict.txt"
if "$dev_workspace" merge --repo "$source_repo" --root "$root" "$overlap_workspace" --target main --gate true --teardown >/tmp/kitsoki-dev-workspace-overlap.log 2>&1; then
  fail "merge succeeded despite overlapping dirty primary file"
fi
grep -Fq "conflict.txt" /tmp/kitsoki-dev-workspace-overlap.log || fail "overlap failure did not name conflicting file"
"$dev_workspace" close --repo "$source_repo" --root "$root" --force "$overlap_workspace" >/dev/null

dirty_json="$("$dev_workspace" create --repo "$source_repo" --root "$root" --id dirty --branch agent/dirty --base main --json)"
dirty_workspace="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["path"])' <<<"$dirty_json")"
printf 'dirty\n' >"$dirty_workspace/untracked.txt"
if "$dev_workspace" close --repo "$source_repo" --root "$root" "$dirty_workspace" >/tmp/kitsoki-dev-workspace-close.log 2>&1; then
  fail "close succeeded on a dirty workspace"
fi
"$dev_workspace" close --repo "$source_repo" --root "$root" --force "$dirty_workspace" >/dev/null
[ ! -e "$dirty_workspace" ] || fail "close --force left workspace behind"

echo "dev-workspace tests passed"
