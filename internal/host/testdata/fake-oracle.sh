#!/usr/bin/env bash
# fake-oracle.sh — emulates `claude -p` for Oracle handler tests.
# Echoes an answer that includes the session-id it was invoked with so tests
# can assert the handler forwarded the session correctly.
set -euo pipefail

session_id=""
while [ $# -gt 0 ]; do
  case "$1" in
    --session-id) session_id="$2"; shift 2 ;;
    *) shift ;;
  esac
done

question="$(cat /dev/stdin)"
printf 'ANSWER for q=[%s] sid=%s\n' "$question" "$session_id"
