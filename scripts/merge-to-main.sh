#!/usr/bin/env bash
# merge-to-main.sh <branch> - fast-forward <branch> into protected local main.
#
# The primary checkout keeps tracked source read-only so agents do implementation
# work in managed clone-backed capsule workspaces and only write the primary
# checkout during intentional landings. This helper is that landing path: it
# lifts write permissions on the exact files and directories changed by the
# fast-forward, runs the merge, then restores the read-only guard.
#
# It refuses non-fast-forward merges. Rebase or rebuild the branch in its
# managed workspace first when main has advanced.
#
# Run from the primary checkout, while on main:
#   scripts/merge-to-main.sh <branch>
#
# Optional /goal (goal-seeker) ledger integration: when a change being landed is
# part of a tracked goal, pass --goal-dir and --change-id (both required
# together) to record the landing in the goal's append-only log immediately,
# instead of reconstructing ledger history from `git log` after the fact:
#
#   scripts/merge-to-main.sh <branch> --goal-dir <dir> --change-id <id> \
#     [--goal-work <dir>] [--goal-script <path>]
#
#   --goal-dir <dir>     the goal's docs/goals/<slug> directory (decomposition.yaml)
#   --change-id <id>     the decomposition change id this branch/merge completes
#   --goal-work <dir>    the goal's --work dir (log.jsonl/ledger.json). Defaults
#                        to .artifacts/goal/<basename(goal-dir)>, mirroring
#                        goal.py's own `_work_for` default derivation.
#   --goal-script <path> explicit path to goal.py. If omitted, this script looks
#                        for it at .agents/skills/goal/goal.py, then
#                        .claude/skills/goal/goal.py (its dev symlink) — the
#                        canonical committed location once the goal-seeker
#                        story (WM.0) lands it there. Until then, pass
#                        --goal-script explicitly (e.g. a branch worktree's copy).
#
# These flags are pure additions: omit them and this script behaves exactly as
# it always has. If both are given, after a successful merge this script
# appends one combined `kind: gate` log entry (state=integrated,
# gate.status=green, actor=integrator, branch=main, sha=<new HEAD sha>) via
# `goal.py append`. The ledger update is observability, not a correctness gate
# for the merge: if it fails for any reason (bad path, malformed json, missing
# goal.py, ...) this script prints a warning to stderr and still exits 0, since
# the merge itself already succeeded and is the important, hard-to-reverse part.
set -euo pipefail

branch=""
goal_dir=""
change_id=""
goal_work=""
goal_script=""

while [ $# -gt 0 ]; do
  case "$1" in
    --goal-dir)
      goal_dir="${2:?--goal-dir requires a value}"
      shift 2
      ;;
    --change-id)
      change_id="${2:?--change-id requires a value}"
      shift 2
      ;;
    --goal-work)
      goal_work="${2:?--goal-work requires a value}"
      shift 2
      ;;
    --goal-script)
      goal_script="${2:?--goal-script requires a value}"
      shift 2
      ;;
    -*)
      echo "error: unknown flag: $1" >&2
      exit 1
      ;;
    *)
      if [ -n "$branch" ]; then
        echo "error: unexpected extra argument: $1" >&2
        exit 1
      fi
      branch="$1"
      shift
      ;;
  esac
done

branch="${branch:?usage: scripts/merge-to-main.sh <branch> [--goal-dir <dir> --change-id <id> [--goal-work <dir>] [--goal-script <path>]]}"

if [ -n "$goal_dir" ] && [ -z "$change_id" ]; then
  echo "error: --goal-dir requires --change-id" >&2
  exit 1
fi
if [ -z "$goal_dir" ] && [ -n "$change_id" ]; then
  echo "error: --change-id requires --goal-dir" >&2
  exit 1
fi

if [ "$(git rev-parse --git-dir)" != "$(git rev-parse --git-common-dir)" ]; then
  echo "error: run this in the primary checkout, not a linked worktree or secondary checkout" >&2
  exit 1
fi
if [ "$(git symbolic-ref --quiet --short HEAD 2>/dev/null || true)" != "main" ]; then
  echo "error: the primary checkout is not on main" >&2
  exit 1
fi
if ! git rev-parse --verify --quiet "$branch" >/dev/null; then
  echo "error: no such branch: $branch" >&2
  exit 1
fi
if ! git merge-base --is-ancestor HEAD "$branch"; then
  echo "error: $branch is not a fast-forward of main; rebase it onto main in its managed workspace first" >&2
  exit 1
fi
if [ -n "$(git status --porcelain)" ]; then
  echo "error: primary checkout has uncommitted changes; refusing to merge" >&2
  git status --short >&2
  exit 1
fi

files="$(git diff --name-only HEAD "$branch")"
if [ -z "$files" ]; then
  echo "nothing to merge; main already contains $branch"
  exit 0
fi
dirs="$(printf '%s\n' "$files" | xargs -n1 dirname | sort -u)"
guard_paths="$(
  printf '%s\n' "$files" | while IFS= read -r f; do
    [ -e "$f" ] && printf '%s\n' "$f"
    d="$(dirname "$f")"
    [ "$d" = "." ] && printf '%s\n' "."
    while [ "$d" != "." ] && [ "$d" != "/" ]; do
      [ -e "$d" ] && printf '%s\n' "$d"
      parent="$(dirname "$d")"
      [ "$parent" = "." ] && printf '%s\n' "."
      [ "$parent" = "$d" ] && break
      d="$parent"
    done
  done | sort -u
)"

restore_guard_fallback() {
  printf '%s\n' "$files" | while IFS= read -r f; do
    [ -e "$f" ] && chmod a-w "$f" || true
  done
  printf '%s\n' "$dirs" | while IFS= read -r d; do
    [ -n "$d" ] && [ -e "$d" ] && chmod a-w "$d" 2>/dev/null || true
  done
  printf '%s\n' "$guard_paths" | while IFS= read -r p; do
    [ -n "$p" ] && [ -e "$p" ] && chmod a-w "$p" 2>/dev/null || true
  done
}

restore_guard() {
  restore_guard_fallback
  if [ -x scripts/protected-main-mode.sh ]; then
    if ! scripts/protected-main-mode.sh repair-writable-roots --quiet; then
      echo "warning: protected-main-mode.sh repair-writable-roots failed; scratch roots may need manual chmod repair" >&2
    fi
  fi
}

trap restore_guard EXIT

printf '%s\n' "$guard_paths" | while IFS= read -r p; do
  [ -n "$p" ] && chmod u+w "$p" 2>/dev/null || true
done
printf '%s\n' "$files" | while IFS= read -r f; do
  [ -e "$f" ] && chmod u+w "$f" || true
done

merge_out=""
set +e
merge_out="$(git merge --ff-only "$branch" 2>&1)"
merge_status=$?
set -e
if [ -n "$merge_out" ]; then
  printf '%s\n' "$merge_out"
fi
if [ "$merge_status" -ne 0 ]; then
  current_head="$(git rev-parse --verify HEAD 2>/dev/null || true)"
  if [ -n "$current_head" ]; then
    git reset --hard "$current_head" >/dev/null 2>&1 || true
    echo "error: merge failed; reset primary checkout index/worktree to HEAD $current_head" >&2
  else
    git reset --hard >/dev/null 2>&1 || true
    echo "error: merge failed; attempted to reset primary checkout index/worktree" >&2
  fi
  exit "$merge_status"
fi

restore_guard
trap - EXIT

if [ -n "$goal_dir" ] && [ -n "$change_id" ]; then
  resolved_goal_script="$goal_script"
  if [ -z "$resolved_goal_script" ]; then
    for candidate in ".agents/skills/goal/goal.py" ".claude/skills/goal/goal.py"; do
      if [ -f "$candidate" ]; then
        resolved_goal_script="$candidate"
        break
      fi
    done
  fi

  if [ -z "$resolved_goal_script" ]; then
    echo "warning: --goal-dir/--change-id given but no goal.py found (looked for .agents/skills/goal/goal.py, .claude/skills/goal/goal.py; pass --goal-script explicitly) — skipping ledger update" >&2
  elif [ ! -f "$resolved_goal_script" ]; then
    echo "warning: --goal-script $resolved_goal_script does not exist — skipping ledger update" >&2
  else
    resolved_goal_work="$goal_work"
    if [ -z "$resolved_goal_work" ]; then
      resolved_goal_work=".artifacts/goal/$(basename "$goal_dir")"
    fi
    new_sha="$(git rev-parse --short HEAD)"
    if entry_json="$(python3 - "$change_id" "$new_sha" <<'PY'
import json
import sys

change_id, sha = sys.argv[1], sys.argv[2]
print(json.dumps({
    "kind": "gate",
    "change_id": change_id,
    "actor": "integrator",
    "state": "integrated",
    "gate": {"status": "green"},
    "branch": "main",
    "sha": sha,
}))
PY
    )"; then
      if ! append_out="$(python3 "$resolved_goal_script" append --work "$resolved_goal_work" --entry "$entry_json" 2>&1)"; then
        echo "warning: goal ledger update failed (goal.py append exited non-zero): $append_out" >&2
      else
        printf '%s\n' "$append_out"
      fi
    else
      echo "warning: failed to build goal ledger entry JSON for change $change_id — skipping ledger update" >&2
    fi
  fi
fi

echo "main -> $(git rev-parse --short HEAD); read-only guard restored for changed paths"
