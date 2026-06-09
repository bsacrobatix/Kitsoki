#!/usr/bin/env bash
# kitsoki-ui-review · STAGE 3 (deterministic, no LLM): merge + gate.
#
# Combines the deterministic capture (audit.json: DOM-geometry + axe a11y) with
# the multi-agent vision review (vision.json: heuristic judgement) into one
# verdict.json and a human-readable review-report.md, then sets the GATE exit
# code from the merged severities — it does NOT trust any model's own verdict.
#
#   exit 0  no blocking findings
#   exit 1  at least one blocking finding (error always; warn under --strict)
#   exit 2  pipeline error (bad inputs)
#
# Usage: report.sh --audit <audit.json> --vision <vision.json>
#                  --out <review-report.md> --verdict <verdict.json> [--strict]
set -euo pipefail

audit="" vision="" out="" verdict="" strict=0
while [ $# -gt 0 ]; do
  case "$1" in
    --audit)   audit="$2"; shift 2 ;;
    --vision)  vision="$2"; shift 2 ;;
    --out)     out="$2"; shift 2 ;;
    --verdict) verdict="$2"; shift 2 ;;
    --strict)  strict=1; shift ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done
command -v jq >/dev/null 2>&1 || { echo "jq not on PATH" >&2; exit 2; }
[ -f "$audit" ]  || { echo "no such audit.json: $audit" >&2; exit 2; }
[ -f "$vision" ] || { echo "no such vision.json: $vision" >&2; exit 2; }
[ -n "$out" ] && [ -n "$verdict" ] || { echo "--out and --verdict are required" >&2; exit 2; }
mkdir -p "$(dirname "$out")" "$(dirname "$verdict")"

# ── Build the merged verdict.json. Normalise both sources to one finding shape:
#    {source, check, severity, surface(step), viewport, frame, detail, recommendation}
jq -n \
  --slurpfile a "$audit" \
  --slurpfile v "$vision" \
  --argjson strict "$strict" '
  ($a[0]) as $audit |
  ($v[0]) as $vision |
  ([ $audit.findings[]? | {
      source: (if (.source=="a11y") then "a11y" else "geometry" end),
      check, severity, surface:.step, viewport, frame,
      detail, recommendation:""
    } ]) as $det |
  ([ $vision.findings[]? | {
      source:"vision", check, severity, surface:.shard, viewport,
      frame, detail:.observation, recommendation:(.recommendation // "")
    } ]) as $vis |
  ($det + $vis) as $raw |
  # Dedup repeated findings — persistent UI chrome is captured on every step, so
  # the same defect recurs across frames. Collapse by source+check+locus+viewport
  # into ONE finding that lists every frame it appears on (with a count).
  ([ $raw
     | group_by([.source, .check, (.selector // ""), (.surface // ""), .viewport])
     | .[]
     | (map(.frame) | map(select(. != "")) | unique) as $frames
     | (.[0] + { count: length, frames: $frames }) ]) as $all |
  ($all | map(select(.severity=="error"))) as $errs |
  ($all | map(select(.severity=="warn"))) as $warns |
  ($all | map(select(.severity=="info"))) as $infos |
  (($errs|length) + (if $strict==1 then ($warns|length) else 0 end)) as $blocking |
  {
    overall: (if $blocking>0 then "fail" else "pass" end),
    strict: ($strict==1),
    summary: {
      error:($errs|length), warn:($warns|length), info:($infos|length),
      blocking:$blocking,
      by_source: {
        geometry: ($all|map(select(.source=="geometry"))|length),
        a11y:     ($all|map(select(.source=="a11y"))|length),
        vision:   ($all|map(select(.source=="vision"))|length)
      }
    },
    findings: ($all | sort_by(
      (if .severity=="error" then 0 elif .severity=="warn" then 1 else 2 end),
      .surface, .viewport))
  }' > "$verdict"

# ── Render review-report.md from the verdict. ───────────────────────────────
{
  overall="$(jq -r '.overall' "$verdict")"
  strictnote=""; [ "$strict" -eq 1 ] && strictnote=" *(strict: warnings block)*"
  echo "# UI layout & usability review"
  echo
  if [ "$overall" = "pass" ]; then echo "**Gate: ✅ PASS**$strictnote"; else echo "**Gate: ❌ FAIL**$strictnote"; fi
  echo
  jq -r '"- **\(.summary.error)** error · **\(.summary.warn)** warn · **\(.summary.info)** info " +
         "— \(.summary.blocking) blocking\n" +
         "- by source: \(.summary.by_source.geometry) geometry · " +
         "\(.summary.by_source.a11y) a11y · \(.summary.by_source.vision) vision"' "$verdict"
  echo

  for sev in error warn info; do
    cnt="$(jq --arg s "$sev" '[.findings[]|select(.severity==$s)]|length' "$verdict")"
    [ "$cnt" -eq 0 ] && continue
    case "$sev" in
      error) echo "## ❌ Errors ($cnt)";;
      warn)  echo "## ⚠️  Warnings ($cnt)";;
      info)  echo "## ℹ️  Info ($cnt)";;
    esac
    echo
    echo "| surface | viewport | source | check | observation | fix | frame | seen |"
    echo "|---|---|---|---|---|---|---|---|"
    jq -r --arg s "$sev" '
      .findings[] | select(.severity==$s) |
      (.frames // []) as $fr |
      (if ($fr|length)>0 then $fr[0] else (.frame // "") end) as $cite |
      "| \(.surface) | \(.viewport) | \(.source) | `\(.check)` | " +
      "\((.detail // "")|gsub("\\|";"\\\\|")|gsub("\n";" ")) | " +
      "\((.recommendation // "")|gsub("\\|";"\\\\|")|gsub("\n";" ")) | " +
      "\(if $cite=="" then "—" else "`\($cite)`" end) | " +
      "\(if (.count // 1)>1 then "×\(.count)" else "1" end) |"' "$verdict"
    echo
  done
} > "$out"

echo "wrote $verdict and $out" >&2
jq -r '"gate: " + (.overall|ascii_upcase) + " (" + (.summary.blocking|tostring) + " blocking)"' "$verdict" >&2

# Gate exit code.
blocking="$(jq '.summary.blocking' "$verdict")"
[ "$blocking" -eq 0 ] || exit 1
exit 0
