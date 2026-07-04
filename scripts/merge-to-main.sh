#!/usr/bin/env bash
# merge-to-main.sh <branch> - fast-forward <branch> into protected local main.
#
# The primary checkout keeps tracked source read-only so agents do implementation
# work in .worktrees/* and only write the primary checkout during intentional
# landings. This helper is that landing path: it lifts write permissions on the
# exact files and directories changed by the fast-forward, runs the merge, then
# restores the read-only guard.
#
# It refuses non-fast-forward merges. Rebase or rebuild the branch in its
# worktree first when main has advanced.
#
# Run from the primary checkout, while on main:
#   scripts/merge-to-main.sh <branch>
set -euo pipefail

branch="${1:?usage: scripts/merge-to-main.sh <branch>}"

if [ "$(git rev-parse --git-dir)" != "$(git rev-parse --git-common-dir)" ]; then
  echo "error: run this in the primary checkout, not a linked worktree" >&2
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
  echo "error: $branch is not a fast-forward of main; rebase it onto main in its worktree first" >&2
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
      [ "$parent" = "$d" ] && break
      d="$parent"
    done
  done | sort -u
)"

restore_guard() {
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

trap restore_guard EXIT

printf '%s\n' "$guard_paths" | while IFS= read -r p; do
  [ -n "$p" ] && chmod u+w "$p" 2>/dev/null || true
done
printf '%s\n' "$files" | while IFS= read -r f; do
  [ -e "$f" ] && chmod u+w "$f" || true
done

git merge --ff-only "$branch"

restore_guard
trap - EXIT

echo "main -> $(git rev-parse --short HEAD); read-only guard restored for changed paths"
