#!/usr/bin/env bash
# drive_cell.sh — run ONE external bake-off cell end-to-end from the manifest.
#
# Codifies the live-drive recipe (worktree prep + every load-bearing initial_world
# knob + the headless MCP delegation) so a cell is one command instead of a
# hand-assembled prompt. COST-BEARING (real LLM) — operator-run, never in CI.
#
#   drive_cell.sh --project <name> --bug <id> --candidate <key> [--score] [--no-drive]
#
#   --score      after the drive, grade the worktree with bench.py + extract cost
#   --no-drive   only prepare the worktree + print the prompt (free; for inspection)
#
# Reads projects/<name>/manifest.yaml (per-bug facts via `bench.py meta --bug`) and
# candidates.yaml (the model/profile axis). Clones the repo ONCE into a cache and
# reuses node_modules across cells. The worker model is whatever the candidate's
# profile selects (codex-native → GPT-5.5, synthetic-claude → GLM-5.2); the
# orchestrator (cheap sonnet) only clicks the pipeline forward.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$HERE" && git rev-parse --show-toplevel)"
CACHE="$REPO_ROOT/.artifacts/qs-bakeoff"      # gitignored work area

project=""; bug=""; cand=""; do_score=0; no_drive=0; orch="${MCP_DRIVE_MODEL:-opus}"
while [[ $# -gt 0 ]]; do
  case "$1" in
    --project) project="$2"; shift 2;;
    --bug) bug="$2"; shift 2;;
    --candidate) cand="$2"; shift 2;;
    --orchestrator) orch="$2"; shift 2;;
    --score) do_score=1; shift;;
    --no-drive) no_drive=1; shift;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done
[[ -n "$project" && -n "$bug" && -n "$cand" ]] || {
  echo "usage: drive_cell.sh --project <name> --bug <id> --candidate <key> [--score] [--no-drive]" >&2; exit 2; }

# --- read manifest (per-bug) + candidate --------------------------------------
meta="$(cd "$HERE" && python3 bench.py meta --project "$project" --bug "$bug")"
jget() { python3 -c 'import json,sys; print(json.load(sys.stdin).get(sys.argv[1],""))' "$1" <<<"$meta"; }
repo="$(jget repo)"; install="$(jget install)"; test_cmd="$(jget test_cmd)"
baseline="$(jget baseline_sha)"; title="$(jget title)"; ticket="$(jget ticket)"

cand_field() { python3 -c '
import sys,yaml
d=yaml.safe_load(open(sys.argv[1]))
for c in d["candidates"]:
    if c["key"]==sys.argv[2]: print(c.get(sys.argv[3],"")); break
' "$HERE/candidates.yaml" "$cand" "$1"; }
profile="$(cand_field profile)"; short="$(cand_field short)"
[[ -n "$profile" ]] || { echo "unknown candidate '$cand' in candidates.yaml" >&2; exit 2; }

# --- fail fast (before any clone/spend) ---------------------------------------
# local_only projects (kitsoki-self, gears-rust) aren't live-drivable here yet:
# drive_cell clones $repo + runs the JS install. Grade them with bench.py
# score/verify against a throwaway local mirror instead (see the project README).
if [[ "$(jget local_only)" == "True" || "$(jget local_only)" == "true" ]]; then
  echo "[cell] '$project' is local_only — not live-drivable via drive_cell yet." >&2
  echo "       Grade it deterministically: bench.py verify/score against a 'git clone --local' mirror." >&2
  exit 2
fi
# the candidate's profile must be configured, or session_new fails late + cryptic.
if ! grep -qE "^  ${profile}:[[:space:]]*$" "$REPO_ROOT/.kitsoki.yaml" "$REPO_ROOT/.kitsoki.local.yaml" 2>/dev/null; then
  echo "[cell] profile '$profile' (candidate $cand) not found in .kitsoki.yaml/.kitsoki.local.yaml." >&2
  echo "       Add it (see .kitsoki.local.yaml.example) before driving this candidate." >&2
  exit 2
fi

# rich ticket_title: pack the full bug description (the reproducer is fed only
# ticket_id + ticket_title; no ticket file). One line, quotes stripped.
desc="$(printf '%s — %s' "$title" "$(printf '%s' "$ticket" | tr '\n' ' ' | sed 's/  */ /g')" | sed 's/"/\\"/g')"

# --- clone cache + node_modules (once) ----------------------------------------
clone="$CACHE/clone/$project"
if [[ ! -d "$clone/.git" ]]; then
  echo "[cell] cloning $repo -> $clone" >&2
  mkdir -p "$(dirname "$clone")"
  git clone -q "$repo" "$clone"
fi
git -C "$clone" cat-file -e "$baseline" 2>/dev/null || git -C "$clone" fetch -q origin "$baseline"
if [[ ! -d "$clone/node_modules" ]]; then
  echo "[cell] $install (once) in $clone" >&2
  ( cd "$clone" && git checkout -q "$baseline" && eval "$install" )
fi

# --- per-cell worktree at baseline on its own branch --------------------------
cell="$CACHE/cells/$bug-$cand"
branch="bench-$bug-$short"
if [[ -d "$cell" ]]; then
  git -C "$cell" reset --hard -q "$baseline"; git -C "$cell" clean -fdq
else
  git -C "$clone" worktree add -q --detach "$cell" "$baseline"
fi
git -C "$cell" checkout -q -B "$branch"
ln -sfn "$clone/node_modules" "$cell/node_modules"

trace="$CACHE/traces/$bug-$cand.jsonl"; rm -f "$trace"
thread_file="$CACHE/threads/$bug-$cand.md"
mkdir -p "$CACHE/traces" "$CACHE/drive-logs" "$CACHE/results/cells" "$CACHE/threads"

# --- the orchestrator prompt (all tuning knobs baked in) ----------------------
prompt="$(cat <<EOF
Drive ONE kitsoki bug-fix pipeline cell to completion via the kitsoki studio MCP.
The fix MUST be generated by the live worker model inside the session (profile
**$profile** = $cand); you (orchestrator) only click studio tools — do NOT edit source.

1. studio_ping.
2. session_new EXACTLY:
   - story_path: "$REPO_ROOT/stories/bench-bugfix/app.yaml"
   - harness: "live"
   - profile: "$profile"
   - trace: "$trace"
   - initial_world:
       ticket_id: "$bug"
       thread: "$thread_file"
       ticket_title: "$desc"
       workdir: "$cell"
       workspace_id: ""
       feature_branch: "$branch"
       base_branch: "$branch"
       bugfix_mode: "full"
       judge_mode: "llm"
       test_cmd: "$test_cmd"
       bf_autostart_attempted: true
   (workspace_id EMPTY ⇒ implementer edits the prepared workdir directly + commits.)
3. Drive **full_pipeline** ONCE, then only advance explicit gates (accept/continue/
   confirm/proceed) and answer ask-gates affirmatively ("looks correct, proceed").
   Do NOT re-drive start — the LLM judge auto-emits accept/refine. Give each
   on_enter step time ($cand does the real work there).
4. STOP at a terminal state, ~25 forward turns, or a repeated stuck state. If a
   host_error bounces you to idle, read world.last_error, report it verbatim, STOP.
5. Then run + include: git -C "$cell" log --oneline -5 ; git -C "$cell" show --stat HEAD -- base.js ; git -C "$cell" status --short
Report: final state; trace path; base.js modified (y/n) + fix SHA; 1-line fix; reproduction bug_verified (t/f); forward turns; last_error if any.
EOF
)"
pf="$CACHE/drive-prompts/$bug-$cand.md"; mkdir -p "$(dirname "$pf")"; printf '%s\n' "$prompt" > "$pf"
echo "[cell] project=$project bug=$bug candidate=$cand profile=$profile" >&2
echo "[cell] worktree=$cell branch=$branch trace=$trace" >&2

if [[ "$no_drive" == 1 ]]; then echo "[cell] --no-drive: prompt at $pf"; exit 0; fi

# --- drive (COST) -------------------------------------------------------------
log="$CACHE/drive-logs/$bug-$cand.json"
echo "[cell] driving (orchestrator=$orch, worker=$cand)…" >&2
MCP_DRIVE_MODEL="$orch" "$REPO_ROOT/tools/mcp-drive/drive.sh" --prompt-file "$pf" > "$log" 2>"${log%.json}.err" || true
echo "[cell] drive done -> $log" >&2

if [[ "$do_score" == 1 ]]; then
  out="$CACHE/results/cells/$bug-$cand-kitsoki.json"
  QS_NODE_MODULES="$clone/node_modules" python3 "$HERE/bench.py" score \
    --project "$project" --bug "$bug" --tree "$cell" \
    --candidate "$cand" --treatment kitsoki --out "$out" \
    --trace "$trace" --candidates "$HERE/candidates.yaml" || true
  echo "[cell] cost: $(python3 "$HERE/bench.py" cost --trace "$trace")"
  echo "[cell] verdict: $(python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["outcome"]["quality"])' "$out" 2>/dev/null || echo "?")"
fi
