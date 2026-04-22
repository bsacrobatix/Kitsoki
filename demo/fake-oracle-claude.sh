#!/usr/bin/env bash
# fake-oracle-claude.sh — stand-in for `claude -p` in the dev-story demo tape.
# Emits a deterministic, nicely-formatted answer so the recorded GIF does not
# depend on the real Claude Code CLI or network latency.
set -euo pipefail

session_id=""
while [ $# -gt 0 ]; do
  case "$1" in
    --session-id) session_id="$2"; shift 2 ;;
    *) shift ;;
  esac
done

question="$(cat /dev/stdin)"

cat <<ANSWER
The auth token refresh path lives in internal/auth/refresh.go. The flow is:

  1. Middleware checks access-token expiry on each request.
  2. If within the 60s refresh window, it calls RefreshClient.Exchange.
  3. The new token is persisted to the session store and set as a cookie.

Relevant symbols: RefreshClient, Session.TouchToken, cookieForToken.

(session $session_id, demo fake)
ANSWER
