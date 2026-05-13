#!/usr/bin/env bash
# fake-tmux.sh — emulates the tmux subset internal/tmux uses for tests.
#
# State lives in $KITSOKI_FAKE_TMUX_STATE_DIR (caller-set). Each session
# is one file in that dir named after the session.
#
# Supports exactly the invocations the Client makes:
#   tmux -S <sock> new-session -d -s NAME [-f CONF] [-c CWD] [-e KEY=VAL ...] CMD
#   tmux -S <sock> has-session -t NAME
#   tmux -S <sock> kill-session -t NAME
#   tmux -S <sock> list-sessions -F '#{session_name}'
#   tmux -S <sock> attach -t NAME
#
# Everything else exits 1 with a "fake-tmux: unsupported …" stderr.
set -euo pipefail

state_dir="${KITSOKI_FAKE_TMUX_STATE_DIR:-}"
if [ -z "$state_dir" ]; then
  echo "fake-tmux: KITSOKI_FAKE_TMUX_STATE_DIR not set" >&2
  exit 2
fi
mkdir -p "$state_dir"

# Skip the -S <sock> prefix we always pass.
if [ "${1:-}" = "-S" ]; then
  shift 2
fi

# Optional -f CONF. When set, copy the file contents into
# $state_dir/.last-conf so tests can assert the kitsoki-shipped
# config (or any caller-supplied config) made it through.
if [ "${1:-}" = "-f" ]; then
  if [ -n "${2:-}" ] && [ -f "$2" ]; then
    cp "$2" "$state_dir/.last-conf"
  fi
  shift 2
fi

verb="${1:-}"
shift || true

case "$verb" in
  new-session)
    name=""
    while [ $# -gt 0 ]; do
      case "$1" in
        -d) shift ;;
        -s) name="$2"; shift 2 ;;
        -c) shift 2 ;;
        -e) shift 2 ;;
        --) shift; break ;;
        *) break ;;
      esac
    done
    if [ -z "$name" ]; then
      echo "fake-tmux: new-session missing -s NAME" >&2
      exit 2
    fi
    if [ -f "$state_dir/$name" ]; then
      echo "duplicate session: $name" >&2
      exit 1
    fi
    # Whatever remains is the in-pane command. Record it so tests can
    # assert on the claude argv (--session-id vs --resume, permission
    # mode, etc.) without having to spawn a real tmux.
    : > "$state_dir/$name"
    printf '%s' "$*" > "$state_dir/$name.cmd"
    exit 0
    ;;
  has-session)
    name=""
    while [ $# -gt 0 ]; do
      case "$1" in
        -t) name="$2"; shift 2 ;;
        *) shift ;;
      esac
    done
    if [ -f "$state_dir/$name" ]; then
      exit 0
    fi
    echo "can't find session: $name" >&2
    exit 1
    ;;
  kill-session)
    name=""
    while [ $# -gt 0 ]; do
      case "$1" in
        -t) name="$2"; shift 2 ;;
        *) shift ;;
      esac
    done
    if [ ! -f "$state_dir/$name" ]; then
      echo "can't find session: $name" >&2
      exit 1
    fi
    rm -f "$state_dir/$name"
    exit 0
    ;;
  list-sessions)
    # Ignore -F flag; we always emit one name per line. Sidecar
    # files (NAME.cmd, NAME.status-right, .last-conf) are excluded
    # so the listing only contains real session names.
    if [ -d "$state_dir" ]; then
      shopt -s nullglob
      for f in "$state_dir"/*; do
        b="$(basename "$f")"
        case "$b" in
          .*) continue ;;
          *.cmd|*.status-right) continue ;;
        esac
        printf '%s\n' "$b"
      done
    fi
    exit 0
    ;;
  attach)
    name=""
    while [ $# -gt 0 ]; do
      case "$1" in
        -t) name="$2"; shift 2 ;;
        *) shift ;;
      esac
    done
    if [ ! -f "$state_dir/$name" ]; then
      echo "can't find session: $name" >&2
      exit 1
    fi
    # For tests, attach is a no-op success.
    exit 0
    ;;
  set-option)
    # set-option -t NAME KEY VALUE.  We record the value in
    # $state_dir/<name>.<key> so tests can read it back.
    name=""
    while [ "${1:-}" = "-t" ] || [ "${1:-}" = "-g" ]; do
      if [ "$1" = "-t" ]; then
        name="$2"; shift 2
      else
        shift
      fi
    done
    key="${1:-}"
    value="${2:-}"
    if [ -z "$name" ] || [ -z "$key" ]; then
      echo "fake-tmux: set-option missing -t NAME or key" >&2
      exit 2
    fi
    if [ ! -f "$state_dir/$name" ]; then
      echo "can't find session: $name" >&2
      exit 1
    fi
    printf '%s' "$value" > "$state_dir/$name.$key"
    exit 0
    ;;
  *)
    echo "fake-tmux: unsupported verb $verb" >&2
    exit 2
    ;;
esac
