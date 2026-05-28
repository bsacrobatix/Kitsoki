#!/usr/bin/env bash
# fake-claude-nosubmit.sh — stub that simulates a turn where the LLM never
# calls the validator's submit tool. The capture file is left absent so
# the harness must surface a meaningful error.
set -e
cat /dev/stdin > /dev/null
cat <<'ENVELOPE'
{"type":"result","subtype":"success","is_error":false,"result":"I'm not sure what you mean.","session_id":"fake-session","total_cost_usd":0}
ENVELOPE
