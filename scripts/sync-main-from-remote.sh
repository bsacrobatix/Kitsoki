#!/usr/bin/env bash
# sync-main-from-remote.sh - reconcile protected local main with a remote main.
#
# This does not pull into the primary checkout. It creates a writable integration
# worktree under .worktrees, merges the selected remote branch there, and prints
# the protected fast-forward command to run after validation. If the merge
# conflicts, the conflicted files stay isolated in that integration worktree.
set -euo pipefail

usage() {
  cat <<'EOF'
usage:
  scripts/sync-main-from-remote.sh [--remote origin] [--remote-branch main] [--base main] [--name NAME] [--no-fetch]
  scripts/sync-main-from-remote.sh --continue <branch>

Prepare mode:
  Fetches the remote, creates .worktrees/<name> from local main, and merges
  <remote>/<remote-branch> there. If the merge is clean, validate in that
  worktree and land with:

    scripts/merge-to-main.sh <branch>

  If conflicts occur, resolve and commit them in the integration worktree, then
  run:

    scripts/sync-main-from-remote.sh --continue <branch>

Continue mode:
  Verifies the integration branch has no unresolved conflicts, commits an
  in-progress merge if needed, and prints the validation and landing commands.
EOF
}

die() {
  echo "error: $*" >&2
  exit 1
}

remote=origin
remote_branch=main
base=main
name=
do_fetch=1
continue_branch=

while [ "$#" -gt 0 ]; do
  case "$1" in
    --remote)
      remote="${2:?--remote requires a value}"
      shift 2
      ;;
    --remote-branch)
      remote_branch="${2:?--remote-branch requires a value}"
      shift 2
      ;;
    --base)
      base="${2:?--base requires a value}"
      shift 2
      ;;
    --name)
      name="${2:?--name requires a value}"
      shift 2
      ;;
    --no-fetch)
      do_fetch=0
      shift
      ;;
    --continue)
      continue_branch="${2:?--continue requires a branch name}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

if [ "$(git rev-parse --git-dir)" != "$(git rev-parse --git-common-dir)" ]; then
  die "run this from the primary checkout, not a linked worktree"
fi
current_branch="$(git symbolic-ref --quiet --short HEAD 2>/dev/null || true)"
if [ "$current_branch" != "$base" ]; then
  die "primary checkout is on '$current_branch', expected '$base'"
fi

find_worktree_for_branch() {
  target_ref="refs/heads/$1"
  git worktree list --porcelain | awk -v target="$target_ref" '
    /^worktree / { path=substr($0, 10) }
    /^branch / && substr($0, 8) == target { print path; found=1 }
    END { if (!found) exit 1 }
  '
}

print_next_steps() {
  branch="$1"
  wt="$2"
  cat <<EOF
Integration branch: $branch
Integration worktree: $wt

Validate in the integration worktree, for example:
  (cd "$wt" && go test ./...)

Then land into protected local $base from the primary checkout:
  scripts/merge-to-main.sh $branch
EOF
}

if [ -n "$continue_branch" ]; then
  git rev-parse --verify --quiet "$continue_branch" >/dev/null ||
    die "no such branch: $continue_branch"
  wt="$(find_worktree_for_branch "$continue_branch")" ||
    die "no worktree found for branch: $continue_branch"

  if [ -n "$(git -C "$wt" ls-files -u)" ]; then
    die "unresolved conflicts remain in $wt"
  fi
  git_dir="$(git -C "$wt" rev-parse --git-dir)"
  if [ -e "$git_dir/MERGE_HEAD" ]; then
    git -C "$wt" commit --no-edit
  fi
  if ! git merge-base --is-ancestor "$base" "$continue_branch"; then
    die "$continue_branch is not based on $base"
  fi
  print_next_steps "$continue_branch" "$wt"
  exit 0
fi

remote_ref="refs/remotes/$remote/$remote_branch"
if [ "$do_fetch" -eq 1 ]; then
  git fetch "$remote" "$remote_branch:$remote_ref"
fi
git rev-parse --verify --quiet "$remote_ref" >/dev/null ||
  die "remote ref not found: $remote_ref"

if git merge-base --is-ancestor "$remote_ref" "$base"; then
  echo "$base already contains $remote/$remote_branch"
  exit 0
fi

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
safe_remote_branch="${remote_branch//\//-}"
if [ -z "$name" ]; then
  name="sync-main-${remote}-${safe_remote_branch}-${stamp}"
fi
branch="sync/main-${remote}-${safe_remote_branch}-${stamp}"
worktree=".worktrees/$name"

git rev-parse --verify --quiet "$branch" >/dev/null &&
  die "branch already exists: $branch"
[ -e "$worktree" ] && die "worktree path already exists: $worktree"

if git merge-base --is-ancestor "$base" "$remote_ref"; then
  git branch "$branch" "$remote_ref"
  git worktree add "$worktree" "$branch"
  echo "$remote/$remote_branch is a fast-forward of $base."
  print_next_steps "$branch" "$repo_root/$worktree"
  exit 0
fi

git worktree add "$worktree" -b "$branch" "$base"
set +e
git -C "$worktree" merge --no-ff --no-edit "$remote_ref"
merge_status=$?
set -e

if [ "$merge_status" -eq 0 ]; then
  print_next_steps "$branch" "$repo_root/$worktree"
  exit 0
fi

cat <<EOF
Merge conflicts are isolated in:
  $repo_root/$worktree

Resolve conflicts there, then run from the primary checkout:
  scripts/sync-main-from-remote.sh --continue $branch
EOF
exit "$merge_status"
