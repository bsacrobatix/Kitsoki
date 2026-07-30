#!/usr/bin/env bash
# Installed as .kitsoki/bin/{claude,codex}. It is intentionally tiny: policy
# resolution stays in `kitsoki agent launch`, while this wrapper preserves the
# exact native CLI argv that the caller supplied.
set -euo pipefail

backend="$(basename "$0")"
case "$backend" in claude|codex) ;; *) echo "unsupported Kitsoki launcher shim: $backend" >&2; exit 2 ;; esac

upper="$(printf '%s' "$backend" | tr '[:lower:]' '[:upper:]')"
override="KITSOKI_AGENT_${upper}_BIN"
real_override="KITSOKI_AGENT_${upper}_REAL_BIN"

shim_depth="${KITSOKI_AGENT_LAUNCH_SHIM_ACTIVE:-0}"
if [ "$shim_depth" -ge 1 ]; then
  # Hard backstop: a correct policy-approved launch only ever re-enters this
  # branch once (the launch plan's resolved binary lands back on this same
  # shim via PATH/override). If that ever fails to converge — e.g. a PATH
  # entry reaches this shim's directory through a symlink that doesn't
  # string-match its canonicalized form below — fail loudly instead of
  # forking itself forever.
  if [ "$shim_depth" -ge 3 ]; then
    echo "Kitsoki launcher shim: recursion guard tripped after $shim_depth levels; refusing to loop (set $real_override to break the cycle)" >&2
    exit 111
  fi
  real="${!real_override:-}"
  if [ -z "$real" ]; then
    shim_dir="$(cd "$(dirname "$0")" && pwd -P)"
    old_ifs="$IFS"
    new_path=""
    IFS=:
    for entry in $PATH; do
      [ -n "$entry" ] || continue
      # Compare canonicalized forms on both sides — $entry may reach this
      # same directory through a symlink (e.g. macOS's /tmp -> /private/tmp)
      # that would not string-match $shim_dir otherwise.
      entry_resolved="$(cd "$entry" 2>/dev/null && pwd -P || printf '%s' "$entry")"
      if [ "$entry_resolved" != "$shim_dir" ]; then
        new_path="${new_path:+$new_path:}$entry"
      fi
    done
    IFS="$old_ifs"
    PATH="$new_path"
    real="$(command -v "$backend" || true)"
  fi
  [ -n "$real" ] || { echo "Kitsoki launcher shim: real $backend binary not found; set $real_override" >&2; exit 127; }
  export KITSOKI_AGENT_LAUNCH_SHIM_ACTIVE=$((shim_depth + 1))
  exec "$real" "$@"
fi

kitsoki_bin="${KITSOKI_BIN:-kitsoki}"
# kitsoki agent launch reads --config literally relative to its own process
# CWD, with no upward search. The caller may have cd'd anywhere (that is the
# whole point of --working-dir "$PWD" below), so the installed pack's own
# config must be passed explicitly here or the policy gate silently loads an
# empty config and no-ops the moment the caller isn't sitting in the repo
# root that owns .kitsoki.yaml.
install_root="$(cd "$(dirname "$0")/../.." && pwd -P)"
working_dir="$(pwd -P)"
# Activation intentionally leaves this shim at the front of PATH while a shell
# moves between directories. It owns policy only for its installation root;
# outside that tree it must act like the native backend rather than applying a
# repository-local allowlist to the whole machine. Re-enter the existing
# depth-guard path so the real backend resolution and argv preservation remain
# centralized.
case "$working_dir/" in
  "$install_root/"*) ;;
  *) KITSOKI_AGENT_LAUNCH_SHIM_ACTIVE=1 exec "$0" "$@" ;;
esac
config_path="$install_root/.kitsoki.yaml"
if [ "${1:-}" = "pog-drive" ]; then
  shift
  if [ "$backend" != "codex" ]; then
    echo "Kitsoki launcher shim: pog-drive currently supports the Codex backend only" >&2
    exit 2
  fi
  workspace_id="pog-driver-${backend}-$(date +%Y%m%d-%H%M%S)-$$"
  workspace_owner="pog-driver/${backend}/$$"
  "$kitsoki_bin" capsule workspace create-script \
    --project "$install_root" \
    --id "$workspace_id" \
    --owner "$workspace_owner"
  working_dir="$install_root/.capsules/workspaces/$workspace_id"
  [ -d "$working_dir" ] || { echo "Kitsoki launcher shim: POG Capsule workspace was not created: $working_dir" >&2; exit 1; }
  echo "Kitsoki launcher shim: starting pog-driver in $working_dir" >&2
  pog_args=(agent launch --agent pog-driver --profile pog-driver-codex --backend codex --working-dir "$working_dir" --config "$config_path")
  if [ "$#" -gt 0 ]; then
    task=""
    for part in "$@"; do task="${task:+$task }$part"; done
    pog_args+=(--task "$task" --exec)
  else
    pog_args+=(--interactive)
  fi
  KITSOKI_AGENT_LAUNCH_SHIM_ACTIVE=1 exec "$kitsoki_bin" "${pog_args[@]}"
fi
if [ "${1:-}" = "superagent" ]; then
  shift
  workspace_id="${backend}-$(date +%Y%m%d-%H%M%S)-$$"
  workspace_owner="superagent/${backend}/$$"
  "$kitsoki_bin" capsule workspace create-script \
    --project "$install_root" \
    --id "$workspace_id" \
    --owner "$workspace_owner"
  working_dir="$install_root/.capsules/workspaces/$workspace_id"
  [ -d "$working_dir" ] || { echo "Kitsoki launcher shim: Capsule workspace was not created: $working_dir" >&2; exit 1; }
  echo "Kitsoki launcher shim: starting $backend superagent in $working_dir" >&2
fi
if [ "${1:-}" = "unlimited" ]; then
  shift
  # Same full-permissions session as `superagent`, but in a plain git worktree:
  # direct `git commit` in the worktree is the intended workflow, so there is no
  # Capsule workspace, no sentinel, no Capsule CI, and no reconcile/promote step.
  # The worktree is REUSED across launches — an operator must not accumulate one
  # per session. Name it explicitly with `claude unlimited <name> [native args…]`
  # or KITSOKI_UNLIMITED_WORKTREE; anything starting with `-` is native argv.
  worktree_name="${KITSOKI_UNLIMITED_WORKTREE:-unlimited-$backend}"
  case "${1:-}" in ""|-*) ;; *) worktree_name="$1"; shift ;; esac
  case "$worktree_name" in
    ""|.*|*/*) echo "Kitsoki launcher shim: unlimited worktree name must be a single path segment: $worktree_name" >&2; exit 2 ;;
  esac
  working_dir="$install_root/.worktrees/$worktree_name"
  if [ ! -d "$working_dir" ]; then
    # Fresh worktrees branch off staging/local when it exists, else the repo's
    # default branch (same resolution the pack's reference-transaction hook uses).
    base="staging/local"
    if ! git -C "$install_root" show-ref --verify --quiet "refs/heads/$base"; then
      base="$(git -C "$install_root" symbolic-ref --quiet --short refs/remotes/origin/HEAD 2>/dev/null | sed 's@^origin/@@' || true)"
      if [ -z "$base" ]; then
        if git -C "$install_root" show-ref --verify --quiet refs/heads/main; then base=main; else base=master; fi
      fi
    fi
    # No hook opt-out here: the pack's reference-transaction hook recognizes
    # `git worktree add` by its invoking command and lets it through, so the
    # primary checkout's branch pin stays fully enforced for this call.
    git_add=(git -C "$install_root" worktree add)
    if git -C "$install_root" show-ref --verify --quiet "refs/heads/$worktree_name"; then
      "${git_add[@]}" "$working_dir" "$worktree_name" >&2
    else
      "${git_add[@]}" -b "$worktree_name" "$working_dir" "$base" >&2
    fi
  fi
  [ -d "$working_dir" ] || { echo "Kitsoki launcher shim: git worktree was not created: $working_dir" >&2; exit 1; }
  echo "Kitsoki launcher shim: starting $backend unlimited in $working_dir (plain git worktree; direct git, no Capsule)" >&2
fi

args=(agent launch --raw --interactive --backend "$backend" --working-dir "$working_dir" --config "$config_path")
# Agent Mail is an opt-in, machine-local endpoint shared by all repositories
# under a developer's orchestration root.  Keep its URL and bearer token out of
# project configuration: launcher-env.sh loads them from agent-mail.env, and
# the shim attaches the appropriate native transport only for governed launches.
#
# Claude consumes the portable MCP JSON file (which may contain ${VAR}
# expansion); Codex consumes direct TOML overrides and reads the token from its
# named environment variable.  Caller-supplied native args remain last so they
# retain normal CLI precedence.
if [ -n "${KITSOKI_AGENT_MAIL_URL:-}" ]; then
  case "$backend" in
    claude)
      agent_mail_mcp_config="${KITSOKI_AGENT_MAIL_MCP_CONFIG:-${XDG_CONFIG_HOME:-$HOME/.config}/kitsoki/agent-mail.mcp.json}"
      [ -r "$agent_mail_mcp_config" ] || {
        echo "Kitsoki launcher shim: KITSOKI_AGENT_MAIL_URL is set but Agent Mail MCP config is unreadable: $agent_mail_mcp_config" >&2
        exit 2
      }
      args+=(--raw-arg --mcp-config --raw-arg "$agent_mail_mcp_config")
      ;;
    codex)
      args+=(--raw-arg -c --raw-arg "mcp_servers.agent-mail.url=${KITSOKI_AGENT_MAIL_URL}")
      if [ -n "${KITSOKI_AGENT_MAIL_TOKEN:-}" ]; then
        args+=(--raw-arg -c --raw-arg "mcp_servers.agent-mail.bearer_token_env_var=KITSOKI_AGENT_MAIL_TOKEN")
      fi
      ;;
  esac
fi
for arg in "$@"; do args+=(--raw-arg "$arg"); done
KITSOKI_AGENT_LAUNCH_SHIM_ACTIVE=1 "$kitsoki_bin" "${args[@]}"
