#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
tmp="$(mktemp -d "${TMPDIR:-/tmp}/kitsoki-promotion-route.XXXXXX")"
trap 'rm -rf "$tmp"' EXIT
repo="$tmp/repo"
mkdir -p "$repo"
repo="$(cd "$repo" && pwd -P)"
log="$tmp/calls"
queue_root="$tmp/external-queue"
mkdir -p "$queue_root"

cat >"$tmp/kitsoki" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${KITSOKI_ROUTE_TEST_LOG:?}"
case "$*" in
  "queue gate-policy "*)
    case "$*" in
      *"--target main"*) printf 'make test-full\n' ;;
      *) printf 'make capsule-ci-quick\n' ;;
    esac
    ;;
  "capsule promote "*)
    printf '{"status":"promoted","queue_candidate":{"id":"queue-staging"}}\n'
    ;;
  "capsule promote-existing "*)
    printf '{"status":"queued","candidate":{"id":"queue-main"}}\n'
    ;;
  "queue process "*)
    printf '{"candidates":[{"id":"queue-main","phase":"landed","status":"landed"}]}\n'
    ;;
  *)
    echo "unexpected fake kitsoki call: $*" >&2
    exit 70
    ;;
esac
SH
chmod +x "$tmp/kitsoki"

KITSOKI_PROMOTION_KITSOKI="$tmp/kitsoki" KITSOKI_ROUTE_TEST_LOG="$log" KITSOKI_QUEUE_ROOT="$queue_root" \
  "$root/scripts/kitsoki-promotion-route.sh" workspace-to-staging \
  --repo "$repo" --workspace agent-1 --gate "make capsule-ci-quick" >/dev/null
[ "$(wc -l <"$log" | tr -d ' ')" = 2 ] || {
  echo "workspace route did not use exactly one policy resolution and one promotion" >&2
  exit 1
}
grep -F -- "capsule promote --project $repo --queue-root $queue_root --workspace agent-1 --target staging/local --pipeline change --gate make capsule-ci-quick --wait --json" "$log" >/dev/null

: >"$log"
KITSOKI_PROMOTION_KITSOKI="$tmp/kitsoki" KITSOKI_ROUTE_TEST_LOG="$log" KITSOKI_QUEUE_ROOT="$queue_root" \
  "$root/scripts/kitsoki-promotion-route.sh" staging-to-main \
  --repo "$repo" --sha 0123456789012345678901234567890123456789 \
  --gate "make test-full" >/dev/null
[ "$(wc -l <"$log" | tr -d ' ')" = 3 ] || {
  echo "main route did not use exactly one policy resolution, admission, and drain" >&2
  exit 1
}
grep -F -- "--queue-root $queue_root" "$log" >/dev/null
grep -F -- "--pipeline change --gate make test-full" "$log" >/dev/null
grep -F -- "queue process --project $repo --queue-root $queue_root --candidate queue-main --target main --gate make test-full --capacity-pool default --capacity 1" "$log" >/dev/null
[ ! -e "$repo/.capsules/queue" ] || {
  echo "external authority route forked project-local queue state" >&2
  exit 1
}
if KITSOKI_PROMOTION_KITSOKI="$tmp/kitsoki" KITSOKI_ROUTE_TEST_LOG="$log" KITSOKI_QUEUE_ROOT="$queue_root" \
  "$root/scripts/kitsoki-promotion-route.sh" staging-to-main \
  --repo "$repo" --sha 0123456789012345678901234567890123456789 \
  --gate true >"$tmp/weaken-main.out" 2>&1; then
  echo "ordinary main route accepted a weakened custom gate" >&2
  exit 1
fi
grep -F "requires the tracked full gate 'make test-full'" "$tmp/weaken-main.out" >/dev/null

: >"$log"
KITSOKI_PROMOTION_KITSOKI="$tmp/kitsoki" KITSOKI_ROUTE_TEST_LOG="$log" KITSOKI_QUEUE_ROOT="$queue_root" \
  KITSOKI_GATE_CAPACITY_ROOT="$tmp/capacity-root" KITSOKI_GATE_CAPACITY_POOL="local-fast" KITSOKI_GATE_CAPACITY=3 \
  "$root/scripts/kitsoki-promotion-route.sh" main-to-staging \
  --repo "$repo" --sha 0123456789012345678901234567890123456789 \
  --gate "make capsule-ci-quick" >/dev/null
[ "$(wc -l <"$log" | tr -d ' ')" = 3 ] || {
  echo "refresh route did not use exactly one policy resolution, admission, and candidate-scoped drain" >&2
  exit 1
}
grep -F -- "--source-target main --sha 0123456789012345678901234567890123456789 --target staging/local --pipeline change" "$log" >/dev/null
grep -F -- "--candidate queue-main --target staging/local --gate make capsule-ci-quick --capacity-pool local-fast --capacity 3 --capacity-root $tmp/capacity-root" "$log" >/dev/null

# A Kitsoki source checkout must bootstrap through its own combined source,
# even when PATH still contains an older installed binary.
self_repo="$tmp/self-source"
mkdir -p "$self_repo/cmd/kitsoki" "$tmp/self-bin"
self_repo="$(cd "$self_repo" && pwd -P)"
printf 'module self-test\n' >"$self_repo/go.mod"
self_log="$tmp/self-source-calls"
cat >"$tmp/self-bin/go" <<'SH'
#!/usr/bin/env bash
printf 'go %s\n' "$*" >>"${KITSOKI_SELF_LOG:?}"
case "$*" in
  "run ./cmd/kitsoki queue gate-policy "*) printf 'make capsule-ci-quick\n' ;;
  "run ./cmd/kitsoki capsule promote "*) printf '{"status":"promoted","queue_candidate":{"id":"queue-self"}}\n' ;;
  *) echo "unexpected source go call: $*" >&2; exit 70 ;;
esac
SH
cat >"$tmp/self-bin/kitsoki" <<'SH'
#!/usr/bin/env bash
printf 'stale %s\n' "$*" >>"${KITSOKI_SELF_LOG:?}"
exit 71
SH
chmod +x "$tmp/self-bin/go" "$tmp/self-bin/kitsoki"
PATH="$tmp/self-bin:$PATH" KITSOKI_SELF_LOG="$self_log" \
  "$root/scripts/kitsoki-promotion-route.sh" workspace-to-staging \
  --repo "$self_repo" --workspace self --gate "make capsule-ci-quick" >/dev/null
[ "$(wc -l <"$self_log" | tr -d ' ')" = 2 ] && ! grep -F "stale " "$self_log" >/dev/null || {
  echo "Kitsoki self-host route selected stale PATH binary instead of repo source" >&2
  exit 1
}

# Execute the actual compatibility helpers, not just the central router, and
# make their legacy gates fatal. A passing test therefore proves each helper
# returned through the native authority instead of also running its old
# rebase/gate/ref-mutation path.
surface="$tmp/surface"
mkdir -p "$surface/scripts"
surface="$(cd "$surface" && pwd -P)"
git -C "$surface" init --quiet --initial-branch=main
cp "$root/scripts/dev-workspace.sh" "$surface/scripts/dev-workspace.sh"
cp "$root/scripts/merge-to-main.sh" "$surface/scripts/merge-to-main.sh"
cp "$root/scripts/refresh-staging-local.sh" "$surface/scripts/refresh-staging-local.sh"
surface_log="$tmp/surface-calls"
cat >"$surface/scripts/kitsoki-promotion-route.sh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
action="${1:?action required}"
shift
printf '%s %s\n' "$action" "$*" >>"${KITSOKI_SURFACE_LOG:?}"
repo=""
workspace=""
sha=""
gate=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --repo) repo="$2"; shift 2 ;;
    --workspace) workspace="$2"; shift 2 ;;
    --sha) sha="$2"; shift 2 ;;
    --gate) gate="$2"; shift 2 ;;
    *) shift ;;
  esac
done
case "$action" in
  workspace-to-staging)
    workspace_path="$repo/.capsules/workspaces/$workspace"
    tip="$("${KITSOKI_SURFACE_GIT:?}" -C "$workspace_path" rev-parse HEAD)"
    "${KITSOKI_SURFACE_GIT:?}" -C "$repo" fetch --quiet --no-tags "$workspace_path" "$tip"
    old="$("${KITSOKI_SURFACE_GIT:?}" -C "$repo" rev-parse refs/heads/staging/local)"
    "${KITSOKI_SURFACE_GIT:?}" -C "$repo" update-ref refs/heads/staging/local "$tip" "$old"
    ;;
  staging-to-main)
    [ -z "$gate" ] || [ "$gate" = "make test-full" ] || {
      echo "ordinary main promotion requires the tracked full gate 'make test-full'" >&2
      exit 2
    }
    "${KITSOKI_SURFACE_GIT:?}" -C "$repo" reset --hard "$sha" >/dev/null
    ;;
  main-to-staging)
    old="$("${KITSOKI_SURFACE_GIT:?}" -C "$repo" rev-parse refs/heads/staging/local)"
    "${KITSOKI_SURFACE_GIT:?}" -C "$repo" update-ref refs/heads/staging/local "$sha" "$old"
    ;;
  *) exit 70 ;;
esac
SH
chmod +x "$surface/scripts/"*.sh
printf '.capsules/\n' >"$surface/.gitignore"
printf 'base\n' >"$surface/base.txt"
git -C "$surface" add -A
git -C "$surface" -c user.name=Test -c user.email=test@example.com commit --quiet -m base
git -C "$surface" branch staging/local

export GIT_AUTHOR_NAME=Test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=Test GIT_COMMITTER_EMAIL=test@example.com
surface_json="$("$surface/scripts/dev-workspace.sh" create \
  --repo "$surface" --id routed --branch agent/routed --json)"
surface_workspace="$(printf '%s' "$surface_json" |
  python3 -c 'import json,sys; print(json.load(sys.stdin)["path"])')"
printf 'routed\n' >"$surface_workspace/routed.txt"
git -C "$surface_workspace" add routed.txt
git -C "$surface_workspace" commit --quiet -m routed
surface_workspace_tip="$(git -C "$surface_workspace" rev-parse HEAD)"
: >"$surface_log"
KITSOKI_SURFACE_LOG="$surface_log" KITSOKI_SURFACE_GIT="$(command -v git)" \
  "$surface/scripts/dev-workspace.sh" merge --repo "$surface" "$surface_workspace" \
  --gate 'exit 91' >/dev/null
[ "$(git -C "$surface" rev-parse staging/local)" = "$surface_workspace_tip" ] || {
  echo "dev-workspace native route did not land the exact workspace tip" >&2
  exit 1
}
[ "$(wc -l <"$surface_log" | tr -d ' ')" = 1 ] &&
  grep -F "workspace-to-staging --repo $surface --workspace routed --gate exit 91" "$surface_log" >/dev/null || {
  echo "dev-workspace did not use exactly one native staging route" >&2
  exit 1
}

main_before="$(git -C "$surface" rev-parse main)"
staging_before="$(git -C "$surface" rev-parse staging/local)"
git -C "$surface" branch feature main
git -C "$surface" checkout --quiet main
: >"$surface_log"
if (cd "$surface" && KITSOKI_SURFACE_LOG="$surface_log" KITSOKI_SURFACE_GIT="$(command -v git)" \
  scripts/merge-to-main.sh feature) >"$tmp/explicit-main.out" 2>&1; then
  echo "installed merge helper accepted direct branch-to-main mutation" >&2
  exit 1
fi
grep -F "refuses direct branch-to-main mutation" "$tmp/explicit-main.out" >/dev/null || {
  cat "$tmp/explicit-main.out" >&2
  echo "explicit branch rejection did not name the native authority" >&2
  exit 1
}
[ "$(git -C "$surface" rev-parse main)" = "$main_before" ] &&
  [ "$(git -C "$surface" rev-parse staging/local)" = "$staging_before" ] &&
  [ ! -s "$surface_log" ] || {
  echo "rejected explicit branch promotion mutated protected refs or entered route" >&2
  exit 1
}

mkdir -p "$surface/.capsules/staging"
git clone --quiet --branch staging/local "$surface" "$surface/.capsules/staging/local"
touch "$surface/.capsules/staging/local/.kitsoki-capsule"
mkdir -p "$surface/.capsules/locks/staging-local-promotion"
printf 'pid=stale\n' >"$surface/.capsules/locks/staging-local-promotion/owner"
: >"$surface_log"
(cd "$surface" && KITSOKI_SURFACE_LOG="$surface_log" KITSOKI_SURFACE_GIT="$(command -v git)" \
  scripts/merge-to-main.sh) >/dev/null
[ "$(git -C "$surface" rev-parse main)" = "$staging_before" ] || {
  echo "staging-to-main route did not advance main to exact staged SHA" >&2
  exit 1
}
[ "$(git -C "$surface" rev-parse staging/local)" = "$staging_before" ] || {
  echo "staging-to-main route rewrote staging" >&2
  exit 1
}
[ -f "$surface/.capsules/locks/staging-local-promotion/owner" ] || {
  echo "native merge route consumed or deleted the legacy stale lease directory" >&2
  exit 1
}
[ "$(wc -l <"$surface_log" | tr -d ' ')" = 1 ] &&
  grep -F "staging-to-main --repo $surface --sha $staging_before" "$surface_log" >/dev/null || {
  echo "merge-to-main did not use exactly one candidate-scoped native route" >&2
  exit 1
}

main_before="$(git -C "$surface" rev-parse main)"
staging_before="$(git -C "$surface" rev-parse staging/local)"
: >"$surface_log"
if (cd "$surface" && KITSOKI_SURFACE_LOG="$surface_log" KITSOKI_SURFACE_GIT="$(command -v git)" \
  scripts/merge-to-main.sh --gate true) >"$tmp/custom-main-gate.out" 2>&1; then
  echo "merge helper accepted an arbitrary main gate" >&2
  exit 1
fi
grep -F "requires the tracked full gate 'make test-full'" "$tmp/custom-main-gate.out" >/dev/null
[ "$(git -C "$surface" rev-parse main)" = "$main_before" ] &&
  [ "$(git -C "$surface" rev-parse staging/local)" = "$staging_before" ] &&
  [ ! -s "$surface_log" ] || {
  echo "rejected custom main gate mutated refs or entered the native route" >&2
  exit 1
}

# Installed --resume must preserve the exact resolved tip but never fall
# through to the legacy staging/main compare-and-swap path.
printf 'resolved\n' >"$surface/.capsules/staging/local/resolved.txt"
git -C "$surface/.capsules/staging/local" add resolved.txt
git -C "$surface/.capsules/staging/local" commit --quiet -m resolved
resolved_tip="$(git -C "$surface/.capsules/staging/local" rev-parse HEAD)"
main_before="$(git -C "$surface" rev-parse main)"
staging_before="$(git -C "$surface" rev-parse staging/local)"
: >"$surface_log"
if (cd "$surface" && KITSOKI_SURFACE_LOG="$surface_log" KITSOKI_SURFACE_GIT="$(command -v git)" \
  scripts/merge-to-main.sh --resume --gate true) >"$tmp/merge-resume.out" 2>&1; then
  echo "installed merge helper accepted direct --resume import" >&2
  exit 1
fi
grep -F "refuses direct --resume import" "$tmp/merge-resume.out" >/dev/null
[ "$(git -C "$surface" rev-parse "refs/kitsoki/promotion-source-recovery/$resolved_tip")" = "$resolved_tip" ] &&
  [ "$(git -C "$surface" rev-parse main)" = "$main_before" ] &&
  [ "$(git -C "$surface" rev-parse staging/local)" = "$staging_before" ] &&
  [ ! -s "$surface_log" ] || {
  echo "merge --resume lost its recovery tip or mutated main/staging" >&2
  exit 1
}
git -C "$surface/.capsules/staging/local" fetch --quiet source staging/local
git -C "$surface/.capsules/staging/local" reset --hard FETCH_HEAD >/dev/null

printf 'main advance\n' >"$surface/main-only.txt"
git -C "$surface" add main-only.txt
git -C "$surface" commit --quiet -m 'main advance'
main_before="$(git -C "$surface" rev-parse main)"
staging_before="$(git -C "$surface" rev-parse staging/local)"
: >"$surface_log"
(cd "$surface" && KITSOKI_SURFACE_LOG="$surface_log" KITSOKI_SURFACE_GIT="$(command -v git)" \
  scripts/refresh-staging-local.sh --skip-remote --gate 'exit 93') >/dev/null
[ "$(git -C "$surface" rev-parse main)" = "$main_before" ] &&
  [ "$(git -C "$surface" rev-parse staging/local)" = "$main_before" ] || {
  echo "refresh route did not converge staging to main without mutating main" >&2
  exit 1
}
[ -f "$surface/.capsules/locks/staging-local-promotion/owner" ] || {
  echo "native refresh route consumed or deleted the legacy stale lease directory" >&2
  exit 1
}
[ "$(wc -l <"$surface_log" | tr -d ' ')" = 1 ] &&
  grep -F "main-to-staging --repo $surface --sha $main_before --gate exit 93" "$surface_log" >/dev/null || {
  echo "refresh helper did not use exactly one native main-to-staging route" >&2
  exit 1
}

printf 'refresh resolved\n' >"$surface/.capsules/staging/local/refresh-resolved.txt"
git -C "$surface/.capsules/staging/local" add refresh-resolved.txt
git -C "$surface/.capsules/staging/local" commit --quiet -m 'refresh resolved'
refresh_resolved_tip="$(git -C "$surface/.capsules/staging/local" rev-parse HEAD)"
main_before="$(git -C "$surface" rev-parse main)"
staging_before="$(git -C "$surface" rev-parse staging/local)"
: >"$surface_log"
if (cd "$surface" && KITSOKI_SURFACE_LOG="$surface_log" KITSOKI_SURFACE_GIT="$(command -v git)" \
  scripts/refresh-staging-local.sh --skip-remote --resume) \
  >"$tmp/refresh-resume.out" 2>&1; then
  echo "queue-owned refresh accepted direct --resume import" >&2
  exit 1
fi
grep -E "preserved at refs/kitsoki/staging-capsule-recovery/$refresh_resolved_tip|refuses direct --resume import" \
  "$tmp/refresh-resume.out" >/dev/null
[ "$(git -C "$surface" rev-parse "refs/kitsoki/staging-capsule-recovery/$refresh_resolved_tip")" = "$refresh_resolved_tip" ] &&
  [ "$(git -C "$surface" rev-parse main)" = "$main_before" ] &&
  [ "$(git -C "$surface" rev-parse staging/local)" = "$staging_before" ] &&
  [ ! -s "$surface_log" ] || {
  echo "refresh --resume lost recovery evidence or mutated main/staging" >&2
  exit 1
}

cmp -s "$root/scripts/kitsoki-promotion-route.sh" \
  "$root/pack/launch-policy/scripts/kitsoki-promotion-route.sh" || {
  echo "pack promotion route drifted from the installed Kitsoki route" >&2
  exit 1
}

echo "kitsoki promotion route tests passed"
