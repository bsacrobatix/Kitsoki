#!/usr/bin/env bash
# One native routing surface for Kitsoki's receipt-bound promotion authority.
set -euo pipefail

action="${1:?usage: kitsoki-promotion-route.sh <workspace-to-staging|staging-to-main|main-to-staging> ...}"
shift

repo=""
workspace=""
sha=""
gate=""
queue_root="${KITSOKI_QUEUE_ROOT:-}"
capacity_root="${KITSOKI_GATE_CAPACITY_ROOT:-}"
capacity_pool="${KITSOKI_GATE_CAPACITY_POOL:-default}"
capacity="${KITSOKI_GATE_CAPACITY:-1}"
while [ "$#" -gt 0 ]; do
  case "$1" in
    --repo) repo="${2:?--repo requires a value}"; shift 2 ;;
    --workspace) workspace="${2:?--workspace requires a value}"; shift 2 ;;
    --sha) sha="${2:?--sha requires a value}"; shift 2 ;;
    --gate) gate="${2:?--gate requires a value}"; shift 2 ;;
    --queue-root) queue_root="${2:?--queue-root requires a value}"; shift 2 ;;
    *) echo "error: unknown promotion route argument: $1" >&2; exit 2 ;;
  esac
done

[ -n "$repo" ] || { echo "error: --repo is required" >&2; exit 2; }
repo="$(cd "$repo" && pwd -P)"
[ -n "$queue_root" ] || queue_root="$repo/.capsules/queue"
case "$capacity" in
  ''|*[!0-9]*|0) echo "error: KITSOKI_GATE_CAPACITY must be a positive integer" >&2; exit 2 ;;
esac
process_capacity_args=(--capacity-pool "$capacity_pool" --capacity "$capacity")
[ -z "$capacity_root" ] || process_capacity_args+=(--capacity-root "$capacity_root")

run_kitsoki() {
  if [ -n "${KITSOKI_PROMOTION_KITSOKI:-}" ]; then
    "$KITSOKI_PROMOTION_KITSOKI" "$@"
  elif [ -f "$repo/go.mod" ] && [ -d "$repo/cmd/kitsoki" ]; then
    (cd "$repo" && go run ./cmd/kitsoki "$@")
  elif command -v kitsoki >/dev/null 2>&1; then
    kitsoki "$@"
  else
    echo "error: kitsoki promotion runtime is unavailable" >&2
    return 127
  fi
}

json_field() {
  python3 -c 'import json,sys; print(json.load(sys.stdin).get(sys.argv[1], ""))' "$1"
}

case "$action" in
  workspace-to-staging)
    [ -n "$workspace" ] || { echo "error: --workspace is required" >&2; exit 2; }
    required_gate="$(run_kitsoki queue gate-policy --project "$repo" --target staging/local)"
    required_gate="${required_gate%$'\n'}"
    [ -z "$gate" ] || [ "$gate" = "$required_gate" ] || {
      echo "error: staging promotion requires the tracked change gate '$required_gate'; refusing '$gate'" >&2
      exit 2
    }
    gate="$required_gate"
    result="$(run_kitsoki capsule promote \
      --project "$repo" --queue-root "$queue_root" --workspace "$workspace" \
      --target staging/local --pipeline change --gate "$gate" \
      --wait --json "${process_capacity_args[@]}")"
    status="$(printf '%s' "$result" | json_field status)"
    [ "$status" = promoted ] || {
      printf '%s\n' "$result"
      echo "error: staging candidate did not reach a terminal landing (status=$status)" >&2
      exit 1
    }
    printf '%s\n' "$result"
    ;;
  staging-to-main)
    [ -n "$sha" ] || { echo "error: --sha is required" >&2; exit 2; }
    required_gate="$(run_kitsoki queue gate-policy --project "$repo" --target main)"
    required_gate="${required_gate%$'\n'}"
    [ -z "$gate" ] || [ "$gate" = "$required_gate" ] || {
      echo "error: ordinary main promotion requires the tracked full gate '$required_gate'; custom gates require a separately governed emergency path" >&2
      exit 2
    }
    gate="$required_gate"
    admission="$(run_kitsoki capsule promote-existing \
      --project "$repo" --queue-root "$queue_root" \
      --source-target staging/local --sha "$sha" --target main \
      --pipeline change --gate "$gate" --json)"
    candidate_id="$(printf '%s' "$admission" |
      python3 -c 'import json,sys; print(json.load(sys.stdin).get("candidate", {}).get("id", ""))')"
    [ -n "$candidate_id" ] || {
      printf '%s\n' "$admission"
      echo "error: promotion authority did not return a destination candidate" >&2
      exit 1
    }
    state="$(run_kitsoki queue process --project "$repo" --queue-root "$queue_root" \
      --candidate "$candidate_id" --target main --gate "$gate" "${process_capacity_args[@]}")"
    printf '%s' "$state" | python3 -c '
import json,sys
candidate_id=sys.argv[1]
state=json.load(sys.stdin)
matches=[c for c in state.get("candidates", []) if c.get("id")==candidate_id]
if len(matches)!=1 or (matches[0].get("phase") or matches[0].get("status"))!="landed":
    raise SystemExit("promotion candidate did not land exactly once")
' "$candidate_id"
    printf '%s\n' "$admission"
    ;;
  main-to-staging)
    [ -n "$sha" ] || { echo "error: --sha is required" >&2; exit 2; }
    required_gate="$(run_kitsoki queue gate-policy --project "$repo" --target staging/local)"
    required_gate="${required_gate%$'\n'}"
    [ -z "$gate" ] || [ "$gate" = "$required_gate" ] || {
      echo "error: staging refresh requires the tracked change gate '$required_gate'; refusing '$gate'" >&2
      exit 2
    }
    gate="$required_gate"
    admission="$(run_kitsoki capsule promote-existing \
      --project "$repo" --queue-root "$queue_root" \
      --source-target main --sha "$sha" --target staging/local \
      --pipeline change --gate "$gate" --json)"
    candidate_id="$(printf '%s' "$admission" |
      python3 -c 'import json,sys; print(json.load(sys.stdin).get("candidate", {}).get("id", ""))')"
    [ -n "$candidate_id" ] || {
      printf '%s\n' "$admission"
      echo "error: refresh promotion authority did not return a staging candidate" >&2
      exit 1
    }
    state="$(run_kitsoki queue process --project "$repo" --queue-root "$queue_root" \
      --candidate "$candidate_id" --target staging/local --gate "$gate" "${process_capacity_args[@]}")"
    printf '%s' "$state" | python3 -c '
import json,sys
candidate_id=sys.argv[1]
state=json.load(sys.stdin)
matches=[c for c in state.get("candidates", []) if c.get("id")==candidate_id]
if len(matches)!=1 or (matches[0].get("phase") or matches[0].get("status"))!="landed":
    raise SystemExit("refresh promotion candidate did not land exactly once")
' "$candidate_id"
    printf '%s\n' "$admission"
    ;;
  *)
    echo "error: unknown promotion route action: $action" >&2
    exit 2
    ;;
esac
