#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
script="$script_dir/protected-main-mode.sh"

tmp="$(mktemp -d)"
trap 'chmod -R u+w "$tmp" 2>/dev/null || true; rm -rf "$tmp"' EXIT

git_init() {
  git init -q "$1"
  git -C "$1" config user.name "Test User"
  git -C "$1" config user.email "test@example.invalid"
}

commit_file() {
  local repo="$1"
  local file="$2"
  local content="$3"
  mkdir -p "$(dirname "$repo/$file")"
  printf '%s\n' "$content" > "$repo/$file"
  git -C "$repo" add "$file"
  git -C "$repo" commit -q -m "$content"
}

assert_writable() {
  local path="$1"
  [ -w "$path" ] || { echo "expected writable: $path" >&2; exit 1; }
}

assert_readonly() {
  local path="$1"
  [ ! -w "$path" ] || { echo "expected read-only: $path" >&2; exit 1; }
}

assert_contains() {
  local file="$1"
  local needle="$2"
  if ! grep -Fq "$needle" "$file"; then
    echo "expected '$needle' in $file" >&2
    cat "$file" >&2
    exit 1
  fi
}

repo="$tmp/repo"
git_init "$repo"
mkdir -p "$repo/scripts"
cp "$script" "$repo/scripts/protected-main-mode.sh"
chmod +x "$repo/scripts/protected-main-mode.sh"

commit_file "$repo" src/app.txt source
commit_file "$repo" .context/note.md context
commit_file "$repo" .artifacts/report.txt artifact
commit_file "$repo" .kitsoki/project-profile.yaml profile
commit_file "$repo" .gitignore ".context/
.artifacts/
.temp/
.claude/
.worktrees/
.kitsoki/sessions/"
git -C "$repo" branch -M main

(
  cd "$repo"
  scripts/protected-main-mode.sh lock >"$tmp/lock.out"
)
assert_contains "$tmp/lock.out" "locked source paths"

assert_readonly "$repo"
assert_readonly "$repo/src"
assert_readonly "$repo/src/app.txt"

assert_writable "$repo/.context"
assert_writable "$repo/.context/note.md"
assert_writable "$repo/.artifacts"
assert_writable "$repo/.artifacts/report.txt"
assert_writable "$repo/.temp"
assert_writable "$repo/.claude"
assert_writable "$repo/.worktrees"
assert_writable "$repo/.kitsoki"
assert_writable "$repo/.kitsoki/project-profile.yaml"

touch "$repo/.context/new-note.md"
touch "$repo/.artifacts/new-artifact.txt"
touch "$repo/.temp/scratch.txt"
touch "$repo/.claude/local-state"
mkdir "$repo/.worktrees/new-worktree-dir"
touch "$repo/.kitsoki/local-state"

if touch "$repo/src/new-source.txt" 2>/dev/null; then
  echo "expected source directory creation to fail while locked" >&2
  exit 1
fi

(
  cd "$repo"
  scripts/protected-main-mode.sh status >"$tmp/status.out"
)
assert_contains "$tmp/status.out" "root: read-only"
assert_contains "$tmp/status.out" ".context: writable"
assert_contains "$tmp/status.out" ".artifacts: writable"
assert_contains "$tmp/status.out" ".temp: writable"
assert_contains "$tmp/status.out" ".claude: writable"
assert_contains "$tmp/status.out" ".worktrees: writable"
assert_contains "$tmp/status.out" ".kitsoki: writable"

(
  cd "$repo"
  scripts/protected-main-mode.sh unlock >"$tmp/unlock.out"
)
assert_contains "$tmp/unlock.out" "unlocked tracked source paths"

assert_writable "$repo"
assert_writable "$repo/src"
assert_writable "$repo/src/app.txt"
assert_writable "$repo/.context"
assert_writable "$repo/.artifacts"
assert_writable "$repo/.temp"
assert_writable "$repo/.claude"
assert_writable "$repo/.worktrees"
assert_writable "$repo/.kitsoki"

echo "protected-main-mode tests passed"

merge_repo="$tmp/merge-repo"
git_init "$merge_repo"
mkdir -p "$merge_repo/scripts"
cp "$script" "$merge_repo/scripts/protected-main-mode.sh"
cp "$script_dir/merge-to-main.sh" "$merge_repo/scripts/merge-to-main.sh"
chmod +x "$merge_repo/scripts/"*.sh

commit_file "$merge_repo" src/app.txt source
commit_file "$merge_repo" .context/note.md context
commit_file "$merge_repo" .gitignore ".context/
.artifacts/
.temp/
.claude/
.worktrees/"
git -C "$merge_repo" branch -M main

git -C "$merge_repo" checkout -q -b feature
printf '%s\n' updated-source > "$merge_repo/src/app.txt"
printf '%s\n' updated-context > "$merge_repo/.context/note.md"
git -C "$merge_repo" add -u src/app.txt .context/note.md
git -C "$merge_repo" commit -q -m "feature update"
git -C "$merge_repo" checkout -q main

(
  cd "$merge_repo"
  scripts/protected-main-mode.sh lock --quiet
  scripts/merge-to-main.sh feature >"$tmp/merge.out"
)
assert_contains "$tmp/merge.out" "read-only guard restored"

assert_readonly "$merge_repo"
assert_readonly "$merge_repo/src"
assert_readonly "$merge_repo/src/app.txt"
assert_writable "$merge_repo/.context"
assert_writable "$merge_repo/.context/note.md"
touch "$merge_repo/.context/post-merge-note.md"

echo "merge-to-main protected-mode integration tests passed"
