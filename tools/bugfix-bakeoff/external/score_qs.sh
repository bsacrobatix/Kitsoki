#!/usr/bin/env bash
# score_qs.sh — DETERMINISTIC good/bad-fix detector for the query-string
# external bake-off. No LLM, no cost. Given a candidate worktree that already
# carries the candidate's SOURCE fix on top of the bug's baseline, it overlays
# the HIDDEN regression-test oracle the real PR shipped and runs it.
#
#   GREEN oracle  -> the fix is behaviorally correct (the bug is gone)
#   RED   oracle  -> the fix is wrong / incomplete (the bug remains)
#
# It also runs the full ava suite as an informational regression signal. For
# qs1/qs3 a *correct* behavioral fix legitimately changes one PRE-EXISTING test's
# expectation (the real PR edited it too), so full-suite-green is NOT a hard gate
# — the oracle is the verdict; suite deltas are reported for adjudication.
#
# Usage:
#   score_qs.sh --bug qs1 --tree <candidate-worktree> [--out <cell.json>]
#               [--candidate <key>] [--treatment kitsoki|single]
#
# Reuse a prebuilt node_modules to avoid re-installing per cell:
#   QS_NODE_MODULES=/path/to/query-string/node_modules score_qs.sh ...
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

bug=""; tree=""; out=""; candidate="real-fix"; treatment="oracle"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --bug) bug="$2"; shift 2;;
    --tree) tree="$2"; shift 2;;
    --out) out="$2"; shift 2;;
    --candidate) candidate="$2"; shift 2;;
    --treatment) treatment="$2"; shift 2;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done
[[ -n "$bug" && -n "$tree" ]] || { echo "usage: score_qs.sh --bug <id> --tree <dir> [--out json]" >&2; exit 2; }
[[ -d "$tree" ]] || { echo "tree not found: $tree" >&2; exit 2; }

# Per-bug oracle metadata (mirrors query-string/manifest.yaml; 3 stable bugs).
case "$bug" in
  qs1) oracle="$HERE/query-string/oracles/qs1.test.js"; match="single value with encoded separator should not be split into array";;
  qs2) oracle="$HERE/query-string/oracles/qs2.test.js"; match="*and type: number*";;
  qs3) oracle="$HERE/query-string/oracles/qs3.test.js"; match="*bracket-separator* with a URL encoded value*";;
  *) echo "unknown bug: $bug" >&2; exit 2;;
esac
[[ -f "$oracle" ]] || { echo "oracle missing: $oracle" >&2; exit 2; }

# Score in a throwaway copy so the candidate tree is never mutated.
scratch="$(mktemp -d)"; trap 'rm -rf "$scratch"' EXIT
# rsync without node_modules; we re-link a shared one below.
( cd "$tree" && git ls-files -z 2>/dev/null | rsync -a --files-from=- --from0 . "$scratch/" ) \
  || cp -R "$tree/." "$scratch/" 2>/dev/null
rm -rf "$scratch/node_modules"

if [[ -n "${QS_NODE_MODULES:-}" && -d "$QS_NODE_MODULES" ]]; then
  ln -s "$QS_NODE_MODULES" "$scratch/node_modules"
elif [[ -d "$tree/node_modules" ]]; then
  ln -s "$tree/node_modules" "$scratch/node_modules"
else
  echo "[score] npm install in scratch (set QS_NODE_MODULES to skip)..." >&2
  ( cd "$scratch" && npm install --no-audit --no-fund --silent )
fi

# Overlay the hidden oracle onto the candidate's test/parse.js.
{ echo; echo "// ---- injected hidden oracle ($bug) ----"; cat "$oracle"; } >> "$scratch/test/parse.js"

oracle_log="$scratch/.oracle.log"; suite_log="$scratch/.suite.log"
oracle_status="fail"; suite_status="fail"
if ( cd "$scratch" && npx --no-install ava test/parse.js --match "$match" ) >"$oracle_log" 2>&1; then
  oracle_status="pass"
fi
# Full suite (informational). The candidate's own test edits, if any, are in $tree.
if ( cd "$scratch" && npx --no-install ava ) >"$suite_log" 2>&1; then
  suite_status="pass"
fi

oracle_pass=false; [[ "$oracle_status" == pass ]] && oracle_pass=true
suite_pass=false;  [[ "$suite_status" == pass ]] && suite_pass=true
if $oracle_pass; then
  if $suite_pass; then quality="solved"; else quality="partial"; fi
else
  quality="failed"
fi

echo "[score] bug=$bug oracle=$oracle_status suite=$suite_status -> $quality" >&2

if [[ -n "$out" ]]; then
  mkdir -p "$(dirname "$out")"
  cat >"$out" <<JSON
{
  "bug": "$bug",
  "candidate": "$candidate",
  "treatment": "$treatment",
  "outcome": {
    "oracle_pass": $oracle_pass,
    "oracle_status": "$oracle_status",
    "build_pass": null,
    "suite_pass": $suite_pass,
    "quality": "$quality",
    "adjudicated": false,
    "adjudication_note": ""
  },
  "notes": "external query-string oracle; suite_pass is informational (qs1/qs3 legitimately edit one pre-existing test)"
}
JSON
  echo "[score] wrote $out" >&2
fi

# Exit 0 iff the oracle passed (good fix), so callers can gate on $?.
$oracle_pass
