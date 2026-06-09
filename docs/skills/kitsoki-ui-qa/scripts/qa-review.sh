#!/usr/bin/env bash
# Ground a vision QA review of a UI demo against a feature description + usage
# scenarios. Spawns the local `claude` CLI (no API key, no per-call cost —
# see memory project_oracle_uses_claude_cli) as a READ-ONLY agent that reads the
# extracted frame PNGs with its Read tool and emits a structured verdict.json.
#
# Reliability is NOT from the model being deterministic — it comes from:
#   • a fixed, deterministic frame set (extract-frames.sh) as the ONLY evidence;
#   • every verdict MUST cite a frame filename + quote what is literally visible;
#     a claim with no citable frame is `unsupported`, never `pass`;
#   • an adversarial second pass (a skeptic that may only DOWNGRADE) re-checks
#     each `pass` against its cited frame (disable with --no-adversary).
#
# This is an LLM-driven review tool by design. It is NOT a no-LLM flow test and
# must never be wired into the automated test suite (CLAUDE.md). The surrounding
# deterministic pieces (extract-frames.sh, report.sh) are testable without an LLM.
#
# Usage: qa-review.sh --frames <dir> --feature <file> --scenarios <file>
#                     --out <verdict.json> [--model M] [--no-adversary]
set -euo pipefail

frames="" feature="" scenarios="" out="" model="claude-opus-4-8" adversary=1
while [ $# -gt 0 ]; do
  case "$1" in
    --frames)       frames="$2"; shift 2 ;;
    --feature)      feature="$2"; shift 2 ;;
    --scenarios)    scenarios="$2"; shift 2 ;;
    --out)          out="$2"; shift 2 ;;
    --model)        model="$2"; shift 2 ;;
    --no-adversary) adversary=0; shift ;;
    *) echo "unknown arg: $1" >&2; exit 1 ;;
  esac
done

command -v claude >/dev/null 2>&1 || { echo "claude CLI not on PATH" >&2; exit 1; }
command -v jq     >/dev/null 2>&1 || { echo "jq not on PATH" >&2; exit 1; }
[ -d "$frames" ]      || { echo "no such frames dir: $frames" >&2; exit 1; }
[ -f "$feature" ]     || { echo "no such feature file: $feature" >&2; exit 1; }
[ -f "$scenarios" ]   || { echo "no such scenarios file: $scenarios" >&2; exit 1; }
[ -n "$out" ]         || { echo "--out is required" >&2; exit 1; }

frames="$(cd "$frames" && pwd)"           # absolute, for --add-dir
mkdir -p "$(dirname "$out")"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT

frame_list="$(cd "$frames" && ls -1 [0-9]*.png 2>/dev/null | sort || ls -1 *.png | sort)"
[ -n "$frame_list" ] || { echo "no PNG frames in $frames" >&2; exit 1; }

# ---- one read-only claude call; final assistant message must be raw verdict JSON
call_claude() { # <promptfile> ; echoes parsed+validated JSON, or exits 2
  local pf="$1" raw result json
  raw="$(claude -p \
          --output-format json \
          --model "$model" \
          --permission-mode bypassPermissions \
          --allowedTools "Read" \
          --add-dir "$frames" \
          < "$pf")" || { echo "claude invocation failed" >&2; exit 2; }
  result="$(printf '%s' "$raw" | jq -r '.result // .text // empty')"
  [ -n "$result" ] || result="$raw"          # tolerate a bare-JSON CLI build
  json="$(printf '%s\n' "$result" | sed '/^```/d')"   # strip ``` fences if any
  if ! printf '%s' "$json" | jq -e . >/dev/null 2>&1; then
    printf '%s' "$result" > "$tmp/bad.txt"
    echo "model did not return valid JSON; raw saved to $tmp/bad.txt" >&2
    cp "$tmp/bad.txt" "${out%.json}.raw.txt" 2>/dev/null || true
    exit 2
  fi
  printf '%s' "$json" | jq .
}

# ---------- pass 1: grounded review ----------
review_prompt="$tmp/review.txt"
{
  cat <<'HEAD'
You are a meticulous UI QA reviewer. You are given screenshots ("frames") taken
from a product demo video, a feature description, and a list of usage scenarios.
Decide, for each scenario step, whether the demo actually demonstrates it.

EVIDENCE RULES (these make the review trustworthy — follow them exactly):
1. The frame PNG files are the ONLY admissible evidence. Use the Read tool to
   open the specific frames you need. Read enough frames to judge every step.
2. For every step you mark `pass`, you MUST cite at least one frame filename and
   quote what is LITERALLY visible in it that demonstrates the step (visible text,
   a button, a state badge, a list, etc.). Do not infer beyond the pixels.
3. If no frame shows the step, its status is `unsupported` (the demo neither
   proves nor disproves it) — NOT `pass`. If a frame actively contradicts it
   (wrong text, error state, missing element), its status is `fail`.
4. Never invent UI that you did not see in a frame. When unsure, prefer
   `unsupported` over `pass`.

Compute each scenario's status as the worst of its steps (fail < unsupported <
pass). Copy each scenario's `id`, `title`, and `required` exactly from the YAML
(default `required` to true if absent). `overall` is `pass` only if every
required scenario is `pass`, else `fail`.

OUTPUT: print ONLY a single raw JSON object (no prose, no ``` fences) of shape:
{
  "overall": "pass|fail",
  "summary": {"scenarios_total":0,"passed":0,"failed":0,"unsupported":0},
  "frames_reviewed": ["0001-0ms.png"],
  "scenarios": [
    {"id":"...","title":"...","required":true,"status":"pass|fail|unsupported",
     "steps":[
       {"text":"...","status":"pass|fail|unsupported",
        "evidence":[{"frame":"0003-1200ms.png","observation":"<literal, what is visible>"}],
        "confidence":0.0}
     ]}
  ]
}
HEAD
  echo; echo "## FEATURE DESCRIPTION"; echo; cat "$feature"
  echo; echo "## USAGE SCENARIOS (YAML)"; echo; echo '```yaml'; cat "$scenarios"; echo '```'
  echo; echo "## AVAILABLE FRAMES"
  echo "Located in: $frames (Read them by filename)."
  echo "$frame_list" | sed 's/^/  - /'
} > "$review_prompt"

echo "▸ grounded review ($model, $(echo "$frame_list" | wc -l | tr -d ' ') frames)…" >&2
call_claude "$review_prompt" > "$out"

# ---------- pass 2: adversarial verification (downgrade-only) ----------
if [ "$adversary" -eq 1 ]; then
  adv_prompt="$tmp/adversary.txt"
  {
    cat <<'HEAD'
You are an adversarial verifier. Below is a prior QA verdict (JSON) for a demo
video, plus the frames it cited. Your ONLY job is to catch over-claims.

For each step currently marked `pass`: Read its cited frame(s) and confirm the
quoted observation is ACTUALLY visible there. If the cited frame does not clearly
show it (wrong frame, the element is absent, the text differs, it was inferred),
DOWNGRADE the step to `fail` (frame contradicts) or `unsupported` (frame simply
doesn't show it) and rewrite its observation to state what the frame really shows.

You may ONLY downgrade. Never upgrade a `fail`/`unsupported` to `pass`. Leave
non-pass steps untouched. Then recompute each scenario status (worst of steps)
and `overall` (pass only if all required scenarios pass) and the summary counts.

OUTPUT: print ONLY the full revised verdict as a single raw JSON object of the
exact same shape as the input (no prose, no ``` fences).
HEAD
    echo; echo "## PRIOR VERDICT"; echo; echo '```json'; cat "$out"; echo '```'
    echo; echo "## AVAILABLE FRAMES"
    echo "Located in: $frames (Read them by filename)."
    echo "$frame_list" | sed 's/^/  - /'
  } > "$adv_prompt"

  echo "▸ adversarial verification…" >&2
  call_claude "$adv_prompt" > "$tmp/verified.json"
  mv "$tmp/verified.json" "$out"
fi

echo "wrote $out" >&2
