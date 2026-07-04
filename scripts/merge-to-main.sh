#!/usr/bin/env bash
# merge-to-main.sh <branch> — the sanctioned fast-forward of <branch> into main.
#
# The primary checkout keeps its tracked source read-only so an agent (or a stray
# `echo >`) can't UNINTENTIONALLY write it outside a worktree — see the
# reference-transaction hook and scripts/git-hooks/. That guard is about accidental
# writes, NOT about blocking an intentional merge: a wall lands on main by
# fast-forward, and this script is the one path that writes the primary on purpose.
#
# It lifts read-only on EXACTLY the paths the merge touches, does the ff-merge, then
# re-freezes them (files 0444, dirs 0555) — so the guard is restored precisely with
# no manual chmod dance and no risk of leaving a file writable. It refuses anything
# that is not a clean fast-forward (a diverged branch must be rebased first, in its
# worktree), so it can never rewrite or lose history on main.
#
# Run it FROM the primary checkout, on main:  scripts/merge-to-main.sh <branch>
set -euo pipefail

branch="${1:?usage: scripts/merge-to-main.sh <branch>}"

# Guard: primary checkout only (linked worktrees are already writable), on main.
if [ "$(git rev-parse --git-dir)" != "$(git rev-parse --git-common-dir)" ]; then
  echo "✗ run this in the PRIMARY checkout, not a linked worktree" >&2; exit 1
fi
if [ "$(git symbolic-ref --quiet --short HEAD 2>/dev/null || true)" != "main" ]; then
  echo "✗ the primary checkout is not on main" >&2; exit 1
fi
if ! git rev-parse --verify --quiet "$branch" >/dev/null; then
  echo "✗ no such branch: $branch" >&2; exit 1
fi
# Fast-forward only: main must be an ancestor of the branch.
if ! git merge-base --is-ancestor HEAD "$branch"; then
  echo "✗ $branch is not a fast-forward of main — rebase it onto main (in its worktree) first" >&2
  exit 1
fi

files="$(git diff --name-only HEAD "$branch")"
if [ -z "$files" ]; then
  echo "nothing to merge (main already at or ahead of $branch)"; exit 0
fi
dirs="$(printf '%s\n' "$files" | xargs -n1 dirname | sort -u)"

# Lift read-only on just the merge's dirs+files (new-dir chmod may not exist yet → ignore).
printf '%s\n' "$dirs"  | while IFS= read -r d; do [ -n "$d" ] && chmod u+w "$d" 2>/dev/null || true; done
printf '%s\n' "$files" | while IFS= read -r f; do [ -e "$f" ] && chmod u+w "$f" || true; done

git merge --ff-only "$branch"

# Re-freeze exactly those paths (files that now exist → 0444; their dirs → 0555).
printf '%s\n' "$files" | while IFS= read -r f; do [ -e "$f" ] && chmod a-w "$f" || true; done
printf '%s\n' "$dirs"  | while IFS= read -r d; do [ -n "$d" ] && chmod a-w "$d" 2>/dev/null || true; done

echo "✓ main → $(git rev-parse --short HEAD) ($(printf '%s\n' "$files" | wc -l | tr -d ' ') files; read-only restored)"
