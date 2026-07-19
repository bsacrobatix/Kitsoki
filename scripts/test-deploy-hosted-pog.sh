#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
deploy="$root/scripts/deploy-hosted-pog.sh"
packager="$root/scripts/package-hosted-pog-state.sh"
assets="$root/deploy/hosted-pog"

bash -n "$deploy" "$packager" "$assets/install.sh"

for required in \
  "$assets/Caddyfile" \
  "$assets/hosted-pog.yaml" \
  "$assets/kitsoki-pog.service" \
  "$assets/node-runtime.env" \
  "$assets/pog-portal.service" \
  "$packager"; do
  [ -f "$required" ] || { echo "missing hosted POG deployment asset: $required" >&2; exit 1; }
done

grep -q 'forward_auth 127.0.0.1:7777' "$assets/Caddyfile"
grep -q 'uri /auth/check' "$assets/Caddyfile"
grep -A3 -q 'handle /gh-agent/webhook.*reverse_proxy 127.0.0.1:8787' "$assets/Caddyfile" || {
  sed -n '/handle \/gh-agent\/webhook/,/}/p' "$assets/Caddyfile" | grep -q 'reverse_proxy 127.0.0.1:8787'
}
sed -n '/handle @gh_agent/,/}/p' "$assets/Caddyfile" | grep -q 'import pog_login'
sed -n '/handle \/decks\/\*/,/}/p' "$assets/Caddyfile" | grep -q 'import pog_login'
grep -q 'handle /api/feedback\*' "$assets/Caddyfile"
grep -q 'reverse_proxy 127.0.0.1:5183' "$assets/Caddyfile"
grep -q 'mode: required' "$assets/hosted-pog.yaml"
grep -q 'client_id: __GITHUB_CLIENT_ID__' "$assets/hosted-pog.yaml"
grep -q 'device_flow: true' "$assets/hosted-pog.yaml"
! grep -q 'client_secret' "$assets/hosted-pog.yaml"
! grep -q 'EnvironmentFile=' "$assets/kitsoki-pog.service"
grep -q 'POG_KITSOKI_BROWSER_URL=' "$assets/pog-portal.service"
grep -q 'POG_AGENT_RUNNER_DB=/var/lib/pog/runtime/agent-runner/sessions.db' "$assets/pog-portal.service"
grep -q 'POG_STREAMS_DIR=/var/lib/pog/runtime/streams' "$assets/pog-portal.service"
grep -q "show.*POG_KITSOKI_BROWSER_URL" "$deploy"
grep -q 'login-gated portal, API, agent health/run/deck, and evidence routes' "$deploy"
grep -q 'GitHub Device Flow endpoints and the HMAC-verified webhook only' "$deploy"
grep -q 'github.com/login/device/code' "$deploy"
grep -q '/opt/kitsoki-hosted-pog/current/kitsoki' "$assets/kitsoki-pog.service"
grep -q '^KITSOKI_HOSTED_POG_NODE_VERSION=v[0-9]' "$assets/node-runtime.env"
grep -Eq '^KITSOKI_HOSTED_POG_NODE_SHA256=[0-9a-f]{64}$' "$assets/node-runtime.env"
grep -q 'nodejs.org/download/release/' "$assets/node-runtime.env"
grep -q 'shasum -a 256 -c' "$deploy"
grep -q 'sha256sum -c' "$assets/install.sh"
grep -q 'require(.*node:sqlite' "$assets/install.sh"
grep -q '/opt/kitsoki-hosted-pog/node/current/bin/npm' "$assets/pog-portal.service"
grep -q '__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS=__PUBLIC_HOST__' "$assets/pog-portal.service"
grep -q 'pog-portal.service.rendered' "$assets/install.sh"
grep -q 'Host: \$public_host' "$assets/install.sh"
grep -q "Host: \$PUBLIC_HOST" "$deploy"
grep -q 'products.join.*pog,constructor-studio' "$assets/install.sh"
grep -q 'exact products=pog,constructor-studio' "$deploy"
grep -q 'state mode must be preserve or sync' "$assets/install.sh"
grep -q 'local-state sync refused to overwrite divergent hosted state' "$assets/install.sh"
grep -q 'runtime_current_changed' "$assets/install.sh"
grep -q 'ln -s.*runtime_current.*release/.artifacts' "$assets/install.sh"
grep -q 'package-hosted-pog-state.sh' "$deploy"
grep -q -- '--sync-local-state' "$deploy"
grep -q 'previous_node_current' "$assets/install.sh"
grep -q 'npm.* run build' "$assets/install.sh"
grep -q 'runuser -u pog -- git -C.*rev-parse HEAD' "$assets/install.sh"
grep -q 'runuser -u pog -- git -C.*status --porcelain' "$assets/install.sh"
grep -q 'caddy validate' "$assets/install.sh"
grep -q 'expect_public_status 401 /decks/access-probe' "$deploy"
grep -q 'expect_public_status 401 /constructor-studio/decks/access-probe' "$deploy"
grep -q 'expect_public_status 401 /api/feedback-reports' "$deploy"
grep -q 'expect_public_status 401 /api/colony' "$deploy"
grep -q 'expect_public_status 401 /api/streams' "$deploy"
grep -q 'expect_public_status 401 /api/agent-runner/reaped-sessions' "$deploy"
grep -q 'expect_public_status 401 /api/feedback-reports' "$assets/install.sh"
grep -q 'expect_public_status 401 /api/colony' "$assets/install.sh"
grep -q 'expect_public_status 401 /api/streams' "$assets/install.sh"
grep -q 'expect_public_status 401 /api/agent-runner/reaped-sessions' "$assets/install.sh"
grep -q 'expect_public_status 401 /api/run/access-probe' "$assets/install.sh"
grep -q 'expect_public_status 410 /auth/github/device/poll' "$deploy"
grep -q 'expect_public_status 410 /auth/github/device/poll' "$assets/install.sh"
grep -q '__GITHUB_CLIENT_ID__.*github_client_id' "$assets/install.sh"
grep -q 'curl -fsS http://127.0.0.1:8787/healthz' "$root/scripts/deploy-gh-agent.sh"
grep -q "ssh.*curl -fsS http://127.0.0.1:8787/healthz" "$root/scripts/collect-gh-agent-poc-evidence.sh"
grep -q 'restoring the prior release targets and Caddyfile' "$assets/install.sh"

fixture="$(mktemp -d "${TMPDIR:-/tmp}/kitsoki-hosted-pog-state-test.XXXXXX")"
cleanup() {
  rm -rf -- "$fixture"
}
trap cleanup EXIT
mkdir -p \
  "$fixture/pog/.git" \
  "$fixture/pog/.artifacts/feedback/evidence" \
  "$fixture/pog/.artifacts/graph-mcp" \
  "$fixture/pog/.artifacts/streams" \
  "$fixture/pog/.artifacts/colony" \
  "$fixture/pog/.artifacts/agent-runner" \
  "$fixture/pog/.artifacts/bin"
printf '%s\n' '{"id":"feedback-local"}' >"$fixture/pog/.artifacts/feedback/feedback.jsonl"
printf '%s\n' '{"id":"feedback-graph"}' >"$fixture/pog/.artifacts/graph-mcp/feedback.jsonl"
printf '%s\n' '{"schema":"pog/stream/v1"}' >"$fixture/pog/.artifacts/streams/stream.json"
printf '%s\n' '{"campaigns":{}}' >"$fixture/pog/.artifacts/colony/state.json"
printf '%s\n' '99999' >"$fixture/pog/.artifacts/colony/runner.pid"
printf '%s\n' 'local log' >"$fixture/pog/.artifacts/colony/runner-watch.log"
printf '%s\n' 'must not upload' >"$fixture/pog/.artifacts/bin/private-cache"
sqlite3 "$fixture/pog/.artifacts/agent-runner/sessions.db" \
  'CREATE TABLE sessions (id TEXT PRIMARY KEY); INSERT INTO sessions VALUES ("session-local");'
mkdir -p "$fixture/out" "$fixture/out-repeat" "$fixture/unpacked"
"$packager" "$fixture/pog" 0123456789012345678901234567890123456789 "$fixture/out" >/dev/null
sleep 1
"$packager" "$fixture/pog" 0123456789012345678901234567890123456789 "$fixture/out-repeat" >/dev/null
[ "$(tar -xOzf "$fixture/out-repeat/pog-state.tar.gz" ./manifest.json | jq -r .content_sha256)" = \
  "$(tar -xOzf "$fixture/out/pog-state.tar.gz" ./manifest.json | jq -r .content_sha256)" ]
printf '%s  %s\n' "$(cat "$fixture/out/pog-state.sha256")" "$fixture/out/pog-state.tar.gz" | shasum -a 256 -c - >/dev/null
tar -xzf "$fixture/out/pog-state.tar.gz" -C "$fixture/unpacked"
jq -e '
  .schema == "kitsoki/hosted-pog-local-state/v1" and
  .pog_sha == "0123456789012345678901234567890123456789" and
  (.content_sha256 | test("^[0-9a-f]{64}$")) and
  .file_count == 5
' "$fixture/unpacked/manifest.json" >/dev/null
[ -f "$fixture/unpacked/feedback/feedback.jsonl" ]
[ -f "$fixture/unpacked/graph-mcp/feedback.jsonl" ]
[ -f "$fixture/unpacked/streams/stream.json" ]
[ -f "$fixture/unpacked/colony/state.json" ]
[ -f "$fixture/unpacked/agent-runner/sessions.db" ]
[ ! -e "$fixture/unpacked/colony/runner.pid" ]
[ ! -e "$fixture/unpacked/colony/runner-watch.log" ]
[ ! -e "$fixture/unpacked/bin/private-cache" ]
[ "$(sqlite3 "$fixture/unpacked/agent-runner/sessions.db" 'SELECT id FROM sessions;')" = "session-local" ]

trap - EXIT
cleanup

echo "hosted POG deployment assets: OK"
