#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
usage: scripts/generate-implementation-handoff.sh --changeset ID --target-repo PATH --acceptance TEXT --out PATH [--context PATH] [--notes TEXT]
USAGE
}

changeset=""
target_repo=""
acceptance=()
context_paths=()
notes=""
out=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --changeset) changeset="${2:-}"; shift 2 ;;
    --target-repo) target_repo="${2:-}"; shift 2 ;;
    --acceptance) acceptance+=("${2:-}"); shift 2 ;;
    --context) context_paths+=("${2:-}"); shift 2 ;;
    --notes) notes="${2:-}"; shift 2 ;;
    --out) out="${2:-}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage; exit 2 ;;
  esac
done

if [[ -z "$changeset" || -z "$target_repo" || ${#acceptance[@]} -eq 0 || -z "$out" ]]; then
  usage
  exit 2
fi

mkdir -p "$(dirname "$out")"
{
  printf '%s\n\n' '---'
  printf 'schema: implementation-handoff/v0\n'
  printf 'changeset: %s\n' "$changeset"
  printf 'target_repo: %s\n' "$target_repo"
  printf 'acceptance:\n'
  for item in "${acceptance[@]}"; do
    printf '  - %s\n' "$item"
  done
  if [[ ${#context_paths[@]} -gt 0 ]]; then
    printf 'context:\n'
    for item in "${context_paths[@]}"; do
      printf '  - %s\n' "$item"
    done
  fi
  printf 'sanctioned_command: codex superagent --cd %q --task-file .context/implementation-handoff.md\n' "$target_repo"
  if [[ -n "$notes" ]]; then
    printf 'notes: %s\n' "$notes"
  fi
  printf '%s\n\n' '---'
  printf '# Implementation Handoff\n\n'
  printf 'This record is non-executing. It does not launch an agent or mutate `%s`.\n' "$target_repo"
} >"$out"

printf 'wrote %s\n' "$out"
