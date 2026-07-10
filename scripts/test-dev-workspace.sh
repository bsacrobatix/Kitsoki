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

stat_device_inode() {
  stat -f '%d:%i' "$1" 2>/dev/null || stat -c '%d:%i' "$1"
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
	@echo "bootstrap output"

bootstrap-worktree: bootstrap-workspace
MK
printf 'one\n' >"$source_repo/README.md"
printf 'harness_profiles:\n  local:\n    backend: codex\n' >"$source_repo/.kitsoki.local.yaml"
gitc "$source_repo" add -A
gitc "$source_repo" commit --quiet -m initial
gitc "$source_repo" switch -q -c staging/local
printf 'staged baseline\n' >"$source_repo/staged-baseline.txt"
gitc "$source_repo" add staged-baseline.txt
gitc "$source_repo" commit --quiet -m 'staged baseline'
gitc "$source_repo" switch -q main

root="$tmp/workspaces"

# Creation places its atomic lock beside targets, not inside the clone target.
# A concurrent status must surface that state instead of calling the target an
# unmanaged workspace, and a second create must not race it.
initializing_id="initializing-case"
initializing_path="$root/$initializing_id"
initializing_marker="$root/.initializing/$initializing_id"
mkdir -p "$initializing_marker"
cat >"$initializing_marker/metadata" <<EOF
pid=$$
path=$initializing_path
id=$initializing_id
branch=agent/$initializing_id
session_id=
EOF
if "$dev_workspace" status --repo "$source_repo" --root "$root" "$initializing_id" --json >"$tmp/initializing-status.json" 2>&1; then
  fail "status succeeded while workspace was initializing"
fi
[ "$(python3 -c 'import json,sys; print(json.load(sys.stdin)["state"])' <"$tmp/initializing-status.json")" = "initializing" ] || fail "status did not report initializing state"
[ "$(python3 -c 'import json,sys; print(json.load(sys.stdin)["initialization"])' <"$tmp/initializing-status.json")" = "active" ] || fail "status did not report active initialization"
if "$dev_workspace" create --repo "$source_repo" --root "$root" --id "$initializing_id" --branch "agent/$initializing_id" >/tmp/kitsoki-dev-workspace-initializing-create.log 2>&1; then
  fail "create raced an initializing workspace"
fi
grep -Fq "workspace is initializing" /tmp/kitsoki-dev-workspace-initializing-create.log || fail "create did not identify initializing workspace"
if "$dev_workspace" close --repo "$source_repo" --root "$root" --force "$initializing_id" >/tmp/kitsoki-dev-workspace-initializing-close.log 2>&1; then
  fail "close removed an initializing workspace"
fi
grep -Fq "workspace is initializing" /tmp/kitsoki-dev-workspace-initializing-close.log || fail "close did not protect initializing workspace"
[ ! -e "$initializing_path" ] || fail "initialization marker wrote into target before clone"
rm -rf "$initializing_marker"

# SIGKILL cannot run the script's EXIT cleanup. A stale marker keeps the
# incomplete clone explicit until an operator deliberately discards it.
stale_id="stale-incomplete"
stale_path="$root/$stale_id"
stale_marker="$root/.initializing/$stale_id"
mkdir -p "$stale_path/.git" "$stale_marker"
cat >"$stale_marker/metadata" <<EOF
pid=99999999
path=$stale_path
id=$stale_id
branch=agent/$stale_id
session_id=
EOF
if "$dev_workspace" status --repo "$source_repo" --root "$root" "$stale_id" >/tmp/kitsoki-dev-workspace-stale-status.log 2>&1; then
  fail "status succeeded for stale initialization"
fi
grep -Fq "state: initializing (stale)" /tmp/kitsoki-dev-workspace-stale-status.log || fail "status did not identify stale initialization"
if "$dev_workspace" recover --repo "$source_repo" --root "$root" "$stale_id" >/tmp/kitsoki-dev-workspace-recover.log 2>&1; then
  fail "recover discarded incomplete clone without explicit approval"
fi
grep -Fq -- "--discard-incomplete" /tmp/kitsoki-dev-workspace-recover.log || fail "recover did not explain explicit stale recovery"
"$dev_workspace" recover --repo "$source_repo" --root "$root" "$stale_id" --discard-incomplete >/dev/null
[ ! -e "$stale_path" ] || fail "recover --discard-incomplete left interrupted clone behind"
[ ! -e "$stale_marker" ] || fail "recover --discard-incomplete left stale marker behind"

create_json="$("$dev_workspace" create --repo "$source_repo" --root "$root" --id case-1 --branch agent/case-1 --json)"
workspace="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["path"])' <<<"$create_json")"
[ -d "$workspace/.git" ] || fail "create did not produce a git workspace"
[ -f "$workspace/.kitsoki-capsule" ] || fail "missing capsule sentinel"
[ -f "$workspace/capsule-manifest.json" ] || fail "missing capsule manifest"
[ -f "$workspace/.kitsoki-clone" ] || fail "missing clone sentinel"
[ "$(git -C "$workspace" branch --show-current)" = "agent/case-1" ] || fail "workspace branch mismatch"
[ "$(python3 -c 'import json,sys; print(json.load(sys.stdin)["base"])' <"$workspace/.kitsoki-dev-workspace.json")" = "staging/local" ] || fail "default base was not recorded as staging/local"
[ "$(cat "$workspace/staged-baseline.txt")" = "staged baseline" ] || fail "default create did not start from staging/local"
# An interruption after manifests are durable leaves a valid workspace. Recovery
# must only release its stale marker, never discard the completed clone.
completed_marker="$root/.initializing/case-1"
mkdir -p "$completed_marker"
cat >"$completed_marker/metadata" <<EOF
pid=99999999
path=$workspace
id=case-1
branch=agent/case-1
session_id=
EOF
"$dev_workspace" recover --repo "$source_repo" --root "$root" case-1 >/dev/null
[ -f "$workspace/.kitsoki-dev-workspace.json" ] || fail "recover discarded a completed managed workspace"
[ ! -e "$completed_marker" ] || fail "recover left stale marker on completed workspace"
readme_blob="$(git -C "$source_repo" rev-parse HEAD:README.md)"
source_readme_obj="$source_repo/.git/objects/${readme_blob:0:2}/${readme_blob:2}"
workspace_readme_obj="$workspace/.git/objects/${readme_blob:0:2}/${readme_blob:2}"
if [ -f "$source_readme_obj" ] && [ -f "$workspace_readme_obj" ]; then
  [ "$(stat_device_inode "$source_readme_obj")" = "$(stat_device_inode "$workspace_readme_obj")" ] || fail "create did not hardlink local git objects"
fi

"$dev_workspace" create --repo "$source_repo" --root "$root" --id local-config --branch agent/local-config --bootstrap >/dev/null
local_config_workspace="$root/local-config"
[ -f "$local_config_workspace/.kitsoki.local.yaml" ] || fail "bootstrap did not copy .kitsoki.local.yaml"
cmp "$source_repo/.kitsoki.local.yaml" "$local_config_workspace/.kitsoki.local.yaml" >/dev/null || fail "bootstrap copied the wrong .kitsoki.local.yaml content"
"$dev_workspace" close --repo "$source_repo" --root "$root" "$local_config_workspace" >/dev/null

json_bootstrap="$("$dev_workspace" create --repo "$source_repo" --root "$root" --id json-bootstrap --branch agent/json-bootstrap --bootstrap --json)"
json_bootstrap_workspace="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["path"])' <<<"$json_bootstrap")"
[ -d "$json_bootstrap_workspace" ] || fail "bootstrap --json did not preserve a machine-readable response"
"$dev_workspace" close --repo "$source_repo" --root "$root" "$json_bootstrap_workspace" >/dev/null

printf 'local scratch\n' >"$source_repo/untracked-primary.txt"
printf 'two\n' >"$workspace/feature.txt"
"$dev_workspace" commit --repo "$source_repo" --root "$root" "$workspace" --message 'add feature' >/dev/null
"$dev_workspace" merge --repo "$source_repo" --root "$root" "$workspace" --teardown >/dev/null
[ ! -e "$workspace" ] || fail "merge --teardown left workspace behind"
[ ! -e "$source_repo/feature.txt" ] || fail "default merge should not modify primary main working tree"
[ "$(git -C "$source_repo" show staging/local:feature.txt)" = "two" ] || fail "merge did not fast-forward staging/local"
[ "$(git -C "$source_repo" show staging/local:staged-baseline.txt)" = "staged baseline" ] || fail "merge dropped existing staging baseline"
[ "$(git -C "$source_repo" branch --show-current)" = "main" ] || fail "default merge moved primary checkout off main"
rm "$source_repo/untracked-primary.txt"

from_staging_json="$("$dev_workspace" create --repo "$source_repo" --root "$root" --id from-staging --branch agent/from-staging --json)"
from_staging_workspace="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["path"])' <<<"$from_staging_json")"
[ "$(cat "$from_staging_workspace/feature.txt")" = "two" ] || fail "default create did not start from staging branch"
"$dev_workspace" close --repo "$source_repo" --root "$root" "$from_staging_workspace" >/dev/null

# A Studio MCP server can itself run inside an agent capsule. That clone tracks
# staging/local as source/staging/local and intentionally has no local
# staging/local branch. Nested workspace creation and merge must preserve that
# tracked staging base rather than failing creation or falling back to main.
nested_repo="$tmp/nested-agent-capsule"
git clone -q --local --origin source "$source_repo" "$nested_repo"
gitc "$nested_repo" switch -q -c agent/nested source/staging/local
[ -z "$(git -C "$nested_repo" branch --list staging/local)" ] || fail "nested fixture unexpectedly has local staging/local"
nested_root="$nested_repo/.capsules/workspaces"
nested_json="$("$dev_workspace" create --repo "$nested_repo" --root "$nested_root" --id nested-child --branch agent/nested-child --json)"
nested_workspace="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["path"])' <<<"$nested_json")"
[ "$(cat "$nested_workspace/feature.txt")" = "two" ] || fail "nested create did not resolve source/staging/local"
printf 'nested\n' >"$nested_workspace/nested.txt"
"$dev_workspace" commit --repo "$nested_repo" --root "$nested_root" "$nested_workspace" --message 'add nested feature' >/dev/null
"$dev_workspace" merge --repo "$nested_repo" --root "$nested_root" "$nested_workspace" --teardown >/dev/null
[ "$(git -C "$nested_repo" show staging/local:nested.txt)" = "nested" ] || fail "nested merge did not land on staging/local"

second_json="$("$dev_workspace" create --repo "$source_repo" --root "$root" --id case-2 --branch agent/case-2 --json)"
second_workspace="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["path"])' <<<"$second_json")"
[ "$(cat "$second_workspace/feature.txt")" = "two" ] || fail "second default create did not include current staging content"
printf 'three\n' >"$second_workspace/feature2.txt"
"$dev_workspace" commit --repo "$source_repo" --root "$root" "$second_workspace" --message 'add second feature' >/dev/null
"$dev_workspace" merge --repo "$source_repo" --root "$root" "$second_workspace" --teardown >/dev/null
[ ! -e "$second_workspace" ] || fail "second merge --teardown left workspace behind"
[ "$(git -C "$source_repo" show staging/local:feature.txt)" = "two" ] || fail "second merge dropped existing staging/local content"
[ "$(git -C "$source_repo" show staging/local:feature2.txt)" = "three" ] || fail "second merge did not advance existing staging/local"

# The target can advance after a capsule clone is created. Merge must fetch
# and prove the target graph inside the capsule before rebase, run the gate on
# the rebased clean checkout, then land and tear the capsule down successfully.
integrity_json="$("$dev_workspace" create --repo "$source_repo" --root "$root" --id merge-object-integrity --branch agent/merge-object-integrity --json)"
integrity_workspace="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["path"])' <<<"$integrity_json")"
printf 'workspace feature\n' >"$integrity_workspace/object-integrity-feature.txt"
"$dev_workspace" commit --repo "$source_repo" --root "$root" "$integrity_workspace" --message 'add object integrity feature' >/dev/null
gitc "$source_repo" switch -q staging/local
printf 'target advanced\n' >"$source_repo/target-advanced-after-clone.txt"
gitc "$source_repo" add target-advanced-after-clone.txt
gitc "$source_repo" commit --quiet -m 'advance target after capsule creation'
advanced_target="$(git -C "$source_repo" rev-parse staging/local)"
gitc "$source_repo" switch -q main
gate_order="$tmp/merge-object-integrity-gate"
"$dev_workspace" merge --repo "$source_repo" --root "$root" "$integrity_workspace" --gate "test '\$(cat target-advanced-after-clone.txt)' = 'target advanced'; test -z '\$(git status --porcelain)'; printf rebased-and-clean > '$gate_order'" --teardown >/dev/null
[ "$(cat "$gate_order")" = "rebased-and-clean" ] || fail "merge gate did not run after target fetch and rebase"
[ ! -e "$integrity_workspace" ] || fail "object-integrity merge --teardown left workspace behind"
[ -z "$(git -C "$source_repo" branch --list capsule/merge-object-integrity-land)" ] || fail "object-integrity merge left temporary landing branch behind"
[ "$(git -C "$source_repo" merge-base "$advanced_target" staging/local)" = "$advanced_target" ] || fail "object-integrity merge dropped target advancement"
[ "$(git -C "$source_repo" show staging/local:target-advanced-after-clone.txt)" = "target advanced" ] || fail "object-integrity merge cannot read advanced target object"
[ "$(git -C "$source_repo" show staging/local:object-integrity-feature.txt)" = "workspace feature" ] || fail "object-integrity merge did not land workspace feature"
git -C "$source_repo" fsck --connectivity-only --no-dangling staging/local >/dev/null || fail "object-integrity merge left unreadable target objects"

# Teardown happens after the target update. A cleanup failure must leave a
# truthful successful merge result (with a warning), not tell callers to retry
# a landing that already advanced the target.
truthful_json="$("$dev_workspace" create --repo "$source_repo" --root "$root" --id truthful-teardown --branch agent/truthful-teardown --json)"
truthful_workspace="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["path"])' <<<"$truthful_json")"
printf 'landed despite teardown failure\n' >"$truthful_workspace/truthful-teardown.txt"
"$dev_workspace" commit --repo "$source_repo" --root "$root" "$truthful_workspace" --message 'add truthful teardown feature' >/dev/null
fakebin="$tmp/fakebin"
mkdir -p "$fakebin"
cat >"$fakebin/rm" <<EOF
#!/usr/bin/env bash
echo "forced teardown failure" >&2
exit 1
EOF
chmod +x "$fakebin/rm"
if ! PATH="$fakebin:$PATH" "$dev_workspace" merge --repo "$source_repo" --root "$root" "$truthful_workspace" --teardown >"$tmp/truthful-teardown.log" 2>&1; then
  fail "merge reported failure after target advanced and teardown failed"
fi
grep -Fq "warning: merge landed but teardown failed" "$tmp/truthful-teardown.log" || fail "merge did not warn about post-landing teardown failure"
grep -Fq "merged: agent/truthful-teardown -> staging/local" "$tmp/truthful-teardown.log" || fail "merge did not report successful target landing"
[ "$(git -C "$source_repo" show staging/local:truthful-teardown.txt)" = "landed despite teardown failure" ] || fail "truthful merge did not advance target"
[ -e "$truthful_workspace" ] || fail "forced teardown failure unexpectedly removed workspace"
"$dev_workspace" close --repo "$source_repo" --root "$root" "$truthful_workspace" >/dev/null

main_json="$("$dev_workspace" create --repo "$source_repo" --root "$root" --id case-main --branch agent/case-main --base main --target main --json)"
main_workspace="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["path"])' <<<"$main_json")"
[ "$(python3 -c 'import json,sys; print(json.load(sys.stdin)["target"])' <"$main_workspace/.kitsoki-dev-workspace.json")" = "main" ] || fail "create --target main was not recorded in workspace manifest"
printf 'main landing\n' >"$main_workspace/main-feature.txt"
"$dev_workspace" commit --repo "$source_repo" --root "$root" "$main_workspace" --message 'add main feature' >/dev/null
"$dev_workspace" merge --repo "$source_repo" --root "$root" "$main_workspace" --target main --gate true --teardown >/dev/null
[ ! -e "$main_workspace" ] || fail "merge --target main --teardown left workspace behind"
[ "$(cat "$source_repo/main-feature.txt")" = "main landing" ] || fail "explicit main merge did not update primary checkout"

unrelated_json="$("$dev_workspace" create --repo "$source_repo" --root "$root" --id unrelated-dirty --branch agent/unrelated-dirty --json)"
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

dirty_json="$("$dev_workspace" create --repo "$source_repo" --root "$root" --id dirty --branch agent/dirty --json)"
dirty_workspace="$(python3 -c 'import json,sys; print(json.load(sys.stdin)["path"])' <<<"$dirty_json")"
printf 'dirty\n' >"$dirty_workspace/untracked.txt"
if "$dev_workspace" close --repo "$source_repo" --root "$root" "$dirty_workspace" >/tmp/kitsoki-dev-workspace-close.log 2>&1; then
  fail "close succeeded on a dirty workspace"
fi
"$dev_workspace" close --repo "$source_repo" --root "$root" --force "$dirty_workspace" >/dev/null
[ ! -e "$dirty_workspace" ] || fail "close --force left workspace behind"

echo "dev-workspace tests passed"
